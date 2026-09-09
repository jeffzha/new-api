package marginreport

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type Service struct {
	db *gorm.DB
}

type Command struct {
	CustomerID *uint64
	StartAt    *time.Time
	EndAt      *time.Time
}

type Breakdown struct {
	AppExactCostCNY            string `json:"app_exact_cost_cny"`
	AppExactRecords            int64  `json:"app_exact_records"`
	EstimatedAllocationCostCNY string `json:"estimated_allocation_cost_cny"`
	EstimatedAllocationRecords int64  `json:"estimated_allocation_records"`
	AccountOnlyCostCNY         string `json:"account_only_cost_cny"`
	AccountOnlyRecords         int64  `json:"account_only_records"`
	UnverifiedCostCNY          string `json:"unverified_cost_cny"`
	UnverifiedRecords          int64  `json:"unverified_records"`
}

type Report struct {
	Scope                          string     `json:"scope"`
	CustomerID                     *uint64    `json:"customer_id,omitempty"`
	StartAt                        *time.Time `json:"start_at,omitempty"`
	EndAt                          *time.Time `json:"end_at,omitempty"`
	InvoicedRevenueCNY             string     `json:"invoiced_revenue_cny"`
	ReviewedCostCNY                string     `json:"reviewed_cost_cny"`
	MarginCNY                      string     `json:"margin_cny"`
	Confidence                     string     `json:"confidence"`
	AccountOnlyAllocatedToCustomer bool       `json:"account_only_allocated_to_customer"`
	Formula                        string     `json:"formula"`
	Breakdown                      Breakdown  `json:"breakdown"`
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

// Build reports fixed, already-issued plan revenue against reviewed upstream
// cost. It is intentionally read-only and never changes invoices, quotas, or
// customer balances. account_only cost participates only in the platform
// report and is never allocated to an individual customer.
func (s *Service) Build(command Command) (*Report, error) {
	if (command.StartAt == nil) != (command.EndAt == nil) {
		return nil, domain.Invalid("start_at and end_at must be supplied together")
	}
	if command.StartAt != nil {
		start := command.StartAt.UTC()
		end := command.EndAt.UTC()
		if !end.After(start) {
			return nil, domain.Invalid("end_at must be after start_at")
		}
		command.StartAt = &start
		command.EndAt = &end
	}
	if command.CustomerID != nil {
		var count int64
		if err := s.db.Model(&model.Customer{}).Where("id = ?", *command.CustomerID).Count(&count).Error; err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, domain.NotFound("customer not found")
		}
	}

	invoiceQuery := s.db.Where("status = ?", model.InvoiceStatusPaid)
	if command.CustomerID != nil {
		invoiceQuery = invoiceQuery.Where("customer_id = ?", *command.CustomerID)
	}
	if command.StartAt != nil {
		invoiceQuery = invoiceQuery.Where("issued_at >= ? AND issued_at < ?", *command.StartAt, *command.EndAt)
	}
	var invoices []model.CustomerInvoice
	if err := invoiceQuery.Find(&invoices).Error; err != nil {
		return nil, err
	}
	revenue := decimal.Zero
	for _, invoice := range invoices {
		amount, parseErr := decimal.NewFromString(strings.TrimSpace(invoice.AmountCNY))
		if parseErr != nil || amount.IsNegative() {
			return nil, fmt.Errorf("invoice %s contains invalid non-negative revenue", invoice.InvoiceNumber)
		}
		revenue = revenue.Add(amount)
	}

	usageQuery := s.db.Model(&model.UsageAudit{})
	if command.CustomerID != nil {
		usageQuery = usageQuery.Where("customer_id = ?", *command.CustomerID)
	}
	if command.StartAt != nil {
		usageQuery = usageQuery.Where("period_start >= ? AND period_end <= ?", *command.StartAt, *command.EndAt)
	}
	var audits []model.UsageAudit
	if err := usageQuery.Find(&audits).Error; err != nil {
		return nil, err
	}

	exact := decimal.Zero
	estimated := decimal.Zero
	accountOnly := decimal.Zero
	unverified := decimal.Zero
	breakdown := Breakdown{}
	for _, audit := range audits {
		cost, parseErr := decimal.NewFromString(strings.TrimSpace(audit.UpstreamCostCNY))
		if parseErr != nil || cost.IsNegative() {
			return nil, fmt.Errorf("usage audit %s contains invalid upstream cost", audit.PublicID)
		}
		if audit.Status == model.UsageAuditStatusLocked {
			switch audit.AllocationConfidence {
			case model.AllocationAppExact:
				exact = exact.Add(cost)
				breakdown.AppExactRecords++
			case model.AllocationEstimatedAllocation:
				estimated = estimated.Add(cost)
				breakdown.EstimatedAllocationRecords++
			case model.AllocationAccountOnly:
				if command.CustomerID != nil {
					return nil, fmt.Errorf("customer-attributed account_only audit %s violates the allocation boundary", audit.PublicID)
				}
				accountOnly = accountOnly.Add(cost)
				breakdown.AccountOnlyRecords++
			default:
				return nil, fmt.Errorf("reviewed usage audit %s has invalid confidence", audit.PublicID)
			}
		} else if audit.AllocationConfidence == model.AllocationUnverified {
			unverified = unverified.Add(cost)
			breakdown.UnverifiedRecords++
		}
	}

	reviewed := exact.Add(estimated)
	confidence := model.AllocationUnverified
	if breakdown.EstimatedAllocationRecords > 0 {
		confidence = model.AllocationEstimatedAllocation
	} else if breakdown.AppExactRecords > 0 {
		confidence = model.AllocationAppExact
	}
	scope := "customer"
	if command.CustomerID == nil {
		scope = "platform"
		reviewed = reviewed.Add(accountOnly)
		if breakdown.AccountOnlyRecords > 0 {
			confidence = model.AllocationAccountOnly
		}
	}
	breakdown.AppExactCostCNY = exact.StringFixed(2)
	breakdown.EstimatedAllocationCostCNY = estimated.StringFixed(2)
	breakdown.AccountOnlyCostCNY = accountOnly.StringFixed(2)
	breakdown.UnverifiedCostCNY = unverified.StringFixed(2)
	return &Report{
		Scope: scope, CustomerID: command.CustomerID, StartAt: command.StartAt, EndAt: command.EndAt,
		InvoicedRevenueCNY: revenue.StringFixed(2), ReviewedCostCNY: reviewed.StringFixed(2),
		MarginCNY: revenue.Sub(reviewed).StringFixed(2), Confidence: confidence,
		AccountOnlyAllocatedToCustomer: false,
		Formula:                        "margin_cny = invoiced_revenue_cny - reviewed_cost_cny",
		Breakdown:                      breakdown,
	}, nil
}
