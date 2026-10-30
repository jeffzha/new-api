package opsexport

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const walletHistoryPhaseCount = 6

type walletHistoryCursor struct {
	Gateway   string   `json:"gateway"`
	Until     int64    `json:"until"`
	StartedAt int64    `json:"startedAt"`
	MaxIDs    [6]int64 `json:"maxIds"`
	Counts    [6]int64 `json:"counts"`
	Phase     int      `json:"phase"`
	After     int64    `json:"after"`
}

type walletAccountExport struct {
	ExternalUserID      string `json:"externalUserId"`
	PaidAvailable       int64  `json:"paidAvailable"`
	NonpaidAvailable    int64  `json:"nonpaidAvailable"`
	DebtQuota           int64  `json:"debtQuota"`
	MoneySeq            int64  `json:"moneySeq"`
	Version             int64  `json:"version"`
	OpeningSnapshotHash string `json:"openingSnapshotHash,omitempty"`
	ReconcileBlocked    bool   `json:"reconcileBlocked"`
	ReconcileReasonHash string `json:"reconcileReasonHash,omitempty"`
	UpdatedAt           int64  `json:"updatedAt"`
}

type walletLotExport struct {
	ExternalLotID       string `json:"externalLotId"`
	ExternalUserID      string `json:"externalUserId"`
	SourceKind          string `json:"sourceKind"`
	SourceID            string `json:"sourceId"`
	ActorExternalUserID string `json:"actorExternalUserId,omitempty"`
	SourceSnapshotHash  string `json:"sourceSnapshotHash,omitempty"`
	CompletionSource    string `json:"completionSource"`
	PaidInitial         int64  `json:"paidInitial"`
	BonusInitial        int64  `json:"bonusInitial"`
	PaidAvailable       int64  `json:"paidAvailable"`
	PaidReserved        int64  `json:"paidReserved"`
	PaidConsumed        int64  `json:"paidConsumed"`
	PaidRevoked         int64  `json:"paidRevoked"`
	BonusAvailable      int64  `json:"bonusAvailable"`
	BonusReserved       int64  `json:"bonusReserved"`
	BonusConsumed       int64  `json:"bonusConsumed"`
	BonusRevoked        int64  `json:"bonusRevoked"`
	BonusExpired        int64  `json:"bonusExpired"`
	PaidDebtRepaid      int64  `json:"paidDebtRepaid"`
	BonusDebtRepaid     int64  `json:"bonusDebtRepaid"`
	ExpiresAt           int64  `json:"expiresAt"`
	MoneySeq            int64  `json:"moneySeq"`
	Version             int64  `json:"version"`
	CreatedAt           int64  `json:"createdAt"`
}

type walletAllocationExport struct {
	ExternalAllocationID string `json:"externalAllocationId"`
	ChargeID             string `json:"chargeId"`
	SegmentNo            int    `json:"segmentNo"`
	ComponentID          string `json:"componentId"`
	ExternalUserID       string `json:"externalUserId"`
	ExternalLotID        string `json:"externalLotId,omitempty"`
	SourceSnapshotHash   string `json:"sourceSnapshotHash,omitempty"`
	Reserved             int64  `json:"reserved"`
	Consumed             int64  `json:"consumed"`
	NonpaidConsumed      int64  `json:"nonpaidConsumed"`
	DebtConsumed         int64  `json:"debtConsumed"`
	Released             int64  `json:"released"`
	Reversed             int64  `json:"reversed"`
	RevokedReservedDebt  int64  `json:"revokedReservedDebt"`
	RevokedNonpaid       int64  `json:"revokedNonpaid"`
	Version              int64  `json:"version"`
}

type walletLedgerExport struct {
	ExternalEntryID   string `json:"externalEntryId"`
	OperationID       string `json:"operationId"`
	EntryNo           int    `json:"entryNo"`
	ExternalUserID    string `json:"externalUserId"`
	MoneySeq          int64  `json:"moneySeq"`
	SourceKind        string `json:"sourceKind"`
	ExternalLotID     string `json:"externalLotId,omitempty"`
	PaidDelta         int64  `json:"paidDelta"`
	NonpaidDelta      int64  `json:"nonpaidDelta"`
	DebtDelta         int64  `json:"debtDelta"`
	PaidAfter         int64  `json:"paidAfter"`
	NonpaidAfter      int64  `json:"nonpaidAfter"`
	DebtAfter         int64  `json:"debtAfter"`
	AgencyExternalID  string `json:"agencyExternalId,omitempty"`
	BindingExternalID string `json:"bindingExternalId,omitempty"`
	CurrencyCode      string `json:"currencyCode,omitempty"`
	CreatedAtMS       int64  `json:"createdAtMs"`
}

type walletTopupExport struct {
	ExternalFactID          string `json:"externalFactId"`
	SourceOperationID       string `json:"sourceOperationId"`
	SourceID                string `json:"sourceId"`
	ExternalUserID          string `json:"externalUserId"`
	InitiatedByExternalID   string `json:"initiatedByExternalUserId,omitempty"`
	AgencyExternalID        string `json:"agencyExternalId,omitempty"`
	BindingExternalID       string `json:"bindingExternalId,omitempty"`
	PaymentReferenceHash    string `json:"paymentReferenceHash,omitempty"`
	ActualMoney             string `json:"actualMoney,omitempty"`
	CurrencyCode            string `json:"currencyCode,omitempty"`
	QuotaConversionSnapshot string `json:"quotaConversionSnapshot,omitempty"`
	CreditedQuota           int64  `json:"creditedQuota"`
	PaidQuota               int64  `json:"paidQuota"`
	BonusQuota              int64  `json:"bonusQuota"`
	FundingSource           string `json:"fundingSource"`
	CompletionSource        string `json:"completionSource"`
	PaymentStatus           string `json:"paymentStatus"`
	RefundedQuota           int64  `json:"refundedQuota"`
	ExpiredQuota            int64  `json:"expiredQuota"`
	ExpiresAt               int64  `json:"expiresAt"`
	OccurredAtMS            int64  `json:"occurredAtMs"`
}

type walletReversalExport struct {
	ExternalReversalID    string `json:"externalReversalId"`
	RefundID              string `json:"refundId"`
	SourceOperationID     string `json:"sourceOperationId"`
	ExternalUserID        string `json:"externalUserId"`
	Quota                 int64  `json:"quota"`
	CurrencyCode          string `json:"currencyCode,omitempty"`
	PaymentReferenceHash  string `json:"paymentReferenceHash,omitempty"`
	EvidenceReferenceHash string `json:"evidenceReferenceHash,omitempty"`
	ReasonHash            string `json:"reasonHash,omitempty"`
	CreatedAtMS           int64  `json:"createdAtMs"`
}

func walletHistoryAvailable(db *gorm.DB) bool {
	for _, table := range []any{&model.AgencyFundingAccount{}, &model.AgencyFundingLot{}, &model.AgencyFundingAllocation{}, &model.AgencyFundingLedger{}, &model.AgencyTopupFact{}, &model.AgencyFundingReversal{}} {
		if !db.Migrator().HasTable(table) {
			return false
		}
	}
	return true
}

func (s *Server) walletHistoryCursor(raw string) (walletHistoryCursor, error) {
	var page walletHistoryCursor
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 2048 {
		return page, errors.New("invalid cursor")
	}
	mac := hmac.New(sha256.New, s.TokenHash[:])
	mac.Write([]byte(parts[0]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return page, errors.New("invalid cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || common.Unmarshal(data, &page) != nil || page.Gateway != s.GatewayID || page.Until < time.Now().Unix() || page.Phase < 0 || page.Phase >= walletHistoryPhaseCount || page.After < 0 {
		return page, errors.New("expired cursor")
	}
	if page.StartedAt < 0 || page.After > page.MaxIDs[page.Phase] {
		return page, errors.New("invalid cursor boundary")
	}
	for i := range page.MaxIDs {
		if page.MaxIDs[i] < 0 || page.Counts[i] < 0 {
			return page, errors.New("invalid cursor boundary")
		}
	}
	return page, nil
}

func (s *Server) signWalletHistoryCursor(page walletHistoryCursor) string {
	data, _ := common.Marshal(page)
	raw := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.TokenHash[:])
	mac.Write([]byte(raw))
	return raw + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func walletHistoryHash(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func walletOptionalID(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func (s *Server) WalletHistory(c *gin.Context) {
	limit := 50
	for key, values := range c.Request.URL.Query() {
		if (key != "page_size" && key != "cursor") || len(values) != 1 {
			c.JSON(400, gin.H{"error": "invalid page"})
			return
		}
	}
	if raw, ok := c.GetQuery("page_size"); ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			c.JSON(400, gin.H{"error": "invalid page size"})
			return
		}
		limit = n
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !walletHistoryAvailable(db) {
		c.JSON(501, gin.H{"error": "wallet history export unavailable"})
		return
	}
	page := walletHistoryCursor{Gateway: s.GatewayID, Until: time.Now().Add(2 * time.Hour).Unix(), StartedAt: time.Now().Unix()}
	if raw, ok := c.GetQuery("cursor"); ok {
		var err error
		page, err = s.walletHistoryCursor(raw)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid cursor"})
			return
		}
	} else {
		models := []any{&model.AgencyFundingAccount{}, &model.AgencyFundingLot{}, &model.AgencyFundingAllocation{}, &model.AgencyFundingLedger{}, &model.AgencyTopupFact{}, &model.AgencyFundingReversal{}}
		columns := []string{"user_id", "id", "id", "id", "id", "id"}
		for i := range models {
			var boundary struct {
				MaxID int64 `gorm:"column:max_id"`
				Count int64 `gorm:"column:row_count"`
			}
			if db.Model(models[i]).Select("COALESCE(MAX("+columns[i]+"),0) AS max_id, COUNT(*) AS row_count").Scan(&boundary).Error != nil {
				c.JSON(503, gin.H{"error": "wallet history unavailable"})
				return
			}
			page.MaxIDs[i], page.Counts[i] = boundary.MaxID, boundary.Count
		}
	}
	identity := page
	identity.Phase, identity.After = 0, 0
	snapshotID := walletHistoryHash(s.signWalletHistoryCursor(identity))
	resource, items, next := "complete", any([]any{}), ""
	for page.Phase < walletHistoryPhaseCount {
		phase, phaseItems, lastID, more, err := readWalletHistoryPage(db, page, limit)
		if err != nil {
			c.JSON(503, gin.H{"error": "wallet history unavailable"})
			return
		}
		if phaseItems != nil {
			resource, items = phase, phaseItems
		}
		if more {
			page.After = lastID
			next = s.signWalletHistoryCursor(page)
			break
		}
		page.Phase++
		page.After = 0
		if page.Phase < walletHistoryPhaseCount {
			next = s.signWalletHistoryCursor(page)
		}
		if phaseItems != nil {
			break
		}
	}
	hashInput, _ := common.Marshal(struct {
		Resource string `json:"resource"`
		Items    any    `json:"items"`
	}{resource, items})
	pageHash := sha256.Sum256(hashInput)
	totals := map[string]int64{"accounts": page.Counts[0], "lots": page.Counts[1], "allocations": page.Counts[2], "ledgerEntries": page.Counts[3], "topups": page.Counts[4], "reversals": page.Counts[5]}
	c.JSON(200, gin.H{"schemaVersion": 1, "gatewayId": s.GatewayID, "supported": true, "consistentSnapshot": false, "snapshotId": snapshotID, "scanStartedAt": page.StartedAt, "resource": resource, "items": items, "pageHash": hex.EncodeToString(pageHash[:]), "controlTotals": totals, "nextCursor": next})
}

func readWalletHistoryPage(db *gorm.DB, page walletHistoryCursor, limit int) (string, any, int64, bool, error) {
	switch page.Phase {
	case 0:
		var rows []model.AgencyFundingAccount
		if err := db.Where("user_id > ? AND user_id <= ?", page.After, page.MaxIDs[0]).Order("user_id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return "", nil, 0, false, err
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		if len(rows) == 0 {
			return "accounts", nil, 0, false, nil
		}
		items := make([]walletAccountExport, len(rows))
		for i, row := range rows {
			items[i] = walletAccountExport{strconv.FormatInt(row.UserID, 10), row.PaidAvailable, row.NonpaidAvailable, row.DebtQuota, row.MoneySeq, row.Version, walletHistoryHash(row.OpeningSnapshot), row.ReconcileBlocked, walletHistoryHash(row.ReconcileReason), row.UpdatedAt}
		}
		return "accounts", items, rows[len(rows)-1].UserID, more, nil
	case 1:
		var rows []model.AgencyFundingLot
		if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[1]).Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return "", nil, 0, false, err
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		if len(rows) == 0 {
			return "lots", nil, 0, false, nil
		}
		items := make([]walletLotExport, len(rows))
		for i, row := range rows {
			actor := ""
			if row.ActorUserID > 0 {
				actor = strconv.FormatInt(row.ActorUserID, 10)
			}
			items[i] = walletLotExport{strconv.FormatInt(row.ID, 10), strconv.FormatInt(row.UserID, 10), row.SourceKind, row.SourceID, actor, walletHistoryHash(row.SourceSnapshotJSON), row.CompletionSource, row.PaidInitial, row.BonusInitial, row.PaidAvailable, row.PaidReserved, row.PaidConsumed, row.PaidRevoked, row.BonusAvailable, row.BonusReserved, row.BonusConsumed, row.BonusRevoked, row.BonusExpired, row.PaidDebtRepaid, row.BonusDebtRepaid, row.ExpiresAt, row.MoneySeq, row.Version, row.CreatedAt}
		}
		return "lots", items, rows[len(rows)-1].ID, more, nil
	case 2:
		var rows []model.AgencyFundingAllocation
		if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[2]).Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return "", nil, 0, false, err
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		if len(rows) == 0 {
			return "allocations", nil, 0, false, nil
		}
		items := make([]walletAllocationExport, len(rows))
		for i, row := range rows {
			lot := ""
			if row.LotID > 0 {
				lot = strconv.FormatInt(row.LotID, 10)
			}
			items[i] = walletAllocationExport{strconv.FormatInt(row.ID, 10), row.ChargeID, row.SegmentNo, row.ComponentID, strconv.FormatInt(row.UserID, 10), lot, walletHistoryHash(row.SourceSnapshotJSON), row.Reserved, row.Consumed, row.NonpaidConsumed, row.DebtConsumed, row.Released, row.Reversed, row.RevokedReservedDebt, row.RevokedNonpaid, row.Version}
		}
		return "allocations", items, rows[len(rows)-1].ID, more, nil
	case 3:
		var rows []model.AgencyFundingLedger
		if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[3]).Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return "", nil, 0, false, err
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		if len(rows) == 0 {
			return "ledgerEntries", nil, 0, false, nil
		}
		items := make([]walletLedgerExport, len(rows))
		for i, row := range rows {
			items[i] = walletLedgerExport{strconv.FormatInt(row.ID, 10), row.OperationID, row.EntryNo, strconv.FormatInt(row.UserID, 10), row.MoneySeq, row.SourceKind, walletOptionalID(row.LotID), row.PaidDelta, row.NonpaidDelta, row.DebtDelta, row.PaidAfter, row.NonpaidAfter, row.DebtAfter, walletOptionalID(row.AgencyID), walletOptionalID(row.BindingID), row.CurrencyCode, row.CreatedAtMS}
		}
		return "ledgerEntries", items, rows[len(rows)-1].ID, more, nil
	case 4:
		var rows []model.AgencyTopupFact
		if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[4]).Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return "", nil, 0, false, err
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		if len(rows) == 0 {
			return "topups", nil, 0, false, nil
		}
		items := make([]walletTopupExport, len(rows))
		for i, row := range rows {
			initiator := ""
			if row.InitiatedByUserID > 0 {
				initiator = strconv.FormatInt(row.InitiatedByUserID, 10)
			}
			items[i] = walletTopupExport{strconv.FormatInt(row.ID, 10), row.SourceOperationID, row.SourceID, strconv.FormatInt(row.UserID, 10), initiator, walletOptionalID(row.AgencyID), walletOptionalID(row.BindingID), walletHistoryHash(row.PaymentReference), row.ActualMoney, row.CurrencyCode, row.QuotaConversionSnapshot, row.CreditedQuota, row.PaidQuota, row.BonusQuota, row.FundingSource, row.CompletionSource, row.PaymentStatus, row.RefundedQuota, row.ExpiredQuota, row.ExpiresAt, row.OccurredAtMS}
		}
		return "topups", items, rows[len(rows)-1].ID, more, nil
	case 5:
		var rows []model.AgencyFundingReversal
		if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[5]).Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return "", nil, 0, false, err
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		if len(rows) == 0 {
			return "reversals", nil, 0, false, nil
		}
		items := make([]walletReversalExport, len(rows))
		for i, row := range rows {
			items[i] = walletReversalExport{strconv.FormatInt(row.ID, 10), row.RefundID, row.SourceOperationID, strconv.FormatInt(row.UserID, 10), row.Quota, row.CurrencyCode, walletHistoryHash(row.PaymentReference), walletHistoryHash(row.EvidenceRef), walletHistoryHash(row.Reason), row.CreatedAtMS}
		}
		return "reversals", items, rows[len(rows)-1].ID, more, nil
	default:
		return "", nil, 0, false, errors.New("invalid phase")
	}
}
