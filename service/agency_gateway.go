package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// RecordAgencyRealtimeSegment persists one successful realtime usage segment.
//
// Realtime providers commonly report cumulative usage and may repeat a usage
// frame during websocket retries. The gateway gives each logical connection a
// stable charge id and a local segment number; this function makes
// (charge_id, segment_no, usage_hash) durable in the journal and immutable
// outbox. Replaying the same frame is a no-op, while a different frame for an
// already committed segment is rejected as an integrity conflict.
//
// quota and paidAllocated are the values actually settled for this segment;
// the function deliberately does not recalculate provider pricing. The
// caller's existing quota engine remains the source of truth.
func RecordAgencyRealtimeSegment(relayInfo *relaycommon.RelayInfo, segmentNo int, usage, cumulative *dto.RealtimeUsage, quota, paidAllocated int64, status string) error {
	if relayInfo == nil || relayInfo.AgencyPricing == nil {
		return nil
	}
	if segmentNo < 0 || quota < 0 || paidAllocated < 0 || paidAllocated > quota {
		return errors.New("invalid realtime agency segment")
	}
	if usage == nil {
		usage = &dto.RealtimeUsage{}
	}
	if cumulative == nil {
		cumulative = usage
	}
	if strings.TrimSpace(status) == "" {
		status = "success"
	}
	chargeID := strings.TrimSpace(relayInfo.RequestId)
	if chargeID == "" {
		chargeID = strings.TrimSpace(relayInfo.AgencyBillingEventID)
	}
	if chargeID == "" {
		chargeID = common.NewRequestId()
	}
	usageHash, cumulativePayload, err := agencyRealtimeUsageDigest(usage, cumulative)
	if err != nil {
		return err
	}
	// Hash-derived ids are stable across process restarts and avoid using the
	// client's request id as an event id when it is absent or too long.
	idDigest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", chargeID, segmentNo, usageHash)))
	eventID := "agency-realtime-" + hex.EncodeToString(idDigest[:])
	operationID := "agency-realtime-finalize-" + hex.EncodeToString(idDigest[:])
	snapshot := *relayInfo.AgencyPricing
	standard := relayInfo.AgencyStandardQuota
	if standard == 0 {
		standard = quota
	}
	policy := agencycontract.ResolvedPolicy{SettlementBPS: snapshot.SettlementBPS, SalesBPS: snapshot.SalesBPS, ModelKey: snapshot.ModelKey, OriginModelName: snapshot.OriginModelName}
	basis, err := agencycontract.Calculate(standard, policy, 0, true)
	if err != nil {
		return err
	}
	theoretical := quota - basis.SettlementCostQuota
	if theoretical < 0 {
		return errors.New("negative realtime agency commission")
	}
	commissionEligible := snapshot.CommissionEligible && status == "success"
	commissionQuota := int64(0)
	if commissionEligible {
		commissionQuota, err = agencycontract.CommissionForPaid(theoretical, paidAllocated, quota, true)
		if err != nil {
			return err
		}
	}
	commissionMicros := int64(0)
	if commissionEligible {
		commissionMicros, err = agencyCommissionMicros(commissionQuota, snapshot)
		if err != nil {
			return err
		}
	}
	skipReason := snapshot.EligibilityReason
	if !commissionEligible && skipReason == "" {
		skipReason = "realtime_segment_not_success"
	}
	commissionableQuota := quota
	noncommissionableQuota := int64(0)
	if !commissionEligible {
		commissionableQuota = 0
		noncommissionableQuota = quota
	}
	moneySeq := relayInfo.AgencyMoneySeq
	if moneySeq <= 0 {
		_ = model.DB.Model(&model.AgencyFundingAccount{}).
			Where("user_id = ?", relayInfo.UserId).
			Pluck("money_seq", &moneySeq).Error
	}
	billingBasis := strings.TrimSpace(relayInfo.AgencyBillingBasis)
	if billingBasis == "" {
		billingBasis = mustMarshal(map[string]any{
			"version":            "gateway-basis-v1",
			"billing_source":     relayInfo.BillingSource,
			"relay_mode":         relayInfo.RelayMode,
			"is_stream":          true,
			"realtime_segment":   segmentNo,
			"standard_quota":     standard,
			"charged_quota":      quota,
			"rounding_policy_id": "realtime-v1",
		})
	}
	event := agencycontract.BillingEvent{
		SchemaVersion: agencycontract.SchemaVersion, EventID: eventID,
		EventType: "agency.billing_finalized", FinancialChargeID: chargeID,
		OperationID: operationID, SegmentNo: segmentNo, JournalRevision: 1,
		MoneySeq: moneySeq, EventIndex: 0, EventCount: 1, OccurredAtMS: time.Now().UnixMilli(),
		UserID: int64(relayInfo.UserId), TokenID: int64Ptr(int64(relayInfo.TokenId)),
		AgencyID: &snapshot.AgencyID, BindingID: &snapshot.BindingID,
		OriginModelName: snapshot.OriginModelName, Endpoint: relayInfo.RequestURLPath,
		BusinessStatus: status, BillingStatus: "finalized", CurrencyCode: snapshot.CurrencyCode,
		QuotaPerUnit: snapshot.QuotaPerUnit, ExchangeRate: snapshot.ExchangeRate,
		SettlementBPS: snapshot.SettlementBPS, SalesBPS: snapshot.SalesBPS,
		CommissionEligible: commissionEligible, CommissionSkipReason: skipReason,
		BillingBasis: billingBasis,
		InputTokens:  int64(usage.InputTokens), OutputTokens: int64(usage.OutputTokens),
		CacheReadTokens: int64(usage.InputTokenDetails.CachedTokens),
		StandardQuota:   standard, ChargedTotalQuota: quota,
		CommissionableQuota: commissionableQuota, NoncommissionableQuota: noncommissionableQuota, SettlementCostQuota: basis.SettlementCostQuota,
		TheoreticalCommissionQuota: func() int64 {
			if commissionEligible {
				return theoretical
			}
			return 0
		}(),
		PaidAllocatedQuota: paidAllocated, CommissionQuota: commissionQuota,
		CommissionAmountMicros: commissionMicros, FinancialFinal: true, UsageHash: usageHash,
		CumulativeUsage: string(cumulativePayload),
	}
	payload, err := common.Marshal(event)
	if err != nil {
		return err
	}
	payloadHash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		return err
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.AgencyBillingJournal
		lookupErr := tx.Where("charge_id = ? AND segment_no = ?", chargeID, segmentNo).First(&existing).Error
		if lookupErr == nil {
			if existing.UsageHash == usageHash {
				return nil
			}
			return errors.New("realtime segment usage hash conflict")
		}
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
		journal := &model.AgencyBillingJournal{
			ChargeID: chargeID, SegmentNo: segmentNo, UserID: event.UserID,
			TokenID: event.TokenID, Status: "finalized", BusinessStatus: status,
			DeliveryStatus: "pending", PricingSnapshot: mustMarshal(snapshot),
			BillingBasis: billingBasis, ReserveQuota: quota,
			ChargedTotalQuota: quota, CommissionableQuota: commissionableQuota,
			SettlementCostQuota:        basis.SettlementCostQuota,
			TheoreticalCommissionQuota: event.TheoreticalCommissionQuota,
			PaidAllocatedQuota:         paidAllocated, CommissionQuota: commissionQuota,
			CommissionAmountMicros: commissionMicros, CurrencyCode: snapshot.CurrencyCode,
			UsageHash: usageHash, LastCumulativeUsage: string(cumulativePayload),
			Revision: 1, Version: 1, CreatedAtMS: event.OccurredAtMS, UpdatedAtMS: event.OccurredAtMS,
		}
		if err := tx.Create(journal).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				var raced model.AgencyBillingJournal
				if findErr := tx.Where("charge_id = ? AND segment_no = ?", chargeID, segmentNo).First(&raced).Error; findErr == nil && raced.UsageHash == usageHash {
					return nil
				}
			}
			return err
		}
		// Keep a whitelisted usage projection next to the immutable financial
		// fact. It contains token counters and the hash/cumulative snapshot only;
		// credentials and the raw realtime frame are never persisted.
		usageFact := &model.AgencyUsageFact{
			EventID: event.EventID, ComponentID: "realtime",
			UsageHash: usageHash, CumulativeUsage: string(cumulativePayload),
			UserID: event.UserID, AgencyID: event.AgencyID, BindingID: event.BindingID,
			OriginModelName: event.OriginModelName, ModelKey: snapshot.ModelKey,
			Endpoint: event.Endpoint, BusinessStatus: status,
			InputTokens: int64(max(usage.InputTokens, 0)), OutputTokens: int64(max(usage.OutputTokens, 0)),
			CacheReadTokens: int64(max(usage.InputTokenDetails.CachedTokens, 0)),
			StandardQuota:   event.StandardQuota, SalesBPS: event.SalesBPS,
			ChargedQuota: event.ChargedTotalQuota, CurrencyCode: event.CurrencyCode,
			OccurredAtMS: event.OccurredAtMS,
		}
		if err := tx.Create(usageFact).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.AgencyBillingOperation{ChargeID: chargeID, SegmentNo: segmentNo, Revision: 1, Operation: "finalize", InputHash: usageHash, CommittedResult: string(payload), EventCount: 1, UsageHash: usageHash, CreatedAtMS: event.OccurredAtMS}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.AgencyBillingOutbox{EventID: eventID, OperationID: operationID, EventIndex: 0, EventCount: 1, EventKind: event.EventType, UserID: event.UserID, MoneySeq: event.MoneySeq, Payload: string(payload), PayloadHash: payloadHash, SchemaVersion: event.SchemaVersion, CreatedAtMS: event.OccurredAtMS}).Error; err != nil {
			return err
		}
		return tx.Create(&model.AgencyEventDelivery{EventID: eventID, Status: "pending", NextRetryAt: time.Now().Unix(), CreatedAt: time.Now().Unix()}).Error
	}); err != nil {
		return err
	}
	if relayInfo.AgencyBillingEventID == "" {
		relayInfo.AgencyBillingEventID = eventID
	}
	relayInfo.AgencyRealtimeSegmentsRecorded++
	return nil
}

// AgencyRealtimeSegmentRecorded reports whether an identical cumulative usage
// frame already has a durable journal. It is checked before wallet reservation
// so a repeated response.done frame cannot perform a second reservation.
func AgencyRealtimeSegmentRecorded(relayInfo *relaycommon.RelayInfo, usage, cumulative *dto.RealtimeUsage) (bool, error) {
	if relayInfo == nil || relayInfo.AgencyPricing == nil {
		return false, nil
	}
	chargeID := strings.TrimSpace(relayInfo.RequestId)
	if chargeID == "" {
		chargeID = strings.TrimSpace(relayInfo.AgencyBillingEventID)
	}
	if chargeID == "" {
		return false, nil
	}
	usageHash, _, err := agencyRealtimeUsageDigest(usage, cumulative)
	if err != nil {
		return false, err
	}
	var count int64
	if err := model.DB.Model(&model.AgencyBillingJournal{}).
		Where("charge_id = ? AND usage_hash = ?", chargeID, usageHash).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func agencyRealtimeUsageDigest(usage, cumulative *dto.RealtimeUsage) (string, []byte, error) {
	if usage == nil {
		usage = &dto.RealtimeUsage{}
	}
	if cumulative == nil {
		cumulative = usage
	}
	usagePayload, err := common.Marshal(struct {
		Usage      *dto.RealtimeUsage `json:"usage"`
		Cumulative *dto.RealtimeUsage `json:"cumulative"`
	}{Usage: usage, Cumulative: cumulative})
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(usagePayload)
	cumulativePayload, err := common.Marshal(cumulative)
	if err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(digest[:]), cumulativePayload, nil
}

// AgencyQuoteForUser resolves an agency policy inside the gateway. It is a
// narrow local read: no HTTP call to agency-hub is made on the model path.
// Callers must persist the returned snapshot together with their billing
// journal before sending the upstream request.
func AgencyQuoteForUser(userID, tokenID int, originModelName string, acceptedAtMS int64) (*agencycontract.PricingSnapshot, error) {
	if userID <= 0 || strings.TrimSpace(originModelName) != originModelName || originModelName == "" {
		return nil, errors.New("invalid agency quote input")
	}
	var active model.AgencyActiveUserBinding
	if err := model.DB.Where("user_id = ?", userID).First(&active).Error; err != nil {
		if agencySchemaUnavailable(err) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, err
	}
	var binding model.AgencyUserBinding
	if err := model.DB.Where("id = ? AND ended_at_ms IS NULL", active.BindingID).First(&binding).Error; err != nil {
		return nil, err
	}
	var agency model.Agency
	if err := model.DB.First(&agency, binding.AgencyID).Error; err != nil {
		return nil, err
	}
	var policyRow model.AgencyPricePolicyVersion
	if err := model.DB.First(&policyRow, agency.CurrentPolicyVersionID).Error; err != nil {
		return nil, err
	}
	var policy agencycontract.Policy
	if err := common.Unmarshal([]byte(policyRow.PolicyJSON), &policy); err != nil {
		return nil, err
	}
	platform, err := model.LoadAgencyPlatformPolicy(model.DB)
	if err != nil {
		return nil, err
	}
	// Platform pricing owns the root agency's procurement boundary. Child
	// policies already contain their immutable inherited cost and must not be
	// overwritten with the platform cost here.
	if agency.ParentAgencyID == nil {
		policy, err = agencycontract.ApplyPlatformPolicy(policy, platform)
		if err != nil {
			return nil, err
		}
	}
	resolved, err := agencycontract.Resolve(policy, originModelName)
	if err != nil {
		return nil, err
	}
	// A customer-specific sales override is only valid for the agency that
	// currently owns this binding. It wins over model and agency defaults.
	var customerOverride model.AgencyCustomerSalesOverride
	modelKey := resolved.ModelKey
	overrideErr := model.DB.Where("agency_id = ? AND user_id = ? AND model_key IN ?", agency.ID, userID, []string{modelKey, ""}).
		Order("CASE WHEN model_key = '' THEN 1 ELSE 0 END ASC").First(&customerOverride).Error
	if overrideErr == nil {
		resolved.SalesBPS = customerOverride.SalesBPS
	} else if !errors.Is(overrideErr, gorm.ErrRecordNotFound) {
		return nil, overrideErr
	}
	if acceptedAtMS == 0 {
		acceptedAtMS = time.Now().UnixMilli()
	}
	currencyCode := operation_setting.GetQuotaDisplayType()
	exchangeRate := operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate)
	if currencyCode == "" {
		currencyCode = "TOKENS"
	}
	if currencyCode == operation_setting.QuotaDisplayTypeTokens {
		exchangeRate = 1
	}
	if exchangeRate <= 0 || math.IsNaN(exchangeRate) || math.IsInf(exchangeRate, 0) {
		return nil, errors.New("invalid agency exchange rate")
	}
	eligible := agency.Status == "active"
	reason := ""
	if !eligible {
		reason = "agency_disabled"
	}
	if resolved.SalesBPS < resolved.SettlementBPS {
		return nil, errors.New("customer sales coefficient is below agency cost")
	}
	hierarchy, hierarchyEligible, hierarchyReason, err := agencyPricingHierarchy(agency, originModelName, resolved.SalesBPS, modelKey, platform)
	if err != nil {
		return nil, err
	}
	if !hierarchyEligible {
		eligible = false
		if reason == "" {
			reason = hierarchyReason
		}
	}
	platformCostBPS := 0
	if platformPrice, priceErr := agencycontract.ResolvePlatform(platform, originModelName); priceErr == nil && platformPrice != nil {
		platformCostBPS = platformPrice.PlatformCostBPS
	}
	return &agencycontract.PricingSnapshot{SchemaVersion: agencycontract.SchemaVersion, UserID: int64(userID), TokenID: int64(tokenID), AgencyID: agency.ID, BindingID: binding.ID, BindingRevision: binding.Revision, AgencyStateRevision: agency.StateRevision, PolicyVersionID: policyRow.ID, PolicyRevision: policyRow.Revision, OriginModelName: originModelName, ModelKey: resolved.ModelKey, SettlementBPS: resolved.SettlementBPS, SalesBPS: resolved.SalesBPS, CommissionEligible: eligible, EligibilityReason: reason, CurrencyCode: currencyCode, CurrencyConfigVersion: "operation-setting-v1", QuotaPerUnit: strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64), ExchangeRate: strconv.FormatFloat(exchangeRate, 'f', -1, 64), AcceptedAtMS: acceptedAtMS, FundingRuleVersion: agencycontract.FundingRuleVersion, PricingEngineVersion: "gateway-v1", Hierarchy: hierarchy, MinSpreadBPS: hierarchyMinSpreadBPS(hierarchy, policy.MinSpreadBPS), CustomerSalesOverrideBPS: func() *int {
		if overrideErr == nil {
			value := customerOverride.SalesBPS
			return &value
		}
		return nil
	}(), PlatformCostBPS: platformCostBPS}, nil
}

// agencyPricingHierarchy loads the immutable root-to-leaf chain at quote time.
// Legacy agencies have a nil parent and naturally produce a one-node chain.
// Any broken/disabled ancestor fails closed for new requests while preserving
// the legacy snapshot fields used by existing settlement code.
func agencyPricingHierarchy(leaf model.Agency, originModelName string, leafSales int, modelKey string, platform agencycontract.PlatformPolicy) ([]agencycontract.PricingTierNode, bool, string, error) {
	const maxDepth = 10
	chain := make([]model.Agency, 0, maxDepth)
	seen := make(map[int64]struct{}, maxDepth)
	current := leaf
	for len(chain) < maxDepth {
		if current.ID <= 0 {
			return nil, false, "agency_hierarchy_invalid", errors.New("agency hierarchy contains invalid agency")
		}
		if _, exists := seen[current.ID]; exists {
			return nil, false, "agency_hierarchy_cycle", errors.New("agency hierarchy cycle detected")
		}
		seen[current.ID] = struct{}{}
		chain = append(chain, current)
		if current.ParentAgencyID == nil || *current.ParentAgencyID == 0 {
			break
		}
		var parent model.Agency
		if err := model.DB.First(&parent, *current.ParentAgencyID).Error; err != nil {
			return nil, false, "agency_hierarchy_invalid", err
		}
		current = parent
	}
	if len(chain) == maxDepth && chain[len(chain)-1].ParentAgencyID != nil {
		return nil, false, "agency_depth_exceeded", errors.New("agency hierarchy depth exceeds limit")
	}
	nodes := make([]agencycontract.PricingTierNode, len(chain))
	eligible := true
	reason := ""
	minSpreadBPS := 0
	for i := range chain {
		agency := chain[len(chain)-1-i]
		if agency.Status != "active" {
			eligible = false
			if reason == "" {
				reason = "agency_ancestor_disabled"
			}
		}
		var policyRow model.AgencyPricePolicyVersion
		if err := model.DB.First(&policyRow, agency.CurrentPolicyVersionID).Error; err != nil {
			return nil, false, "agency_policy_unavailable", err
		}
		var policy agencycontract.Policy
		if err := common.Unmarshal([]byte(policyRow.PolicyJSON), &policy); err != nil {
			return nil, false, "agency_policy_invalid", err
		}
		effective := policy
		if agency.ParentAgencyID == nil {
			var applyErr error
			effective, applyErr = agencycontract.ApplyPlatformPolicy(policy, platform)
			if applyErr != nil {
				return nil, false, "agency_policy_invalid", applyErr
			}
		}
		if effective.MinSpreadBPS > minSpreadBPS {
			minSpreadBPS = effective.MinSpreadBPS
		}
		resolved, err := agencycontract.Resolve(effective, originModelName)
		if err != nil {
			return nil, false, "agency_model_unavailable", err
		}
		node := agencycontract.PricingTierNode{AgencyID: agency.ID, ParentAgencyID: agency.ParentAgencyID, Depth: agency.Depth, CostBPS: resolved.SettlementBPS, MinSpreadBPS: effective.MinSpreadBPS, PolicyVersionID: policyRow.ID, PolicyRevision: policyRow.Revision, StateRevision: agency.StateRevision}
		if i == len(chain)-1 {
			node.SalesBPS = &leafSales
		}
		nodes[i] = node
	}
	// Verify the persisted depth agrees with the traversed chain where it is
	// available; this catches accidental cycles or partial migrations early.
	for i := range nodes {
		if nodes[i].Depth > 0 && nodes[i].Depth != i+1 {
			return nil, false, "agency_hierarchy_invalid", errors.New("agency hierarchy depth is inconsistent")
		}
		if i > 0 && nodes[i].CostBPS < nodes[i-1].CostBPS+minSpreadBPS {
			return nil, false, "agency_cost_invalid", errors.New("agency hierarchy cost spread is below minimum")
		}
	}
	if len(nodes) > 0 && nodes[len(nodes)-1].SalesBPS != nil && *nodes[len(nodes)-1].SalesBPS < nodes[len(nodes)-1].CostBPS+minSpreadBPS {
		return nil, false, "agency_sales_invalid", errors.New("leaf sales coefficient is below cost plus minimum spread")
	}
	if len(nodes) > 0 {
		if platformPrice, err := agencycontract.ResolvePlatform(platform, originModelName); err == nil && platformPrice != nil && platformPrice.PlatformCostBPS > nodes[0].CostBPS {
			return nil, false, "platform_cost_invalid", errors.New("platform cost exceeds root agency cost")
		}
	}
	_ = modelKey
	return nodes, eligible, reason, nil
}

// hierarchyMinSpreadBPS preserves the leaf policy value in the immutable
// snapshot. The hierarchy validator already uses the maximum spread found
// along the chain; legacy policies have one shared spread value.
func hierarchyMinSpreadBPS(nodes []agencycontract.PricingTierNode, fallback int) int {
	if fallback < 0 {
		fallback = 0
	}
	for _, node := range nodes {
		if node.MinSpreadBPS > fallback {
			fallback = node.MinSpreadBPS
		}
	}
	return fallback
}

func agencySchemaUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table") || strings.Contains(message, "doesn't exist") || strings.Contains(message, "does not exist") || strings.Contains(message, "undefined table")
}

// AttachAgencyQuote stores the snapshot on RelayInfo; it is intentionally a
// pure assignment so protocol adapters can call it before any upstream I/O.
func AttachAgencyQuote(relayInfo *relaycommon.RelayInfo, snapshot *agencycontract.PricingSnapshot, standardQuota int64) error {
	if relayInfo == nil || snapshot == nil || standardQuota < 0 {
		return errors.New("invalid agency quote snapshot")
	}
	copy := *snapshot
	relayInfo.AgencyPricing = &copy
	relayInfo.AgencyStandardQuota = standardQuota
	relayInfo.AgencyCurrencyCode = snapshot.CurrencyCode
	return nil
}

// RecordAgencyBillingEvent writes the gateway-owned journal, immutable outbox
// payload, and its independent delivery row in one transaction. It is safe to
// call after the HTTP response was sent and on retry because all business keys
// are unique.
func RecordAgencyBillingEvent(relayInfo *relaycommon.RelayInfo, actualQuota int64, status string) error {
	if relayInfo == nil || relayInfo.AgencyPricing == nil {
		return nil
	}
	// Realtime sessions commit one immutable journal/outbox per successful
	// segment. The connection-close hook receives cumulative usage and must
	// not append a second aggregate event after those segment facts exist.
	if relayInfo.AgencyRealtimeSegmentsRecorded > 0 {
		return nil
	}
	if actualQuota < 0 {
		return errors.New("negative agency charge")
	}
	snapshot := *relayInfo.AgencyPricing
	standard := relayInfo.AgencyStandardQuota
	if standard == 0 {
		standard = actualQuota
	}
	charged := actualQuota
	policy := agencycontract.ResolvedPolicy{SettlementBPS: snapshot.SettlementBPS, SalesBPS: snapshot.SalesBPS, ModelKey: snapshot.ModelKey, OriginModelName: snapshot.OriginModelName}
	basis, err := agencycontract.Calculate(standard, policy, 0, true)
	if err != nil {
		return err
	}
	settlement := basis.SettlementCostQuota
	if relayInfo.AgencySettlementCostQuota != nil {
		settlement = *relayInfo.AgencySettlementCostQuota
	}
	if status != "success" {
		settlement = 0
	}
	theoretical := charged - settlement
	if theoretical < 0 {
		return errors.New("negative agency commission")
	}
	paidForEstimate := relayInfo.AgencyPaidAllocatedQuota
	if paidForEstimate > charged {
		paidForEstimate = charged
	}
	commissionQuota, err := agencycontract.CommissionForPaid(theoretical, paidForEstimate, charged, true)
	if err != nil {
		return err
	}
	commissionEligible := snapshot.CommissionEligible && status == "success"
	if !commissionEligible {
		// Failure, violation-fee and administrative charges remain immutable
		// usage/funding facts but never create reseller commission.
		theoretical = 0
		commissionQuota = 0
		settlement = 0
	}
	commissionableQuota := charged
	noncommissionableQuota := int64(0)
	if !commissionEligible {
		commissionableQuota = 0
		noncommissionableQuota = charged
	}
	commission := relayInfo.AgencyCommissionAmountMicros
	if !commissionEligible {
		commission = 0
	}
	chargeID := strings.TrimSpace(relayInfo.RequestId)
	eventID := strings.TrimSpace(relayInfo.AgencyBillingEventID)
	if eventID == "" && chargeID != "" {
		digest := sha256.Sum256([]byte("agency-finalized:" + chargeID))
		eventID = "agency-finalized-" + hex.EncodeToString(digest[:])
		relayInfo.AgencyBillingEventID = eventID
	} else if eventID == "" {
		eventID = common.NewRequestId()
		chargeID = eventID
		relayInfo.AgencyBillingEventID = eventID
	} else if chargeID == "" {
		chargeID = eventID
	}
	operationID := "agency-finalize-" + eventID
	skipReason := snapshot.EligibilityReason
	if !commissionEligible && skipReason == "" {
		skipReason = "business_status_not_success"
	}
	moneySeq := relayInfo.AgencyMoneySeq
	if moneySeq <= 0 {
		_ = model.DB.Model(&model.AgencyFundingAccount{}).
			Where("user_id = ?", relayInfo.UserId).
			Pluck("money_seq", &moneySeq).Error
	}
	billingBasis := strings.TrimSpace(relayInfo.AgencyBillingBasis)
	if billingBasis == "" {
		billingBasis = mustMarshal(map[string]any{
			"version":              "gateway-basis-v1",
			"billing_source":       relayInfo.BillingSource,
			"relay_mode":           relayInfo.RelayMode,
			"is_stream":            relayInfo.IsStream,
			"is_channel_test":      relayInfo.IsChannelTest,
			"free_model":           relayInfo.PriceData.FreeModel,
			"model_price":          relayInfo.PriceData.ModelPrice,
			"model_ratio":          relayInfo.PriceData.ModelRatio,
			"completion_ratio":     relayInfo.PriceData.CompletionRatio,
			"cache_ratio":          relayInfo.PriceData.CacheRatio,
			"cache_creation_ratio": relayInfo.PriceData.CacheCreationRatio,
			"standard_quota":       standard,
			"charged_quota":        charged,
			"rounding_policy_id":   "gateway-v1",
		})
	}
	event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: eventID, EventType: "agency.billing_finalized", FinancialChargeID: chargeID, OperationID: operationID, SegmentNo: 0, JournalRevision: 1, MoneySeq: moneySeq, EventIndex: 0, EventCount: 1, OccurredAtMS: time.Now().UnixMilli(), UserID: int64(relayInfo.UserId), TokenID: int64Ptr(int64(relayInfo.TokenId)), AgencyID: &snapshot.AgencyID, BindingID: &snapshot.BindingID, OriginModelName: snapshot.OriginModelName, Endpoint: relayInfo.RequestURLPath, BusinessStatus: status, BillingStatus: "finalized", CurrencyCode: snapshot.CurrencyCode, QuotaPerUnit: snapshot.QuotaPerUnit, ExchangeRate: snapshot.ExchangeRate, SettlementBPS: snapshot.SettlementBPS, SalesBPS: snapshot.SalesBPS, CommissionEligible: commissionEligible, CommissionSkipReason: skipReason, BillingBasis: billingBasis, InputTokens: relayInfo.AgencyInputTokens, OutputTokens: relayInfo.AgencyOutputTokens, CacheReadTokens: relayInfo.AgencyCacheReadTokens, CacheWriteTokens: relayInfo.AgencyCacheWriteTokens, StandardQuota: standard, ChargedTotalQuota: charged, CommissionableQuota: commissionableQuota, NoncommissionableQuota: noncommissionableQuota, SettlementCostQuota: settlement, TheoreticalCommissionQuota: theoretical, PaidAllocatedQuota: relayInfo.AgencyPaidAllocatedQuota, CommissionQuota: commissionQuota, FinancialFinal: true}
	if commission == 0 {
		commission, err = agencyCommissionMicros(commissionQuota, snapshot)
		if err != nil {
			return err
		}
	}
	event.CommissionAmountMicros = commission
	// A one-node hierarchy is the legacy single-agency shape. Keep its
	// historical commission formula; tier splits are only emitted when an
	// actual parent/child chain exists.
	if commissionEligible && len(snapshot.Hierarchy) > 1 {
		splits, splitErr := agencyTierCommissionSplits(snapshot, standard, charged, event.PaidAllocatedQuota)
		if splitErr != nil {
			return splitErr
		}
		event.CommissionSplits = splits
		var tierTheoretical, tierCommission, tierMicros int64
		for _, split := range splits {
			tierTheoretical += split.TheoreticalQuota
			tierCommission += split.CommissionQuota
			tierMicros += split.CommissionAmountMicros
		}
		// For a hierarchy the aggregate settlement boundary is the platform
		// cost, while the commission is the sum of all tier differences.
		event.SettlementCostQuota = charged - tierTheoretical
		event.TheoreticalCommissionQuota = tierTheoretical
		event.CommissionQuota = tierCommission
		event.CommissionAmountMicros = tierMicros
	}
	if snapshot.FinancialChargeID != "" {
		committed, commitErr := model.AgencyCommitWalletCharge(event, relayInfo.TokenKey)
		if commitErr != nil {
			return commitErr
		}
		relayInfo.AgencyPaidAllocatedQuota = committed.PaidAllocatedQuota
		relayInfo.AgencyCommissionAmountMicros = committed.CommissionAmountMicros
		relayInfo.AgencyMoneySeq = committed.MoneySeq
		relayInfo.AgencyBillingEventID = committed.EventID
		return nil
	}
	payload, err := common.Marshal(event)
	if err != nil {
		return err
	}
	payloadHash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		return err
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var existingJournal model.AgencyBillingJournal
		lookupErr := tx.Where("charge_id = ? AND segment_no = ?", event.FinancialChargeID, 0).First(&existingJournal).Error
		if lookupErr == nil {
			var existing model.AgencyBillingOperation
			if operationErr := tx.Where(
				"charge_id = ? AND segment_no = ? AND revision = ? AND operation = ?",
				event.FinancialChargeID, 0, int64(1), "finalize",
			).First(&existing).Error; operationErr != nil {
				return operationErr
			}
			if existing.InputHash == payloadHash {
				return nil
			}
			return errors.New("agency billing payload hash conflict")
		}
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
		journal := &model.AgencyBillingJournal{ChargeID: event.FinancialChargeID, SegmentNo: 0, UserID: event.UserID, TokenID: event.TokenID, Status: "finalized", BusinessStatus: status, DeliveryStatus: "pending", PricingSnapshot: mustMarshal(snapshot), BillingBasis: billingBasis, ReserveQuota: int64(relayInfo.FinalPreConsumedQuota), ChargedTotalQuota: charged, CommissionableQuota: commissionableQuota, SettlementCostQuota: settlement, TheoreticalCommissionQuota: theoretical, PaidAllocatedQuota: relayInfo.AgencyPaidAllocatedQuota, CommissionQuota: commissionQuota, CommissionAmountMicros: commission, CurrencyCode: snapshot.CurrencyCode, Revision: 1, Version: 1, CreatedAtMS: event.OccurredAtMS, UpdatedAtMS: event.OccurredAtMS}
		if err := tx.Create(journal).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(strings.ToLower(err.Error()), "unique constraint") || strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
				// A repeated finalize for the same financial charge is
				// idempotent only when its immutable payload is identical.
				// Silently accepting a different actual quota would hide a
				// settlement conflict and leave the journal/outbox carrying
				// whichever request won the race.
				var existing model.AgencyBillingOperation
				if lookupErr := tx.Where(
					"charge_id = ? AND segment_no = ? AND revision = ? AND operation = ?",
					event.FinancialChargeID, 0, int64(1), "finalize",
				).First(&existing).Error; lookupErr != nil {
					return lookupErr
				}
				if existing.InputHash == payloadHash {
					return nil
				}
				return errors.New("agency billing payload hash conflict")
			}
			return err
		}
		op := &model.AgencyBillingOperation{ChargeID: event.FinancialChargeID, SegmentNo: 0, Revision: 1, Operation: "finalize", InputHash: payloadHash, CommittedResult: string(payload), EventCount: 1, CreatedAtMS: event.OccurredAtMS}
		if err := tx.Create(op).Error; err != nil {
			return err
		}
		outbox := &model.AgencyBillingOutbox{EventID: event.EventID, OperationID: operationID, EventIndex: 0, EventCount: 1, EventKind: event.EventType, UserID: event.UserID, MoneySeq: event.MoneySeq, Payload: string(payload), PayloadHash: payloadHash, SchemaVersion: event.SchemaVersion, CreatedAtMS: event.OccurredAtMS}
		if err := tx.Create(outbox).Error; err != nil {
			return err
		}
		return tx.Create(&model.AgencyEventDelivery{EventID: event.EventID, Status: "pending", NextRetryAt: time.Now().Unix(), CreatedAt: time.Now().Unix()}).Error
	})
}

func agencyTierCommissionSplits(snapshot agencycontract.PricingSnapshot, standard, charged, paidAllocated int64) ([]agencycontract.CommissionSplit, error) {
	if len(snapshot.Hierarchy) == 0 || standard < 0 || charged < 0 || paidAllocated < 0 || paidAllocated > charged {
		return nil, errors.New("invalid tier commission snapshot")
	}
	nodes := make([]agencycontract.TierNode, len(snapshot.Hierarchy))
	for i, node := range snapshot.Hierarchy {
		nodes[i] = agencycontract.TierNode{AgencyID: node.AgencyID, CostBPS: node.CostBPS, MinSpreadBPS: node.MinSpreadBPS, SalesBPS: node.SalesBPS}
	}
	result, err := agencycontract.CalculateTieredCommission(standard, nodes, snapshot.PlatformCostBPS, snapshot.MinSpreadBPS, paidAllocated, true)
	if err != nil {
		return nil, err
	}
	if result.CustomerChargedQuota != charged {
		return nil, errors.New("tier commission customer charge mismatch")
	}
	splits := make([]agencycontract.CommissionSplit, 0, len(snapshot.Hierarchy))
	for i, segment := range result.Segments {
		node := snapshot.Hierarchy[i]
		amount, err := agencyCommissionMicros(segment.CommissionQuota, snapshot)
		if err != nil {
			return nil, err
		}
		splits = append(splits, agencycontract.CommissionSplit{AgencyID: segment.AgencyID, ParentAgencyID: node.ParentAgencyID, Depth: node.Depth, CostBPS: node.CostBPS, SalesBPS: node.SalesBPS, TheoreticalQuota: segment.TheoreticalQuota, PaidAllocatedQuota: segment.PaidAllocatedQuota, CommissionQuota: segment.CommissionQuota, CommissionAmountMicros: amount})
	}
	return splits, nil
}

func agencyCommissionMicros(quota int64, snapshot agencycontract.PricingSnapshot) (int64, error) {
	if quota < 0 {
		return 0, errors.New("negative agency commission")
	}
	if snapshot.CurrencyCode == operation_setting.QuotaDisplayTypeTokens {
		return quota, nil
	}
	quotaPerUnit, err := decimal.NewFromString(snapshot.QuotaPerUnit)
	if err != nil || !quotaPerUnit.IsPositive() {
		return 0, errors.New("invalid agency quota unit")
	}
	exchangeRate, err := decimal.NewFromString(snapshot.ExchangeRate)
	if err != nil || !exchangeRate.IsPositive() {
		return 0, errors.New("invalid agency exchange rate")
	}
	amount := decimal.NewFromInt(quota).Div(quotaPerUnit).Mul(exchangeRate).Mul(decimal.NewFromInt(1_000_000)).Round(0)
	if amount.GreaterThan(decimal.NewFromInt(math.MaxInt64)) || amount.LessThan(decimal.NewFromInt(math.MinInt64)) {
		return 0, errors.New("agency commission amount overflow")
	}
	return amount.IntPart(), nil
}

// RecordAgencyRefundEvent appends a compensating financial event. It never
// deletes or mutates the original outbox payload, which makes repeated task
// callbacks and partial refunds auditable and idempotent.
func RecordAgencyRefundEvent(relayInfo *relaycommon.RelayInfo, reversedQuota, reversedCommissionMicros int64, reason string) error {
	if relayInfo == nil || relayInfo.AgencyPricing == nil || reversedQuota < 0 || reversedCommissionMicros < 0 {
		return nil
	}
	if relayInfo.AgencyBillingEventID == "" {
		return nil
	}
	snapshot := *relayInfo.AgencyPricing
	eventID := common.NewRequestId()
	original := relayInfo.RequestId
	if original == "" {
		original = eventID
	}
	event := agencycontract.BillingEvent{SchemaVersion: agencycontract.SchemaVersion, EventID: eventID, EventType: "agency.billing_reversed", OriginalEventID: relayInfo.AgencyBillingEventID, FinancialChargeID: original, OperationID: "agency-reversal-" + eventID, SegmentNo: 0, JournalRevision: 2, EventIndex: 0, EventCount: 1, OccurredAtMS: time.Now().UnixMilli(), UserID: int64(relayInfo.UserId), TokenID: int64Ptr(int64(relayInfo.TokenId)), AgencyID: &snapshot.AgencyID, BindingID: &snapshot.BindingID, OriginModelName: snapshot.OriginModelName, BusinessStatus: reason, BillingStatus: "reversed", CurrencyCode: snapshot.CurrencyCode, QuotaPerUnit: snapshot.QuotaPerUnit, ExchangeRate: snapshot.ExchangeRate, CommissionEligible: snapshot.CommissionEligible, ReversedCommissionAmountMicros: reversedCommissionMicros}
	payload, err := common.Marshal(event)
	if err != nil {
		return err
	}
	hash, err := agencycontract.CanonicalHash(event)
	if err != nil {
		return err
	}
	now := event.OccurredAtMS
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.AgencyBillingOutbox{EventID: eventID, OperationID: event.OperationID, EventIndex: 0, EventCount: 1, EventKind: event.EventType, UserID: event.UserID, Payload: string(payload), PayloadHash: hash, SchemaVersion: event.SchemaVersion, CreatedAtMS: now}).Error; err != nil {
			return err
		}
		return tx.Create(&model.AgencyEventDelivery{EventID: eventID, Status: "pending", NextRetryAt: time.Now().Unix(), CreatedAt: time.Now().Unix()}).Error
	})
}

// RecordAgencyTaskRefundEvent emits a proportional commission reversal for an
// asynchronous task whose original finalized event is persisted in the
// gateway outbox. Task rows intentionally keep only the immutable event ID;
// the original commission and charged quota are read from that event so a
// later pricing change cannot affect the reversal.
func RecordAgencyTaskRefundEvent(task *model.Task, reversedQuota int64, reason string) error {
	if task == nil || reversedQuota <= 0 || task.PrivateData.BillingContext == nil {
		return nil
	}
	bc := task.PrivateData.BillingContext
	if strings.TrimSpace(bc.AgencyBillingEventID) == "" && strings.TrimSpace(bc.AgencyChargeID) == "" {
		return nil
	}
	return recordAgencyProportionalRefundEvent(bc.AgencyBillingEventID, taskAgencyChargeID(task), reversedQuota, reason)
}

// RecordAgencyMidjourneyRefundEvent emits a proportional compensating event
// for a legacy Midjourney task. The task stores both the immutable event id
// and the financial charge id; the latter is retained as a compatibility
// fallback for rows created before event ids were persisted.
func RecordAgencyMidjourneyRefundEvent(task *model.Midjourney, reversedQuota int64, reason string) error {
	if task == nil || reversedQuota <= 0 {
		return nil
	}
	if strings.TrimSpace(task.AgencyBillingEventID) == "" && strings.TrimSpace(task.AgencyChargeID) == "" {
		return nil
	}
	err := recordAgencyProportionalRefundEvent(task.AgencyBillingEventID, task.AgencyChargeID, reversedQuota, reason)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

func recordAgencyProportionalRefundEvent(originalEventID, chargeID string, reversedQuota int64, reason string) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		return recordAgencyProportionalRefundEventTx(tx, originalEventID, chargeID, reversedQuota, reason)
	})
}

// RecordAgencyRefundByReferenceTx is the transaction-scoped Root chargeback
// hook.  Funding reversal and commission reversal must commit together; the
// caller owns tx and can roll back both sides on any conflict.
func RecordAgencyRefundByReferenceTx(tx *gorm.DB, originalEventID, chargeID string, reversedQuota int64, reason string) error {
	return recordAgencyProportionalRefundEventTx(tx, originalEventID, chargeID, reversedQuota, reason)
}

// RefundAgencyModelCharge is the gateway's cumulative model-refund command.
// The authorized caller supplies a stable refund ID and a cumulative target;
// this function restores wallet/token/source funding and emits the component
// commission reversal in one transaction. It must run before any separate
// quota release. Payment chargebacks continue through their own Root command.
func RefundAgencyModelCharge(input model.AgencyComponentRefundInput, tokenKey string) (agencycontract.BillingEvent, error) {
	return model.AgencyRefundWalletCharge(input, tokenKey)
}

func recordAgencyProportionalRefundEventTx(tx *gorm.DB, originalEventID, chargeID string, reversedQuota int64, reason string) error {
	if reversedQuota <= 0 {
		return nil
	}
	if tx == nil {
		return errors.New("agency refund transaction is required")
	}
	original, err := loadAgencyOriginalBillingEventTx(tx, originalEventID, chargeID)
	if err != nil {
		return err
	}
	if original.SchemaVersion == agencycontract.ComponentSchemaVersion && len(original.CommissionSplits) == 0 {
		// A v2 charge can mix model fees and noncommissionable components.
		// The old aggregate callback cannot identify which original allocation
		// was reversed and must never restore funds or guess its commission.
		return model.ErrAgencyComponentRefundProvenance
	}
	if original.EventID == "" {
		original.EventID = strings.TrimSpace(originalEventID)
	}
	if original.FinancialChargeID == "" {
		original.FinancialChargeID = strings.TrimSpace(chargeID)
	}
	if original.EventID == "" || original.FinancialChargeID == "" {
		return gorm.ErrRecordNotFound
	}
	if original.CommissionAmountMicros <= 0 || original.ChargedTotalQuota <= 0 || !original.CommissionEligible {
		return nil
	}
	{
		var journal model.AgencyBillingJournal
		if err := model.AgencyLockForUpdate(tx).
			Where("charge_id = ? AND segment_no = ?", original.FinancialChargeID, original.SegmentNo).
			First(&journal).Error; err != nil {
			return err
		}
		remainingQuota := original.ChargedTotalQuota - journal.ReversedQuota
		if remainingQuota <= 0 {
			return nil
		}
		if reversedQuota > remainingQuota {
			reversedQuota = remainingQuota
		}
		remainingCommission := original.CommissionAmountMicros - journal.ReversedCommissionQuota
		// Refund amounts are accumulated on the journal before calculating the
		// commission delta. Rounding each callback independently can front-load
		// a micro-commission (for example, eight one-quota refunds against a
		// five-micro commission), making the partial-refund history differ from
		// one cumulative refund. The journal is the durable cumulative
		// watermark; the emitted reversal contains only the newly due delta.
		cumulativeQuota := journal.ReversedQuota + reversedQuota
		commissionTarget := decimal.NewFromInt(original.CommissionAmountMicros).
			Mul(decimal.NewFromInt(cumulativeQuota)).
			Div(decimal.NewFromInt(original.ChargedTotalQuota)).
			Round(0)
		if commissionTarget.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
			return errors.New("agency refund commission overflow")
		}
		reversedCommission := commissionTarget.IntPart() - journal.ReversedCommissionQuota
		if reversedCommission < 0 {
			return errors.New("agency refund commission rounding regressed")
		}
		if remainingCommission < 0 {
			return errors.New("agency refund commission state is invalid")
		}
		// A small refund can legitimately round to zero commission. Keep the
		// zero-amount reversal event below so the gateway journal, operation
		// receipt, and sidecar delivery still record that quota refund; the
		// sidecar will mark the event skipped without changing its balance.
		if reversedCommission > remainingCommission {
			reversedCommission = remainingCommission
		}
		eventID := common.NewRequestId()
		now := time.Now().UnixMilli()
		event := original
		event.EventID = eventID
		event.EventType = "agency.billing_reversed"
		event.OriginalEventID = original.EventID
		event.OperationID = "agency-reversal-" + eventID
		event.JournalRevision = journal.Revision + 1
		event.EventIndex = 0
		event.EventCount = 1
		event.OccurredAtMS = now
		event.BusinessStatus = reason
		event.BillingStatus = "reversed"
		event.CommissionAmountMicros = 0
		event.ReversedCommissionAmountMicros = reversedCommission
		if len(original.CommissionSplits) > 0 {
			splits, splitTotal, splitErr := buildTieredReversalSplits(tx, original, cumulativeQuota)
			if splitErr != nil {
				return splitErr
			}
			event.CommissionSplits = splits
			event.ReversedCommissionAmountMicros = splitTotal
		}
		if reversedCommission == 0 {
			event.CommissionSkipReason = "zero_commission_refund"
		}
		payload, err := common.Marshal(event)
		if err != nil {
			return err
		}
		hash, err := agencycontract.CanonicalHash(event)
		if err != nil {
			return err
		}
		revision := journal.Revision + 1
		if err := tx.Model(&journal).Updates(map[string]any{
			"reversed_quota":            cumulativeQuota,
			"reversed_commission_quota": journal.ReversedCommissionQuota + reversedCommission,
			"status": func() string {
				if cumulativeQuota >= original.ChargedTotalQuota {
					return "reversed"
				}
				return "partially_reversed"
			}(),
			"revision":      revision,
			"updated_at_ms": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.AgencyBillingOperation{
			ChargeID: original.FinancialChargeID, SegmentNo: original.SegmentNo,
			Revision: revision, Operation: "reverse", InputHash: hash,
			CommittedResult: string(payload), EventCount: 1, CreatedAtMS: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.AgencyBillingOutbox{
			EventID: eventID, OperationID: event.OperationID,
			EventIndex: 0, EventCount: 1, EventKind: event.EventType,
			UserID: event.UserID, MoneySeq: event.MoneySeq, Payload: string(payload),
			PayloadHash: hash, SchemaVersion: event.SchemaVersion, CreatedAtMS: now,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&model.AgencyEventDelivery{
			EventID: eventID, Status: "pending", NextRetryAt: time.Now().Unix(),
			CreatedAt: time.Now().Unix(),
		}).Error
	}
}

func buildTieredReversalSplits(tx *gorm.DB, original agencycontract.BillingEvent, cumulativeQuota int64) ([]agencycontract.CommissionSplit, int64, error) {
	if tx == nil || original.ChargedTotalQuota <= 0 || cumulativeQuota < 0 || cumulativeQuota > original.ChargedTotalQuota {
		return nil, 0, errors.New("invalid tiered reversal inputs")
	}
	result := make([]agencycontract.CommissionSplit, 0, len(original.CommissionSplits))
	var total int64
	for _, split := range original.CommissionSplits {
		var originalEntry model.AgencyCommissionLedger
		if err := tx.Where("event_id = ? AND agency_id = ? AND entry_type = ?", original.EventID, split.AgencyID, "earned").First(&originalEntry).Error; err != nil {
			return nil, 0, err
		}
		var already int64
		if err := tx.Model(&model.AgencyCommissionLedger{}).
			Where("original_entry_id = ? AND entry_type = ?", originalEntry.ID, "reversal").
			Select("COALESCE(SUM(-amount_micros),0)").Scan(&already).Error; err != nil {
			return nil, 0, err
		}
		target := decimal.NewFromInt(split.CommissionAmountMicros).
			Mul(decimal.NewFromInt(cumulativeQuota)).
			Div(decimal.NewFromInt(original.ChargedTotalQuota)).Round(0).IntPart()
		remaining := split.CommissionAmountMicros - already
		if target < already {
			return nil, 0, errors.New("tiered reversal rounding regressed")
		}
		amount := target - already
		if amount > remaining {
			amount = remaining
		}
		if amount < 0 {
			return nil, 0, errors.New("tiered reversal exceeds original split")
		}
		result = append(result, agencycontract.CommissionSplit{
			AgencyID: split.AgencyID, ParentAgencyID: split.ParentAgencyID, Depth: split.Depth,
			CostBPS: split.CostBPS, SalesBPS: split.SalesBPS,
			TheoreticalQuota: split.TheoreticalQuota, PaidAllocatedQuota: split.PaidAllocatedQuota,
			CommissionQuota: split.CommissionQuota, ReversedCommissionAmountMicros: amount,
		})
		if total > math.MaxInt64-amount {
			return nil, 0, errors.New("tiered reversal commission overflow")
		}
		total += amount
	}
	return result, total, nil
}

// RecordAgencyRefundByReference is used by the signed Root command worker.
// The command path supplies only immutable event/charge identifiers; pricing
// and the remaining refundable amount are loaded from the gateway journal.
func RecordAgencyRefundByReference(originalEventID, chargeID string, reversedQuota int64, reason string) error {
	return recordAgencyProportionalRefundEvent(originalEventID, chargeID, reversedQuota, reason)
}

func loadAgencyOriginalBillingEvent(eventID, chargeID string) (agencycontract.BillingEvent, error) {
	return loadAgencyOriginalBillingEventTx(model.DB, eventID, chargeID)
}

func loadAgencyOriginalBillingEventTx(db *gorm.DB, eventID, chargeID string) (agencycontract.BillingEvent, error) {
	var payload string
	eventID = strings.TrimSpace(eventID)
	chargeID = strings.TrimSpace(chargeID)
	if eventID != "" {
		var outbox model.AgencyBillingOutbox
		if err := db.Where("event_id = ?", eventID).First(&outbox).Error; err != nil {
			return agencycontract.BillingEvent{}, err
		}
		payload = outbox.Payload
	} else if chargeID != "" {
		var operation model.AgencyBillingOperation
		if err := db.Where("charge_id = ? AND segment_no = ? AND operation = ?", chargeID, 0, "finalize").
			Order("revision desc").First(&operation).Error; err != nil {
			return agencycontract.BillingEvent{}, err
		}
		payload = operation.CommittedResult
	} else {
		return agencycontract.BillingEvent{}, gorm.ErrRecordNotFound
	}
	var event agencycontract.BillingEvent
	if err := common.Unmarshal([]byte(payload), &event); err != nil {
		return agencycontract.BillingEvent{}, err
	}
	return event, nil
}

func int64Ptr(value int64) *int64  { return &value }
func mustMarshal(value any) string { encoded, _ := common.Marshal(value); return string(encoded) }
