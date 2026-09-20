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
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMCPAccessCredentialStoresOnlyHashAndEnforcesControls(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:mcp-credential-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&User{}, &MCPAccessCredential{}))
	require.NoError(t, db.AutoMigrate(&User{}, &MCPAccessCredential{}))
	require.NoError(t, db.Create(&User{Id: 7, Username: "root-credential-owner", Role: common.RoleRootUser, Status: common.UserStatusEnabled}).Error)

	expiresAt := common.GetTimestamp() + 3600
	credential, secret, err := CreateMCPAccessCredential(7, "external pricing reader", &expiresAt, "203.0.113.0/24, 2001:db8::1")
	require.NoError(t, err)
	require.Greater(t, len(secret), len(credential.TokenPrefix))
	assert.NotEqual(t, secret, credential.TokenHash)
	assert.NotContains(t, credential.TokenHash, secret)

	var stored MCPAccessCredential
	require.NoError(t, db.First(&stored, credential.ID).Error)
	assert.NotEqual(t, secret, stored.TokenHash)
	assert.Equal(t, "203.0.113.0/24\n2001:db8::1/128", stored.AllowIPs)

	authenticated, err := AuthenticateMCPAccessCredential(secret, "203.0.113.10")
	require.NoError(t, err)
	assert.Equal(t, credential.ID, authenticated.ID)
	require.NotNil(t, authenticated.LastUsedAt)

	_, err = AuthenticateMCPAccessCredential(secret, "198.51.100.10")
	assert.ErrorIs(t, err, ErrMCPAccessCredentialIPNotAllowed)

	revoked, err := RevokeMCPAccessCredential(7, credential.ID)
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)
	_, err = AuthenticateMCPAccessCredential(secret, "203.0.113.10")
	assert.ErrorIs(t, err, ErrMCPAccessCredentialUnavailable)

	_, err = RevokeMCPAccessCredential(8, credential.ID)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))

	require.NoError(t, db.Model(&User{}).Where("id = ?", 7).Update("status", common.UserStatusDisabled).Error)
	activeCredential, activeSecret, err := CreateMCPAccessCredential(7, "inactive owner", nil, "")
	require.NoError(t, err)
	_, err = AuthenticateMCPAccessCredential(activeSecret, "203.0.113.10")
	assert.ErrorIs(t, err, ErrMCPAccessCredentialUnavailable)
	assert.Positive(t, activeCredential.ID)
}

func TestRotateMCPAccessCredentialImmediatelyInvalidatesPreviousSecret(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:mcp-credential-rotate-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&User{}, &MCPAccessCredential{}))
	require.NoError(t, db.Create(&User{Id: 7, Username: "root-rotation-owner", Role: common.RoleRootUser, Status: common.UserStatusEnabled}).Error)
	credential, previousSecret, err := CreateMCPAccessCredential(7, "rotate me", nil, "")
	require.NoError(t, err)
	replacement, replacementSecret, err := RotateMCPAccessCredential(7, credential.ID)
	require.NoError(t, err)
	assert.NotEqual(t, credential.ID, replacement.ID)
	assert.NotEqual(t, previousSecret, replacementSecret)
	_, err = AuthenticateMCPAccessCredential(previousSecret, "203.0.113.10")
	assert.ErrorIs(t, err, ErrMCPAccessCredentialUnavailable)
	authenticated, err := AuthenticateMCPAccessCredential(replacementSecret, "203.0.113.10")
	require.NoError(t, err)
	assert.Equal(t, replacement.ID, authenticated.ID)
}
