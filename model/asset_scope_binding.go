package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AssetScopeTokenBinding struct {
	ID          int64  `json:"id" gorm:"primaryKey"`
	NamespaceID string `json:"namespace_id" gorm:"type:varchar(100);uniqueIndex:idx_asset_scope_token_namespace"`
	TokenID     int    `json:"token_id" gorm:"uniqueIndex:idx_asset_scope_token_namespace"`
	ScopeID     string `json:"scope_id" gorm:"type:varchar(100);index"`
	ChannelID   int    `json:"channel_id" gorm:"index"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type AssetGroupScopeBinding struct {
	ID               int64  `json:"id" gorm:"primaryKey"`
	ExternalID       string `json:"external_id" gorm:"type:varchar(191);uniqueIndex"`
	NamespaceID      string `json:"namespace_id" gorm:"type:varchar(100);index"`
	ScopeID          string `json:"scope_id" gorm:"type:varchar(100);index"`
	ChannelID        int    `json:"channel_id" gorm:"index"`
	CreatedByTokenID int    `json:"created_by_token_id" gorm:"index"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

type AssetAuthSessionBinding struct {
	ID               int64  `json:"id" gorm:"primaryKey"`
	TokenHash        string `json:"token_hash" gorm:"type:varchar(64);uniqueIndex"`
	NamespaceID      string `json:"namespace_id" gorm:"type:varchar(100);index"`
	ScopeID          string `json:"scope_id" gorm:"type:varchar(100);index"`
	ChannelID        int    `json:"channel_id" gorm:"index"`
	CreatedByTokenID int    `json:"created_by_token_id" gorm:"index"`
	ExpiresAt        int64  `json:"expires_at" gorm:"index"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

func UpsertAssetScopeTokenBinding(namespaceID string, scopeID string, channelID int, tokenID int) error {
	if namespaceID == "" || scopeID == "" || channelID <= 0 || tokenID <= 0 {
		return fmt.Errorf("asset scope token binding is incomplete")
	}
	now := time.Now().Unix()
	binding := &AssetScopeTokenBinding{
		NamespaceID: namespaceID,
		TokenID:     tokenID,
		ScopeID:     scopeID,
		ChannelID:   channelID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "token_id"}},
		DoNothing: true,
	}).Create(binding).Error; err != nil {
		return err
	}
	var stored AssetScopeTokenBinding
	if err := DB.Where("namespace_id = ? AND token_id = ?", namespaceID, tokenID).First(&stored).Error; err != nil {
		return err
	}
	if stored.ScopeID != scopeID || stored.ChannelID != channelID {
		return fmt.Errorf("API token is already assigned to another asset scope or channel")
	}
	return nil
}

func AssetScopeAllowsToken(namespaceID string, scopeID string, channelID int, tokenID int) (bool, error) {
	if namespaceID == "" || scopeID == "" || channelID <= 0 || tokenID <= 0 {
		return false, nil
	}
	var count int64
	err := DB.Model(&AssetScopeTokenBinding{}).
		Where("namespace_id = ? AND scope_id = ? AND channel_id = ? AND token_id = ?", namespaceID, scopeID, channelID, tokenID).
		Count(&count).Error
	return count > 0, err
}

func UpsertAssetGroupScopeBinding(
	externalID string,
	namespaceID string,
	scopeID string,
	channelID int,
	tokenID int,
) error {
	if externalID == "" || namespaceID == "" || scopeID == "" || channelID <= 0 || tokenID <= 0 {
		return fmt.Errorf("asset group scope binding is incomplete")
	}
	now := time.Now().Unix()
	binding := &AssetGroupScopeBinding{
		ExternalID:       externalID,
		NamespaceID:      namespaceID,
		ScopeID:          scopeID,
		ChannelID:        channelID,
		CreatedByTokenID: tokenID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "external_id"}},
		DoNothing: true,
	}).Create(binding).Error; err != nil {
		return err
	}
	var stored AssetGroupScopeBinding
	if err := DB.Where("external_id = ?", externalID).First(&stored).Error; err != nil {
		return err
	}
	if stored.NamespaceID != namespaceID || stored.ScopeID != scopeID || stored.ChannelID != channelID {
		return fmt.Errorf("asset group identifier is already bound to another asset scope or channel")
	}
	return nil
}

func AssetGroupBelongsToScope(externalID string, namespaceID string, scopeID string, channelID int) (bool, error) {
	if externalID == "" || namespaceID == "" || scopeID == "" || channelID <= 0 {
		return false, nil
	}
	var count int64
	err := DB.Model(&AssetGroupScopeBinding{}).
		Where("external_id = ? AND namespace_id = ? AND scope_id = ? AND channel_id = ?", externalID, namespaceID, scopeID, channelID).
		Count(&count).Error
	return count > 0, err
}

func AssetBelongsToScope(externalID string, namespaceID string, scopeID string, channelID int) (bool, error) {
	if externalID == "" || namespaceID == "" || scopeID == "" || channelID <= 0 {
		return false, nil
	}
	var count int64
	err := DB.Model(&AssetChannelBinding{}).
		Where("external_id = ? AND namespace_id = ? AND scope_id = ? AND channel_id = ?", externalID, namespaceID, scopeID, channelID).
		Count(&count).Error
	return count > 0, err
}

func ListAssetGroupIDsForScope(namespaceID string, scopeID string, channelID int) ([]string, error) {
	groupIDs := make([]string, 0)
	err := DB.Model(&AssetGroupScopeBinding{}).
		Where("namespace_id = ? AND scope_id = ? AND channel_id = ?", namespaceID, scopeID, channelID).
		Order("external_id ASC").
		Pluck("external_id", &groupIDs).Error
	return groupIDs, err
}

func DeleteScopedAssetBinding(externalID string, namespaceID string, scopeID string, channelID int) error {
	return DB.Where(
		"external_id = ? AND namespace_id = ? AND scope_id = ? AND channel_id = ?",
		externalID,
		namespaceID,
		scopeID,
		channelID,
	).Delete(&AssetChannelBinding{}).Error
}

func DeleteScopedAssetGroupBinding(externalID string, namespaceID string, scopeID string, channelID int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(
			"group_id = ? AND namespace_id = ? AND scope_id = ? AND channel_id = ?",
			externalID,
			namespaceID,
			scopeID,
			channelID,
		).Delete(&AssetChannelBinding{}).Error; err != nil {
			return err
		}
		return tx.Where(
			"external_id = ? AND namespace_id = ? AND scope_id = ? AND channel_id = ?",
			externalID,
			namespaceID,
			scopeID,
			channelID,
		).Delete(&AssetGroupScopeBinding{}).Error
	})
}

func UpsertAssetAuthSessionBinding(
	bytedToken string,
	namespaceID string,
	scopeID string,
	channelID int,
	tokenID int,
	expiresInSeconds int64,
) error {
	if strings.TrimSpace(bytedToken) == "" || namespaceID == "" || scopeID == "" || channelID <= 0 || tokenID <= 0 {
		return fmt.Errorf("asset authentication session binding is incomplete")
	}
	now := time.Now().Unix()
	expiresAt := int64(0)
	if expiresInSeconds > 0 {
		expiresAt = now + expiresInSeconds
	}
	binding := &AssetAuthSessionBinding{
		TokenHash:        assetAuthTokenHash(bytedToken),
		NamespaceID:      namespaceID,
		ScopeID:          scopeID,
		ChannelID:        channelID,
		CreatedByTokenID: tokenID,
		ExpiresAt:        expiresAt,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "token_hash"}},
		DoNothing: true,
	}).Create(binding).Error; err != nil {
		return err
	}
	var stored AssetAuthSessionBinding
	if err := DB.Where("token_hash = ?", binding.TokenHash).First(&stored).Error; err != nil {
		return err
	}
	if stored.NamespaceID != namespaceID || stored.ScopeID != scopeID || stored.ChannelID != channelID {
		return fmt.Errorf("authentication session is already bound to another asset scope or channel")
	}
	return nil
}

func AssetAuthSessionBelongsToScope(
	bytedToken string,
	namespaceID string,
	scopeID string,
	channelID int,
) (bool, error) {
	if strings.TrimSpace(bytedToken) == "" || namespaceID == "" || scopeID == "" || channelID <= 0 {
		return false, nil
	}
	var binding AssetAuthSessionBinding
	err := DB.Where(
		"token_hash = ? AND namespace_id = ? AND scope_id = ? AND channel_id = ?",
		assetAuthTokenHash(bytedToken),
		namespaceID,
		scopeID,
		channelID,
	).First(&binding).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if binding.ExpiresAt > 0 && binding.ExpiresAt < time.Now().Unix() {
		return false, nil
	}
	return true, nil
}

func assetAuthTokenHash(bytedToken string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(bytedToken)))
	return hex.EncodeToString(sum[:])
}
