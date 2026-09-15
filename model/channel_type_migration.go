package model

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

// migratePrivateChannelTypeIDs moves the fork's historical channel IDs out
// of the upstream namespace. Metadata filters ensure legacy 59/60 rows are
// changed only when the row clearly belongs to this fork.
func migratePrivateChannelTypeIDs() error {
	if DB == nil {
		return nil
	}
	tx := DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	rollback := func(err error) error {
		tx.Rollback()
		return err
	}
	legacy := []struct{ oldType, newType int }{
		{59, constant.ChannelTypeSeedanceDomestic},
		{60, constant.ChannelTypeMobileCloudSeedance},
	}
	for _, item := range legacy {
		query := tx.Model(&Channel{}).Where("type = ?", item.oldType)
		switch item.oldType {
		case 59:
			query = query.Where("(lower(name) like ? OR lower(COALESCE(base_url, '')) like ? OR lower(models) like ?)", "%seedance%", "%laomandi%", "%doubao-seedance%")
		case 60:
			query = query.Where("(lower(name) like ? OR lower(COALESCE(base_url, '')) like ? OR lower(models) like ?)", "%mobile%", "%cmecloud%", "%mobilecloud%")
		}
		var channels []Channel
		if err := query.Find(&channels).Error; err != nil {
			return rollback(fmt.Errorf("find legacy channel type %d: %w", item.oldType, err))
		}
		if len(channels) == 0 {
			continue
		}
		ids := make([]int, 0, len(channels))
		for _, channel := range channels {
			ids = append(ids, channel.Id)
		}
		if err := tx.Model(&Channel{}).Where("id IN ?", ids).Update("type", item.newType).Error; err != nil {
			return rollback(fmt.Errorf("migrate channel type %d to %d: %w", item.oldType, item.newType, err))
		}
		if err := tx.Model(&Task{}).Where("channel_id IN ? AND platform = ?", ids, fmt.Sprintf("%d", item.oldType)).Update("platform", fmt.Sprintf("%d", item.newType)).Error; err != nil {
			return rollback(fmt.Errorf("migrate task platform %d to %d: %w", item.oldType, item.newType, err))
		}
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	common.SysLog("private channel type IDs migrated")
	return nil
}
