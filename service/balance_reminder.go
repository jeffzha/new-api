package service

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-redis/redis/v8"
)

var balanceReminderOnce sync.Once
var balanceReminderMemory = struct {
	sync.Mutex
	states map[int]map[string]bool
}{states: make(map[int]map[string]bool)}

// Balance reminders are independent of relay settlement so asynchronous image
// and video charges are checked too. Only one page is held in memory at a time.
func StartBalanceReminderTask(ctx context.Context) {
	balanceReminderOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := runBalanceReminderPass(ctx); err != nil {
						common.SysError("balance reminder pass: " + err.Error())
					}
				}
			}
		}()
	})
}

func runBalanceReminderPass(ctx context.Context) error {
	for afterID := 0; ; {
		var users []model.User
		if err := model.DB.WithContext(ctx).Select("id", "email", "setting").
			Where("id > ? AND status = ? AND (used_quota > 0 OR request_count > 0)", afterID, common.UserStatusEnabled).
			Order("id").Limit(100).Find(&users).Error; err != nil {
			return err
		}
		if len(users) == 0 {
			return nil
		}
		for _, user := range users {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := checkBalanceReminder(ctx, user, NotifyUser); err != nil {
				common.SysError(fmt.Sprintf("balance reminder user %d: %v", user.Id, err))
			}
			afterID = user.Id
		}
	}
}

func balanceReminderThresholds(settings dto.UserSetting) []int {
	rate := operation_setting.USDExchangeRate
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		rate = 7.3
	}
	thresholds := []int{0}
	for _, cny := range []float64{1, 2, 5} {
		thresholds = append(thresholds, common.QuotaRound(cny/rate*common.QuotaPerUnit))
	}
	// Keep a user's higher custom early-warning threshold, without allowing an
	// old tiny quota-unit default to suppress the standard RMB warning tiers.
	if settings.QuotaWarningThreshold > float64(thresholds[len(thresholds)-1]) &&
		!math.IsInf(settings.QuotaWarningThreshold, 0) {
		thresholds = append(thresholds, common.QuotaRound(settings.QuotaWarningThreshold))
	}
	slices.Sort(thresholds)
	return slices.Compact(thresholds)
}

// Evaluate the most urgent unsent tier; crossing several tiers sends one alert
// and marks all crossed tiers. Recovery above a tier rearms only that tier.
func balanceReminderTransition(quota int, thresholds []int, sent map[string]bool) (map[string]bool, int, bool) {
	next := make(map[string]bool, len(thresholds))
	selected, notify := 0, false
	for _, threshold := range thresholds {
		key := strconv.Itoa(threshold)
		crossed := quota < threshold || threshold == 0 && quota <= 0
		if !crossed {
			continue
		}
		next[key] = true
		if !sent[key] && !notify {
			selected, notify = threshold, true
		}
	}
	return next, selected, notify
}

func checkBalanceReminder(ctx context.Context, user model.User, send func(int, string, dto.UserSetting, dto.Notify) error) error {
	settings := user.GetSetting()
	switch settings.NotifyType {
	case "", dto.NotifyTypeEmail:
		if settings.NotificationEmail == "" && user.Email == "" {
			return nil
		}
	case dto.NotifyTypeWebhook:
		if settings.WebhookUrl == "" {
			return nil
		}
	case dto.NotifyTypeBark:
		if settings.BarkUrl == "" {
			return nil
		}
	case dto.NotifyTypeGotify:
		if settings.GotifyUrl == "" || settings.GotifyToken == "" {
			return nil
		}
	default:
		return nil
	}
	process := func(sent map[string]bool, save func(map[string]bool) error) error {
		// Read after acquiring the lease, not from a stale per-request snapshot.
		quota, err := model.GetUserQuota(user.Id, false)
		if err != nil {
			return err
		}
		next, tier, notify := balanceReminderTransition(quota, balanceReminderThresholds(settings), sent)
		if notify {
			title := "余额不足提醒"
			if quota <= 0 {
				title = "余额已耗尽，请及时充值"
			}
			content := "{{value}}，当前钱包余额：{{value}}。请及时充值，避免后续调用因余额不足而失败。充值地址：{{value}}"
			data := dto.NewNotify("balance_tier_"+strconv.Itoa(tier), title, content,
				[]any{title, logger.FormatQuota(quota), PaymentReturnURL("/wallet")})
			if err := send(user.Id, user.Email, settings, data); err != nil {
				return err
			}
		}
		return save(next)
	}
	if !common.RedisEnabled {
		balanceReminderMemory.Lock()
		defer balanceReminderMemory.Unlock()
		return process(balanceReminderMemory.states[user.Id], func(next map[string]bool) error {
			balanceReminderMemory.states[user.Id] = next
			return nil
		})
	}
	if common.RDB == nil {
		return fmt.Errorf("balance reminder Redis unavailable")
	}
	key := fmt.Sprintf("balance_reminder:v1:%d", user.Id)
	lockKey, owner := key+":lock", common.GetRandomString(24)
	locked, err := common.RDB.SetNX(ctx, lockKey, owner, 2*time.Minute).Result()
	if err != nil || !locked {
		return err
	}
	leaseCtx, stopLease := context.WithCancel(ctx)
	defer stopLease()
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				_, _ = common.RDB.Eval(leaseCtx, `if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('expire', KEYS[1], 120) end return 0`, []string{lockKey}, owner).Result()
			}
		}
	}()
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = common.RDB.Eval(releaseCtx, `if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('del', KEYS[1]) end return 0`, []string{lockKey}, owner).Result()
	}()
	sent := make(map[string]bool)
	raw, err := common.RDB.Get(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return err
	}
	if raw != "" {
		if err := common.UnmarshalJsonStr(raw, &sent); err != nil {
			return err
		}
	}
	return process(sent, func(next map[string]bool) error {
		data, err := common.Marshal(next)
		if err != nil {
			return err
		}
		saved, err := common.RDB.Eval(ctx, `if redis.call('get', KEYS[1]) == ARGV[1] then redis.call('set', KEYS[2], ARGV[2]); return 1 end return 0`, []string{lockKey, key}, owner, string(data)).Int()
		if err != nil {
			return err
		}
		if saved != 1 {
			return fmt.Errorf("balance reminder lease lost")
		}
		return nil
	})
}
