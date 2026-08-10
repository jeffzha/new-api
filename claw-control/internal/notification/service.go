package notification

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ExpiryWarningWindow = 7 * 24 * time.Hour

type Service struct{ db *gorm.DB }

type ListQuery struct {
	CustomerID *uint64
	UnreadOnly bool
	BeforeID   uint64
	Limit      int
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

// Reconcile persists in-site management notifications. Stable event keys and
// unique inserts make repeated runs safe across blue/green workers.
func (s *Service) Reconcile(now time.Time) error {
	now = now.UTC()
	var periods []model.PlanPeriod
	if err := s.db.Where(
		"payment_status = ? AND status IN ? AND end_at > ? AND end_at <= ?",
		model.PaymentStatusPaid,
		[]string{model.PeriodStatusScheduled, model.PeriodStatusActive},
		now, now.Add(ExpiryWarningWindow),
	).Find(&periods).Error; err != nil {
		return err
	}
	for index := range periods {
		period := periods[index]
		if err := s.create(model.GovernanceNotification{
			EventKey:   "plan-expiring:" + strconv.FormatUint(period.ID, 10),
			CustomerID: period.CustomerID, Type: "plan_expiring",
			Title:        "Plan period expiring",
			Message:      fmt.Sprintf("Paid plan period %d expires at %s.", period.ID, period.EndAt.UTC().Format(time.RFC3339)),
			ResourceType: "plan_period", ResourceID: support.ResourceID(period.ID),
			OccurredAt: now,
		}); err != nil {
			return err
		}
	}

	var renewalPeriods []model.PlanPeriod
	if err := s.db.Where(
		"payment_status = ? AND status IN ?",
		model.PaymentStatusPaid,
		[]string{model.PeriodStatusScheduled, model.PeriodStatusActive},
	).Find(&renewalPeriods).Error; err != nil {
		return err
	}
	for index := range renewalPeriods {
		period := renewalPeriods[index]
		var prior int64
		if err := s.db.Model(&model.PlanPeriod{}).Where(
			"customer_id = ? AND id <> ? AND payment_status = ? AND end_at <= ?",
			period.CustomerID, period.ID, model.PaymentStatusPaid, period.StartAt,
		).Count(&prior).Error; err != nil {
			return err
		}
		if prior > 0 {
			occurredAt := period.CreatedAt
			if period.ActivatedAt != nil {
				occurredAt = *period.ActivatedAt
			}
			if err := s.create(model.GovernanceNotification{
				EventKey:   "plan-renewed:" + strconv.FormatUint(period.ID, 10),
				CustomerID: period.CustomerID, Type: "plan_renewed",
				Title:        "Plan period renewed",
				Message:      fmt.Sprintf("Paid renewal period %d covers %s through %s.", period.ID, period.StartAt.UTC().Format(time.RFC3339), period.EndAt.UTC().Format(time.RFC3339)),
				ResourceType: "plan_period", ResourceID: support.ResourceID(period.ID),
				OccurredAt: occurredAt,
			}); err != nil {
				return err
			}
		}
	}

	var suspensionAudits []model.AdminAudit
	if err := s.db.Where("action = ?", "app.suspend.plan_inactive").Order("id asc").Find(&suspensionAudits).Error; err != nil {
		return err
	}
	for index := range suspensionAudits {
		audit := suspensionAudits[index]
		if audit.CustomerID == nil {
			continue
		}
		if err := s.create(model.GovernanceNotification{
			EventKey:   "plan-suspended:" + strconv.FormatUint(audit.ID, 10),
			CustomerID: *audit.CustomerID, Type: "plan_suspended",
			Title:        "App suspended after plan inactivity",
			Message:      "The customer App was suspended because no active paid plan period remained.",
			ResourceType: audit.ResourceType, ResourceID: audit.ResourceID,
			OccurredAt: audit.CreatedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) create(item model.GovernanceNotification) error {
	item.PublicID = support.PublicID("ntf")
	item.Status = model.NotificationStatusUnread
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error
}

func (s *Service) List(query ListQuery) ([]model.GovernanceNotification, error) {
	if query.Limit <= 0 || query.Limit > 200 {
		query.Limit = 100
	}
	db := s.db.Order("id desc").Limit(query.Limit)
	if query.CustomerID != nil {
		if *query.CustomerID == 0 {
			return nil, domain.Invalid("customer_id must be positive")
		}
		db = db.Where("customer_id = ?", *query.CustomerID)
	}
	if query.UnreadOnly {
		db = db.Where("status = ?", model.NotificationStatusUnread)
	}
	if query.BeforeID > 0 {
		db = db.Where("id < ?", query.BeforeID)
	}
	var result []model.GovernanceNotification
	return result, db.Find(&result).Error
}

func (s *Service) MarkRead(publicID, actor, requestID string) (*model.GovernanceNotification, error) {
	publicID = strings.TrimSpace(publicID)
	if publicID == "" {
		return nil, domain.Invalid("notification_id is required")
	}
	var result model.GovernanceNotification
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).Where("public_id = ?", publicID).First(&result).Error; err != nil {
			return domain.NotFound("notification not found")
		}
		if result.Status == model.NotificationStatusRead {
			return nil
		}
		before := result
		now := time.Now().UTC()
		result.Status = model.NotificationStatusRead
		result.ReadAt = &now
		result.ReadBy = actor
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		return support.Audit(tx, &result.CustomerID, actor, "notification.read", "governance_notification", result.PublicID, &before, &result, "", requestID)
	})
	return &result, err
}
