package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// ResolveUACUser reuses the external identity ownership table. A digest of the
// exact (issuer, subject) pair fits its portable indexed subject column.
// Email is never used for account lookup or automatic account linking.
func ResolveUACUser(issuer, subject, name string, initialRole int) (*User, error) {
	if issuer == "" || subject == "" || (initialRole != common.RoleAdminUser && initialRole != common.RoleRootUser) {
		return nil, errors.New("invalid UAC identity")
	}
	digest := sha256.Sum256([]byte(issuer + "\x00" + subject))
	key := hex.EncodeToString(digest[:])
	var user User
	err := DB.Transaction(func(tx *gorm.DB) error {
		var claim ExternalIdentityClaim
		err := tx.Where("provider = ? AND subject = ?", "uac", key).First(&claim).Error
		if err == nil {
			return tx.First(&user, claim.UserId).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		display := []rune(strings.TrimSpace(name))
		if len(display) > 20 {
			display = display[:20]
		}
		if len(display) == 0 {
			display = []rune("统一认证运维人员")
		}
		user = User{Username: "uac_" + key[:32], DisplayName: string(display), Role: initialRole, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
		if err := user.InsertWithTx(tx, 0); err != nil {
			return err
		}
		return ClaimExternalIdentityWithTx(tx, "uac", key, user.Id)
	})
	if err != nil {
		// A concurrent first login may have committed the single-owner claim.
		var claim ExternalIdentityClaim
		if lookup := DB.Where("provider = ? AND subject = ?", "uac", key).First(&claim).Error; lookup != nil {
			return nil, err
		}
		if lookup := DB.First(&user, claim.UserId).Error; lookup != nil {
			return nil, lookup
		}
	}
	if user.Status != common.UserStatusEnabled || user.Role < common.RoleAdminUser {
		return nil, errors.New("local administrator account is unavailable")
	}
	return &user, nil
}
