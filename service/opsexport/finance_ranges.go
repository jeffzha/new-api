package opsexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// FinanceRange exposes bounded, resumable journal reads. These rows are not an
// atomic snapshot and cannot be used to open balances or switch writers.
func (s *Server) FinanceRange(c *gin.Context) {
	for key, values := range c.Request.URL.Query() {
		if (key != "after" && key != "through" && key != "page_size") || len(values) != 1 {
			c.JSON(400, gin.H{"error": "invalid range"})
			return
		}
	}
	domain, resource := c.Param("domain"), c.Param("resource")
	var phase int
	var source any
	switch {
	case domain == "wallet-history" && resource == "ledgerEntries":
		phase, source = 3, &model.AgencyFundingLedger{}
	case domain == "commission-history" && resource == "ledgerEntries":
		phase, source = 0, &model.AgencyCommissionLedger{}
	case domain == "withdrawals" && resource == "transitions":
		phase, source = 2, &model.AgencyWithdrawalTransition{}
	default:
		c.JSON(400, gin.H{"error": "unsupported range"})
		return
	}
	limit := 50
	if raw, ok := c.GetQuery("page_size"); ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			c.JSON(400, gin.H{"error": "invalid page size"})
			return
		}
		limit = n
	}
	readID := func(key string) (int64, bool, bool) {
		raw, present := c.GetQuery(key)
		if !present {
			return 0, false, true
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		return value, true, err == nil && value >= 0 && strconv.FormatInt(value, 10) == raw
	}
	after, _, validAfter := readID("after")
	through, supplied, validThrough := readID("through")
	if !validAfter || !validThrough || supplied && (through == 0 || after > through) || !supplied && after != 0 {
		c.JSON(400, gin.H{"error": "invalid range boundary"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !db.Migrator().HasTable(source) {
		c.JSON(501, gin.H{"error": "range unavailable"})
		return
	}
	if !supplied {
		var boundary struct {
			MaxID int64 `gorm:"column:max_id"`
		}
		if err := db.Model(source).Select("COALESCE(MAX(id),0) AS max_id").Scan(&boundary).Error; err != nil {
			c.JSON(503, gin.H{"error": "range unavailable"})
			return
		}
		through = boundary.MaxID
	}
	var first struct{ ID int64 }
	if err := db.Model(source).Select("id").Where("id > ? AND id <= ?", after, through).Order("id").Limit(1).Find(&first).Error; err != nil {
		c.JSON(503, gin.H{"error": "range unavailable"})
		return
	}
	var items any
	var more bool
	var err error
	var nextAfter int64
	switch {
	case first.ID == 0:
		items = []any{}
	case domain == "wallet-history":
		page := walletHistoryCursor{Phase: phase, After: after}
		page.MaxIDs[phase] = through
		_, items, nextAfter, more, err = readWalletHistoryPage(db, page, limit)
	default:
		phases := map[string]int{"commission-history": 2, "withdrawals": 3}[domain]
		page := financeHistoryCursor{Phase: phase, After: after, MaxIDs: make([]int64, phases)}
		page.MaxIDs[phase] = through
		switch domain {
		case "commission-history":
			_, items, more, err = readCommissionHistoryPage(db, &page, limit)
		case "withdrawals":
			_, items, more, err = readWithdrawalHistoryPage(db, &page, limit)
		}
		nextAfter = page.After
	}
	if err != nil {
		c.JSON(503, gin.H{"error": "range unavailable"})
		return
	}
	if items == nil {
		items = []any{}
	}
	if first.ID != 0 && domain != "wallet-history" {
		// The legacy reader advances to the next resource after a non-full page.
		// A range ends at the current resource, so derive its own continuation.
		switch rows := items.(type) {
		case []commissionLedgerExport:
			if len(rows) > 0 {
				nextAfter, _ = strconv.ParseInt(rows[len(rows)-1].ExternalEntryID, 10, 64)
			}
		case []withdrawalTransitionExport:
			if len(rows) > 0 {
				nextAfter, _ = strconv.ParseInt(rows[len(rows)-1].ExternalTransitionID, 10, 64)
			}
		}
		var remaining struct{ ID int64 }
		if nextAfter > after {
			if err := db.Model(source).Select("id").Where("id > ? AND id <= ?", nextAfter, through).Order("id").Limit(1).Find(&remaining).Error; err != nil {
				c.JSON(503, gin.H{"error": "range unavailable"})
				return
			}
		}
		more = remaining.ID > 0
	}
	encoded, err := common.Marshal(items)
	if err != nil {
		c.JSON(503, gin.H{"error": "range unavailable"})
		return
	}
	if !more {
		nextAfter = 0
	}
	digest := sha256.Sum256(encoded)
	c.JSON(200, gin.H{"schemaVersion": 1, "gatewayId": s.GatewayID,
		"domain": domain, "resource": resource, "through": strconv.FormatInt(through, 10),
		"after": strconv.FormatInt(after, 10), "nextAfter": strconv.FormatInt(nextAfter, 10),
		"hasMore": more, "consistentSnapshot": false, "items": items,
		"pageHash": hex.EncodeToString(digest[:])})
}
