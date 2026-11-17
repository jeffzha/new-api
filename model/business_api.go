package model

import (
	"container/list"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

type businessInvocationKey struct {
	db               *gorm.DB
	userID, tokenID  int
	requestID, scope string
}
type businessInvocation struct {
	key     businessInvocationKey
	log     Log
	expires time.Time
}

var businessInvocations = struct {
	sync.Mutex
	entries map[businessInvocationKey]*list.Element
	order   *list.List
	loads   singleflight.Group
}{entries: make(map[businessInvocationKey]*list.Element), order: list.New()}

// CachedBusinessRequestLog caches only the public immutable invocation view.
// Token authentication still runs on every HTTP request. Failed attempts,
// missing rows and mutable task logs must be read again, not negatively cached.
func CachedBusinessRequestLog(userID, tokenID int, requestID string) (*Log, error) {
	if os.Getenv("OPS_BILLING_RECEIPTS") != "charges" && !OpsFeesEnabled() {
		return GetBusinessRequestLog(userID, tokenID, requestID)
	}
	key := businessInvocationKey{LOG_DB, userID, tokenID, requestID, opsFeeScope()}
	// The second cache check inside singleflight also covers callers that
	// arrived together on a cold key, without issuing duplicate SQL reads.
	businessInvocations.Lock()
	if e := businessInvocations.entries[key]; e != nil && time.Now().Before(e.Value.(businessInvocation).expires) {
		value := e.Value.(businessInvocation).log
		businessInvocations.Unlock()
		return &value, nil
	}
	businessInvocations.Unlock()
	loadKey := fmt.Sprintf("%p:%d:%d:%s:%s", key.db, userID, tokenID, key.scope, requestID)
	value, err, _ := businessInvocations.loads.Do(loadKey, func() (any, error) {
		businessInvocations.Lock()
		if e := businessInvocations.entries[key]; e != nil && time.Now().Before(e.Value.(businessInvocation).expires) {
			value := e.Value.(businessInvocation).log
			businessInvocations.Unlock()
			return value, nil
		}
		businessInvocations.Unlock()
		log, err := GetBusinessRequestLog(userID, tokenID, requestID)
		if err != nil {
			return nil, err
		}
		cacheBusinessInvocation(log)
		return *log, nil
	})
	if err != nil {
		return nil, err
	}
	log := value.(Log)
	return &log, nil
}

// cacheBusinessInvocation is called only after a successful log commit or read.
// It retains no prompt, caller context, channel information or API credentials.
func cacheBusinessInvocation(log *Log) {
	if os.Getenv("OPS_BILLING_RECEIPTS") != "charges" && !OpsFeesEnabled() || log == nil || log.Type != LogTypeConsume || log.UserId <= 0 || log.TokenId <= 0 || log.RequestId == "" {
		return
	}
	var meta struct {
		Billing *businessapi.Billing `json:"billing"`
		IsTask  bool                 `json:"is_task"`
	}
	if common.UnmarshalJsonStr(log.Other, &meta) != nil || meta.IsTask {
		return
	}
	other, err := common.Marshal(map[string]any{"billing": meta.Billing})
	if err != nil {
		return
	}
	key := businessInvocationKey{LOG_DB, log.UserId, log.TokenId, log.RequestId, opsFeeScope()}
	view := Log{UserId: log.UserId, TokenId: log.TokenId, RequestId: log.RequestId, Type: log.Type, ModelName: log.ModelName, CreatedAt: log.CreatedAt, IsStream: log.IsStream, UseTime: log.UseTime, PromptTokens: log.PromptTokens, CompletionTokens: log.CompletionTokens, Other: string(other)}
	businessInvocations.Lock()
	defer businessInvocations.Unlock()
	if e := businessInvocations.entries[key]; e != nil {
		businessInvocations.order.Remove(e)
	}
	e := businessInvocations.order.PushBack(businessInvocation{key, view, time.Now().Add(30 * time.Second)})
	businessInvocations.entries[key] = e
	if len(businessInvocations.entries) > 10000 {
		oldest := businessInvocations.order.Front()
		delete(businessInvocations.entries, oldest.Value.(businessInvocation).key)
		businessInvocations.order.Remove(oldest)
	}
}

// Caller attribution is stored with the invocation, independently of the
// account used for permissions and billing. It is never part of UI projections.
const logOtherCallerContextKey = "caller_context"

func businessCallerLogJSON(c *gin.Context, other *LogOther) string {
	caller, exists := businessapi.FromContext(c)
	if !exists {
		return other.JSONString()
	}
	values := other.toMap()
	values[logOtherCallerContextKey] = caller
	data, err := common.Marshal(values)
	if err != nil {
		common.SysError("failed to marshal business invocation metadata")
		return other.JSONString()
	}
	return string(data)
}

// Scope at the database boundary; another key on the same account cannot read
// a business system's invocation by knowing its request ID.
func GetBusinessRequestLog(userID, tokenID int, requestID string) (*Log, error) {
	if userID <= 0 || tokenID <= 0 || requestID == "" {
		return nil, errors.New("request identity is required")
	}
	if LOG_DB == nil {
		return nil, errors.New("log database is not initialized")
	}
	var log Log
	err := LOG_DB.Where("user_id = ? AND token_id = ? AND request_id = ? AND type IN ?", userID, tokenID, requestID, []int{LogTypeConsume, LogTypeError}).
		Order("type ASC").Order("created_at DESC").Take(&log).Error
	return &log, err
}
