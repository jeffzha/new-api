package opsmonitor

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ops_monitor_setting"
)

type ConcurrencyChannelStats struct {
	ChannelID         int      `json:"channel_id"`
	ChannelName       string   `json:"channel_name"`
	ChannelType       int      `json:"channel_type"`
	MultiKeySize      int      `json:"multi_key_size"`
	Platform          string   `json:"platform"`
	Groups            []string `json:"groups"`
	Status            int      `json:"status"`
	InUse             int64    `json:"in_use"`
	Capacity          int      `json:"capacity"`
	Waiting           int64    `json:"waiting"`
	LoadPercent       float64  `json:"load_percent"`
	LimitConfigured   bool     `json:"limit_configured"`
	LimitEnabled      bool     `json:"limit_enabled"`
	Available         bool     `json:"available"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
}

type ConcurrencyAggregateStats struct {
	Name        string  `json:"name"`
	InUse       int64   `json:"in_use"`
	Capacity    int     `json:"capacity"`
	Waiting     int64   `json:"waiting"`
	LoadPercent float64 `json:"load_percent"`
	Available   int     `json:"available"`
	Total       int     `json:"total"`
}

type UserConcurrencyStats struct {
	UserID int   `json:"user_id"`
	InUse  int64 `json:"in_use"`
}

type ConcurrencySnapshot struct {
	EnforcementEnabled bool                        `json:"enforcement_enabled"`
	RedisBacked        bool                        `json:"redis_backed"`
	Channels           []ConcurrencyChannelStats   `json:"channels"`
	Platforms          []ConcurrencyAggregateStats `json:"platforms"`
	Groups             []ConcurrencyAggregateStats `json:"groups"`
	Users              []UserConcurrencyStats      `json:"users"`
	CollectedAt        int64                       `json:"collected_at"`
}

func GetConcurrencySnapshot() (ConcurrencySnapshot, error) {
	channels, err := model.ListOpsChannels()
	if err != nil {
		return ConcurrencySnapshot{}, err
	}
	limits, err := model.ListOpsConcurrencyLimits()
	if err != nil {
		return ConcurrencySnapshot{}, err
	}
	limitsByChannel := make(map[int][]model.OpsConcurrencyLimit)
	for _, limit := range limits {
		limitsByChannel[limit.ChannelID] = append(limitsByChannel[limit.ChannelID], limit)
	}

	snapshot := ConcurrencySnapshot{
		EnforcementEnabled: ops_monitor_setting.Get().ConcurrencyEnforcementEnabled,
		RedisBacked:        common.RedisEnabled && common.RDB != nil,
		CollectedAt:        time.Now().Unix(),
	}
	platforms := map[string]*ConcurrencyAggregateStats{}
	groups := map[string]*ConcurrencyAggregateStats{}
	for _, channel := range channels {
		inUse, waiting := currentChannelConcurrency(channel.ID, limitsByChannel[channel.ID])
		capacity, configured, enabled := configuredCapacity(limitsByChannel[channel.ID])
		available, reason := channelAvailability(channel, inUse, capacity, enabled)
		load := 0.0
		if capacity > 0 {
			load = float64(inUse) / float64(capacity) * 100
		}
		platform := constant.ChannelTypeNames[channel.Type]
		if platform == "" {
			platform = fmt.Sprintf("type-%d", channel.Type)
		}
		channelGroups := splitChannelGroups(channel.Group)
		row := ConcurrencyChannelStats{
			ChannelID: channel.ID, ChannelName: channel.Name, ChannelType: channel.Type,
			MultiKeySize: channel.ChannelInfo.MultiKeySize,
			Platform:     platform, Groups: channelGroups, Status: channel.Status,
			InUse: inUse, Capacity: capacity, Waiting: waiting, LoadPercent: load,
			LimitConfigured: configured, LimitEnabled: enabled,
			Available: available, UnavailableReason: reason,
		}
		snapshot.Channels = append(snapshot.Channels, row)
		addConcurrencyAggregate(platforms, platform, row)
		for _, group := range channelGroups {
			addConcurrencyAggregate(groups, group, row)
		}
	}
	for _, aggregate := range platforms {
		finalizeConcurrencyAggregate(aggregate)
		snapshot.Platforms = append(snapshot.Platforms, *aggregate)
	}
	for _, aggregate := range groups {
		finalizeConcurrencyAggregate(aggregate)
		snapshot.Groups = append(snapshot.Groups, *aggregate)
	}
	sort.Slice(snapshot.Platforms, func(i, j int) bool { return snapshot.Platforms[i].Name < snapshot.Platforms[j].Name })
	sort.Slice(snapshot.Groups, func(i, j int) bool { return snapshot.Groups[i].Name < snapshot.Groups[j].Name })
	snapshot.Users = currentUserConcurrency()
	return snapshot, nil
}

func configuredCapacity(limits []model.OpsConcurrencyLimit) (int, bool, bool) {
	capacity := 0
	configured := len(limits) > 0
	enabled := false
	for _, limit := range limits {
		if !limit.Enabled || limit.MaxConcurrency <= 0 {
			continue
		}
		enabled = true
		if limit.KeyIndex == model.OpsConcurrencyAllKeys {
			return limit.MaxConcurrency, configured, true
		}
		capacity += limit.MaxConcurrency
	}
	return capacity, configured, enabled
}

func currentChannelConcurrency(channelID int, limits []model.OpsConcurrencyLimit) (int64, int64) {
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		now := time.Now().UnixMilli()
		leaseKey := concurrencyLeaseKey(channelID, model.OpsConcurrencyAllKeys)
		_ = common.RDB.ZRemRangeByScore(ctx, leaseKey, "-inf", strconv.FormatInt(now, 10)).Err()
		inUse, _ := common.RDB.ZCard(ctx, leaseKey).Result()
		waiting := int64(0)
		for _, limit := range limits {
			waiterKey := concurrencyWaiterKey(channelID, limit.KeyIndex)
			count, _ := common.RDB.ZCard(ctx, waiterKey).Result()
			waiting += count
		}
		return inUse, waiting
	}
	localMu.Lock()
	defer localMu.Unlock()
	state := localState[concurrencyScope{channelID: channelID, keyIndex: model.OpsConcurrencyAllKeys}]
	if state == nil {
		return 0, 0
	}
	waiting := state.waiting
	seen := map[concurrencyScope]struct{}{{channelID: channelID, keyIndex: model.OpsConcurrencyAllKeys}: {}}
	for _, limit := range limits {
		scope := concurrencyScope{channelID: channelID, keyIndex: limit.KeyIndex}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		if limitState := localState[scope]; limitState != nil {
			waiting += limitState.waiting
		}
	}
	return int64(state.inUse), int64(waiting)
}

func channelAvailability(channel model.OpsChannelInfo, inUse int64, capacity int, limitEnabled bool) (bool, string) {
	if channel.Status != common.ChannelStatusEnabled {
		return false, "disabled"
	}
	if runtimeAvailabilityActive("rate_limit", channel) {
		return false, "rate_limited"
	}
	if runtimeAvailabilityActive("overload", channel) {
		return false, "overloaded"
	}
	if runtimeAvailabilityActive("temporary", channel) {
		return false, "temporarily_unschedulable"
	}
	if limitEnabled && capacity > 0 && inUse >= int64(capacity) {
		return false, "at_capacity"
	}
	return true, ""
}

func runtimeAvailabilityActive(state string, channel model.OpsChannelInfo) bool {
	if !common.RedisEnabled || common.RDB == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	keyCount := channel.ChannelInfo.MultiKeySize
	if keyCount < 1 {
		keyCount = 1
	}
	keys := make([]string, 0, keyCount+1)
	for keyIndex := model.OpsConcurrencyAllKeys; keyIndex < keyCount; keyIndex++ {
		keys = append(keys, fmt.Sprintf("ops:availability:%s:channel:%d:key:%d", state, channel.ID, keyIndex))
	}
	count, err := common.RDB.Exists(ctx, keys...).Result()
	return err == nil && count > 0
}

func currentUserConcurrency() []UserConcurrencyStats {
	if !common.RedisEnabled || common.RDB == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ids, err := common.RDB.SMembers(ctx, "ops:concurrency:active_users").Result()
	if err != nil {
		return nil
	}
	users := make([]UserConcurrencyStats, 0, len(ids))
	now := time.Now().UnixMilli()
	for _, idText := range ids {
		userID, parseErr := strconv.Atoi(idText)
		if parseErr != nil {
			continue
		}
		key := fmt.Sprintf("ops:concurrency:leases:user:%d", userID)
		_ = common.RDB.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now, 10)).Err()
		count, _ := common.RDB.ZCard(ctx, key).Result()
		if count <= 0 {
			_ = common.RDB.SRem(ctx, "ops:concurrency:active_users", userID).Err()
			continue
		}
		users = append(users, UserConcurrencyStats{UserID: userID, InUse: count})
	}
	sort.Slice(users, func(i, j int) bool {
		if users[i].InUse == users[j].InUse {
			return users[i].UserID < users[j].UserID
		}
		return users[i].InUse > users[j].InUse
	})
	return users
}

func currentQueueDepth() int64 {
	limits, err := model.ListOpsConcurrencyLimits()
	if err != nil {
		return 0
	}
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var total int64
		for _, limit := range limits {
			count, _ := common.RDB.ZCard(ctx, concurrencyWaiterKey(limit.ChannelID, limit.KeyIndex)).Result()
			total += count
		}
		return total
	}
	localMu.Lock()
	defer localMu.Unlock()
	var total int64
	for _, state := range localState {
		total += int64(state.waiting)
	}
	return total
}

func splitChannelGroups(value string) []string {
	parts := strings.Split(value, ",")
	groups := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		groups = append(groups, part)
	}
	return groups
}

func addConcurrencyAggregate(target map[string]*ConcurrencyAggregateStats, name string, row ConcurrencyChannelStats) {
	aggregate := target[name]
	if aggregate == nil {
		aggregate = &ConcurrencyAggregateStats{Name: name}
		target[name] = aggregate
	}
	aggregate.InUse += row.InUse
	aggregate.Capacity += row.Capacity
	aggregate.Waiting += row.Waiting
	aggregate.Total++
	if row.Available {
		aggregate.Available++
	}
}

func finalizeConcurrencyAggregate(aggregate *ConcurrencyAggregateStats) {
	if aggregate.Capacity > 0 {
		aggregate.LoadPercent = float64(aggregate.InUse) / float64(aggregate.Capacity) * 100
	}
}
