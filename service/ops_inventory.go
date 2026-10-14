package service

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// SyncOpsObjects exports only identity fields, never entire database models.
// Organization/project inventories remain owned by Enterprise Policy Hub.
func SyncOpsObjects(ctx context.Context, bridge *opsbridge.Bridge) (map[string]int, error) {
	objects := make([]map[string]any, 0)
	counts := map[string]int{"user": 0, "api_key": 0, "channel": 0}
	const maxObjects = 10000
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		users, _, err := model.GetAllUsers(&common.PageInfo{Page: page, PageSize: 100}, model.NewUserSortOptions("id", "asc"))
		if err != nil {
			return nil, errors.New("cannot read Ops user inventory")
		}
		for _, user := range users {
			if user.DeletedAt.Valid {
				continue
			}
			id := strconv.Itoa(user.Id)
			objects = append(objects, map[string]any{"object_type": "user", "external_id": id, "gateway_user_id": id,
				"gateway_customer_id": id, "display_name": user.DisplayName})
			counts["user"]++
			for offset := 0; ; offset += 100 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				tokens, err := model.GetAllUserTokens(user.Id, offset, 100)
				if err != nil {
					return nil, errors.New("cannot read Ops token inventory")
				}
				for _, token := range tokens {
					objects = append(objects, map[string]any{"object_type": "api_key", "external_id": strconv.Itoa(token.Id),
						"token_id": strconv.Itoa(token.Id), "gateway_user_id": id, "display_name": token.Name})
					counts["api_key"]++
				}
				if len(objects) > maxObjects {
					return nil, errors.New("Ops inventory exceeds 10000 objects; no partial snapshot was queued")
				}
				if len(tokens) < 100 {
					break
				}
			}
		}
		if len(users) < 100 {
			break
		}
	}
	for offset := 0; ; offset += 100 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		channels, err := model.GetAllChannels(offset, 100, false, false, model.NewChannelSortOptions("id", "asc", false))
		if err != nil {
			return nil, errors.New("cannot read Ops channel inventory")
		}
		for _, channel := range channels {
			status := "disabled"
			if channel.Status == common.ChannelStatusEnabled {
				status = "active"
			}
			objects = append(objects, map[string]any{"object_type": "channel", "external_id": strconv.Itoa(channel.Id),
				"channel_id": strconv.Itoa(channel.Id), "display_name": channel.Name, "channel_type": constant.GetChannelTypeName(channel.Type), "object_status": status})
			counts["channel"]++
		}
		if len(objects) > maxObjects {
			return nil, errors.New("Ops inventory exceeds 10000 objects; no partial snapshot was queued")
		}
		if len(channels) < 100 {
			break
		}
	}
	if err := bridge.QueueInventory(rand.Text(), objects); err != nil {
		return nil, err
	}
	return counts, nil
}

// SyncOpsPricing submits local configuration evidence. Ops administrators still
// control official prices and any subsequent catalog publication.
func SyncOpsPricing(ctx context.Context, bridge *opsbridge.Bridge) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	snapshot, err := model.GetModelPricingSnapshot(nil)
	if err != nil {
		return 0, errors.New("cannot read local pricing snapshot")
	}
	entries := make([]map[string]any, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries = append(entries, map[string]any{"model_name": entry.ModelName, "version": entry.Version,
			"configured": entry.Configured, "effective": entry.Effective})
	}
	payload := map[string]any{"entries": entries, "complete": true, "captured_at": time.Now().UTC().Format(time.RFC3339Nano),
		"quota_per_unit": common.QuotaPerUnit, "group_ratio": ratio_setting.GetGroupRatioCopy(),
		"group_group_ratio": ratio_setting.GetGroupRatioSetting().GroupGroupRatio.ReadAll(),
		"usd_exchange_rate": operation_setting.USDExchangeRate}
	if err := bridge.QueuePricingSnapshot(rand.Text(), payload); err != nil {
		return 0, err
	}
	return len(entries), nil
}
