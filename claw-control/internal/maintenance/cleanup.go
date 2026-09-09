package maintenance

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"gorm.io/gorm"
)

type CleanupResult struct {
	EntryTickets    int64 `json:"entry_tickets"`
	SSOTickets      int64 `json:"sso_tickets"`
	ControlSessions int64 `json:"control_sessions"`
	AdminSessions   int64 `json:"admin_sessions"`
	ServiceNonces   int64 `json:"service_nonces"`
	SelectionNonces int64 `json:"selection_nonces"`
	DeliveredOutbox int64 `json:"delivered_outbox"`
}

type Service struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Cleanup(ctx context.Context, now time.Time, ephemeralRetention, deliveredOutboxRetention time.Duration) (*CleanupResult, error) {
	if ephemeralRetention <= 0 || deliveredOutboxRetention <= 0 {
		return nil, gorm.ErrInvalidValue
	}
	now = now.UTC()
	ephemeralBefore := now.Add(-ephemeralRetention)
	outboxBefore := now.Add(-deliveredOutboxRetention)
	result := &CleanupResult{}
	operations := []struct {
		model any
		where string
		args  []any
		count *int64
	}{
		{model: &model.EntryTicket{}, where: "expires_at < ?", args: []any{ephemeralBefore}, count: &result.EntryTickets},
		{model: &model.SSOTicket{}, where: "expires_at < ?", args: []any{ephemeralBefore}, count: &result.SSOTickets},
		{model: &model.ContextSelectionNonce{}, where: "expires_at < ?", args: []any{ephemeralBefore}, count: &result.SelectionNonces},
		{model: &model.ControlSession{}, where: "expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)", args: []any{ephemeralBefore, ephemeralBefore}, count: &result.ControlSessions},
		{model: &model.AdminSession{}, where: "expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)", args: []any{ephemeralBefore, ephemeralBefore}, count: &result.AdminSessions},
		{model: &model.ServiceNonce{}, where: "expires_at < ?", args: []any{ephemeralBefore}, count: &result.ServiceNonces},
		{model: &model.ControlOutbox{}, where: "status = ? AND delivered_at < ?", args: []any{model.OutboxStatusDelivered, outboxBefore}, count: &result.DeliveredOutbox},
	}
	for _, operation := range operations {
		deleted := s.db.WithContext(ctx).Where(operation.where, operation.args...).Delete(operation.model)
		if deleted.Error != nil {
			return nil, deleted.Error
		}
		*operation.count = deleted.RowsAffected
	}
	return result, nil
}
