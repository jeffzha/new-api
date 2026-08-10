package plan

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type Service struct {
	db *gorm.DB
}

type PublishCommand struct {
	PlanCode        string
	DisplayName     string
	MonthlyPriceCNY string
	Capabilities    []string
	Limits          productpolicy.Limits
	ValidFrom       time.Time
	ValidTo         *time.Time
	Actor           string
	RequestID       string
}

type PublishResult struct {
	Plan    *model.Plan        `json:"plan"`
	Version *model.PlanVersion `json:"version"`
}

type CreatePeriodCommand struct {
	CustomerID    uint64
	PlanVersionID uint64
	StartAt       time.Time
	EndAt         time.Time
	Actor         string
	RequestID     string
}

type ConfirmPaymentCommand struct {
	PeriodID           uint64
	ExpectedVersion    int64
	PaymentEvidenceRef string
	Actor              string
	RequestID          string
}

type CancelPeriodCommand struct {
	PeriodID        uint64
	ExpectedVersion int64
	Reason          string
	Actor           string
	RequestID       string
}

type VoidInvoiceCommand struct {
	InvoiceID uint64
	Reason    string
	Actor     string
	RequestID string
}

type PaymentResult struct {
	Period  *model.PlanPeriod      `json:"period"`
	Invoice *model.CustomerInvoice `json:"invoice"`
}

type planSnapshot struct {
	PlanCode        string               `json:"plan_code"`
	PlanName        string               `json:"plan_name"`
	PlanVersion     int64                `json:"plan_version"`
	MonthlyPriceCNY string               `json:"monthly_price_cny"`
	Currency        string               `json:"currency"`
	Capabilities    []string             `json:"capabilities"`
	Limits          productpolicy.Limits `json:"limits"`
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Publish(command PublishCommand) (*PublishResult, error) {
	command.PlanCode = strings.ToLower(strings.TrimSpace(command.PlanCode))
	command.DisplayName = strings.TrimSpace(command.DisplayName)
	if command.PlanCode == "" || len(command.PlanCode) > 80 || command.DisplayName == "" || len(command.DisplayName) > 160 {
		return nil, domain.Invalid("plan_code and display_name are required and exceed their supported length")
	}
	price, err := parseMoney(command.MonthlyPriceCNY, true)
	if err != nil {
		return nil, err
	}
	if command.ValidFrom.IsZero() {
		command.ValidFrom = time.Now().UTC()
	} else {
		command.ValidFrom = command.ValidFrom.UTC()
	}
	if command.ValidTo != nil {
		validTo := command.ValidTo.UTC()
		if !validTo.After(command.ValidFrom) {
			return nil, domain.Invalid("valid_to must be after valid_from")
		}
		command.ValidTo = &validTo
	}
	capabilities, err := productpolicy.NormalizeCapabilities(command.Capabilities)
	if err != nil {
		return nil, err
	}
	if err := productpolicy.ValidateLimits(command.Limits); err != nil {
		return nil, err
	}
	capabilitiesJSON, err := jsonx.Marshal(capabilities)
	if err != nil {
		return nil, err
	}
	limitsJSON, err := jsonx.Marshal(command.Limits)
	if err != nil {
		return nil, err
	}
	result := &PublishResult{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var stable model.Plan
		find := database.ForUpdate(tx).Where("plan_code = ?", command.PlanCode).First(&stable)
		if find.Error != nil && find.Error != gorm.ErrRecordNotFound {
			return find.Error
		}
		if find.Error == gorm.ErrRecordNotFound {
			stable = model.Plan{
				PlanCode:    command.PlanCode,
				DisplayName: command.DisplayName,
				Status:      model.PlanStatusPublished,
			}
			if err := tx.Create(&stable).Error; err != nil {
				return domain.Conflict("plan_code already exists or plan could not be created")
			}
		} else {
			stable.DisplayName = command.DisplayName
			stable.Status = model.PlanStatusPublished
			if err := tx.Save(&stable).Error; err != nil {
				return err
			}
		}
		var maxVersion int64
		if err := tx.Model(&model.PlanVersion{}).Where("plan_id = ?", stable.ID).
			Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
			return err
		}
		version := &model.PlanVersion{
			PlanID:           stable.ID,
			Version:          maxVersion + 1,
			Name:             command.DisplayName,
			MonthlyPriceCNY:  price,
			Currency:         "CNY",
			CapabilitiesJSON: string(capabilitiesJSON),
			LimitsJSON:       string(limitsJSON),
			Status:           model.PlanStatusPublished,
			ValidFrom:        command.ValidFrom,
			ValidTo:          command.ValidTo,
			PublishedBy:      command.Actor,
			PublishedAt:      time.Now().UTC(),
		}
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		if err := support.Audit(tx, nil, command.Actor, "plan.version.publish", "plan_version", support.ResourceID(version.ID), nil, version, "", command.RequestID); err != nil {
			return err
		}
		result.Plan = &stable
		result.Version = version
		return nil
	})
	return result, err
}

func (s *Service) CreatePeriod(command CreatePeriodCommand) (*model.PlanPeriod, error) {
	command.StartAt = command.StartAt.UTC()
	command.EndAt = command.EndAt.UTC()
	if command.CustomerID == 0 || command.PlanVersionID == 0 || command.StartAt.IsZero() || !command.EndAt.Equal(command.StartAt.AddDate(0, 1, 0)) {
		return nil, domain.Invalid("customer_id, plan_version_id, and an exact one-calendar-month period are required")
	}
	period := &model.PlanPeriod{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var customer model.Customer
		if err := database.ForUpdate(tx).First(&customer, command.CustomerID).Error; err != nil {
			return domain.NotFound("customer not found")
		}
		if customer.Status == model.CustomerStatusDisabled || customer.Status == model.CustomerStatusArchived {
			return domain.Conflict("customer status %s does not allow a new plan period", customer.Status)
		}
		var version model.PlanVersion
		if err := database.ForUpdate(tx).First(&version, command.PlanVersionID).Error; err != nil {
			return domain.NotFound("plan version not found")
		}
		if version.Status != model.PlanStatusPublished || command.StartAt.Before(version.ValidFrom) || (version.ValidTo != nil && !command.StartAt.Before(*version.ValidTo)) {
			return domain.Conflict("plan version is not published for the requested period start")
		}
		var stable model.Plan
		if err := tx.First(&stable, version.PlanID).Error; err != nil {
			return err
		}
		var overlap int64
		if err := tx.Model(&model.PlanPeriod{}).
			Where("customer_id = ? AND status <> ? AND start_at < ? AND end_at > ?", command.CustomerID, model.PeriodStatusCanceled, command.EndAt, command.StartAt).
			Count(&overlap).Error; err != nil {
			return err
		}
		if overlap > 0 {
			return domain.Conflict("customer already has an overlapping plan period")
		}
		var capabilities []string
		var limits productpolicy.Limits
		if err := jsonx.Unmarshal([]byte(version.CapabilitiesJSON), &capabilities); err != nil {
			return err
		}
		if err := jsonx.Unmarshal([]byte(version.LimitsJSON), &limits); err != nil {
			return err
		}
		snapshotJSON, err := jsonx.Marshal(planSnapshot{
			PlanCode: stable.PlanCode, PlanName: version.Name, PlanVersion: version.Version,
			MonthlyPriceCNY: version.MonthlyPriceCNY, Currency: version.Currency,
			Capabilities: capabilities, Limits: limits,
		})
		if err != nil {
			return err
		}
		period = &model.PlanPeriod{
			CustomerID:    command.CustomerID,
			PlanVersionID: command.PlanVersionID,
			StartAt:       command.StartAt,
			EndAt:         command.EndAt,
			AmountCNY:     version.MonthlyPriceCNY,
			PaymentMode:   model.PaymentModeOfflineManual,
			PaymentStatus: model.PaymentStatusPending,
			Status:        model.PeriodStatusPendingPayment,
			SnapshotJSON:  string(snapshotJSON),
			RowVersion:    1,
		}
		if err := tx.Create(period).Error; err != nil {
			return domain.Conflict("plan period overlaps an existing period or could not be created")
		}
		return support.Audit(tx, &command.CustomerID, command.Actor, "plan.period.create", "plan_period", support.ResourceID(period.ID), nil, period, "", command.RequestID)
	})
	return period, err
}

func (s *Service) ConfirmPayment(command ConfirmPaymentCommand) (*PaymentResult, error) {
	command.PaymentEvidenceRef = strings.TrimSpace(command.PaymentEvidenceRef)
	if command.PeriodID == 0 || command.ExpectedVersion <= 0 || command.PaymentEvidenceRef == "" {
		return nil, domain.Invalid("period_id, expected_version, and payment_evidence_ref are required")
	}
	result := &PaymentResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var period model.PlanPeriod
		if err := database.ForUpdate(tx).First(&period, command.PeriodID).Error; err != nil {
			return domain.NotFound("plan period not found")
		}
		if period.RowVersion != command.ExpectedVersion {
			return domain.Conflict("plan period row version changed; expected %d, current %d", command.ExpectedVersion, period.RowVersion)
		}
		var paymentEvidence model.EvidenceObject
		if err := tx.Where(
			"public_id = ? AND status = ?",
			command.PaymentEvidenceRef,
			model.EvidenceStatusActive,
		).First(&paymentEvidence).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.NotFound("uploaded payment evidence not found")
			}
			return err
		}
		if paymentEvidence.CustomerID == nil || *paymentEvidence.CustomerID != period.CustomerID {
			return domain.Conflict("payment evidence does not belong to the plan customer")
		}
		if period.PaymentStatus == model.PaymentStatusPaid {
			if period.PaymentEvidenceRef != command.PaymentEvidenceRef {
				return domain.Conflict("plan period is already paid with different evidence")
			}
			var invoice model.CustomerInvoice
			if err := tx.Where("period_id = ?", period.ID).First(&invoice).Error; err != nil {
				return err
			}
			result.Period = &period
			result.Invoice = &invoice
			return nil
		}
		if period.Status == model.PeriodStatusCanceled {
			return domain.Conflict("canceled plan period cannot be paid")
		}
		now := time.Now().UTC()
		if !now.Before(period.EndAt) {
			return domain.Conflict("expired plan period cannot be paid")
		}
		before := period
		period.PaymentStatus = model.PaymentStatusPaid
		period.PaymentEvidenceRef = command.PaymentEvidenceRef
		period.RowVersion++
		if now.Before(period.StartAt) {
			period.Status = model.PeriodStatusScheduled
		} else {
			period.Status = model.PeriodStatusActive
			period.ActivatedAt = &now
		}
		if err := tx.Save(&period).Error; err != nil {
			return err
		}
		invoice := &model.CustomerInvoice{
			InvoiceNumber:       fmt.Sprintf("CINV-%010d", period.ID),
			PeriodID:            period.ID,
			CustomerID:          period.CustomerID,
			Description:         "Claw fixed monthly plan",
			FixedAmountCNY:      period.AmountCNY,
			ManualAdjustmentCNY: "0.00",
			AmountCNY:           period.AmountCNY,
			Status:              model.InvoiceStatusPaid,
			EvidenceRef:         command.PaymentEvidenceRef,
			IssuedAt:            now,
			PaidAt:              &now,
		}
		if err := tx.Create(invoice).Error; err != nil {
			return domain.Conflict("invoice already exists for this plan period")
		}
		if err := support.Audit(tx, &period.CustomerID, command.Actor, "plan.period.payment.confirm", "plan_period", support.ResourceID(period.ID), &before, &period, command.PaymentEvidenceRef, command.RequestID); err != nil {
			return err
		}
		result.Period = &period
		result.Invoice = invoice
		return nil
	})
	return result, err
}

func (s *Service) CancelPeriod(command CancelPeriodCommand) (*model.PlanPeriod, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	if command.PeriodID == 0 || command.ExpectedVersion <= 0 || command.Reason == "" || len(command.Reason) > 1000 {
		return nil, domain.Invalid("period_id, expected_version, and a reason of at most 1000 characters are required")
	}
	result := &model.PlanPeriod{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var period model.PlanPeriod
		if err := database.ForUpdate(tx).First(&period, command.PeriodID).Error; err != nil {
			return domain.NotFound("plan period not found")
		}
		if period.RowVersion != command.ExpectedVersion {
			return domain.Conflict("plan period row version changed; expected %d, current %d", command.ExpectedVersion, period.RowVersion)
		}
		if period.Status == model.PeriodStatusCanceled {
			*result = period
			return nil
		}
		if period.Status == model.PeriodStatusExpired {
			return domain.Conflict("expired plan period cannot be canceled")
		}
		now := time.Now().UTC()
		before := period
		period.Status = model.PeriodStatusCanceled
		period.CanceledAt = &now
		period.RowVersion++
		if err := tx.Save(&period).Error; err != nil {
			return err
		}
		if err := support.Audit(tx, &period.CustomerID, command.Actor, "plan.period.cancel", "plan_period", support.ResourceID(period.ID), &before, &period, command.Reason, command.RequestID); err != nil {
			return err
		}

		if err := suspendAppsWithoutActivePlan(tx, period.CustomerID, now, command.Actor, "app.suspend.plan_canceled", command.Reason, command.RequestID); err != nil {
			return err
		}
		*result = period
		return nil
	})
	return result, err
}

func (s *Service) VoidInvoice(command VoidInvoiceCommand) (*model.CustomerInvoice, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	if command.InvoiceID == 0 || command.Reason == "" || len(command.Reason) > 1000 {
		return nil, domain.Invalid("invoice_id and a reason of at most 1000 characters are required")
	}
	result := &model.CustomerInvoice{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var invoice model.CustomerInvoice
		if err := database.ForUpdate(tx).First(&invoice, command.InvoiceID).Error; err != nil {
			return domain.NotFound("invoice not found")
		}
		if invoice.Status == model.InvoiceStatusVoid {
			*result = invoice
			return nil
		}
		if invoice.Status != model.InvoiceStatusPaid {
			return domain.Conflict("only a paid invoice can be voided")
		}
		var period model.PlanPeriod
		if err := database.ForUpdate(tx).First(&period, invoice.PeriodID).Error; err != nil {
			return fmt.Errorf("load invoice plan period: %w", err)
		}
		if period.Status != model.PeriodStatusCanceled {
			return domain.Conflict("invoice plan period must be canceled before the invoice can be voided")
		}
		now := time.Now().UTC()
		before := invoice
		invoice.Status = model.InvoiceStatusVoid
		invoice.VoidAt = &now
		if err := tx.Save(&invoice).Error; err != nil {
			return err
		}
		if err := support.Audit(tx, &invoice.CustomerID, command.Actor, "invoice.void", "customer_invoice", support.ResourceID(invoice.ID), &before, &invoice, command.Reason, command.RequestID); err != nil {
			return err
		}
		*result = invoice
		return nil
	})
	return result, err
}

func (s *Service) ListInvoices(customerID uint64, limit int) ([]model.CustomerInvoice, error) {
	if customerID == 0 {
		return nil, domain.Invalid("customer_id is required")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var invoices []model.CustomerInvoice
	err := s.db.Where("customer_id = ?", customerID).Order("id desc").Limit(limit).Find(&invoices).Error
	return invoices, err
}

func (s *Service) ReconcilePeriods(now time.Time) error {
	now = now.UTC()
	var ids []uint64
	if err := s.db.Model(&model.PlanPeriod{}).
		Where("status IN ? AND (start_at <= ? OR end_at <= ?)", []string{
			model.PeriodStatusPendingPayment, model.PeriodStatusScheduled,
			model.PeriodStatusActive, model.PeriodStatusPastDue,
		}, now, now).Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.reconcilePeriod(id, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reconcilePeriod(periodID uint64, now time.Time) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var period model.PlanPeriod
		if err := database.ForUpdate(tx).First(&period, periodID).Error; err != nil {
			return err
		}
		before := period
		if !now.Before(period.EndAt) {
			period.Status = model.PeriodStatusExpired
			period.ExpiredAt = &now
		} else if !now.Before(period.StartAt) {
			if period.PaymentStatus == model.PaymentStatusPaid {
				period.Status = model.PeriodStatusActive
				if period.ActivatedAt == nil {
					period.ActivatedAt = &now
				}
			} else {
				period.Status = model.PeriodStatusPastDue
			}
		}
		if period.Status == before.Status {
			return nil
		}
		period.RowVersion++
		if err := tx.Save(&period).Error; err != nil {
			return err
		}
		if err := support.Audit(tx, &period.CustomerID, "period-worker", "plan.period.reconcile", "plan_period", support.ResourceID(period.ID), &before, &period, "", ""); err != nil {
			return err
		}
		if period.Status != model.PeriodStatusExpired && period.Status != model.PeriodStatusPastDue {
			return nil
		}
		return suspendAppsWithoutActivePlan(tx, period.CustomerID, now, "period-worker", "app.suspend.plan_inactive", "no active paid plan period", "")
	})
}

func suspendAppsWithoutActivePlan(tx *gorm.DB, customerID uint64, now time.Time, actor, action, reason, requestID string) error {
	var activePeriods int64
	if err := tx.Model(&model.PlanPeriod{}).
		Where("customer_id = ? AND status = ? AND payment_status = ? AND start_at <= ? AND end_at > ?", customerID, model.PeriodStatusActive, model.PaymentStatusPaid, now, now).
		Count(&activePeriods).Error; err != nil {
		return err
	}
	if activePeriods > 0 {
		return nil
	}
	var apps []model.CustomerApp
	if err := database.ForUpdate(tx).Where("customer_id = ? AND status = ?", customerID, model.AppStatusActive).Find(&apps).Error; err != nil {
		return err
	}
	for index := range apps {
		appBefore := apps[index]
		apps[index].Status = model.AppStatusSuspended
		apps[index].SuspendedAt = &now
		apps[index].AuthEpoch++
		apps[index].RowVersion++
		if err := tx.Save(&apps[index]).Error; err != nil {
			return err
		}
		if err := support.Enqueue(tx, &customerID, "CACHE_INVALIDATE", fmt.Sprintf("app:%d:epoch:%d", apps[index].ID, apps[index].AuthEpoch), map[string]any{
			"customer_id": customerID, "customer_app_id": apps[index].ID,
			"application_id": apps[index].AppID, "status": apps[index].Status, "auth_epoch": apps[index].AuthEpoch,
		}); err != nil {
			return err
		}
		if err := support.Audit(tx, &customerID, actor, action, "customer_app", support.ResourceID(apps[index].ID), &appBefore, &apps[index], reason, requestID); err != nil {
			return err
		}
	}
	return nil
}

func parseMoney(value string, positive bool) (string, error) {
	money, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil || (positive && !money.IsPositive()) || (!positive && money.IsNegative()) || money.Exponent() < -2 {
		return "", domain.Invalid("money must be a valid CNY amount with at most two decimal places")
	}
	return money.StringFixed(2), nil
}
