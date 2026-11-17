package opsexport

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type debtHistoryExport struct {
	ExternalDebtID       string `json:"externalDebtId"`
	ExternalUserID       string `json:"externalUserId"`
	OriginOperationID    string `json:"originOperationId"`
	DebtKind             string `json:"debtKind"`
	ExternalAllocationID string `json:"externalAllocationId,omitempty"`
	OriginalQuota        int64  `json:"originalQuota"`
	OutstandingQuota     int64  `json:"outstandingQuota"`
	ReversedQuota        int64  `json:"reversedQuota"`
	CreatedAtMS          int64  `json:"createdAtMs"`
}

type repaymentHistoryExport struct {
	ExternalRepaymentID string `json:"externalRepaymentId"`
	ExternalDebtID      string `json:"externalDebtId"`
	ExternalUserID      string `json:"externalUserId"`
	RepaymentID         string `json:"repaymentId"`
	ExternalLotID       string `json:"externalLotId,omitempty"`
	SourceKind          string `json:"sourceKind"`
	Quota               int64  `json:"quota"`
	ReversedQuota       int64  `json:"reversedQuota"`
	RestoredTotal       int64  `json:"restoredTotal"`
	MoneySeq            int64  `json:"moneySeq"`
	CreatedAtMS         int64  `json:"createdAtMs"`
}

func debtHistoryAvailable(db *gorm.DB) bool {
	return db.Migrator().HasTable(&model.AgencyFundingDebt{}) && db.Migrator().HasTable(&model.AgencyDebtRepayment{})
}

func (s *Server) DebtHistory(c *gin.Context) {
	limit, raw, ok := parseFinanceHistoryRequest(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !debtHistoryAvailable(db) {
		c.JSON(501, gin.H{"error": "debt history export unavailable"})
		return
	}
	page := financeHistoryCursor{Gateway: s.GatewayID, Domain: "debt", Until: time.Now().Add(2 * time.Hour).Unix()}
	var err error
	if raw != "" {
		page, err = s.parseFinanceHistoryCursor(raw, "debt", 2)
	} else {
		err = initializeFinanceBoundary(db, &page, []any{&model.AgencyFundingDebt{}, &model.AgencyDebtRepayment{}})
	}
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid cursor"})
		return
	}
	resource, items, more, err := readDebtHistoryPage(db, &page, limit)
	if err != nil {
		c.JSON(503, gin.H{"error": "debt history unavailable"})
		return
	}
	financeHistoryResponse(c, s, page, resource, items, more, false)
}

func readDebtHistoryPage(db *gorm.DB, page *financeHistoryCursor, limit int) (string, any, bool, error) {
	if page.Phase == 0 {
		var rows []model.AgencyFundingDebt
		if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[0]).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
			return "", nil, false, err
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		items := make([]debtHistoryExport, len(rows))
		for i, row := range rows {
			items[i] = debtHistoryExport{strconv.FormatInt(row.ID, 10), strconv.FormatInt(row.UserID, 10), row.OriginOperationID, row.DebtKind, financeOptionalID(row.AllocationID), row.OriginalQuota, row.OutstandingQuota, row.ReversedQuota, row.CreatedAtMS}
		}
		if more {
			page.After = rows[len(rows)-1].ID
			return "debts", items, true, nil
		}
		page.Phase, page.After = 1, 0
		if len(items) > 0 {
			return "debts", items, true, nil
		}
	}
	var rows []model.AgencyDebtRepayment
	if err := db.Where("id > ? AND id <= ?", page.After, page.MaxIDs[1]).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return "", nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.DebtID)
	}
	var debts []model.AgencyFundingDebt
	if len(ids) > 0 {
		if err := db.Select("id", "user_id").Where("id IN ?", ids).Find(&debts).Error; err != nil {
			return "", nil, false, err
		}
	}
	users := make(map[int64]int64, len(debts))
	for _, debt := range debts {
		users[debt.ID] = debt.UserID
	}
	items := make([]repaymentHistoryExport, len(rows))
	for i, row := range rows {
		userID, found := users[row.DebtID]
		if !found {
			return "", nil, false, gorm.ErrRecordNotFound
		}
		items[i] = repaymentHistoryExport{strconv.FormatInt(row.ID, 10), strconv.FormatInt(row.DebtID, 10), strconv.FormatInt(userID, 10), row.RepaymentID, financeOptionalID(row.FundingLotID), row.SourceKind, row.Quota, row.ReversedQuota, row.RestoredTotal, row.MoneySeq, row.CreatedAtMS}
	}
	if more {
		page.After = rows[len(rows)-1].ID
	}
	return "repayments", items, more, nil
}
