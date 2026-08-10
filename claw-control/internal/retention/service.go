package retention

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

const (
	MinimumRetentionDays = 30
	MaximumRetentionDays = 3650
)

type Service struct{ db *gorm.DB }

type SetPolicyCommand struct {
	CustomerID      uint64
	ExpectedVersion int64
	RetentionDays   int
	LegalHold       bool
	Actor           string
	RequestID       string
}

type ExecuteCommand struct {
	RunID           string
	ExpectedVersion int64
	Actor           string
	RequestID       string
}

type Counts struct {
	ResourceBindings   int64 `json:"resource_bindings"`
	ControlSessions    int64 `json:"control_sessions"`
	SSOTickets         int64 `json:"sso_tickets"`
	SelectionNonces    int64 `json:"selection_nonces"`
	ADPAccountBindings int64 `json:"adp_account_bindings"`
	IdentityBindings   int64 `json:"identity_bindings"`
	CustomerMembers    int64 `json:"customer_members"`
	AppVerifications   int64 `json:"app_verifications"`
	AppConfigurations  int64 `json:"app_configurations"`
	CustomerApps       int64 `json:"customer_apps"`
	DeliveredOutbox    int64 `json:"delivered_outbox"`
	PendingOutbox      int64 `json:"pending_outbox"`
	PreservedEvidence  int64 `json:"preserved_evidence"`
	PreservedInvoices  int64 `json:"preserved_invoices"`
	PreservedAudits    int64 `json:"preserved_audits"`
	PreservedUsage     int64 `json:"preserved_usage_audits"`
	PreservedPeriods   int64 `json:"preserved_plan_periods"`
}

type RunView struct {
	Run    model.CustomerRetentionRun `json:"run"`
	Counts Counts                     `json:"counts"`
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) SetPolicy(command SetPolicyCommand) (*model.CustomerRetentionPolicy, error) {
	if command.CustomerID == 0 || command.RetentionDays < MinimumRetentionDays || command.RetentionDays > MaximumRetentionDays {
		return nil, domain.Invalid("customer_id and retention_days between 30 and 3650 are required")
	}
	var result model.CustomerRetentionPolicy
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var customer model.Customer
		if err := tx.First(&customer, command.CustomerID).Error; err != nil {
			return domain.NotFound("customer not found")
		}
		find := database.ForUpdate(tx).Where("customer_id = ?", command.CustomerID).First(&result)
		if find.Error != nil && !errors.Is(find.Error, gorm.ErrRecordNotFound) {
			return find.Error
		}
		var before any
		if errors.Is(find.Error, gorm.ErrRecordNotFound) {
			if command.ExpectedVersion != 0 {
				return domain.Conflict("expected_version must be 0 for the first retention policy")
			}
			result = model.CustomerRetentionPolicy{
				CustomerID: command.CustomerID, RetentionDays: command.RetentionDays,
				LegalHold: command.LegalHold, Status: model.RetentionPolicyStatusActive,
				RowVersion: 1, UpdatedBy: command.Actor,
			}
		} else {
			if result.RowVersion != command.ExpectedVersion {
				return domain.Conflict("retention policy row version changed; expected %d, current %d", command.ExpectedVersion, result.RowVersion)
			}
			copy := result
			before = &copy
			result.RetentionDays = command.RetentionDays
			result.LegalHold = command.LegalHold
			result.Status = model.RetentionPolicyStatusActive
			result.RowVersion++
			result.UpdatedBy = command.Actor
		}
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		return support.Audit(tx, &command.CustomerID, command.Actor, "retention.policy.save", "customer_retention_policy", support.ResourceID(result.ID), before, &result, "", command.RequestID)
	})
	return &result, err
}

func (s *Service) Policy(customerID uint64) (*model.CustomerRetentionPolicy, error) {
	if customerID == 0 {
		return nil, domain.Invalid("customer_id is required")
	}
	var result model.CustomerRetentionPolicy
	if err := s.db.Where("customer_id = ?", customerID).First(&result).Error; err != nil {
		return nil, domain.NotFound("customer retention policy not found")
	}
	return &result, nil
}

func (s *Service) DryRun(customerID uint64, now time.Time, actor, requestID string) (*RunView, error) {
	if customerID == 0 {
		return nil, domain.Invalid("customer_id is required")
	}
	now = now.UTC()
	var result RunView
	err := s.db.Transaction(func(tx *gorm.DB) error {
		customer, policy, err := eligibleCustomer(tx, customerID, now)
		if err != nil {
			return err
		}
		counts, err := countCustomerData(tx, customerID)
		if err != nil {
			return err
		}
		countsJSON, err := jsonx.Marshal(counts)
		if err != nil {
			return err
		}
		run := model.CustomerRetentionRun{
			PublicID: support.PublicID("rtn"), CustomerID: customerID, PolicyID: policy.ID,
			PolicyVersion: policy.RowVersion,
			CutoffAt:      now, CountsJSON: string(countsJSON), Status: model.RetentionRunStatusPlanned,
			RowVersion: 1, RequestedBy: actor,
		}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		if err := support.Audit(tx, &customer.ID, actor, "retention.dry_run", "customer_retention_run", run.PublicID, nil, &run, "", requestID); err != nil {
			return err
		}
		result = RunView{Run: run, Counts: counts}
		return nil
	})
	return &result, err
}

func (s *Service) Execute(command ExecuteCommand, now time.Time) (*RunView, error) {
	command.RunID = strings.TrimSpace(command.RunID)
	if command.RunID == "" || command.ExpectedVersion <= 0 {
		return nil, domain.Invalid("run_id and expected_version are required")
	}
	now = now.UTC()
	var result RunView
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var run model.CustomerRetentionRun
		if err := database.ForUpdate(tx).Where("public_id = ?", command.RunID).First(&run).Error; err != nil {
			return domain.NotFound("retention run not found")
		}
		if run.Status == model.RetentionRunStatusCompleted || run.Status == model.RetentionRunStatusPending {
			var counts Counts
			if err := jsonx.Unmarshal([]byte(run.CountsJSON), &counts); err != nil {
				return err
			}
			result = RunView{Run: run, Counts: counts}
			return nil
		}
		if run.RowVersion != command.ExpectedVersion {
			return domain.Conflict("retention run row version changed; expected %d, current %d", command.ExpectedVersion, run.RowVersion)
		}
		if run.Status != model.RetentionRunStatusPlanned {
			return domain.Conflict("retention run is not executable")
		}
		customer, policy, err := eligibleCustomer(tx, run.CustomerID, now)
		if err != nil {
			return err
		}
		if policy.ID != run.PolicyID || policy.RowVersion != run.PolicyVersion {
			return domain.Conflict("retention policy changed after the dry-run; create a new dry-run")
		}
		counts, err := countCustomerData(tx, run.CustomerID)
		if err != nil {
			return err
		}
		if counts.PendingOutbox > 0 {
			return domain.Conflict("customer has pending control events; retry after outbox delivery")
		}
		countsJSON, err := jsonx.Marshal(counts)
		if err != nil {
			return err
		}
		run.CountsJSON = string(countsJSON)
		run.Status = model.RetentionRunStatusPending
		run.ExecutedBy = command.Actor
		run.RowVersion++
		if err := tx.Save(&run).Error; err != nil {
			return err
		}
		delivery := model.CustomerRetentionDelivery{
			IntentID: support.PublicID("rti"), RunID: run.ID, CustomerID: run.CustomerID,
			PolicyID: policy.ID, PolicyVersion: policy.RowVersion, CutoffAt: run.CutoffAt,
			Status: model.RetentionDeliveryPending, NextRetryAt: now,
		}
		if err := tx.Create(&delivery).Error; err != nil {
			return err
		}
		if err := support.Audit(tx, &customer.ID, command.Actor, "retention.delivery.enqueue", "customer_retention_delivery", delivery.IntentID, nil, &delivery, "", command.RequestID); err != nil {
			return err
		}
		result = RunView{Run: run, Counts: counts}
		return nil
	})
	return &result, err
}

func eligibleCustomer(tx *gorm.DB, customerID uint64, now time.Time) (*model.Customer, *model.CustomerRetentionPolicy, error) {
	var customer model.Customer
	if err := database.ForUpdate(tx).First(&customer, customerID).Error; err != nil {
		return nil, nil, domain.NotFound("customer not found")
	}
	if customer.Status != model.CustomerStatusArchived || customer.ArchivedAt == nil {
		return nil, nil, domain.Conflict("retention cleanup is restricted to archived customers")
	}
	var policy model.CustomerRetentionPolicy
	if err := database.ForUpdate(tx).Where("customer_id = ?", customerID).First(&policy).Error; err != nil {
		return nil, nil, domain.NotFound("active customer retention policy not found")
	}
	if policy.Status != model.RetentionPolicyStatusActive || policy.LegalHold {
		return nil, nil, domain.Conflict("retention policy is inactive or under legal hold")
	}
	eligibleAt := customer.ArchivedAt.AddDate(0, 0, policy.RetentionDays)
	if now.Before(eligibleAt) {
		return nil, nil, domain.Conflict("customer has not satisfied the configured retention period")
	}
	return &customer, &policy, nil
}

func countCustomerData(tx *gorm.DB, customerID uint64) (Counts, error) {
	counts := Counts{}
	queries := []struct {
		model any
		where string
		args  []any
		value *int64
	}{
		{&model.ResourceBinding{}, "customer_id = ?", []any{customerID}, &counts.ResourceBindings},
		{&model.ControlSession{}, "customer_id = ?", []any{customerID}, &counts.ControlSessions},
		{&model.SSOTicket{}, "customer_id = ?", []any{customerID}, &counts.SSOTickets},
		{&model.ContextSelectionNonce{}, "customer_id = ?", []any{customerID}, &counts.SelectionNonces},
		{&model.ADPAccountBinding{}, "customer_id = ?", []any{customerID}, &counts.ADPAccountBindings},
		{&model.IdentityBinding{}, "customer_id = ?", []any{customerID}, &counts.IdentityBindings},
		{&model.CustomerMember{}, "customer_id = ?", []any{customerID}, &counts.CustomerMembers},
		{&model.CustomerApp{}, "customer_id = ?", []any{customerID}, &counts.CustomerApps},
		{&model.ControlOutbox{}, "customer_id = ? AND status = ?", []any{customerID, model.OutboxStatusDelivered}, &counts.DeliveredOutbox},
		{&model.ControlOutbox{}, "customer_id = ? AND status = ?", []any{customerID, model.OutboxStatusPending}, &counts.PendingOutbox},
		{&model.EvidenceObject{}, "customer_id = ?", []any{customerID}, &counts.PreservedEvidence},
		{&model.CustomerInvoice{}, "customer_id = ?", []any{customerID}, &counts.PreservedInvoices},
		{&model.AdminAudit{}, "customer_id = ?", []any{customerID}, &counts.PreservedAudits},
		{&model.UsageAudit{}, "customer_id = ?", []any{customerID}, &counts.PreservedUsage},
		{&model.PlanPeriod{}, "customer_id = ?", []any{customerID}, &counts.PreservedPeriods},
	}
	for _, query := range queries {
		if err := tx.Model(query.model).Where(query.where, query.args...).Count(query.value).Error; err != nil {
			return counts, err
		}
	}
	var appIDs []uint64
	if err := tx.Model(&model.CustomerApp{}).Where("customer_id = ?", customerID).Pluck("id", &appIDs).Error; err != nil {
		return counts, err
	}
	if len(appIDs) > 0 {
		if err := tx.Model(&model.AppVerification{}).Where("customer_app_id IN ?", appIDs).Count(&counts.AppVerifications).Error; err != nil {
			return counts, err
		}
		if err := tx.Model(&model.AppConfigVersion{}).Where("customer_app_id IN ?", appIDs).Count(&counts.AppConfigurations).Error; err != nil {
			return counts, err
		}
	}
	return counts, nil
}

func deleteCustomerOperationalData(tx *gorm.DB, customerID uint64) error {
	var appIDs []uint64
	if err := tx.Model(&model.CustomerApp{}).Where("customer_id = ?", customerID).Pluck("id", &appIDs).Error; err != nil {
		return err
	}
	operations := []struct {
		model any
		where string
		args  []any
	}{
		{&model.ResourceBinding{}, "customer_id = ?", []any{customerID}},
		{&model.ControlSession{}, "customer_id = ?", []any{customerID}},
		{&model.SSOTicket{}, "customer_id = ?", []any{customerID}},
		{&model.ContextSelectionNonce{}, "customer_id = ?", []any{customerID}},
		{&model.ADPAccountBinding{}, "customer_id = ?", []any{customerID}},
	}
	if len(appIDs) > 0 {
		operations = append(operations,
			struct {
				model any
				where string
				args  []any
			}{&model.AppVerification{}, "customer_app_id IN ?", []any{appIDs}},
			struct {
				model any
				where string
				args  []any
			}{&model.AppConfigVersion{}, "customer_app_id IN ?", []any{appIDs}},
		)
	}
	operations = append(operations,
		struct {
			model any
			where string
			args  []any
		}{&model.CustomerApp{}, "customer_id = ?", []any{customerID}},
		struct {
			model any
			where string
			args  []any
		}{&model.IdentityBinding{}, "customer_id = ?", []any{customerID}},
		struct {
			model any
			where string
			args  []any
		}{&model.CustomerMember{}, "customer_id = ?", []any{customerID}},
		struct {
			model any
			where string
			args  []any
		}{&model.ControlOutbox{}, "customer_id = ? AND status = ?", []any{customerID, model.OutboxStatusDelivered}},
	)
	for _, operation := range operations {
		if err := tx.Where(operation.where, operation.args...).Delete(operation.model).Error; err != nil {
			return err
		}
	}
	return nil
}
