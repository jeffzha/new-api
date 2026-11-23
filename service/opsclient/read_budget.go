package opsclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// A credential has one OPS read budget across resources and gateway instances.
// Leave six of the default 60 requests/minute for operations outside this client.
// Background reads leave a further twelve for call-time balance checks.
const readBudgetLimit = 54
const backgroundReadLimit = 42

type callBalanceBudget struct{}

func CallBalanceContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, callBalanceBudget{}, true)
}

func ReadDeferred(err error) (time.Duration, bool) {
	var failure *ReadError
	if errors.As(err, &failure) && failure.Code == "read_budget_deferred" {
		return failure.RetryAfter, true
	}
	return 0, false
}

type readWindow struct {
	times    []time.Time
	cooldown time.Time
}

var localReads = struct {
	sync.Mutex
	windows map[string]*readWindow
}{windows: map[string]*readWindow{}}

const reserveReadScript = `
local t=redis.call('TIME')
local now=t[1]*1000+math.floor(t[2]/1000)
local blocked=redis.call('PTTL',KEYS[2])
if blocked>0 then return blocked end
redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now-60000)
local count=redis.call('ZCARD',KEYS[1])
if count>=tonumber(ARGV[1]) then
 local oldest=redis.call('ZRANGE',KEYS[1],0,0,'WITHSCORES')
 return math.max(1,tonumber(oldest[2])+60000-now)
end
redis.call('ZADD',KEYS[1],now,ARGV[2])
redis.call('PEXPIRE',KEYS[1],61000)
return 0
`

// EnableReadBudget is installed only on configured runtime readers, leaving
// transport fixtures independent. It never gates cached responses, usage
// delivery or money writes, and never queries the SQL database.
func (c *Client) EnableReadBudget() { c.readBudget = true }

func (c *Client) budgetKey() string {
	return fmt.Sprintf("ops-read-budget:v1:%x", sha256.Sum256([]byte(c.BaseURL+"\n"+c.KeyID)))
}

func (c *Client) reserveRead(ctx context.Context) error {
	limit := backgroundReadLimit
	priority, _ := ctx.Value(callBalanceBudget{}).(bool)
	if priority {
		limit = readBudgetLimit
	}
	key := c.budgetKey()
	at := time.Now()
	var wait time.Duration
	if common.RedisEnabled && common.RDB != nil {
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return errors.New("OPS read budget unavailable")
		}
		budgetCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		millis, err := common.RDB.Eval(budgetCtx, reserveReadScript, []string{key, key + ":cooldown"}, limit, hex.EncodeToString(token)).Int64()
		if err != nil {
			return errors.New("OPS read budget unavailable")
		}
		wait = time.Duration(millis) * time.Millisecond
	} else {
		localReads.Lock()
		defer localReads.Unlock()
		w := localReads.windows[key]
		if w == nil {
			w = &readWindow{}
			localReads.windows[key] = w
		}
		n := 0
		for n < len(w.times) && !w.times[n].After(at.Add(-time.Minute)) {
			n++
		}
		w.times = w.times[n:]
		if at.Before(w.cooldown) {
			wait = w.cooldown.Sub(at)
		} else if len(w.times) >= limit {
			wait = w.times[0].Add(time.Minute).Sub(at)
		} else {
			w.times = append(w.times, at)
		}
	}
	if wait > 0 {
		return &ReadError{RetryAfter: wait, Code: "read_budget_deferred"}
	}
	return nil
}

func (c *Client) coolReadBudget(ctx context.Context, wait time.Duration) {
	wait = max(wait, time.Minute)
	key := c.budgetKey()
	if common.RedisEnabled && common.RDB != nil {
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_ = common.RDB.Eval(bounded, `local old=redis.call('PTTL',KEYS[1]); if old<tonumber(ARGV[1]) then redis.call('PSETEX',KEYS[1],ARGV[1],'1') end; return 0`, []string{key + ":cooldown"}, wait.Milliseconds()).Err()
		return
	}
	localReads.Lock()
	defer localReads.Unlock()
	w := localReads.windows[key]
	if w == nil {
		w = &readWindow{}
		localReads.windows[key] = w
	}
	until := time.Now().Add(wait)
	if until.After(w.cooldown) {
		w.cooldown = until
	}
}
