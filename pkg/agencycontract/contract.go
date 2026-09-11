// Package agencycontract contains the versioned, dependency-light contracts
// shared by the gateway and the agency-hub sidecar.
package agencycontract

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

const (
	SchemaVersion      = "agency-billing-v1"
	FundingRuleVersion = "paid_first_v1"
	DefaultSalesCapBPS = 30000
	MinCoefficientBPS  = 0
	MaxCoefficientBPS  = 100000
)

var (
	ErrInvalidPolicy      = errors.New("invalid agency pricing policy")
	ErrInvalidModelName   = errors.New("invalid agency model name")
	ErrPolicyRevision     = errors.New("agency pricing revision conflict")
	ErrInvalidFinancialID = errors.New("invalid financial charge id")
)

// ModelOverride is an exact public-model override. A nil coefficient inherits
// the policy default; zero is an intentional zero coefficient.
type ModelOverride struct {
	OriginModelName string `json:"origin_model_name"`
	SettlementBPS   *int   `json:"settlement_bps,omitempty"`
	SalesBPS        *int   `json:"sales_bps,omitempty"`
}

// Policy is an immutable pricing package. The active agency row points to one
// complete package rather than maintaining independently mutable defaults.
type Policy struct {
	Revision             int64           `json:"revision"`
	DefaultSettlementBPS int             `json:"default_settlement_bps"`
	DefaultSalesBPS      int             `json:"default_sales_bps"`
	MinSpreadBPS         int             `json:"min_spread_bps"`
	SalesCapBPS          int             `json:"sales_cap_bps"`
	ModelOverrides       []ModelOverride `json:"model_overrides,omitempty"`
}

type ResolvedPolicy struct {
	SettlementBPS   int
	SalesBPS        int
	ModelKey        string
	OriginModelName string
}

// ModelKey hashes the exact UTF-8 model name. The original name remains in the
// policy for display and collision verification.
func ModelKey(originModelName string) (string, error) {
	if strings.TrimSpace(originModelName) != originModelName || originModelName == "" || len([]rune(originModelName)) > 191 || len([]byte(originModelName)) > 764 {
		return "", ErrInvalidModelName
	}
	hash := sha256.Sum256([]byte(originModelName))
	return hex.EncodeToString(hash[:]), nil
}

func ValidateCoefficient(value, cap int) error {
	if value < MinCoefficientBPS || value > cap || value > MaxCoefficientBPS {
		return fmt.Errorf("coefficient %d is outside 0..%d", value, cap)
	}
	return nil
}

func ValidatePolicy(policy Policy) error {
	cap := policy.SalesCapBPS
	if cap == 0 {
		cap = DefaultSalesCapBPS
	}
	if cap < 1 || cap > MaxCoefficientBPS {
		return fmt.Errorf("invalid sales cap: %d", policy.SalesCapBPS)
	}
	if policy.MinSpreadBPS < 0 || policy.MinSpreadBPS > cap {
		return fmt.Errorf("invalid minimum spread: %d", policy.MinSpreadBPS)
	}
	if err := ValidateCoefficient(policy.DefaultSettlementBPS, cap); err != nil {
		return err
	}
	if err := ValidateCoefficient(policy.DefaultSalesBPS, cap); err != nil {
		return err
	}
	if policy.DefaultSalesBPS < policy.DefaultSettlementBPS+policy.MinSpreadBPS {
		return fmt.Errorf("default sales coefficient must be at least settlement plus spread")
	}
	seen := make(map[string]struct{}, len(policy.ModelOverrides))
	if len(policy.ModelOverrides) > 1000 {
		return errors.New("too many model overrides")
	}
	for _, override := range policy.ModelOverrides {
		key, err := ModelKey(override.OriginModelName)
		if err != nil {
			return err
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate model override: %s", override.OriginModelName)
		}
		seen[key] = struct{}{}
		settlement := policy.DefaultSettlementBPS
		sales := policy.DefaultSalesBPS
		if override.SettlementBPS != nil {
			settlement = *override.SettlementBPS
		}
		if override.SalesBPS != nil {
			sales = *override.SalesBPS
		}
		if err := ValidateCoefficient(settlement, cap); err != nil {
			return err
		}
		if err := ValidateCoefficient(sales, cap); err != nil {
			return err
		}
		if sales < settlement+policy.MinSpreadBPS {
			return fmt.Errorf("model %s violates minimum spread", override.OriginModelName)
		}
	}
	return nil
}

func Resolve(policy Policy, originModelName string) (ResolvedPolicy, error) {
	if err := ValidatePolicy(policy); err != nil {
		return ResolvedPolicy{}, err
	}
	key, err := ModelKey(originModelName)
	if err != nil {
		return ResolvedPolicy{}, err
	}
	resolved := ResolvedPolicy{SettlementBPS: policy.DefaultSettlementBPS, SalesBPS: policy.DefaultSalesBPS, ModelKey: key, OriginModelName: originModelName}
	for _, override := range policy.ModelOverrides {
		overrideKey, keyErr := ModelKey(override.OriginModelName)
		if keyErr != nil || overrideKey != key {
			continue
		}
		if override.SettlementBPS != nil {
			resolved.SettlementBPS = *override.SettlementBPS
		}
		if override.SalesBPS != nil {
			resolved.SalesBPS = *override.SalesBPS
		}
		break
	}
	return resolved, nil
}

// ChargeResult applies the existing gateway-computed standard quota Q. The
// sidecar never derives Q from model prices; it only applies frozen BPS values.
type ChargeResult struct {
	StandardQuota              int64 `json:"standard_quota"`
	ChargedQuota               int64 `json:"charged_quota"`
	SettlementCostQuota        int64 `json:"settlement_cost_quota"`
	TheoreticalCommissionQuota int64 `json:"theoretical_commission_quota"`
	PaidAllocatedQuota         int64 `json:"paid_allocated_quota"`
	CommissionQuota            int64 `json:"commission_quota"`
}

// BillingEvent is the gateway-owned financial snapshot consumed by the
// sidecar. The gateway must fill StandardQuota and PaidAllocatedQuota after
// its own pricing/funding transaction; the sidecar does not infer them.
type BillingEvent struct {
	SchemaVersion     string `json:"schema_version"`
	EventID           string `json:"event_id"`
	EventType         string `json:"event_type"`
	OriginalEventID   string `json:"original_event_id,omitempty"`
	FinancialChargeID string `json:"financial_charge_id"`
	OperationID       string `json:"operation_id"`
	SegmentNo         int    `json:"segment_no"`
	JournalRevision   int64  `json:"journal_revision"`
	MoneySeq          int64  `json:"money_seq"`
	EventIndex        int    `json:"event_index"`
	EventCount        int    `json:"event_count"`
	// UsageHash identifies the exact usage frame represented by a realtime
	// segment. It is part of the immutable financial payload and is used to
	// reject a same-segment retry with different usage.
	UsageHash string `json:"usage_hash,omitempty"`
	// CumulativeUsage is a canonical JSON snapshot of the upstream cumulative
	// usage at this segment. It is intentionally opaque to the sidecar; the
	// gateway owns usage arithmetic and only persists the audit snapshot.
	CumulativeUsage      string `json:"cumulative_usage,omitempty"`
	OccurredAtMS         int64  `json:"occurred_at_ms"`
	UserID               int64  `json:"user_id"`
	TokenID              *int64 `json:"token_id,omitempty"`
	AgencyID             *int64 `json:"agency_id,omitempty"`
	BindingID            *int64 `json:"binding_id,omitempty"`
	OriginModelName      string `json:"origin_model_name"`
	Endpoint             string `json:"endpoint,omitempty"`
	BusinessStatus       string `json:"business_status"`
	BillingStatus        string `json:"billing_status"`
	CurrencyCode         string `json:"currency_code"`
	QuotaPerUnit         string `json:"quota_per_unit"`
	ExchangeRate         string `json:"exchange_rate"`
	SettlementBPS        int    `json:"settlement_bps"`
	SalesBPS             int    `json:"sales_bps"`
	CommissionEligible   bool   `json:"commission_eligible"`
	CommissionSkipReason string `json:"commission_skip_reason,omitempty"`
	// BillingBasis is a redacted immutable snapshot of the gateway inputs
	// needed to explain the charge. It must not contain credentials or raw
	// request/response bodies.
	BillingBasis                   string `json:"billing_basis,omitempty"`
	StandardQuota                  int64  `json:"standard_quota"`
	ChargedTotalQuota              int64  `json:"charged_total_quota"`
	CommissionableQuota            int64  `json:"commissionable_charged_quota"`
	NoncommissionableQuota         int64  `json:"noncommissionable_quota"`
	SettlementCostQuota            int64  `json:"settlement_cost_quota"`
	TheoreticalCommissionQuota     int64  `json:"theoretical_commission_quota"`
	PaidAllocatedQuota             int64  `json:"paid_allocated_quota"`
	CommissionQuota                int64  `json:"commission_quota"`
	CommissionAmountMicros         int64  `json:"commission_amount_micros"`
	ReversedCommissionAmountMicros int64  `json:"reversed_commission_amount_micros"`
	FinancialFinal                 bool   `json:"financial_final"`
}

type PricingSnapshot struct {
	SchemaVersion         string `json:"schema_version"`
	FinancialChargeID     string `json:"financial_charge_id"`
	UserID                int64  `json:"user_id"`
	TokenID               int64  `json:"token_id"`
	AgencyID              int64  `json:"agency_id"`
	BindingID             int64  `json:"binding_id"`
	BindingRevision       int64  `json:"binding_revision"`
	AgencyStateRevision   int64  `json:"agency_state_revision"`
	PolicyVersionID       int64  `json:"policy_version_id"`
	PolicyRevision        int64  `json:"policy_revision"`
	OriginModelName       string `json:"origin_model_name"`
	ModelKey              string `json:"model_key"`
	SettlementBPS         int    `json:"settlement_bps"`
	SalesBPS              int    `json:"sales_bps"`
	CommissionEligible    bool   `json:"commission_eligible"`
	EligibilityReason     string `json:"eligibility_reason,omitempty"`
	CurrencyCode          string `json:"currency_code"`
	CurrencyConfigVersion string `json:"currency_config_version"`
	QuotaPerUnit          string `json:"quota_per_unit"`
	ExchangeRate          string `json:"exchange_rate"`
	AcceptedAtMS          int64  `json:"accepted_at_ms"`
	FundingRuleVersion    string `json:"funding_rule_version"`
	PricingEngineVersion  string `json:"pricing_engine_version"`
}

func applyBPS(quota int64, bps int, round bool) (int64, error) {
	if quota < 0 || bps < 0 || bps > MaxCoefficientBPS {
		return 0, errors.New("invalid quota or coefficient")
	}
	product := decimal.NewFromInt(quota).Mul(decimal.NewFromInt(int64(bps))).Div(decimal.NewFromInt(10000))
	if round {
		product = product.Round(0)
	} else {
		product = product.Truncate(0)
	}
	max := decimal.NewFromInt(math.MaxInt64)
	if product.GreaterThan(max) {
		return 0, errors.New("quota calculation overflow")
	}
	return product.IntPart(), nil
}

func Calculate(standardQuota int64, resolved ResolvedPolicy, paidAllocatedQuota int64, round bool) (ChargeResult, error) {
	if standardQuota < 0 || paidAllocatedQuota < 0 {
		return ChargeResult{}, errors.New("invalid charge inputs")
	}
	charged, err := applyBPS(standardQuota, resolved.SalesBPS, round)
	if err != nil {
		return ChargeResult{}, err
	}
	if paidAllocatedQuota > charged {
		return ChargeResult{}, errors.New("paid allocation exceeds charged quota")
	}
	settlement := int64(0)
	if resolved.SettlementBPS > 0 {
		settlement, err = applyBPS(standardQuota, resolved.SettlementBPS, round)
		if err != nil {
			return ChargeResult{}, err
		}
	}
	commission := charged - settlement
	if commission < 0 {
		return ChargeResult{}, errors.New("negative theoretical commission")
	}
	// K = Round(G * P / B), with B=0 explicitly yielding zero.
	actualCommission := int64(0)
	if charged > 0 {
		actualCommission, err = applyRatio(commission, paidAllocatedQuota, charged, round)
		if err != nil {
			return ChargeResult{}, err
		}
	}
	return ChargeResult{StandardQuota: standardQuota, ChargedQuota: charged, SettlementCostQuota: settlement, TheoreticalCommissionQuota: commission, PaidAllocatedQuota: paidAllocatedQuota, CommissionQuota: actualCommission}, nil
}

func applyRatio(value, numerator, denominator int64, round bool) (int64, error) {
	if value < 0 || numerator < 0 || denominator <= 0 || numerator > denominator {
		return 0, errors.New("invalid ratio")
	}
	result := decimal.NewFromInt(value).Mul(decimal.NewFromInt(numerator)).Div(decimal.NewFromInt(denominator))
	if round {
		result = result.Round(0)
	} else {
		result = result.Truncate(0)
	}
	if result.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, errors.New("ratio calculation overflow")
	}
	return result.IntPart(), nil
}

// CommissionForPaid applies the paid-funding ratio K = Round(G*P/B).
// It is exported so gateway settlement can use the actual charged quota B
// when the provider's final usage differs from the estimate.
func CommissionForPaid(theoreticalCommission, paidAllocated, chargedQuota int64, round bool) (int64, error) {
	if chargedQuota == 0 {
		return 0, nil
	}
	return applyRatio(theoreticalCommission, paidAllocated, chargedQuota, round)
}

// CanonicalHash is used for immutable policy and event payload fingerprints.
func CanonicalHash(value any) (string, error) {
	encoded, err := common.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}
