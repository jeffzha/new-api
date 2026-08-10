package identity

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

type Service struct {
	db *gorm.DB
}

type ConfirmCommand struct {
	BindingID         string
	CanonicalSubject  string
	ADPAccountID      string
	ADPAccountVersion int64
	Actor             string
	RequestID         string
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Confirm(command ConfirmCommand) (*model.IdentityBinding, error) {
	command.BindingID = strings.TrimSpace(command.BindingID)
	command.CanonicalSubject = strings.TrimSpace(command.CanonicalSubject)
	command.ADPAccountID = strings.TrimSpace(command.ADPAccountID)
	if command.BindingID == "" || command.CanonicalSubject == "" || command.ADPAccountID == "" || command.ADPAccountVersion <= 0 {
		return nil, domain.Invalid("binding_id, canonical_subject, adp_account_id, and positive adp_account_version are required")
	}
	var result model.IdentityBinding
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).Where("public_id = ?", command.BindingID).First(&result).Error; err != nil {
			return domain.NotFound("identity binding not found")
		}
		if result.CanonicalSubject != command.CanonicalSubject {
			return domain.Conflict("canonical subject does not match binding")
		}
		if result.Status == model.IdentityStatusDisabled {
			return domain.Forbidden("identity binding is disabled")
		}
		var accountBinding model.ADPAccountBinding
		accountBindingQuery := database.ForUpdate(tx).Where("adp_account_id = ?", command.ADPAccountID).First(&accountBinding)
		if accountBindingQuery.Error != nil && accountBindingQuery.Error != gorm.ErrRecordNotFound {
			return accountBindingQuery.Error
		}
		if accountBindingQuery.Error == nil && accountBinding.IdentityBindingID != result.ID {
			return domain.Conflict("ADP account is already bound to another identity")
		}
		var identityAccountBinding model.ADPAccountBinding
		identityBindingQuery := database.ForUpdate(tx).Where("identity_binding_id = ?", result.ID).First(&identityAccountBinding)
		if identityBindingQuery.Error != nil && identityBindingQuery.Error != gorm.ErrRecordNotFound {
			return identityBindingQuery.Error
		}
		if identityBindingQuery.Error == nil && identityAccountBinding.ADPAccountID != command.ADPAccountID {
			return domain.Conflict("identity is already bound to another ADP account")
		}
		var legacyConflict int64
		if err := tx.Model(&model.IdentityBinding{}).
			Where("adp_account_id = ? AND id <> ?", command.ADPAccountID, result.ID).
			Count(&legacyConflict).Error; err != nil {
			return err
		}
		if legacyConflict > 0 {
			return domain.Conflict("ADP account is already used by another identity")
		}
		if result.ADPAccountID != "" {
			if result.ADPAccountID != command.ADPAccountID || result.ADPAccountVersion != command.ADPAccountVersion {
				return domain.Conflict("identity binding already points to a different ADP account")
			}
			if identityBindingQuery.Error == gorm.ErrRecordNotFound {
				if err := tx.Create(&model.ADPAccountBinding{
					IdentityBindingID: result.ID, ADPAccountID: command.ADPAccountID,
					CustomerID: result.CustomerID, NewAPIUserID: result.NewAPIUserID,
				}).Error; err != nil {
					if errors.Is(err, gorm.ErrDuplicatedKey) {
						return domain.Conflict("ADP account is already bound to another identity")
					}
					return err
				}
			}
			return nil
		}
		if identityBindingQuery.Error == gorm.ErrRecordNotFound {
			if err := tx.Create(&model.ADPAccountBinding{
				IdentityBindingID: result.ID, ADPAccountID: command.ADPAccountID,
				CustomerID: result.CustomerID, NewAPIUserID: result.NewAPIUserID,
			}).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return domain.Conflict("ADP account is already bound to another identity")
				}
				return err
			}
		}
		before := result
		now := time.Now().UTC()
		result.ADPAccountID = command.ADPAccountID
		result.ADPAccountVersion = command.ADPAccountVersion
		result.Status = model.IdentityStatusActive
		result.ConfirmedAt = &now
		result.RowVersion++
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		return support.Audit(tx, &result.CustomerID, command.Actor, "identity.confirm", "identity_binding", result.PublicID, &before, &result, "", command.RequestID)
	})
	return &result, err
}
