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

type financeHistoryCursor struct {
	Gateway string  `json:"gateway"`
	Domain  string  `json:"domain"`
	Until   int64   `json:"until"`
	MaxIDs  []int64 `json:"maxIds"`
	Counts  []int64 `json:"counts"`
	Phase   int     `json:"phase"`
	After   int64   `json:"after"`
}

type commissionLedgerExport struct {
	ExternalEntryID         string `json:"externalEntryId"`
	EventID                 string `json:"eventId"`
	ComponentID             string `json:"componentId"`
	ComponentKey            string `json:"componentKey,omitempty"`
	EntryType               string `json:"entryType"`
	OriginalExternalEntryID string `json:"originalExternalEntryId,omitempty"`
	AgencyExternalID        string `json:"agencyExternalId"`
	BindingExternalID       string `json:"bindingExternalId,omitempty"`
	ExternalUserID          string `json:"externalUserId,omitempty"`
	OriginModelName         string `json:"originModelName,omitempty"`
	StandardQuota           int64  `json:"standardQuota"`
	SettlementCostQuota     int64  `json:"settlementCostQuota"`
	TheoreticalCommission   int64  `json:"theoreticalCommissionQuota"`
	PaidAllocatedQuota      int64  `json:"paidAllocatedQuota"`
	CommissionQuota         int64  `json:"commissionQuota"`
	AmountMicros            int64  `json:"amountMicros"`
	CurrencyCode            string `json:"currencyCode"`
	QuotaPerUnit            string `json:"quotaPerUnit,omitempty"`
	ExchangeRate            string `json:"exchangeRate,omitempty"`
	OccurredAtMS            int64  `json:"occurredAtMs"`
	HierarchyDepth          int    `json:"hierarchyDepth"`
	ParentAgencyExternalID  string `json:"parentAgencyExternalId,omitempty"`
}

type commissionBalanceExport struct {
	ExternalBalanceID string `json:"externalBalanceId"`
	AgencyExternalID  string `json:"agencyExternalId"`
	CurrencyCode      string `json:"currencyCode"`
	EarnedMicros      int64  `json:"earnedMicros"`
	ReversedMicros    int64  `json:"reversedMicros"`
	AvailableMicros   int64  `json:"availableMicros"`
	LockedMicros      int64  `json:"lockedMicros"`
	PaidMicros        int64  `json:"paidMicros"`
	Version           int64  `json:"version"`
	UpdatedAtMS       int64  `json:"updatedAtMs"`
}

type withdrawalAccountExport struct {
	ExternalAccountID string `json:"externalAccountId"`
	AgencyExternalID  string `json:"agencyExternalId"`
	Version           int64  `json:"version"`
	KeyID             string `json:"keyId"`
	Last4             string `json:"last4,omitempty"`
	Status            string `json:"status"`
	CiphertextHash    string `json:"ciphertextHash"`
	CreatedAtMS       int64  `json:"createdAtMs"`
}

type withdrawalExport struct {
	ExternalWithdrawalID string `json:"externalWithdrawalId"`
	RequestNo            string `json:"requestNo"`
	AgencyExternalID     string `json:"agencyExternalId"`
	CurrencyCode         string `json:"currencyCode"`
	AmountMicros         int64  `json:"amountMicros"`
	Status               string `json:"status"`
	Version              int64  `json:"version"`
	ExternalAccountID    string `json:"externalAccountId"`
	AccountVersion       int64  `json:"accountVersion"`
	AccountSnapshotHash  string `json:"accountSnapshotHash,omitempty"`
	AccountSnapshotKeyID string `json:"accountSnapshotKeyId,omitempty"`
	ReviewerExternalID   string `json:"reviewerExternalUserId,omitempty"`
	PaymentChannel       string `json:"paymentChannel,omitempty"`
	PaymentReferenceHash string `json:"paymentReferenceHash,omitempty"`
	PreviousStatus       string `json:"previousStatus,omitempty"`
	OnHoldReasonHash     string `json:"onHoldReasonHash,omitempty"`
	CreatedAtMS          int64  `json:"createdAtMs"`
	UpdatedAtMS          int64  `json:"updatedAtMs"`
}

type withdrawalTransitionExport struct {
	ExternalTransitionID string `json:"externalTransitionId"`
	ExternalWithdrawalID string `json:"externalWithdrawalId"`
	OperationID          string `json:"operationId"`
	BeforeStatus         string `json:"beforeStatus"`
	AfterStatus          string `json:"afterStatus"`
	AmountDeltaMicros    int64  `json:"amountDeltaMicros"`
	ActorType            string `json:"actorType"`
	ActorExternalID      string `json:"actorExternalId,omitempty"`
	EvidenceHash         string `json:"evidenceHash,omitempty"`
	CreatedAtMS          int64  `json:"createdAtMs"`
}

type auditLogExport struct {
	ExternalAuditID        string `json:"externalAuditId"`
	EventID                string `json:"eventId"`
	ActorType              string `json:"actorType"`
	ActorExternalID        string `json:"actorExternalId"`
	ActingAgencyExternalID string `json:"actingAgencyExternalId,omitempty"`
	Action                 string `json:"action"`
	ObjectType             string `json:"objectType"`
	ObjectID               string `json:"objectId"`
	RequestID              string `json:"requestId"`
	ReasonHash             string `json:"reasonHash,omitempty"`
	BeforeHash             string `json:"beforeHash,omitempty"`
	AfterHash              string `json:"afterHash,omitempty"`
	SourceIPHash           string `json:"sourceIpHash,omitempty"`
	CreatedAtMS            int64  `json:"createdAtMs"`
}

func commissionHistoryAvailable(db *gorm.DB) bool {
	return db.Migrator().HasTable(&model.AgencyCommissionLedger{}) && db.Migrator().HasTable(&model.AgencyCommissionBalance{})
}

func withdrawalHistoryAvailable(db *gorm.DB) bool {
	return db.Migrator().HasTable(&model.AgencyWithdrawalAccount{}) && db.Migrator().HasTable(&model.AgencyWithdrawal{}) && db.Migrator().HasTable(&model.AgencyWithdrawalTransition{})
}

func auditHistoryAvailable(db *gorm.DB) bool {
	return db.Migrator().HasTable(&model.AgencyAuditLog{})
}

func financeHistoryHash(value string) string {
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func financeOptionalID(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func (s *Server) signFinanceHistoryCursor(page financeHistoryCursor) string {
	data, _ := common.Marshal(page)
	raw := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.TokenHash[:])
	mac.Write([]byte(raw))
	return raw + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) parseFinanceHistoryCursor(raw, domain string, phases int) (financeHistoryCursor, error) {
	var page financeHistoryCursor
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
	if err != nil || common.Unmarshal(data, &page) != nil || page.Gateway != s.GatewayID || page.Domain != domain || page.Until < time.Now().Unix() || page.Phase < 0 || page.Phase >= phases || page.After < 0 || len(page.MaxIDs) != phases || len(page.Counts) != phases {
		return page, errors.New("expired cursor")
	}
	for i := range page.MaxIDs {
		if page.MaxIDs[i] < 0 || page.Counts[i] < 0 {
			return page, errors.New("invalid cursor boundary")
		}
	}
	return page, nil
}

func parseFinanceHistoryRequest(c *gin.Context) (int, string, bool) {
	limit := 50
	for key, values := range c.Request.URL.Query() {
		if (key != "page_size" && key != "cursor") || len(values) != 1 {
			c.JSON(400, gin.H{"error": "invalid page"})
			return 0, "", false
		}
	}
	if raw, ok := c.GetQuery("page_size"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			c.JSON(400, gin.H{"error": "invalid page size"})
			return 0, "", false
		}
		limit = parsed
	}
	cursor, _ := c.GetQuery("cursor")
	return limit, cursor, true
}

func initializeFinanceBoundary(db *gorm.DB, page *financeHistoryCursor, models []any) error {
	page.MaxIDs = make([]int64, len(models))
	page.Counts = make([]int64, len(models))
	for i := range models {
		var boundary struct {
			MaxID int64 `gorm:"column:max_id"`
			Count int64 `gorm:"column:row_count"`
		}
		if err := db.Model(models[i]).Select("COALESCE(MAX(id),0) AS max_id, COUNT(*) AS row_count").Scan(&boundary).Error; err != nil {
			return err
		}
		page.MaxIDs[i], page.Counts[i] = boundary.MaxID, boundary.Count
	}
	return nil
}

func financeHistoryResponse(c *gin.Context, s *Server, page financeHistoryCursor, resource string, items any, more bool, consistent bool) {
	encoded, err := common.Marshal(items)
	if err != nil {
		c.JSON(503, gin.H{"error": "history unavailable"})
		return
	}
	digest := sha256.Sum256(encoded)
	next := ""
	if more {
		next = s.signFinanceHistoryCursor(page)
	}
	identity := page
	identity.Phase, identity.After = 0, 0
	c.JSON(200, gin.H{
		"schemaVersion": 1, "sourceSystem": "new-api", "gatewayId": s.GatewayID,
		"supported": true, "consistentSnapshot": consistent, "snapshotId": financeHistoryHash(s.signFinanceHistoryCursor(identity)),
		"resource": resource, "controlTotals": page.Counts, "pageHash": hex.EncodeToString(digest[:]), "items": items, "nextCursor": next,
	})
}

func (s *Server) CommissionHistory(c *gin.Context) {
	limit, raw, ok := parseFinanceHistoryRequest(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !commissionHistoryAvailable(db) {
		c.JSON(501, gin.H{"error": "commission history export unavailable"})
		return
	}
	page := financeHistoryCursor{Gateway: s.GatewayID, Domain: "commission", Until: time.Now().Add(2 * time.Hour).Unix()}
	var err error
	if raw != "" {
		page, err = s.parseFinanceHistoryCursor(raw, "commission", 2)
	} else {
		err = initializeFinanceBoundary(db, &page, []any{&model.AgencyCommissionLedger{}, &model.AgencyCommissionBalance{}})
	}
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid cursor"})
		return
	}
	resource, items, more, err := readCommissionHistoryPage(db, &page, limit)
	if err != nil {
		c.JSON(503, gin.H{"error": "commission history unavailable"})
		return
	}
	financeHistoryResponse(c, s, page, resource, items, more, false)
}

func readCommissionHistoryPage(db *gorm.DB, page *financeHistoryCursor, limit int) (string, any, bool, error) {
	if page.Phase == 0 {
		var rows []model.AgencyCommissionLedger
		if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[0]).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
			return "", nil, false, err
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		items := make([]commissionLedgerExport, len(rows))
		for i := range rows {
			r := rows[i]
			items[i] = commissionLedgerExport{strconv.FormatInt(r.ID, 10), r.EventID, r.ComponentID, r.ComponentKey, r.EntryType, financeOptionalID(r.OriginalEntryID), strconv.FormatInt(r.AgencyID, 10), strconv.FormatInt(r.BindingID, 10), strconv.FormatInt(r.UserID, 10), r.OriginModelName, r.StandardQuota, r.SettlementCostQuota, r.TheoreticalCommissionQuota, r.PaidAllocatedQuota, r.CommissionQuota, r.AmountMicros, r.CurrencyCode, r.QuotaPerUnit, r.ExchangeRate, r.OccurredAtMS, r.HierarchyDepth, financeOptionalID(r.ParentAgencyID)}
		}
		if more {
			page.After = rows[len(rows)-1].ID
			return "ledgerEntries", items, true, nil
		}
		page.Phase, page.After = 1, 0
		if len(items) > 0 {
			return "ledgerEntries", items, true, nil
		}
	}
	var rows []model.AgencyCommissionBalance
	if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[1]).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return "", nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	items := make([]commissionBalanceExport, len(rows))
	for i := range rows {
		r := rows[i]
		items[i] = commissionBalanceExport{strconv.FormatInt(r.ID, 10), strconv.FormatInt(r.AgencyID, 10), r.CurrencyCode, r.EarnedMicros, r.ReversedMicros, r.AvailableMicros, r.LockedMicros, r.PaidMicros, r.Version, r.UpdatedAtMS}
	}
	if more {
		page.After = rows[len(rows)-1].ID
	}
	return "balances", items, more, nil
}

func (s *Server) Withdrawals(c *gin.Context) {
	limit, raw, ok := parseFinanceHistoryRequest(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !withdrawalHistoryAvailable(db) {
		c.JSON(501, gin.H{"error": "withdrawal history export unavailable"})
		return
	}
	page := financeHistoryCursor{Gateway: s.GatewayID, Domain: "withdrawals", Until: time.Now().Add(2 * time.Hour).Unix()}
	var err error
	if raw != "" {
		page, err = s.parseFinanceHistoryCursor(raw, "withdrawals", 3)
	} else {
		err = initializeFinanceBoundary(db, &page, []any{&model.AgencyWithdrawalAccount{}, &model.AgencyWithdrawal{}, &model.AgencyWithdrawalTransition{}})
	}
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid cursor"})
		return
	}
	resource, items, more, err := readWithdrawalHistoryPage(db, &page, limit)
	if err != nil {
		c.JSON(503, gin.H{"error": "withdrawal history unavailable"})
		return
	}
	financeHistoryResponse(c, s, page, resource, items, more, false)
}

func readWithdrawalHistoryPage(db *gorm.DB, page *financeHistoryCursor, limit int) (string, any, bool, error) {
	for page.Phase < 3 {
		switch page.Phase {
		case 0:
			var rows []model.AgencyWithdrawalAccount
			if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[0]).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
				return "", nil, false, err
			}
			more := len(rows) > limit
			if more {
				rows = rows[:limit]
			}
			items := make([]withdrawalAccountExport, len(rows))
			for i := range rows {
				r := rows[i]
				items[i] = withdrawalAccountExport{strconv.FormatInt(r.ID, 10), strconv.FormatInt(r.AgencyID, 10), r.Version, r.KeyID, r.Last4, r.Status, financeHistoryHash(r.Ciphertext), r.CreatedAtMS}
			}
			if more {
				page.After = rows[len(rows)-1].ID
				return "accounts", items, true, nil
			}
			page.Phase, page.After = 1, 0
			if len(items) > 0 {
				return "accounts", items, true, nil
			}
		case 1:
			var rows []model.AgencyWithdrawal
			if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[1]).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
				return "", nil, false, err
			}
			more := len(rows) > limit
			if more {
				rows = rows[:limit]
			}
			items := make([]withdrawalExport, len(rows))
			for i := range rows {
				r := rows[i]
				items[i] = withdrawalExport{strconv.FormatInt(r.ID, 10), r.RequestNo, strconv.FormatInt(r.AgencyID, 10), r.CurrencyCode, r.AmountMicros, r.Status, r.Version, strconv.FormatInt(r.AccountID, 10), r.AccountVersion, r.AccountSnapshotHash, r.AccountSnapshotKeyID, financeOptionalID(r.ReviewerID), r.PaymentChannel, financeHistoryHash(r.PaymentReference), r.PreviousStatus, financeHistoryHash(r.OnHoldReason), r.CreatedAtMS, r.UpdatedAtMS}
			}
			if more {
				page.After = rows[len(rows)-1].ID
				return "withdrawals", items, true, nil
			}
			page.Phase, page.After = 2, 0
			if len(items) > 0 {
				return "withdrawals", items, true, nil
			}
		case 2:
			var rows []model.AgencyWithdrawalTransition
			if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[2]).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
				return "", nil, false, err
			}
			more := len(rows) > limit
			if more {
				rows = rows[:limit]
			}
			items := make([]withdrawalTransitionExport, len(rows))
			for i := range rows {
				r := rows[i]
				items[i] = withdrawalTransitionExport{strconv.FormatInt(r.ID, 10), strconv.FormatInt(r.WithdrawalID, 10), r.OperationID, r.BeforeStatus, r.AfterStatus, r.AmountDeltaMicros, r.ActorType, strconv.FormatInt(r.ActorID, 10), financeHistoryHash(r.Evidence), r.CreatedAtMS}
			}
			if more {
				page.After = rows[len(rows)-1].ID
			}
			return "transitions", items, more, nil
		}
	}
	return "complete", []any{}, false, nil
}

func (s *Server) AuditHistory(c *gin.Context) {
	limit, raw, ok := parseFinanceHistoryRequest(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !auditHistoryAvailable(db) {
		c.JSON(501, gin.H{"error": "audit history export unavailable"})
		return
	}
	page := financeHistoryCursor{Gateway: s.GatewayID, Domain: "audit", Until: time.Now().Add(2 * time.Hour).Unix()}
	var err error
	if raw != "" {
		page, err = s.parseFinanceHistoryCursor(raw, "audit", 1)
	} else {
		err = initializeFinanceBoundary(db, &page, []any{&model.AgencyAuditLog{}})
	}
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid cursor"})
		return
	}
	var rows []model.AgencyAuditLog
	if err = db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[0]).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
		c.JSON(503, gin.H{"error": "audit history unavailable"})
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	items := make([]auditLogExport, len(rows))
	for i := range rows {
		r := rows[i]
		items[i] = auditLogExport{strconv.FormatInt(r.ID, 10), r.EventID, r.ActorType, strconv.FormatInt(r.ActorID, 10), financeOptionalID(r.ActingAgencyID), r.Action, r.ObjectType, r.ObjectID, r.RequestID, financeHistoryHash(r.Reason), financeHistoryHash(r.BeforeJSON), financeHistoryHash(r.AfterJSON), financeHistoryHash(r.SourceIP), r.CreatedAtMS}
	}
	if more {
		page.After = rows[len(rows)-1].ID
	}
	financeHistoryResponse(c, s, page, "auditLogs", items, more, true)
}
