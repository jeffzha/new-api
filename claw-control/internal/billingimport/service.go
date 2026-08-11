package billingimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/pagination"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"github.com/QuantumNous/new-api/claw-control/internal/usageaudit"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	monthPattern        = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
	businessCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	safeErrorPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
	realCostPattern     = regexp.MustCompile(`^-?[0-9]{1,18}(\.[0-9]{1,18})?$`)
	requestIDPattern    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	payerUINPattern     = regexp.MustCompile(`^[0-9]{1,32}$`)
	maximumAbsoluteCost = decimal.RequireFromString("999999999999999999.999999999999999999")
	errCoordinatorRace  = errors.New("billing import coordinator claim raced")
)

const billingCoordinatorKey = "tencent_fee_center"

type Config struct {
	Enabled          bool
	PayerUIN         string
	PageSize         int
	MaxPages         int
	MaxRecords       int
	MaxAttempts      int
	LeaseDuration    time.Duration
	MaxEvidenceBytes int64
}

type Service struct {
	db       *gorm.DB
	client   Client
	evidence *evidence.Service
	usage    *usageaudit.Service
	config   Config
	clock    func() time.Time
}

type CreateCommand struct {
	Month        string
	BusinessCode string
	Actor        string
	RequestID    string
}

type View struct {
	ImportID              string     `json:"import_id"`
	Month                 string     `json:"month"`
	BusinessCode          string     `json:"business_code"`
	Status                string     `json:"status"`
	AttemptCount          int        `json:"attempt_count"`
	MaxAttempts           int        `json:"max_attempts"`
	DetailPageCount       int        `json:"detail_page_count"`
	DetailRecordCount     int        `json:"detail_record_count"`
	UpstreamCostCNY       string     `json:"upstream_cost_cny"`
	ManualReviewRequired  bool       `json:"manual_review_required"`
	ReviewReasons         []string   `json:"review_reasons"`
	BillQueryRequestIDs   []string   `json:"bill_query_request_ids"`
	AdjustQueryRequestIDs []string   `json:"adjust_query_request_ids"`
	EvidenceRef           string     `json:"evidence_ref,omitempty"`
	EvidenceHash          string     `json:"evidence_hash,omitempty"`
	UsageAuditID          string     `json:"usage_audit_id,omitempty"`
	ErrorCode             string     `json:"error_code,omitempty"`
	RequestedBy           string     `json:"requested_by"`
	RequestID             string     `json:"request_id,omitempty"`
	StartedAt             *time.Time `json:"started_at,omitempty"`
	CompletedAt           *time.Time `json:"completed_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	AccountScoped         bool       `json:"account_scoped"`
	AllocationConfidence  string     `json:"allocation_confidence"`
	InvoiceMutation       bool       `json:"invoice_mutation"`
}

type evidenceEnvelope struct {
	SchemaVersion      int                `json:"schema_version"`
	Provider           string             `json:"provider"`
	APIEndpoint        string             `json:"api_endpoint"`
	APIVersion         string             `json:"api_version"`
	Month              string             `json:"month"`
	BusinessCode       string             `json:"business_code"`
	PayerUINSHA256     string             `json:"payer_uin_sha256"`
	RequestIDSemantics string             `json:"request_id_semantics"`
	Responses          []evidenceResponse `json:"responses"`
}

type evidenceResponse struct {
	Action    string          `json:"action"`
	RequestID string          `json:"query_request_id"`
	Response  json.RawMessage `json:"response"`
}

func New(db *gorm.DB, client Client, evidenceService *evidence.Service, usageService *usageaudit.Service, cfg Config) (*Service, error) {
	if db == nil || evidenceService == nil || usageService == nil {
		return nil, errors.New("billing import database, evidence service, and usage audit service are required")
	}
	if cfg.Enabled && client == nil {
		return nil, errors.New("enabled billing import requires a Tencent Billing client")
	}
	if cfg.PageSize < 1 || cfg.PageSize > 300 || cfg.MaxPages < 1 || cfg.MaxPages > 1000 ||
		cfg.MaxRecords < cfg.PageSize || cfg.MaxRecords > 200000 || cfg.MaxAttempts < 1 || cfg.MaxAttempts > 10 ||
		cfg.LeaseDuration < time.Minute || cfg.LeaseDuration > 2*time.Hour || cfg.MaxEvidenceBytes < 1024 || cfg.MaxEvidenceBytes > 100<<20 {
		return nil, errors.New("billing import safety limits are invalid")
	}
	if cfg.Enabled && !payerUINPattern.MatchString(strings.TrimSpace(cfg.PayerUIN)) {
		return nil, errors.New("enabled billing import requires a server-side payer UIN")
	}
	return &Service{db: db, client: client, evidence: evidenceService, usage: usageService, config: cfg, clock: time.Now}, nil
}

func (s *Service) Create(command CreateCommand) (*View, error) {
	if !s.config.Enabled {
		return nil, domain.Unavailable("Tencent Billing import is disabled")
	}
	command.Month = strings.TrimSpace(command.Month)
	command.BusinessCode = strings.TrimSpace(command.BusinessCode)
	if err := validateScope(command.Month, command.BusinessCode, s.clock().UTC()); err != nil {
		return nil, err
	}
	scopeHash := scopeHash(s.config.PayerUIN, command.Month, command.BusinessCode)
	payerHash := hashValue(s.config.PayerUIN)
	now := s.clock().UTC()
	run := &model.TencentBillingImportRun{
		PublicID: support.PublicID("billing_import"), ScopeHash: scopeHash, Month: command.Month,
		BusinessCode: command.BusinessCode, PayerUINHash: payerHash, Status: model.BillingImportStatusPending,
		MaxAttempts: s.config.MaxAttempts, RowVersion: 1, NextAttemptAt: now, UpstreamCostCNY: "0",
		ReviewReasonsJSON: "[]", BillQueryRequestIDsJSON: "[]", AdjustQueryRequestIDsJSON: "[]",
		RequestedBy: strings.TrimSpace(command.Actor), RequestID: strings.TrimSpace(command.RequestID),
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(run)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			return tx.Where("scope_hash = ?", scopeHash).First(run).Error
		}
		return support.Audit(tx, nil, command.Actor, "tencent_billing_import.create", "tencent_billing_import", run.PublicID, nil, project(run), "read-only account-scoped import", command.RequestID)
	})
	if err != nil {
		return nil, err
	}
	view := project(run)
	return &view, nil
}

func (s *Service) List(limit int) ([]View, error) {
	page, err := s.ListPage(0, limit)
	return page.Items, err
}

func (s *Service) ListPage(beforeID uint64, limit int) (pagination.Page[View], error) {
	limit = pagination.Limit(limit)
	var runs []model.TencentBillingImportRun
	query := s.db.Order("id desc").Limit(limit + 1)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	if err := query.Find(&runs).Error; err != nil {
		return pagination.Page[View]{}, err
	}
	page := pagination.Trim(runs, limit, func(value model.TencentBillingImportRun) uint64 { return value.ID })
	result := make([]View, 0, len(page.Items))
	for index := range page.Items {
		result = append(result, project(&page.Items[index]))
	}
	return pagination.Page[View]{Items: result, NextBeforeID: page.NextBeforeID}, nil
}

func (s *Service) Get(importID string) (*View, error) {
	var run model.TencentBillingImportRun
	if err := s.db.Where("public_id = ?", strings.TrimSpace(importID)).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.NotFound("Tencent Billing import not found")
		}
		return nil, err
	}
	view := project(&run)
	return &view, nil
}

func (s *Service) Retry(importID, actor, requestID string) (*View, error) {
	if !s.config.Enabled {
		return nil, domain.Unavailable("Tencent Billing import is disabled")
	}
	var run model.TencentBillingImportRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).Where("public_id = ?", strings.TrimSpace(importID)).First(&run).Error; err != nil {
			return domain.NotFound("Tencent Billing import not found")
		}
		if run.Status != model.BillingImportStatusFailed {
			return domain.Conflict("only a failed Tencent Billing import can be retried")
		}
		before := run
		run.Status = model.BillingImportStatusPending
		run.MaxAttempts = run.AttemptCount + s.config.MaxAttempts
		run.ErrorCode = ""
		run.LeaseToken = ""
		run.LeaseUntil = nil
		run.NextAttemptAt = s.clock().UTC()
		run.RowVersion++
		if err := tx.Save(&run).Error; err != nil {
			return err
		}
		return support.Audit(tx, nil, actor, "tencent_billing_import.retry", "tencent_billing_import", run.PublicID, &before, project(&run), "manual retry", requestID)
	})
	if err != nil {
		return nil, err
	}
	view := project(&run)
	return &view, nil
}

func (s *Service) ProcessOne(ctx context.Context) (bool, error) {
	if !s.config.Enabled {
		return false, nil
	}
	run, err := s.claim()
	if err != nil || run == nil {
		return false, err
	}
	err = s.process(ctx, run)
	if err == nil {
		return true, nil
	}
	if updateErr := s.recordFailure(run, err); updateErr != nil {
		return true, updateErr
	}
	return true, nil
}

func (s *Service) claim() (*model.TencentBillingImportRun, error) {
	now := s.clock().UTC()
	var claimed *model.TencentBillingImportRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.TencentBillingImportRun{}).
			Where("status = ? AND lease_until < ? AND attempt_count >= max_attempts", model.BillingImportStatusRunning, now).
			Updates(map[string]any{
				"status": model.BillingImportStatusFailed, "error_code": "lease_expired_attempts_exhausted",
				"lease_token": "", "lease_until": nil, "row_version": gorm.Expr("row_version + 1"),
			}).Error; err != nil {
			return err
		}
		coordinator := model.TencentBillingImportCoordinator{Key: billingCoordinatorKey, RowVersion: 1, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&coordinator).Error; err != nil {
			return err
		}
		if err := database.ForUpdate(tx).Where("key = ?", billingCoordinatorKey).First(&coordinator).Error; err != nil {
			return err
		}
		if coordinator.LeaseUntil != nil && coordinator.LeaseUntil.After(now) {
			return nil
		}
		var candidate model.TencentBillingImportRun
		err := database.ForUpdate(tx).
			Where("attempt_count < max_attempts").
			Where("(status = ? AND next_attempt_at <= ?) OR (status = ? AND lease_until < ?)",
				model.BillingImportStatusPending, now, model.BillingImportStatusRunning, now).
			Order("next_attempt_at asc, id asc").First(&candidate).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		leaseToken := support.PublicID("lease")
		leaseUntil := now.Add(s.config.LeaseDuration)
		updates := map[string]any{
			"status": model.BillingImportStatusRunning, "attempt_count": candidate.AttemptCount + 1,
			"lease_token": leaseToken, "lease_until": leaseUntil, "started_at": now,
			"row_version": candidate.RowVersion + 1, "error_code": "",
		}
		updated := tx.Model(&model.TencentBillingImportRun{}).
			Where("id = ? AND row_version = ? AND status = ?", candidate.ID, candidate.RowVersion, candidate.Status).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			return nil
		}
		coordinatorUpdate := tx.Model(&model.TencentBillingImportCoordinator{}).
			Where("key = ? AND row_version = ?", billingCoordinatorKey, coordinator.RowVersion).
			Updates(map[string]any{
				"lease_token": leaseToken, "lease_until": leaseUntil,
				"row_version": coordinator.RowVersion + 1,
			})
		if coordinatorUpdate.Error != nil {
			return coordinatorUpdate.Error
		}
		if coordinatorUpdate.RowsAffected != 1 {
			return errCoordinatorRace
		}
		candidate.Status = model.BillingImportStatusRunning
		candidate.AttemptCount++
		candidate.LeaseToken = leaseToken
		candidate.LeaseUntil = &leaseUntil
		candidate.StartedAt = &now
		candidate.RowVersion++
		claimed = &candidate
		return nil
	})
	if errors.Is(err, errCoordinatorRace) {
		return nil, nil
	}
	return claimed, err
}

func (s *Service) process(ctx context.Context, run *model.TencentBillingImportRun) error {
	responses := make([]evidenceResponse, 0, s.config.MaxPages+1)
	billRequestIDs := make([]string, 0, s.config.MaxPages)
	totalCost := decimal.Zero
	negativeComponent := false
	detailCount := 0
	contextValue := ""
	offset := 0
	pageCount := 0
	evidenceBytes := int64(0)
	for pageCount < s.config.MaxPages {
		page, err := s.client.DescribeBillDetail(ctx, DetailRequest{
			Month: run.Month, BusinessCode: run.BusinessCode, Offset: offset, Limit: s.config.PageSize, Context: contextValue,
		})
		if err != nil {
			return err
		}
		if err := s.renewLease(run); err != nil {
			return err
		}
		if !requestIDPattern.MatchString(page.RequestID) || len(page.Context) > 8192 || len(page.Raw) == 0 {
			return errors.New("invalid_response_metadata")
		}
		evidenceBytes += int64(len(page.Raw))
		if evidenceBytes > s.config.MaxEvidenceBytes {
			return errors.New("evidence_limit_exceeded")
		}
		if detailCount+len(page.Details) > s.config.MaxRecords {
			return errors.New("record_limit_exceeded")
		}
		pageCount++
		billRequestIDs = append(billRequestIDs, page.RequestID)
		responses = append(responses, evidenceResponse{Action: "DescribeBillDetail", RequestID: page.RequestID, Response: page.Raw})
		for _, detail := range page.Details {
			if detail.BusinessCode != "" && detail.BusinessCode != run.BusinessCode {
				return errors.New("business_code_mismatch")
			}
			for _, component := range detail.Components {
				realCost := strings.TrimSpace(component.RealCost)
				amount, err := decimal.NewFromString(realCost)
				if err != nil || !realCostPattern.MatchString(realCost) {
					return errors.New("invalid_real_cost")
				}
				if amount.IsNegative() {
					negativeComponent = true
				}
				totalCost = totalCost.Add(amount)
				if totalCost.Abs().GreaterThan(maximumAbsoluteCost) {
					return errors.New("cost_limit_exceeded")
				}
			}
		}
		detailCount += len(page.Details)
		offset += len(page.Details)
		contextValue = page.Context
		if len(page.Details) < s.config.PageSize {
			break
		}
	}
	if pageCount == s.config.MaxPages && detailCount == pageCount*s.config.PageSize {
		return errors.New("page_limit_exceeded")
	}
	adjustment, err := s.client.DescribeBillAdjustInfo(ctx, run.Month)
	if err != nil {
		return err
	}
	if err := s.renewLease(run); err != nil {
		return err
	}
	if !requestIDPattern.MatchString(adjustment.RequestID) || len(adjustment.Raw) == 0 {
		return errors.New("invalid_response_metadata")
	}
	evidenceBytes += int64(len(adjustment.Raw))
	if evidenceBytes > s.config.MaxEvidenceBytes {
		return errors.New("evidence_limit_exceeded")
	}
	responses = append(responses, evidenceResponse{Action: "DescribeBillAdjustInfo", RequestID: adjustment.RequestID, Response: adjustment.Raw})
	reasons := []string{"account_scope_allocation_unverified"}
	if negativeComponent {
		reasons = append(reasons, "negative_bill_component")
	}
	if totalCost.IsNegative() {
		reasons = append(reasons, "negative_net_cost_clamped_to_zero")
		totalCost = decimal.Zero
	}
	if adjustment.Total > 0 || adjustment.Count > 0 {
		reasons = append(reasons, "billing_adjustment_present")
	}
	envelopeBytes, err := jsonx.Marshal(evidenceEnvelope{
		SchemaVersion: 1, Provider: "tencent_fee_center", APIEndpoint: tencentBillingEndpoint,
		APIVersion: tencentBillingVersion, Month: run.Month, BusinessCode: run.BusinessCode,
		PayerUINSHA256:     run.PayerUINHash,
		RequestIDSemantics: "Tencent Fee Center query RequestIds; never ADP Turn or chat RequestIds",
		Responses:          responses,
	})
	if err != nil {
		return err
	}
	if int64(len(envelopeBytes)) > s.config.MaxEvidenceBytes {
		return errors.New("evidence_limit_exceeded")
	}
	metadata, err := s.evidence.Upload(evidence.UploadCommand{
		Filename: run.PublicID + ".json", DeclaredMIME: "application/json", Content: bytes.NewReader(envelopeBytes),
		Actor: "system:tencent-billing-import", RequestID: run.RequestID, Context: ctx,
	})
	if err != nil {
		return errors.New("evidence_store_failed")
	}
	billIDsJSON, err := jsonx.Marshal(billRequestIDs)
	if err != nil {
		return err
	}
	adjustIDs := []string{adjustment.RequestID}
	adjustIDsJSON, err := jsonx.Marshal(adjustIDs)
	if err != nil {
		return err
	}
	reasonsJSON, err := jsonx.Marshal(reasons)
	if err != nil {
		return err
	}
	periodStart, periodEnd, err := billingMonthWindow(run.Month)
	if err != nil {
		return err
	}
	usageDraft, err := s.usage.CreateImportedDraft(usageaudit.ImportedDraftCommand{
		ImportSourceKey: "tencent_billing:" + run.ScopeHash, PeriodStart: periodStart, PeriodEnd: periodEnd,
		UpstreamCostCNY: totalCost.String(), EvidenceRef: metadata.EvidenceRef, EvidenceHash: metadata.ContentHash,
		Usage: map[string]string{
			"bill_detail_pages": fmt.Sprintf("%d", pageCount), "bill_detail_records": fmt.Sprintf("%d", detailCount),
			"bill_query_request_ids": string(billIDsJSON), "adjustment_records": fmt.Sprintf("%d", adjustment.Count),
			"adjust_query_request_ids": string(adjustIDsJSON), "exact_decimal_cost_cny": totalCost.String(),
			"manual_review_required": fmt.Sprintf("%t", len(reasons) > 0), "review_reasons": string(reasonsJSON),
			"request_id_semantics": "fee_center_query_not_adp_turn",
		},
		Note:  "Automatically imported account-level Tencent Fee Center draft. Manual allocation review is mandatory; this draft never changes an invoice.",
		Actor: "system:tencent-billing-import", RequestID: run.RequestID,
	})
	if err != nil {
		return errors.New("usage_draft_failed")
	}
	return s.complete(run, pageCount, detailCount, totalCost.String(), reasons, billRequestIDs, adjustIDs, usageDraft)
}

func (s *Service) complete(run *model.TencentBillingImportRun, pages, records int, cost string, reasons, billIDs, adjustIDs []string, draft *model.UsageAudit) error {
	reasonsJSON, err := jsonx.Marshal(reasons)
	if err != nil {
		return err
	}
	billJSON, err := jsonx.Marshal(billIDs)
	if err != nil {
		return err
	}
	adjustJSON, err := jsonx.Marshal(adjustIDs)
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	updates := map[string]any{
		"status": model.BillingImportStatusDraftCreated, "detail_page_count": pages, "detail_record_count": records,
		"upstream_cost_cny": cost, "manual_review_required": len(reasons) > 0,
		"review_reasons_json": string(reasonsJSON), "bill_query_request_ids_json": string(billJSON),
		"adjust_query_request_ids_json": string(adjustJSON), "evidence_ref": draft.EvidenceRef,
		"evidence_hash": draft.EvidenceHash, "usage_audit_id": draft.ID, "usage_audit_public_id": draft.PublicID,
		"error_code": "", "lease_token": "", "lease_until": nil, "completed_at": now,
		"row_version": run.RowVersion + 1,
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.TencentBillingImportRun{}).
			Where("id = ? AND status = ? AND lease_token = ? AND row_version = ?", run.ID, model.BillingImportStatusRunning, run.LeaseToken, run.RowVersion).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("lease_lost")
		}
		return releaseCoordinator(tx, run.LeaseToken, s.clock().UTC().Add(tencentCallInterval))
	})
}

func (s *Service) renewLease(run *model.TencentBillingImportRun) error {
	leaseUntil := s.clock().UTC().Add(s.config.LeaseDuration)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.TencentBillingImportRun{}).
			Where("id = ? AND status = ? AND lease_token = ? AND row_version = ?", run.ID, model.BillingImportStatusRunning, run.LeaseToken, run.RowVersion).
			Update("lease_until", leaseUntil)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("lease_lost")
		}
		coordinator := tx.Model(&model.TencentBillingImportCoordinator{}).
			Where("key = ? AND lease_token = ?", billingCoordinatorKey, run.LeaseToken).
			Updates(map[string]any{"lease_until": leaseUntil, "row_version": gorm.Expr("row_version + 1")})
		if coordinator.Error != nil {
			return coordinator.Error
		}
		if coordinator.RowsAffected != 1 {
			return errors.New("lease_lost")
		}
		return nil
	})
	if err != nil {
		return err
	}
	run.LeaseUntil = &leaseUntil
	return nil
}

func (s *Service) recordFailure(run *model.TencentBillingImportRun, processError error) error {
	code := safeErrorCode(processError)
	now := s.clock().UTC()
	status := model.BillingImportStatusFailed
	nextAttempt := now
	if run.AttemptCount < run.MaxAttempts && retryable(code) {
		status = model.BillingImportStatusPending
		backoff := time.Duration(1<<min(run.AttemptCount, 6)) * time.Second
		nextAttempt = now.Add(backoff)
	}
	updates := map[string]any{
		"status": status, "error_code": code, "lease_token": "", "lease_until": nil,
		"next_attempt_at": nextAttempt, "row_version": run.RowVersion + 1,
	}
	var providerError *ProviderError
	if errors.As(processError, &providerError) && requestIDPattern.MatchString(providerError.RequestID) {
		requestIDs, err := jsonx.Marshal([]string{providerError.RequestID})
		if err != nil {
			return err
		}
		switch providerError.Action {
		case "DescribeBillDetail":
			updates["bill_query_request_ids_json"] = string(requestIDs)
		case "DescribeBillAdjustInfo":
			updates["adjust_query_request_ids_json"] = string(requestIDs)
		}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.TencentBillingImportRun{}).
			Where("id = ? AND status = ? AND lease_token = ? AND row_version = ?", run.ID, model.BillingImportStatusRunning, run.LeaseToken, run.RowVersion).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("billing import lease was lost")
		}
		return releaseCoordinator(tx, run.LeaseToken, s.clock().UTC().Add(tencentCallInterval))
	})
}

func releaseCoordinator(tx *gorm.DB, leaseToken string, nextProviderCallAt time.Time) error {
	updated := tx.Model(&model.TencentBillingImportCoordinator{}).
		Where("key = ? AND lease_token = ?", billingCoordinatorKey, leaseToken).
		Updates(map[string]any{"lease_token": "", "lease_until": nextProviderCallAt, "row_version": gorm.Expr("row_version + 1")})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return errors.New("billing import coordinator lease was lost")
	}
	return nil
}

func validateScope(month, businessCode string, now time.Time) error {
	if !monthPattern.MatchString(month) || !businessCodePattern.MatchString(businessCode) {
		return domain.Invalid("month must be YYYY-MM and business_code must contain only letters, digits, underscore, or hyphen")
	}
	parsed, err := time.Parse("2006-01", month)
	if err != nil {
		return domain.Invalid("month must be YYYY-MM")
	}
	china := time.FixedZone("Asia/Shanghai", 8*60*60)
	currentChina := now.In(china)
	current := time.Date(currentChina.Year(), currentChina.Month(), 1, 0, 0, 0, 0, time.UTC)
	if parsed.After(current) || parsed.Before(current.AddDate(0, -17, 0)) {
		return domain.Invalid("month must be within Tencent Billing's current 18-month query window")
	}
	return nil
}

func billingMonthWindow(month string) (time.Time, time.Time, error) {
	china := time.FixedZone("Asia/Shanghai", 8*60*60)
	start, err := time.ParseInLocation("2006-01", month, china)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start.UTC(), start.AddDate(0, 1, 0).UTC(), nil
}

func scopeHash(payerUIN, month, businessCode string) string {
	return hashValue(strings.TrimSpace(payerUIN) + "\n" + month + "\n" + businessCode)
}

func hashValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func safeErrorCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var providerError *ProviderError
	if errors.As(err, &providerError) && safeErrorPattern.MatchString(providerError.Code) {
		return providerError.Code
	}
	code := strings.TrimSpace(err.Error())
	if safeErrorPattern.MatchString(code) {
		return code
	}
	return "internal_error"
}

func retryable(code string) bool {
	return code == "transport_error" || code == "response_read_error" || code == "http_status_429" ||
		code == "context_canceled" || code == "deadline_exceeded" || code == "evidence_store_failed" ||
		code == "usage_draft_failed" || code == "internal_error" ||
		strings.HasPrefix(code, "http_status_5") || strings.HasPrefix(code, "InternalError")
}

func project(run *model.TencentBillingImportRun) View {
	view := View{
		ImportID: run.PublicID, Month: run.Month, BusinessCode: run.BusinessCode, Status: run.Status,
		AttemptCount: run.AttemptCount, MaxAttempts: run.MaxAttempts, DetailPageCount: run.DetailPageCount,
		DetailRecordCount: run.DetailRecordCount, UpstreamCostCNY: run.UpstreamCostCNY,
		ManualReviewRequired: run.ManualReviewRequired, EvidenceRef: run.EvidenceRef, EvidenceHash: run.EvidenceHash,
		UsageAuditID: run.UsageAuditPublicID, ErrorCode: run.ErrorCode, RequestedBy: run.RequestedBy,
		RequestID: run.RequestID, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
		CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, AccountScoped: true,
		AllocationConfidence: model.AllocationUnverified, InvoiceMutation: false,
	}
	_ = jsonx.Unmarshal([]byte(run.ReviewReasonsJSON), &view.ReviewReasons)
	_ = jsonx.Unmarshal([]byte(run.BillQueryRequestIDsJSON), &view.BillQueryRequestIDs)
	_ = jsonx.Unmarshal([]byte(run.AdjustQueryRequestIDsJSON), &view.AdjustQueryRequestIDs)
	if view.ReviewReasons == nil {
		view.ReviewReasons = []string{}
	}
	if view.BillQueryRequestIDs == nil {
		view.BillQueryRequestIDs = []string{}
	}
	if view.AdjustQueryRequestIDs == nil {
		view.AdjustQueryRequestIDs = []string{}
	}
	return view
}
