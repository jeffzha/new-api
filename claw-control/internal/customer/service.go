package customer

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/pagination"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

var customerCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,78}[a-z0-9]$`)

type Service struct {
	db               *gorm.DB
	environment      string
	identityResolver IdentityResolver
}

// IdentityResolver resolves the current authoritative new-api identity state.
// Implementations must perform a fresh lookup and must not use a positive cache.
type IdentityResolver interface {
	ResolveEnabled(ctx context.Context, userID int64) (identityVersion string, err error)
}

type CreateCommand struct {
	CustomerCode  string
	DisplayName   string
	BillingUserID *int64
	Actor         string
	RequestID     string
}

type AddMemberCommand struct {
	CustomerID   uint64
	NewAPIUserID int64
	Role         string
	Actor        string
	RequestID    string
}

type UpdateCommand struct {
	CustomerID      uint64
	ExpectedVersion int64
	DisplayName     string
	BillingUserID   *int64
	Actor           string
	RequestID       string
}

type UpdateMemberCommand struct {
	CustomerID        uint64
	NewAPIUserID      int64
	ExpectedAuthEpoch int64
	Role              string
	Actor             string
	RequestID         string
}

type ArchiveCommand struct {
	CustomerID      uint64
	ExpectedVersion int64
	Actor           string
	Reason          string
	RequestID       string
}

type MemberResult struct {
	Member   *model.CustomerMember  `json:"member"`
	Identity *model.IdentityBinding `json:"identity"`
}

func New(db *gorm.DB, environment string, identityResolver IdentityResolver) *Service {
	return &Service{db: db, environment: environment, identityResolver: identityResolver}
}

func (s *Service) Create(command CreateCommand) (*model.Customer, error) {
	command.CustomerCode = strings.ToLower(strings.TrimSpace(command.CustomerCode))
	command.DisplayName = strings.TrimSpace(command.DisplayName)
	if !customerCodePattern.MatchString(command.CustomerCode) {
		return nil, domain.Invalid("customer_code must be 3-80 lowercase letters, digits, or hyphens")
	}
	if command.DisplayName == "" || len(command.DisplayName) > 160 {
		return nil, domain.Invalid("display_name is required and must be at most 160 characters")
	}
	if command.BillingUserID != nil && *command.BillingUserID <= 0 {
		return nil, domain.Invalid("billing_user_id must be positive")
	}
	customer := &model.Customer{
		CustomerCode:  command.CustomerCode,
		DisplayName:   command.DisplayName,
		Status:        model.CustomerStatusActive,
		BillingUserID: command.BillingUserID,
		RowVersion:    1,
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(customer).Error; err != nil {
			return domain.Conflict("customer_code already exists or customer could not be created")
		}
		return support.Audit(tx, &customer.ID, command.Actor, "customer.create", "customer", support.ResourceID(customer.ID), nil, customer, "", command.RequestID)
	})
	return customer, err
}

func (s *Service) List(limit int) ([]model.Customer, error) {
	page, err := s.ListPage(0, limit)
	return page.Items, err
}

func (s *Service) ListPage(beforeID uint64, limit int) (pagination.Page[model.Customer], error) {
	limit = pagination.Limit(limit)
	var customers []model.Customer
	query := s.db.Order("id desc").Limit(limit + 1)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	if err := query.Find(&customers).Error; err != nil {
		return pagination.Page[model.Customer]{}, err
	}
	return pagination.Trim(customers, limit, func(value model.Customer) uint64 { return value.ID }), nil
}

func (s *Service) Update(command UpdateCommand) (*model.Customer, error) {
	command.DisplayName = strings.TrimSpace(command.DisplayName)
	if command.CustomerID == 0 || command.ExpectedVersion <= 0 {
		return nil, domain.Invalid("customer_id and expected_version are required")
	}
	if command.DisplayName == "" || len(command.DisplayName) > 160 {
		return nil, domain.Invalid("display_name is required and must be at most 160 characters")
	}
	if command.BillingUserID != nil && *command.BillingUserID <= 0 {
		return nil, domain.Invalid("billing_user_id must be positive")
	}
	var result model.Customer
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).First(&result, command.CustomerID).Error; err != nil {
			return domain.NotFound("customer not found")
		}
		if result.RowVersion != command.ExpectedVersion {
			return domain.Conflict("customer row version changed; expected %d, current %d", command.ExpectedVersion, result.RowVersion)
		}
		if result.Status == model.CustomerStatusArchived {
			return domain.Conflict("archived customer cannot be updated")
		}
		before := result
		result.DisplayName = command.DisplayName
		result.BillingUserID = command.BillingUserID
		result.RowVersion++
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		return support.Audit(tx, &result.ID, command.Actor, "customer.update", "customer", support.ResourceID(result.ID), &before, &result, "", command.RequestID)
	})
	return &result, err
}

func (s *Service) AddMember(ctx context.Context, command AddMemberCommand) (*MemberResult, error) {
	if command.CustomerID == 0 || command.NewAPIUserID <= 0 {
		return nil, domain.Invalid("customer_id and new_api_user_id are required")
	}
	command.Role = strings.ToLower(strings.TrimSpace(command.Role))
	switch command.Role {
	case "owner", "admin", "member", "viewer":
	default:
		return nil, domain.Invalid("role must be owner, admin, member, or viewer")
	}
	if s.identityResolver == nil {
		return nil, fmt.Errorf("new-api identity resolver is not configured")
	}
	// This network lookup deliberately happens before the database transaction:
	// failure is fail-closed, while database locks are never held across I/O.
	identityVersion, err := s.identityResolver.ResolveEnabled(ctx, command.NewAPIUserID)
	if err != nil {
		return nil, err
	}
	if identityVersion == "" || len(identityVersion) > 128 || strings.TrimSpace(identityVersion) != identityVersion {
		return nil, fmt.Errorf("new-api identity resolver returned an invalid identity version")
	}
	result := &MemberResult{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var customer model.Customer
		if err := database.ForUpdate(tx).First(&customer, command.CustomerID).Error; err != nil {
			return domain.NotFound("customer not found")
		}
		if customer.Status == model.CustomerStatusDisabled || customer.Status == model.CustomerStatusArchived {
			return domain.Conflict("customer does not accept new members in status %s", customer.Status)
		}
		var existingMember model.CustomerMember
		existingMemberQuery := database.ForUpdate(tx).
			Where("customer_id = ? AND new_api_user_id = ?", command.CustomerID, command.NewAPIUserID).
			First(&existingMember)
		if existingMemberQuery.Error != nil && !errors.Is(existingMemberQuery.Error, gorm.ErrRecordNotFound) {
			return existingMemberQuery.Error
		}
		if existingMemberQuery.Error == nil {
			if existingMember.Status != model.MemberStatusDisabled {
				return domain.Conflict("customer membership already exists")
			}
			beforeMember := existingMember
			var existingIdentity model.IdentityBinding
			existingIdentityQuery := database.ForUpdate(tx).
				Where("customer_id = ? AND new_api_user_id = ?", command.CustomerID, command.NewAPIUserID).
				First(&existingIdentity)
			if existingIdentityQuery.Error != nil && !errors.Is(existingIdentityQuery.Error, gorm.ErrRecordNotFound) {
				return existingIdentityQuery.Error
			}

			nextAuthEpoch := existingMember.AuthEpoch + 1
			if existingIdentityQuery.Error == nil && existingIdentity.AuthEpoch >= nextAuthEpoch {
				nextAuthEpoch = existingIdentity.AuthEpoch + 1
			}
			existingMember.Role = command.Role
			existingMember.Status = model.MemberStatusActive
			existingMember.MembershipSlot = model.MembershipSlot(command.CustomerID)
			existingMember.AuthEpoch = nextAuthEpoch
			existingMember.DisabledAt = nil
			if err := tx.Save(&existingMember).Error; err != nil {
				return err
			}

			var identity *model.IdentityBinding
			if errors.Is(existingIdentityQuery.Error, gorm.ErrRecordNotFound) {
				identity = &model.IdentityBinding{
					PublicID:         support.PublicID("wid"),
					CustomerID:       command.CustomerID,
					NewAPIUserID:     command.NewAPIUserID,
					CanonicalSubject: fmt.Sprintf("napi:%s:customer:%d:user:%d", s.environment, command.CustomerID, command.NewAPIUserID),
					IdentityVersion:  identityVersion,
					Status:           model.IdentityStatusProvisioning,
					AuthEpoch:        nextAuthEpoch,
					RowVersion:       1,
				}
				if err := tx.Create(identity).Error; err != nil {
					return domain.Conflict("identity binding could not be recreated")
				}
			} else {
				beforeIdentity := existingIdentity
				existingIdentity.IdentityVersion = identityVersion
				existingIdentity.Status = model.IdentityStatusProvisioning
				if existingIdentity.ADPAccountID != "" {
					existingIdentity.Status = model.IdentityStatusActive
				}
				existingIdentity.AuthEpoch = nextAuthEpoch
				existingIdentity.RowVersion++
				existingIdentity.DisabledAt = nil
				if err := tx.Save(&existingIdentity).Error; err != nil {
					return err
				}
				if err := support.Audit(tx, &customer.ID, command.Actor, "identity.reactivate", "identity_binding", existingIdentity.PublicID, &beforeIdentity, &existingIdentity, "", command.RequestID); err != nil {
					return err
				}
				identity = &existingIdentity
			}
			if err := support.Audit(tx, &customer.ID, command.Actor, "member.reactivate", "customer_member", support.ResourceID(existingMember.ID), &beforeMember, &existingMember, "", command.RequestID); err != nil {
				return err
			}
			result.Member = &existingMember
			result.Identity = identity
			return nil
		}
		member := &model.CustomerMember{
			CustomerID:     command.CustomerID,
			NewAPIUserID:   command.NewAPIUserID,
			Role:           command.Role,
			Status:         model.MemberStatusActive,
			MembershipSlot: model.MembershipSlot(command.CustomerID),
			AuthEpoch:      1,
		}
		if err := tx.Create(member).Error; err != nil {
			return domain.Conflict("customer membership already exists")
		}
		identity := &model.IdentityBinding{
			PublicID:         support.PublicID("wid"),
			CustomerID:       command.CustomerID,
			NewAPIUserID:     command.NewAPIUserID,
			CanonicalSubject: fmt.Sprintf("napi:%s:customer:%d:user:%d", s.environment, command.CustomerID, command.NewAPIUserID),
			IdentityVersion:  identityVersion,
			Status:           model.IdentityStatusProvisioning,
			AuthEpoch:        1,
			RowVersion:       1,
		}
		if err := tx.Create(identity).Error; err != nil {
			return domain.Conflict("identity binding already exists")
		}
		if err := support.Audit(tx, &customer.ID, command.Actor, "member.add", "customer_member", support.ResourceID(member.ID), nil, member, "", command.RequestID); err != nil {
			return err
		}
		result.Member = member
		result.Identity = identity
		return nil
	})
	return result, err
}

func (s *Service) UpdateMemberRole(command UpdateMemberCommand) (*model.CustomerMember, error) {
	if command.CustomerID == 0 || command.NewAPIUserID <= 0 || command.ExpectedAuthEpoch <= 0 {
		return nil, domain.Invalid("customer_id, new_api_user_id, and expected_auth_epoch are required")
	}
	command.Role = strings.ToLower(strings.TrimSpace(command.Role))
	switch command.Role {
	case "owner", "admin", "member", "viewer":
	default:
		return nil, domain.Invalid("role must be owner, admin, member, or viewer")
	}
	var result model.CustomerMember
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).Where("customer_id = ? AND new_api_user_id = ?", command.CustomerID, command.NewAPIUserID).First(&result).Error; err != nil {
			return domain.NotFound("customer member not found")
		}
		if result.Status != model.MemberStatusActive {
			return domain.Conflict("only an active member can change role")
		}
		if result.AuthEpoch != command.ExpectedAuthEpoch {
			return domain.Conflict("member auth epoch changed; expected %d, current %d", command.ExpectedAuthEpoch, result.AuthEpoch)
		}
		if result.Role == command.Role {
			return nil
		}
		before := result
		result.Role = command.Role
		result.AuthEpoch++
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		var binding model.IdentityBinding
		if err := database.ForUpdate(tx).Where("customer_id = ? AND new_api_user_id = ?", command.CustomerID, command.NewAPIUserID).First(&binding).Error; err == nil {
			binding.AuthEpoch++
			binding.RowVersion++
			if err := tx.Save(&binding).Error; err != nil {
				return err
			}
		}
		if err := support.Enqueue(tx, &command.CustomerID, "SESSION_REVOKE", fmt.Sprintf("member-role:%d:%d:%d", command.CustomerID, command.NewAPIUserID, result.AuthEpoch), map[string]any{
			"customer_id": command.CustomerID, "new_api_user_id": command.NewAPIUserID, "auth_epoch": result.AuthEpoch,
		}); err != nil {
			return err
		}
		return support.Audit(tx, &command.CustomerID, command.Actor, "member.role.update", "customer_member", support.ResourceID(result.ID), &before, &result, "", command.RequestID)
	})
	return &result, err
}

func (s *Service) DisableMember(customerID uint64, userID int64, actor, reason, requestID string) error {
	if customerID == 0 || userID <= 0 || strings.TrimSpace(reason) == "" {
		return domain.Invalid("customer_id, user_id, and reason are required")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var member model.CustomerMember
		if err := database.ForUpdate(tx).Where("customer_id = ? AND new_api_user_id = ?", customerID, userID).First(&member).Error; err != nil {
			return domain.NotFound("customer member not found")
		}
		if member.Status == model.MemberStatusDisabled {
			return nil
		}
		before := member
		now := time.Now().UTC()
		member.Status = model.MemberStatusDisabled
		member.MembershipSlot = fmt.Sprintf("historical:%d", member.ID)
		member.AuthEpoch++
		member.DisabledAt = &now
		if err := tx.Save(&member).Error; err != nil {
			return err
		}
		var identity model.IdentityBinding
		if err := database.ForUpdate(tx).Where("customer_id = ? AND new_api_user_id = ?", customerID, userID).First(&identity).Error; err == nil {
			identity.Status = model.IdentityStatusDisabled
			identity.AuthEpoch++
			identity.RowVersion++
			identity.DisabledAt = &now
			if err := tx.Save(&identity).Error; err != nil {
				return err
			}
		}
		if err := support.Enqueue(tx, &customerID, "SESSION_REVOKE", fmt.Sprintf("member-revoke:%d:%d:%d", customerID, userID, member.AuthEpoch), map[string]any{
			"customer_id": customerID, "new_api_user_id": userID, "auth_epoch": member.AuthEpoch,
		}); err != nil {
			return err
		}
		return support.Audit(tx, &customerID, actor, "member.disable", "customer_member", support.ResourceID(member.ID), &before, &member, reason, requestID)
	})
}

func (s *Service) Archive(command ArchiveCommand) (*model.Customer, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	if command.CustomerID == 0 || command.ExpectedVersion <= 0 || command.Reason == "" {
		return nil, domain.Invalid("customer_id, expected_version, and reason are required")
	}
	var result model.Customer
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).First(&result, command.CustomerID).Error; err != nil {
			return domain.NotFound("customer not found")
		}
		if result.Status == model.CustomerStatusArchived {
			return nil
		}
		if result.RowVersion != command.ExpectedVersion {
			return domain.Conflict("customer row version changed; expected %d, current %d", command.ExpectedVersion, result.RowVersion)
		}
		var unsafeApps int64
		if err := tx.Model(&model.CustomerApp{}).Where(
			"customer_id = ? AND status NOT IN ?", command.CustomerID,
			[]string{model.AppStatusDisabled, model.AppStatusArchived},
		).Count(&unsafeApps).Error; err != nil {
			return err
		}
		if unsafeApps > 0 {
			return domain.Conflict("all customer Apps must be disabled before archival")
		}
		var unsettledPeriods int64
		if err := tx.Model(&model.PlanPeriod{}).Where(
			"customer_id = ? AND status NOT IN ?", command.CustomerID,
			[]string{model.PeriodStatusExpired, model.PeriodStatusCanceled},
		).Count(&unsettledPeriods).Error; err != nil {
			return err
		}
		if unsettledPeriods > 0 {
			return domain.Conflict("all plan periods must be expired or canceled before archival")
		}
		before := result
		now := time.Now().UTC()
		result.Status = model.CustomerStatusArchived
		result.ArchivedAt = &now
		result.RowVersion++
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		var members []model.CustomerMember
		if err := database.ForUpdate(tx).Where(
			"customer_id = ? AND status = ?", command.CustomerID, model.MemberStatusActive,
		).Find(&members).Error; err != nil {
			return err
		}
		for index := range members {
			members[index].Status = model.MemberStatusDisabled
			members[index].MembershipSlot = fmt.Sprintf("historical:%d", members[index].ID)
			members[index].AuthEpoch++
			members[index].DisabledAt = &now
			if err := tx.Save(&members[index]).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.IdentityBinding{}).Where(
			"customer_id = ? AND status <> ?", command.CustomerID, model.IdentityStatusDisabled,
		).Updates(map[string]any{
			"status": model.IdentityStatusDisabled, "auth_epoch": gorm.Expr("auth_epoch + 1"),
			"row_version": gorm.Expr("row_version + 1"), "disabled_at": now,
		}).Error; err != nil {
			return err
		}
		if err := support.Enqueue(tx, &command.CustomerID, "SESSION_REVOKE", fmt.Sprintf("customer-archive:%d:%d", command.CustomerID, result.RowVersion), map[string]any{
			"customer_id": command.CustomerID, "status": result.Status, "archived_at": now,
		}); err != nil {
			return err
		}
		return support.Audit(tx, &command.CustomerID, command.Actor, "customer.archive", "customer", support.ResourceID(result.ID), &before, &result, command.Reason, command.RequestID)
	})
	return &result, err
}
