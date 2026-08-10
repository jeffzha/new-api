package usageaudit

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var sha256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var maximumImportedCost = decimal.RequireFromString("999999999999999999.999999999999999999")

type Service struct {
	db *gorm.DB
}

type CreateCommand struct {
	CustomerID           *uint64
	CustomerAppID        *uint64
	PlanPeriodID         *uint64
	PeriodStart          time.Time
	PeriodEnd            time.Time
	Source               string
	AllocationConfidence string
	ResourceIdentifier   string
	AllocationMethod     string
	UpstreamCostCNY      string
	Usage                map[string]string
	Note                 string
	Actor                string
	RequestID            string
}

type LockCommand struct {
	AuditID              string
	ExpectedVersion      int64
	CustomerID           *uint64
	CustomerAppID        *uint64
	PlanPeriodID         *uint64
	AllocationConfidence string
	ResourceIdentifier   string
	AllocationMethod     string
	EvidenceRef          string
	EvidenceHash         string
	Note                 string
	Actor                string
	RequestID            string
}

type ImportedDraftCommand struct {
	ImportSourceKey string
	PeriodStart     time.Time
	PeriodEnd       time.Time
	UpstreamCostCNY string
	Usage           map[string]string
	EvidenceRef     string
	EvidenceHash    string
	Note            string
	Actor           string
	RequestID       string
}

type RevisionCommand struct {
	OriginalAuditID      string
	ExpectedVersion      int64
	CustomerID           *uint64
	CustomerAppID        *uint64
	PlanPeriodID         *uint64
	AllocationConfidence string
	ResourceIdentifier   string
	AllocationMethod     string
	UpstreamCostCNY      string
	Usage                map[string]string
	EvidenceRef          string
	EvidenceHash         string
	Note                 string
	Reason               string
	Actor                string
	RequestID            string
}

type RevisionResult struct {
	Revision    *model.UsageAuditRevision `json:"revision"`
	Replacement *model.UsageAudit         `json:"replacement"`
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Create(command CreateCommand) (*model.UsageAudit, error) {
	if err := normalizeAndValidateCreate(&command); err != nil {
		return nil, err
	}
	usageJSON, err := jsonx.Marshal(command.Usage)
	if err != nil {
		return nil, err
	}
	audit := &model.UsageAudit{
		PublicID:             support.PublicID("usage"),
		CustomerID:           command.CustomerID,
		CustomerAppID:        command.CustomerAppID,
		PlanPeriodID:         command.PlanPeriodID,
		PeriodStart:          command.PeriodStart,
		PeriodEnd:            command.PeriodEnd,
		Source:               command.Source,
		AllocationConfidence: command.AllocationConfidence,
		ResourceIdentifier:   command.ResourceIdentifier,
		AllocationMethod:     command.AllocationMethod,
		UpstreamCostCNY:      command.UpstreamCostCNY,
		UsageJSON:            string(usageJSON),
		Note:                 strings.TrimSpace(command.Note),
		Status:               model.UsageAuditStatusDraft,
		RowVersion:           1,
		CreatedBy:            command.Actor,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if command.CustomerID != nil {
			var count int64
			if err := tx.Model(&model.Customer{}).Where("id = ?", *command.CustomerID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return domain.NotFound("customer not found")
			}
		}
		if command.CustomerAppID != nil {
			var app model.CustomerApp
			if err := tx.First(&app, *command.CustomerAppID).Error; err != nil {
				return domain.NotFound("customer App not found")
			}
			if command.CustomerID == nil || app.CustomerID != *command.CustomerID {
				return domain.Conflict("customer App does not belong to the audit customer")
			}
			if command.AllocationConfidence == model.AllocationAppExact && command.ResourceIdentifier != app.AppID {
				return domain.Conflict("app_exact resource_identifier does not match the selected customer App")
			}
		}
		if command.PlanPeriodID != nil {
			var period model.PlanPeriod
			if err := tx.First(&period, *command.PlanPeriodID).Error; err != nil {
				return domain.NotFound("plan period not found")
			}
			if command.CustomerID == nil || period.CustomerID != *command.CustomerID {
				return domain.Conflict("plan period does not belong to the audit customer")
			}
		}
		if err := tx.Create(audit).Error; err != nil {
			return err
		}
		return support.Audit(tx, command.CustomerID, command.Actor, "usage_audit.create", "usage_audit", audit.PublicID, nil, audit, "", command.RequestID)
	})
	return audit, err
}

// CreateImportedDraft creates an account-scoped, unverified draft backed by
// server-ingested evidence. The unique import source key makes a worker retry
// idempotent. Imported costs may retain provider sub-cent precision; they are
// never rounded through float arithmetic and can never be negative.
func (s *Service) CreateImportedDraft(command ImportedDraftCommand) (*model.UsageAudit, error) {
	command.ImportSourceKey = strings.TrimSpace(command.ImportSourceKey)
	command.PeriodStart = command.PeriodStart.UTC()
	command.PeriodEnd = command.PeriodEnd.UTC()
	command.EvidenceRef = strings.TrimSpace(command.EvidenceRef)
	command.EvidenceHash = strings.ToLower(strings.TrimSpace(command.EvidenceHash))
	command.Note = strings.TrimSpace(command.Note)
	if command.ImportSourceKey == "" || len(command.ImportSourceKey) > 191 || command.PeriodStart.IsZero() ||
		!command.PeriodEnd.After(command.PeriodStart) || command.EvidenceRef == "" || !sha256Pattern.MatchString(command.EvidenceHash) {
		return nil, domain.Invalid("import source, period, and account-scoped evidence are required")
	}
	cost, err := decimal.NewFromString(strings.TrimSpace(command.UpstreamCostCNY))
	if err != nil || cost.IsNegative() || cost.Exponent() < -18 || cost.GreaterThan(maximumImportedCost) {
		return nil, domain.Invalid("imported upstream cost must be non-negative with at most 18 decimal places")
	}
	if command.Usage == nil {
		command.Usage = map[string]string{}
	}
	usageJSON, err := jsonx.Marshal(command.Usage)
	if err != nil {
		return nil, err
	}
	importKey := command.ImportSourceKey
	draft := &model.UsageAudit{
		PublicID: support.PublicID("usage"), PeriodStart: command.PeriodStart, PeriodEnd: command.PeriodEnd,
		Source: "tencent_fee_center_api", AllocationConfidence: model.AllocationUnverified,
		UpstreamCostCNY: cost.String(), UsageJSON: string(usageJSON), EvidenceRef: command.EvidenceRef,
		EvidenceHash: command.EvidenceHash, ImportSourceKey: &importKey, Note: command.Note,
		Status: model.UsageAuditStatusDraft, RowVersion: 1, CreatedBy: command.Actor,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var evidence model.EvidenceObject
		if err := tx.Where("public_id = ? AND status = ?", command.EvidenceRef, model.EvidenceStatusActive).First(&evidence).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.NotFound("uploaded evidence not found")
			}
			return err
		}
		if evidence.CustomerID != nil || evidence.ContentSHA256 != command.EvidenceHash {
			return domain.Conflict("imported usage requires matching account-scoped evidence")
		}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(draft)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			return tx.Where("import_source_key = ?", command.ImportSourceKey).First(draft).Error
		}
		return support.Audit(tx, nil, command.Actor, "usage_audit.import_draft", "usage_audit", draft.PublicID, nil, draft, "requires manual review", command.RequestID)
	})
	return draft, err
}

func (s *Service) Lock(command LockCommand) (*model.UsageAudit, error) {
	command.AuditID = strings.TrimSpace(command.AuditID)
	command.AllocationConfidence = strings.ToLower(strings.TrimSpace(command.AllocationConfidence))
	command.ResourceIdentifier = strings.TrimSpace(command.ResourceIdentifier)
	command.AllocationMethod = strings.TrimSpace(command.AllocationMethod)
	command.EvidenceRef = strings.TrimSpace(command.EvidenceRef)
	command.EvidenceHash = strings.ToLower(strings.TrimSpace(command.EvidenceHash))
	command.Note = strings.TrimSpace(command.Note)
	if command.AuditID == "" || command.ExpectedVersion <= 0 || command.EvidenceRef == "" || !sha256Pattern.MatchString(command.EvidenceHash) {
		return nil, domain.Invalid("audit_id, expected_version, evidence_ref, and a sha256 evidence_hash are required")
	}
	if err := validateFinalAllocation(command.AllocationConfidence, command.ResourceIdentifier, command.AllocationMethod); err != nil {
		return nil, err
	}
	var result model.UsageAudit
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).Where("public_id = ?", command.AuditID).First(&result).Error; err != nil {
			return domain.NotFound("usage audit not found")
		}
		if result.RowVersion != command.ExpectedVersion {
			return domain.Conflict("usage audit row version changed; expected %d, current %d", command.ExpectedVersion, result.RowVersion)
		}
		if result.Status != model.UsageAuditStatusDraft {
			return domain.Conflict("non-draft usage audit is immutable")
		}
		if command.AllocationConfidence == model.AllocationAccountOnly {
			result.CustomerID = nil
			result.CustomerAppID = nil
			result.PlanPeriodID = nil
		} else if command.CustomerID != nil || command.CustomerAppID != nil || command.PlanPeriodID != nil {
			if command.CustomerID == nil || command.CustomerAppID == nil {
				return domain.Invalid("customer_id and customer_app_id must be supplied together for customer attribution")
			}
			result.CustomerID = command.CustomerID
			result.CustomerAppID = command.CustomerAppID
			result.PlanPeriodID = command.PlanPeriodID
		}
		if command.AllocationConfidence == model.AllocationAccountOnly && (result.CustomerID != nil || result.CustomerAppID != nil || result.PlanPeriodID != nil) {
			return domain.Conflict("account_only audit cannot contain customer, App, or plan-period attribution")
		}
		if command.AllocationConfidence != model.AllocationAccountOnly && (result.CustomerID == nil || result.CustomerAppID == nil) {
			return domain.Conflict("customer and App attribution are required for this confidence level")
		}
		if command.AllocationConfidence == model.AllocationAppExact {
			var app model.CustomerApp
			if err := tx.First(&app, *result.CustomerAppID).Error; err != nil {
				return domain.NotFound("customer App not found")
			}
			if app.CustomerID != *result.CustomerID || command.ResourceIdentifier != app.AppID {
				return domain.Conflict("app_exact resource_identifier does not match the selected customer App")
			}
		}
		if result.PlanPeriodID != nil {
			var period model.PlanPeriod
			if err := tx.First(&period, *result.PlanPeriodID).Error; err != nil {
				return domain.NotFound("plan period not found")
			}
			if result.CustomerID == nil || period.CustomerID != *result.CustomerID {
				return domain.Conflict("plan period does not belong to the audit customer")
			}
		}
		var evidence model.EvidenceObject
		if err := tx.Where("public_id = ? AND status = ?", command.EvidenceRef, model.EvidenceStatusActive).First(&evidence).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.NotFound("uploaded evidence not found")
			}
			return err
		}
		if evidence.ContentSHA256 != command.EvidenceHash {
			return domain.Conflict("evidence_hash does not match the uploaded evidence")
		}
		if command.AllocationConfidence == model.AllocationAccountOnly {
			if evidence.CustomerID != nil {
				return domain.Conflict("account_only audit requires account-scoped evidence without a customer")
			}
		} else if evidence.CustomerID == nil || result.CustomerID == nil || *evidence.CustomerID != *result.CustomerID {
			return domain.Conflict("evidence does not belong to the audit customer")
		}
		before := result
		now := time.Now().UTC()
		result.AllocationConfidence = command.AllocationConfidence
		result.ResourceIdentifier = command.ResourceIdentifier
		result.AllocationMethod = command.AllocationMethod
		result.EvidenceRef = command.EvidenceRef
		result.EvidenceHash = command.EvidenceHash
		result.Note = command.Note
		result.Status = model.UsageAuditStatusLocked
		result.ReviewedBy = command.Actor
		result.LockedBy = command.Actor
		result.ReviewedAt = &now
		result.LockedAt = &now
		result.RowVersion++
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		return support.Audit(tx, result.CustomerID, command.Actor, "usage_audit.lock", "usage_audit", result.PublicID, &before, &result, "", command.RequestID)
	})
	return &result, err
}

func (s *Service) Revise(command RevisionCommand) (*RevisionResult, error) {
	command.OriginalAuditID = strings.TrimSpace(command.OriginalAuditID)
	command.AllocationConfidence = strings.ToLower(strings.TrimSpace(command.AllocationConfidence))
	command.ResourceIdentifier = strings.TrimSpace(command.ResourceIdentifier)
	command.AllocationMethod = strings.TrimSpace(command.AllocationMethod)
	command.EvidenceRef = strings.TrimSpace(command.EvidenceRef)
	command.EvidenceHash = strings.ToLower(strings.TrimSpace(command.EvidenceHash))
	command.Note = strings.TrimSpace(command.Note)
	command.Reason = strings.TrimSpace(command.Reason)
	if command.OriginalAuditID == "" || command.ExpectedVersion <= 0 || command.Reason == "" || len(command.Reason) > 1000 ||
		command.EvidenceRef == "" || !sha256Pattern.MatchString(command.EvidenceHash) {
		return nil, domain.Invalid("original audit, expected_version, evidence, and a revision reason of at most 1000 characters are required")
	}
	if err := validateFinalAllocation(command.AllocationConfidence, command.ResourceIdentifier, command.AllocationMethod); err != nil {
		return nil, err
	}
	cost, err := decimal.NewFromString(strings.TrimSpace(command.UpstreamCostCNY))
	if err != nil || cost.IsNegative() || cost.Exponent() < -18 || cost.GreaterThan(maximumImportedCost) {
		return nil, domain.Invalid("revised upstream cost must be non-negative with at most 18 decimal places")
	}
	if command.Usage == nil {
		command.Usage = map[string]string{}
	}
	usageJSON, err := jsonx.Marshal(command.Usage)
	if err != nil {
		return nil, err
	}
	result := &RevisionResult{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var original model.UsageAudit
		if err := database.ForUpdate(tx).Where("public_id = ?", command.OriginalAuditID).First(&original).Error; err != nil {
			return domain.NotFound("usage audit not found")
		}
		if original.RowVersion != command.ExpectedVersion {
			return domain.Conflict("usage audit row version changed; expected %d, current %d", command.ExpectedVersion, original.RowVersion)
		}
		if original.Status != model.UsageAuditStatusLocked {
			return domain.Conflict("only a locked effective usage audit can be revised")
		}

		customerID, customerAppID, planPeriodID := command.CustomerID, command.CustomerAppID, command.PlanPeriodID
		if command.AllocationConfidence == model.AllocationAccountOnly {
			customerID, customerAppID, planPeriodID = nil, nil, nil
		} else {
			if customerID == nil {
				customerID = original.CustomerID
			}
			if customerAppID == nil {
				customerAppID = original.CustomerAppID
			}
			if planPeriodID == nil {
				planPeriodID = original.PlanPeriodID
			}
			if customerID == nil || customerAppID == nil {
				return domain.Invalid("customer_id and customer_app_id are required for a customer-attributed revision")
			}
		}

		if customerAppID != nil {
			var app model.CustomerApp
			if err := tx.First(&app, *customerAppID).Error; err != nil {
				return domain.NotFound("customer App not found")
			}
			if customerID == nil || app.CustomerID != *customerID {
				return domain.Conflict("customer App does not belong to the revision customer")
			}
			if command.AllocationConfidence == model.AllocationAppExact && command.ResourceIdentifier != app.AppID {
				return domain.Conflict("app_exact resource_identifier does not match the selected customer App")
			}
		}
		if planPeriodID != nil {
			var period model.PlanPeriod
			if err := tx.First(&period, *planPeriodID).Error; err != nil {
				return domain.NotFound("plan period not found")
			}
			if customerID == nil || period.CustomerID != *customerID {
				return domain.Conflict("plan period does not belong to the revision customer")
			}
		}
		var evidence model.EvidenceObject
		if err := tx.Where("public_id = ? AND status = ?", command.EvidenceRef, model.EvidenceStatusActive).First(&evidence).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.NotFound("uploaded evidence not found")
			}
			return err
		}
		if evidence.ContentSHA256 != command.EvidenceHash {
			return domain.Conflict("evidence_hash does not match the uploaded evidence")
		}
		if command.AllocationConfidence == model.AllocationAccountOnly {
			if evidence.CustomerID != nil {
				return domain.Conflict("account_only revision requires account-scoped evidence")
			}
		} else if evidence.CustomerID == nil || customerID == nil || *evidence.CustomerID != *customerID {
			return domain.Conflict("evidence does not belong to the revision customer")
		}

		now := time.Now().UTC()
		replacement := &model.UsageAudit{
			PublicID: support.PublicID("usage"), CustomerID: customerID, CustomerAppID: customerAppID,
			PlanPeriodID: planPeriodID, PeriodStart: original.PeriodStart, PeriodEnd: original.PeriodEnd,
			Source: original.Source, AllocationConfidence: command.AllocationConfidence,
			ResourceIdentifier: command.ResourceIdentifier, AllocationMethod: command.AllocationMethod,
			UpstreamCostCNY: cost.String(), UsageJSON: string(usageJSON), EvidenceRef: command.EvidenceRef,
			EvidenceHash: command.EvidenceHash, Note: command.Note, Status: model.UsageAuditStatusLocked,
			RowVersion: 1, CreatedBy: command.Actor, ReviewedBy: command.Actor, LockedBy: command.Actor,
			ReviewedAt: &now, LockedAt: &now,
		}
		if err := tx.Create(replacement).Error; err != nil {
			return err
		}
		before := original
		original.Status = model.UsageAuditStatusSuperseded
		original.RowVersion++
		if err := tx.Save(&original).Error; err != nil {
			return err
		}
		revision := &model.UsageAuditRevision{
			PublicID: support.PublicID("usage_revision"), OriginalAuditID: original.ID,
			ReplacementAuditID: replacement.ID, Reason: command.Reason, CreatedBy: command.Actor, AppliedAt: now,
		}
		if err := tx.Create(revision).Error; err != nil {
			return domain.Conflict("usage audit already has an applied revision")
		}
		if err := support.Audit(tx, customerID, command.Actor, "usage_audit.revise", "usage_audit_revision", revision.PublicID, &before, replacement, command.Reason, command.RequestID); err != nil {
			return err
		}
		result.Revision = revision
		result.Replacement = replacement
		return nil
	})
	return result, err
}

func (s *Service) List(customerID *uint64, limit int) ([]model.UsageAudit, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := s.db.Order("id desc").Limit(limit)
	if customerID != nil {
		query = query.Where("customer_id = ?", *customerID)
	}
	var audits []model.UsageAudit
	err := query.Find(&audits).Error
	return audits, err
}

func normalizeAndValidateCreate(command *CreateCommand) error {
	command.PeriodStart = command.PeriodStart.UTC()
	command.PeriodEnd = command.PeriodEnd.UTC()
	command.Source = strings.ToLower(strings.TrimSpace(command.Source))
	command.AllocationConfidence = strings.ToLower(strings.TrimSpace(command.AllocationConfidence))
	command.ResourceIdentifier = strings.TrimSpace(command.ResourceIdentifier)
	command.AllocationMethod = strings.TrimSpace(command.AllocationMethod)
	if command.PeriodStart.IsZero() || !command.PeriodEnd.After(command.PeriodStart) || command.Source == "" {
		return domain.Invalid("source and a valid period_start/period_end window are required")
	}
	if command.Usage == nil {
		command.Usage = map[string]string{}
	}
	money, err := decimal.NewFromString(strings.TrimSpace(command.UpstreamCostCNY))
	if err != nil || money.IsNegative() || money.Exponent() < -2 {
		return domain.Invalid("upstream_cost_cny must be non-negative with at most two decimal places")
	}
	command.UpstreamCostCNY = money.StringFixed(2)
	if command.AllocationConfidence == model.AllocationUnverified {
		if (command.CustomerID == nil) != (command.CustomerAppID == nil) {
			return domain.Invalid("customer_id and customer_app_id must be supplied together")
		}
		return nil
	}
	if err := validateFinalAllocation(command.AllocationConfidence, command.ResourceIdentifier, command.AllocationMethod); err != nil {
		return err
	}
	if command.AllocationConfidence == model.AllocationAccountOnly {
		if command.CustomerID != nil || command.CustomerAppID != nil || command.PlanPeriodID != nil {
			return domain.Invalid("account_only allocation cannot include customer, App, or plan-period attribution")
		}
		return nil
	}
	if command.CustomerID == nil || command.CustomerAppID == nil {
		return domain.Invalid("customer_id and customer_app_id are required for customer-attributed usage")
	}
	return nil
}

func validateFinalAllocation(confidence, resourceIdentifier, allocationMethod string) error {
	switch confidence {
	case model.AllocationAppExact:
		if resourceIdentifier == "" {
			return domain.Invalid("app_exact allocation requires resource_identifier")
		}
	case model.AllocationEstimatedAllocation:
		if allocationMethod == "" {
			return domain.Invalid("estimated_allocation requires allocation_method")
		}
	case model.AllocationAccountOnly:
	case model.AllocationUnverified:
		return domain.Invalid("unverified records must be resolved before review")
	default:
		return domain.Invalid("allocation_confidence must be app_exact, estimated_allocation, or account_only")
	}
	return nil
}
