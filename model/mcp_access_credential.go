/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const MCPAccessScopePublicPricing = "platform_pricing.public"

var (
	ErrMCPAccessCredentialInvalid      = errors.New("invalid MCP credential")
	ErrMCPAccessCredentialUnavailable  = errors.New("inactive MCP credential")
	ErrMCPAccessCredentialIPNotAllowed = errors.New("MCP credential IP is not allowed")
)

// MCPAccessCredential is intentionally separate from relay API keys and
// dashboard PATs. Only a SHA-256 digest is persisted; its bearer secret is
// returned once when created and cannot be recovered afterwards.
type MCPAccessCredential struct {
	ID          int64  `json:"id" gorm:"primaryKey"`
	OwnerUserID int    `json:"owner_user_id" gorm:"not null;index"`
	Name        string `json:"name" gorm:"type:varchar(80);not null"`
	TokenPrefix string `json:"token_prefix" gorm:"type:varchar(24);not null;uniqueIndex"`
	TokenHash   string `json:"-" gorm:"type:char(64);not null;uniqueIndex"`
	Scope       string `json:"scope" gorm:"type:varchar(64);not null;index"`
	AllowIPs    string `json:"allow_ips" gorm:"type:text"`
	CreatedAt   int64  `json:"created_at" gorm:"not null"`
	ExpiresAt   *int64 `json:"expires_at,omitempty" gorm:"index"`
	LastUsedAt  *int64 `json:"last_used_at,omitempty"`
	RevokedAt   *int64 `json:"revoked_at,omitempty" gorm:"index"`
}

func (MCPAccessCredential) TableName() string { return "mcp_access_credentials" }

type MCPAccessCredentialSummary struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	TokenPrefix string `json:"token_prefix"`
	Scope       string `json:"scope"`
	AllowIPs    string `json:"allow_ips"`
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   *int64 `json:"expires_at,omitempty"`
	LastUsedAt  *int64 `json:"last_used_at,omitempty"`
	RevokedAt   *int64 `json:"revoked_at,omitempty"`
}

func (credential MCPAccessCredential) Summary() MCPAccessCredentialSummary {
	return MCPAccessCredentialSummary{ID: credential.ID, Name: credential.Name, TokenPrefix: credential.TokenPrefix, Scope: credential.Scope, AllowIPs: credential.AllowIPs, CreatedAt: credential.CreatedAt, ExpiresAt: credential.ExpiresAt, LastUsedAt: credential.LastUsedAt, RevokedAt: credential.RevokedAt}
}

func normalizeMCPAllowIPs(value string) (string, error) {
	var normalized []string
	seen := map[string]struct{}{}
	for entry := range strings.FieldsSeq(strings.ReplaceAll(value, ",", " ")) {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			address, addressErr := netip.ParseAddr(entry)
			if addressErr != nil {
				return "", fmt.Errorf("invalid IP allowlist entry %q", entry)
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		canonical := prefix.Masked().String()
		if _, ok := seen[canonical]; !ok {
			seen[canonical] = struct{}{}
			normalized = append(normalized, canonical)
		}
	}
	if len(normalized) > 32 {
		return "", errors.New("IP allowlist supports at most 32 entries")
	}
	return strings.Join(normalized, "\n"), nil
}

func CreateMCPAccessCredential(ownerUserID int, name string, expiresAt *int64, allowIPs string) (MCPAccessCredential, string, error) {
	name = strings.TrimSpace(name)
	if ownerUserID <= 0 || name == "" || len([]rune(name)) > 80 {
		return MCPAccessCredential{}, "", errors.New("credential name must contain 1 to 80 characters")
	}
	if expiresAt != nil && *expiresAt <= common.GetTimestamp() {
		return MCPAccessCredential{}, "", errors.New("credential expiry must be in the future")
	}
	allowIPs, err := normalizeMCPAllowIPs(allowIPs)
	if err != nil {
		return MCPAccessCredential{}, "", err
	}
	secretPart, err := common.GenerateRandomCharsKey(48)
	if err != nil {
		return MCPAccessCredential{}, "", err
	}
	secret := "mcp_" + secretPart
	digest := sha256.Sum256([]byte(secret))
	credential := MCPAccessCredential{OwnerUserID: ownerUserID, Name: name, TokenPrefix: secret[:16], TokenHash: fmt.Sprintf("%x", digest), Scope: MCPAccessScopePublicPricing, AllowIPs: allowIPs, CreatedAt: common.GetTimestamp(), ExpiresAt: expiresAt}
	if err := DB.Create(&credential).Error; err != nil {
		return MCPAccessCredential{}, "", err
	}
	return credential, secret, nil
}

func ListMCPAccessCredentials(ownerUserID int) ([]MCPAccessCredential, error) {
	var credentials []MCPAccessCredential
	err := DB.Where("owner_user_id = ?", ownerUserID).Order("id desc").Find(&credentials).Error
	return credentials, err
}

func RevokeMCPAccessCredential(ownerUserID int, id int64) (*MCPAccessCredential, error) {
	var credential MCPAccessCredential
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ? AND owner_user_id = ?", id, ownerUserID).First(&credential).Error; err != nil {
			return err
		}
		if credential.RevokedAt != nil {
			return nil
		}
		now := common.GetTimestamp()
		return tx.Model(&credential).Update("revoked_at", now).Error
	})
	if err != nil {
		return nil, err
	}
	return &credential, nil
}

// RotateMCPAccessCredential revokes an existing external credential and creates
// its replacement in one transaction. The old bearer secret is never read or
// returned, and the replacement secret is returned only to this caller once.
func RotateMCPAccessCredential(ownerUserID int, id int64) (MCPAccessCredential, string, error) {
	if ownerUserID <= 0 || id <= 0 {
		return MCPAccessCredential{}, "", ErrMCPAccessCredentialInvalid
	}
	secretPart, err := common.GenerateRandomCharsKey(48)
	if err != nil {
		return MCPAccessCredential{}, "", err
	}
	secret := "mcp_" + secretPart
	digest := sha256.Sum256([]byte(secret))
	var replacement MCPAccessCredential
	err = DB.Transaction(func(tx *gorm.DB) error {
		var credential MCPAccessCredential
		if err := lockForUpdate(tx).Where("id = ? AND owner_user_id = ?", id, ownerUserID).First(&credential).Error; err != nil {
			return err
		}
		now := common.GetTimestamp()
		if credential.RevokedAt != nil || (credential.ExpiresAt != nil && *credential.ExpiresAt <= now) {
			return ErrMCPAccessCredentialUnavailable
		}
		if err := tx.Model(&credential).Update("revoked_at", now).Error; err != nil {
			return err
		}
		replacement = MCPAccessCredential{
			OwnerUserID: credential.OwnerUserID,
			Name:        credential.Name,
			TokenPrefix: secret[:16],
			TokenHash:   fmt.Sprintf("%x", digest),
			Scope:       credential.Scope,
			AllowIPs:    credential.AllowIPs,
			CreatedAt:   now,
			ExpiresAt:   credential.ExpiresAt,
		}
		return tx.Create(&replacement).Error
	})
	if err != nil {
		return MCPAccessCredential{}, "", err
	}
	return replacement, secret, nil
}

func AuthenticateMCPAccessCredential(secret, clientIP string) (*MCPAccessCredential, error) {
	if !strings.HasPrefix(secret, "mcp_") || len(secret) < 20 {
		return nil, ErrMCPAccessCredentialInvalid
	}
	digest := sha256.Sum256([]byte(secret))
	var credential MCPAccessCredential
	if err := DB.Where("token_hash = ?", fmt.Sprintf("%x", digest)).First(&credential).Error; err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	if credential.Scope != MCPAccessScopePublicPricing || credential.RevokedAt != nil || (credential.ExpiresAt != nil && *credential.ExpiresAt <= now) {
		return nil, ErrMCPAccessCredentialUnavailable
	}
	owner, err := GetUserById(credential.OwnerUserID, false)
	if err != nil || owner.Status != common.UserStatusEnabled || owner.Role != common.RoleRootUser {
		return nil, ErrMCPAccessCredentialUnavailable
	}
	if credential.AllowIPs != "" {
		address, err := netip.ParseAddr(clientIP)
		if err != nil {
			return nil, ErrMCPAccessCredentialIPNotAllowed
		}
		allowed := false
		for entry := range strings.Lines(credential.AllowIPs) {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(entry))
			if err == nil && prefix.Contains(address) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, ErrMCPAccessCredentialIPNotAllowed
		}
	}
	_ = DB.Model(&credential).Update("last_used_at", now).Error
	credential.LastUsedAt = &now
	return &credential, nil
}
