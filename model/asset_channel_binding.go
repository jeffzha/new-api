package model

import (
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

type AssetChannelBinding struct {
	ID          int64  `json:"id" gorm:"primaryKey"`
	ExternalID  string `json:"external_id" gorm:"type:varchar(191);uniqueIndex"`
	NamespaceID string `json:"namespace_id" gorm:"type:varchar(100);index"`
	ScopeID     string `json:"scope_id" gorm:"type:varchar(100);index"`
	GroupID     string `json:"group_id" gorm:"type:varchar(191);index"`
	ChannelID   int    `json:"channel_id" gorm:"index"`
	TokenID     int    `json:"token_id" gorm:"index"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

func UpsertAssetChannelBinding(externalID string, namespaceID string, channelID int, tokenID int) error {
	return upsertAssetChannelBinding(externalID, "", namespaceID, "", channelID, tokenID)
}

func UpsertScopedAssetChannelBinding(
	externalID string,
	groupID string,
	namespaceID string,
	scopeID string,
	channelID int,
	tokenID int,
) error {
	if scopeID == "" {
		return fmt.Errorf("asset scope is required")
	}
	return upsertAssetChannelBinding(externalID, groupID, namespaceID, scopeID, channelID, tokenID)
}

func upsertAssetChannelBinding(
	externalID string,
	groupID string,
	namespaceID string,
	scopeID string,
	channelID int,
	tokenID int,
) error {
	if externalID == "" || namespaceID == "" || channelID <= 0 || tokenID <= 0 {
		return fmt.Errorf("asset channel binding is incomplete")
	}
	now := time.Now().Unix()
	binding := &AssetChannelBinding{
		ExternalID:  externalID,
		NamespaceID: namespaceID,
		ScopeID:     scopeID,
		GroupID:     groupID,
		ChannelID:   channelID,
		TokenID:     tokenID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "external_id"}},
		DoNothing: true,
	}).Create(binding).Error; err != nil {
		return err
	}

	var stored AssetChannelBinding
	if err := DB.Where("external_id = ?", externalID).First(&stored).Error; err != nil {
		return err
	}
	if stored.ChannelID != channelID || stored.NamespaceID != namespaceID {
		return fmt.Errorf("asset identifier is already bound to another channel or API token")
	}
	if scopeID == "" {
		if stored.TokenID != tokenID || stored.ScopeID != "" {
			return fmt.Errorf("asset identifier is already bound to another channel or API token")
		}
		return nil
	}
	if stored.ScopeID != "" && stored.ScopeID != scopeID {
		return fmt.Errorf("asset identifier is already bound to another asset scope")
	}
	if stored.ScopeID == "" && stored.TokenID != tokenID {
		return fmt.Errorf("asset identifier is already bound to another API token")
	}
	if stored.ScopeID == "" || (stored.GroupID == "" && groupID != "") {
		updates := map[string]interface{}{
			"scope_id":   scopeID,
			"updated_at": now,
		}
		if groupID != "" {
			updates["group_id"] = groupID
		}
		if err := DB.Model(&stored).Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}

func ResolveAssetChannelBinding(externalIDs []string, tokenID int) (int, bool, error) {
	if len(externalIDs) == 0 {
		return 0, false, nil
	}
	if tokenID <= 0 {
		return 0, false, fmt.Errorf("API token identity is required for asset affinity")
	}
	uniqueIDs := make([]string, 0, len(externalIDs))
	seen := make(map[string]struct{}, len(externalIDs))
	for _, externalID := range externalIDs {
		if externalID == "" {
			return 0, false, fmt.Errorf("asset reference is empty")
		}
		if _, ok := seen[externalID]; ok {
			continue
		}
		seen[externalID] = struct{}{}
		uniqueIDs = append(uniqueIDs, externalID)
	}
	var bindings []AssetChannelBinding
	if err := DB.Where("external_id IN ?", uniqueIDs).Find(&bindings).Error; err != nil {
		return 0, false, err
	}
	if len(bindings) == 0 {
		return 0, false, nil
	}
	if len(bindings) != len(uniqueIDs) {
		return 0, false, fmt.Errorf("some asset references have no channel binding")
	}
	channelID := bindings[0].ChannelID
	for _, binding := range bindings {
		if binding.ScopeID == "" {
			if binding.TokenID != tokenID {
				return 0, false, fmt.Errorf("asset reference belongs to another API token")
			}
		} else {
			allowed, err := AssetScopeAllowsToken(
				binding.NamespaceID,
				binding.ScopeID,
				binding.ChannelID,
				tokenID,
			)
			if err != nil {
				return 0, false, err
			}
			if !allowed {
				return 0, false, fmt.Errorf("asset reference belongs to another asset scope")
			}
		}
		if binding.ChannelID != channelID {
			return 0, false, fmt.Errorf("asset references belong to different upstream channels")
		}
	}
	return channelID, true, nil
}
