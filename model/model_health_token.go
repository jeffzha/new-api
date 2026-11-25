package model

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ExtendModelHealthTokenModels changes only the allowlist of a dedicated
// monitoring token. In particular it cannot overwrite a concurrent charge,
// renew an expired token, enable a revoked token or grant unlimited quota.
func ExtendModelHealthTokenModels(ctx context.Context, id int, models []string) error {
	var token Token
	db := DB.WithContext(ctx)
	if err := db.Take(&token, "id = ?", id).Error; err != nil {
		return err
	}
	if !strings.HasPrefix(token.Name, "model-health/") || token.Status != common.TokenStatusEnabled || token.UnlimitedQuota || !token.ModelLimitsEnabled || token.CrossGroupRetry || token.Group == "auto" || token.ExpiredTime <= time.Now().Unix() || token.RemainQuota <= 0 {
		return gorm.ErrInvalidData
	}
	var user User
	if err := db.Select("role", "status").Take(&user, "id = ?", token.UserId).Error; err != nil {
		return err
	}
	if user.Role != common.RoleAdminUser || user.Status != common.UserStatusEnabled {
		return gorm.ErrInvalidData
	}
	models = append(slices.Clone(models), token.GetModelLimits()...)
	slices.Sort(models)
	models = slices.Compact(models)
	limits := strings.Join(models, ",")
	// Compare-and-swap also protects an operator editing the allowlist.
	if limits != token.ModelLimits {
		result := db.Model(&Token{}).Where(clause.Eq{Column: "group", Value: token.Group}).Where("id = ? AND user_id = ? AND model_limits = ? AND name = ? AND status = ? AND model_limits_enabled = ? AND unlimited_quota = ? AND cross_group_retry = ? AND expired_time = ? AND remain_quota > ?", id, token.UserId, token.ModelLimits, token.Name, common.TokenStatusEnabled, true, false, false, token.ExpiredTime, 0).
			UpdateColumn("model_limits", limits)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
	}
	if !common.RedisEnabled || common.RDB == nil {
		return nil
	}
	// A widening allowlist may temporarily remain restrictive. Never delete a
	// live quota hash: it can contain deductions still waiting for batch flush.
	// Fence cold readers, and widen metadata only when its cached permissions
	// are a subset of the database result. This also repairs a failed Redis write
	// on the next reconciliation without touching any quota fields.
	const script = `
redis.call('SET', KEYS[2], 1, 'EX', ARGV[5])
if redis.call('HGET', KEYS[1], 'Id') == ARGV[1]
  and redis.call('HGET', KEYS[1], 'Name') == ARGV[4] then
  local allowed = {}
  for model in string.gmatch(ARGV[3], '[^,]+') do allowed[model] = true end
  for model in string.gmatch(redis.call('HGET', KEYS[1], 'ModelLimits') or '', '[^,]+') do
    if not allowed[model] then return 0 end
  end
  redis.call('HSET', KEYS[1], 'ModelLimits', ARGV[3])
end
return 1`
	return common.RDB.Eval(ctx, script, []string{getTokenCacheKey(token.Key), getTokenCacheFenceKey(token.Key)}, token.Id, token.ModelLimits, limits, token.Name, tokenCacheFenceSeconds).Err()
}
