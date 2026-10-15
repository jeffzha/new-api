package model

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

var ErrOpsInventoryTooLarge = errors.New("inventory_snapshot_too_large")

// ReadOpsInventory shares the Bridge object contract and reads only identity
// columns. Credentials and unnecessary PII never enter the snapshot builder.
func ReadOpsInventory(ctx context.Context, db *gorm.DB, gateway string) ([]byte, error) {
	var encoded []byte
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		const limit = 10000
		var users []User
		if err := tx.Select("id", "display_name").Order("id").Limit(limit + 1).Find(&users).Error; err != nil {
			return err
		}
		if len(users) > limit {
			return ErrOpsInventoryTooLarge
		}
		objects := make([]map[string]any, 0, len(users))
		for _, user := range users {
			id := strconv.Itoa(user.Id)
			objects = append(objects, map[string]any{"object_type": "user", "external_id": id, "gateway_user_id": id, "gateway_customer_id": id, "display_name": user.DisplayName})
		}
		var tokens []Token
		activeUsers := tx.Model(&User{}).Select("id")
		if err := tx.Select("id", "user_id", "name").Where("user_id IN (?)", activeUsers).Order("id").Limit(limit - len(objects) + 1).Find(&tokens).Error; err != nil {
			return err
		}
		for _, token := range tokens {
			objects = append(objects, map[string]any{"object_type": "api_key", "external_id": strconv.Itoa(token.Id), "token_id": strconv.Itoa(token.Id), "gateway_user_id": strconv.Itoa(token.UserId), "display_name": token.Name})
		}
		if len(objects) > limit {
			return ErrOpsInventoryTooLarge
		}
		var channels []Channel
		if err := tx.Select("id", "name", "type", "status").Order("id").Limit(limit - len(objects) + 1).Find(&channels).Error; err != nil {
			return err
		}
		for _, channel := range channels {
			status := "disabled"
			if channel.Status == common.ChannelStatusEnabled {
				status = "active"
			}
			objects = append(objects, map[string]any{"object_type": "channel", "external_id": strconv.Itoa(channel.Id), "channel_id": strconv.Itoa(channel.Id), "display_name": channel.Name, "channel_type": constant.GetChannelTypeName(channel.Type), "object_status": status})
		}
		if len(objects) > limit {
			return ErrOpsInventoryTooLarge
		}
		types := []string{"user", "api_key", "channel"}
		body, err := common.Marshal(map[string]any{"objects": objects, "snapshotTypes": types})
		if err != nil {
			return err
		}
		encoded, err = common.Marshal(map[string]any{"gatewayId": gateway, "sourceSystem": "gateway-cn", "schemaVersion": 1, "snapshotId": CNIdentity(gateway, string(body)), "snapshotTypes": types, "objects": objects})
		if err != nil {
			return err
		}
		if len(encoded) > 8<<20 {
			return ErrOpsInventoryTooLarge
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return encoded, err
}
