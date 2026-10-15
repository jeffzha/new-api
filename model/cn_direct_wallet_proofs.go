package model

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func cnDirectWalletProof(tx *gorm.DB, stream *CNFinanceStream, receipts map[string]bool) (cnProofScan, error) {
	scan := cnProofScan{dependency: CNDependency{SourceID: "ordinary_wallet_direct_realtime", Version: "direct-realtime-response-id-v3", Required: true, CoverageFrom: stream.StartedAt, SupportedPaths: []string{"ordinary-relay/forward-free-quote-final-v1", "ordinary-realtime/response-id-absolute-usage-v1"}, ExcludedPaths: []string{"pre-epoch", "redis", "batch", "direct-relay-without-forward-receipt", "delta-only-realtime", "local-estimated-usage", "subscription", "playground", "fixed-price-realtime", "tiered-expression-realtime", "channel-switch-retry", "unbound-direct-token-flags", "legacy-cumulative-realtime", "conflicting-response-done-usage"}}}
	if !CNDirectWalletAvailable() {
		scan.coverageGap = true
	}
	var rows []CNDirectWallet
	if err := tx.Where("created_at >= ?", stream.StartedAt).Order("request_id").Find(&rows).Error; err != nil {
		return scan, err
	}
	for _, row := range rows {
		scan.records = append(scan.records, []any{row.RequestID, row.UserID, row.TokenID, row.TokenUnlimited, row.TokenBindingVersion, row.TokenAdjustmentStatus, row.TokenDisposedQuota, row.UsageContract, row.ChannelID, row.State, row.Version, row.Reserved, row.SegmentQuota, row.Actual, row.FinalHash, row.ConversionJSON, row.CoverageGap, row.LastErrorCode})
		if row.State != "settled" && row.State != "cancelled" {
			scan.dependency.PendingCount++
		} else if !receipts["cn_direct_wallet_final:"+row.RequestID] {
			scan.dependency.PendingCount++
		}
		if row.ConversionJSON == "" || row.CoverageGap != "" || row.LastErrorCode != "" || row.TokenBindingVersion != 1 || row.TokenDisposedQuota > 0 || (row.Kind == "realtime" && row.UsageContract != "openai-response-done-v1") {
			scan.dependency.UnknownCount++
		}
		var segments []CNDirectWalletSegment
		if err := tx.Where("request_id = ?", row.RequestID).Order("version").Find(&segments).Error; err != nil {
			return scan, err
		}
		var quota int64
		for _, segment := range segments {
			if segment.ResponseID == "" {
				scan.dependency.UnknownCount++
			}
			quota += segment.Quota
			raw, err := common.Marshal(segment)
			if err != nil {
				return scan, err
			}
			scan.records = append(scan.records, []any{segment.Key, CNIdentity(string(raw))})
		}
		if quota != row.SegmentQuota {
			scan.dependency.UnknownCount++
			scan.records = append(scan.records, []any{"segment_sum_mismatch", strconv.FormatInt(quota, 10)})
		}
	}
	return scan, nil
}
