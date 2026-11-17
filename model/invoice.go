package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	InvoiceStatusPendingReview = "pending_review"
	InvoiceStatusRejected      = "rejected"
	InvoiceStatusApproved      = "approved"
	InvoiceStatusIssued        = "issued"
	InvoiceStatusWithdrawn     = "withdrawn"
	InvoiceStatusVoidPending   = "void_pending"
	InvoiceStatusVoided        = "voided"
)

var (
	ErrInvoiceApplicationNotFound = errors.New("invoice application not found")
	ErrInvoiceInvalidStatus       = errors.New("invoice application status does not allow this operation")
	ErrInvoiceSourceUnavailable   = errors.New("invoice source is unavailable")
	ErrInvoiceAmountInvalid       = errors.New("invoice amount is invalid")
	ErrInvoiceSourceExhausted     = errors.New("invoice source available amount is insufficient")
	ErrInvoiceReviewReasonMissing = errors.New("invoice review reason is required")
	ErrInvoiceDocumentRequired    = errors.New("invoice document is required")
	ErrInvoiceDocumentInvalid     = errors.New("invoice document is invalid")
	ErrInvoiceDocumentTooLarge    = errors.New("invoice document is too large")
	ErrInvoiceDuplicateNumber     = errors.New("invoice number has already been recorded")
	ErrInvoiceIssueAmountMismatch = errors.New("invoice issue amount does not match application")
)

const maxInvoiceDocumentBytes int64 = 10 * 1024 * 1024

type InvoiceApplication struct {
	ID               int                      `json:"id"`
	ApplicationNo    string                   `json:"application_no" gorm:"uniqueIndex;type:varchar(32)"`
	UserID           int                      `json:"user_id" gorm:"index"`
	Status           string                   `json:"status" gorm:"index;type:varchar(32)"`
	CurrencyCode     string                   `json:"currency_code" gorm:"type:varchar(3)"`
	TotalCents       int64                    `json:"total_cents"`
	PurchaserName    string                   `json:"purchaser_name" gorm:"type:varchar(255)"`
	TaxNumber        string                   `json:"tax_number" gorm:"type:varchar(64)"`
	ReceiverEmail    string                   `json:"receiver_email" gorm:"type:varchar(254)"`
	BankName         string                   `json:"bank_name,omitempty" gorm:"type:varchar(255)"`
	BankAccount      string                   `json:"bank_account,omitempty" gorm:"type:varchar(128)"`
	Address          string                   `json:"address,omitempty" gorm:"type:varchar(500)"`
	Phone            string                   `json:"phone,omitempty" gorm:"type:varchar(64)"`
	Remark           string                   `json:"remark,omitempty" gorm:"type:varchar(1000)"`
	ReviewReason     string                   `json:"review_reason,omitempty" gorm:"type:varchar(1000)"`
	ReviewerID       int                      `json:"reviewer_id,omitempty" gorm:"index"`
	ReviewedAt       int64                    `json:"reviewed_at,omitempty"`
	InvoiceNumber    string                   `json:"invoice_number,omitempty" gorm:"type:varchar(128)"`
	InvoiceNumberKey *string                  `json:"-" gorm:"uniqueIndex:idx_invoice_number_key;type:char(64)"`
	InvoiceCode      string                   `json:"invoice_code,omitempty" gorm:"type:varchar(128)"`
	InvoiceDate      int64                    `json:"invoice_date,omitempty"`
	IssuerID         int                      `json:"issuer_id,omitempty" gorm:"index"`
	IssuedAt         int64                    `json:"issued_at,omitempty"`
	VoidReason       string                   `json:"void_reason,omitempty" gorm:"type:varchar(1000)"`
	EmailStatus      string                   `json:"email_status" gorm:"type:varchar(16)"`
	EmailSentAt      int64                    `json:"email_sent_at,omitempty"`
	EmailLastError   string                   `json:"-" gorm:"type:varchar(500)"`
	CreatedAt        int64                    `json:"created_at" gorm:"index"`
	UpdatedAt        int64                    `json:"updated_at"`
	Lines            []InvoiceApplicationLine `json:"lines,omitempty" gorm:"foreignKey:ApplicationID"`
}

type InvoiceApplicationLine struct {
	ID               int    `json:"id"`
	ApplicationID    int    `json:"application_id" gorm:"index;uniqueIndex:idx_invoice_application_topup"`
	TopUpID          int    `json:"topup_id" gorm:"index;uniqueIndex:idx_invoice_application_topup"`
	SourceTradeNo    string `json:"source_trade_no" gorm:"type:varchar(255)"`
	PaidCents        int64  `json:"paid_cents"`
	AllocatedCents   int64  `json:"allocated_cents"`
	CurrencyCode     string `json:"currency_code" gorm:"type:varchar(3)"`
	PaymentReference string `json:"-" gorm:"type:varchar(191)"`
	CreatedAt        int64  `json:"created_at"`
}

type InvoiceApplicationEvent struct {
	ID            int    `json:"id"`
	ApplicationID int    `json:"application_id" gorm:"index"`
	ActorID       int    `json:"actor_id" gorm:"index"`
	ActorRole     int    `json:"actor_role"`
	Action        string `json:"action" gorm:"type:varchar(64)"`
	FromStatus    string `json:"from_status" gorm:"type:varchar(32)"`
	ToStatus      string `json:"to_status" gorm:"type:varchar(32)"`
	Reason        string `json:"reason,omitempty" gorm:"type:varchar(1000)"`
	CreatedAt     int64  `json:"created_at" gorm:"index"`
}

type InvoiceApplicationDocument struct {
	ID            int    `json:"id"`
	ApplicationID int    `json:"application_id" gorm:"uniqueIndex"`
	FileName      string `json:"file_name" gorm:"type:varchar(255)"`
	ContentType   string `json:"content_type" gorm:"type:varchar(128)"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256" gorm:"type:char(64)"`
	Data          []byte `json:"-"`
	CreatedAt     int64  `json:"created_at"`
}

type InvoiceSource struct {
	TopUpID        int
	TradeNo        string
	PaidCents      int64
	AvailableCents int64
	CurrencyCode   string
	PaidAt         int64
}

type InvoiceSummary struct {
	AvailableCents int64 `json:"available_cents"`
	ReservedCents  int64 `json:"reserved_cents"`
	IssuedCents    int64 `json:"issued_cents"`
}

type InvoiceApplicationLineInput struct {
	TopUpID        int   `json:"topup_id"`
	AllocatedCents int64 `json:"allocated_cents"`
}

type InvoiceApplicationInput struct {
	PurchaserName string                        `json:"purchaser_name"`
	TaxNumber     string                        `json:"tax_number"`
	ReceiverEmail string                        `json:"receiver_email"`
	BankName      string                        `json:"bank_name"`
	BankAccount   string                        `json:"bank_account"`
	Address       string                        `json:"address"`
	Phone         string                        `json:"phone"`
	Remark        string                        `json:"remark"`
	Lines         []InvoiceApplicationLineInput `json:"lines"`
}

type InvoiceIssueInput struct {
	InvoiceNumber string
	InvoiceCode   string
	InvoiceDate   int64
	AmountCents   int64
	FileName      string
	ContentType   string
	SHA256        string
	Data          []byte
}

func invoiceStatusReserves(status string) bool {
	return status == InvoiceStatusPendingReview || status == InvoiceStatusApproved
}

func invoiceStatusIsIssued(status string) bool {
	return status == InvoiceStatusIssued || status == InvoiceStatusVoidPending
}

func invoiceCentsFromDecimal(raw string) (int64, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || !amount.IsPositive() {
		return 0, ErrInvoiceAmountInvalid
	}
	cents := amount.Mul(decimal.NewFromInt(100))
	if !cents.Equal(cents.Truncate(0)) || !cents.IsPositive() || !cents.BigInt().IsInt64() {
		return 0, ErrInvoiceAmountInvalid
	}
	return cents.IntPart(), nil
}

func invoicePaymentCents(topUp *TopUp) (TopupPaymentSnapshot, int64, bool) {
	if topUp == nil || topUp.Status != common.TopUpStatusSuccess || strings.TrimSpace(topUp.PaymentSnapshot) == "" {
		return TopupPaymentSnapshot{}, 0, false
	}
	var snapshot TopupPaymentSnapshot
	if err := common.UnmarshalJsonStr(topUp.PaymentSnapshot, &snapshot); err != nil {
		return TopupPaymentSnapshot{}, 0, false
	}
	if strings.ToUpper(strings.TrimSpace(snapshot.CurrencyCode)) != "CNY" || strings.TrimSpace(snapshot.PaymentReference) == "" {
		return TopupPaymentSnapshot{}, 0, false
	}
	cents, err := invoiceCentsFromDecimal(snapshot.ActualMoney)
	if err != nil {
		return TopupPaymentSnapshot{}, 0, false
	}
	snapshot.CurrencyCode = "CNY"
	return snapshot, cents, true
}

type invoiceTopUpCapacity struct {
	TopUpID   int
	PaidCents int64
	PaidQuota int64
	PaidAt    int64
}

func invoiceProratedCents(paidCents, consumedQuota, paidQuota int64) (int64, bool) {
	if paidCents <= 0 || consumedQuota < 0 || paidQuota <= 0 || consumedQuota > paidQuota {
		return 0, false
	}
	value := decimal.NewFromInt(paidCents).
		Mul(decimal.NewFromInt(consumedQuota)).
		Div(decimal.NewFromInt(paidQuota)).
		Floor()
	if !value.BigInt().IsInt64() {
		return 0, false
	}
	result := value.IntPart()
	return result, result >= 0 && result <= paidCents
}

// invoiceEligibleCentsByTopUp returns the paid amount that has actually been
// consumed. Durable wallets use exact funding-lot consumption; legacy wallets
// reconstruct the paid remainder from verified top-ups and the current wallet,
// following the platform's non-paid-before-paid consumption order.
func invoiceEligibleCentsByTopUp(db *gorm.DB, userID int, topUps []TopUp, lockUser bool) (map[int]int64, error) {
	var user User
	query := db.Select("id, quota, used_quota, billing_mode")
	if lockUser {
		query = lockForUpdate(query)
	}
	if err := query.First(&user, userID).Error; err != nil {
		return nil, err
	}
	if user.BillingMode != AgencyDurableBillingMode {
		var allTopUps []TopUp
		if err := db.Where("user_id = ? AND status = ?", userID, common.TopUpStatusSuccess).
			Order("id asc").
			Find(&allTopUps).Error; err != nil {
			return nil, err
		}
		topUps = allTopUps
	}

	capacities := make([]invoiceTopUpCapacity, 0, len(topUps))
	for i := range topUps {
		_, paidCents, paymentOK := invoicePaymentCents(&topUps[i])
		paidQuota, quotaOK := topUps[i].settledQuota()
		if !paymentOK || !quotaOK {
			continue
		}
		capacities = append(capacities, invoiceTopUpCapacity{
			TopUpID:   topUps[i].Id,
			PaidCents: paidCents,
			PaidQuota: paidQuota,
			PaidAt:    topUps[i].CompleteTime,
		})
	}
	eligible := make(map[int]int64, len(capacities))
	if len(capacities) == 0 {
		return eligible, nil
	}

	if user.BillingMode == AgencyDurableBillingMode {
		sourceIDs := make([]string, 0, len(capacities))
		topUpByID := make(map[int]TopUp, len(topUps))
		for i := range topUps {
			topUpByID[topUps[i].Id] = topUps[i]
		}
		for _, capacity := range capacities {
			sourceIDs = append(sourceIDs, topUpByID[capacity.TopUpID].TradeNo)
		}
		var totals []struct {
			SourceID     string
			PaidInitial  int64
			PaidConsumed int64
		}
		if err := db.Model(&AgencyFundingLot{}).
			Select("source_id, COALESCE(SUM(paid_initial), 0) AS paid_initial, COALESCE(SUM(paid_consumed), 0) AS paid_consumed").
			Where("user_id = ? AND source_id IN ? AND source_kind IN ?", userID, sourceIDs, []string{"payment_self", "payment_assisted"}).
			Group("source_id").
			Scan(&totals).Error; err != nil {
			return nil, err
		}
		bySource := make(map[string]struct{ initial, consumed int64 }, len(totals))
		for _, total := range totals {
			if total.PaidInitial <= 0 || total.PaidConsumed < 0 || total.PaidConsumed > total.PaidInitial {
				return nil, ErrInvoiceSourceUnavailable
			}
			bySource[total.SourceID] = struct{ initial, consumed int64 }{total.PaidInitial, total.PaidConsumed}
		}
		for _, capacity := range capacities {
			total, ok := bySource[topUpByID[capacity.TopUpID].TradeNo]
			if !ok {
				continue
			}
			cents, ok := invoiceProratedCents(capacity.PaidCents, total.consumed, total.initial)
			if !ok {
				return nil, ErrInvoiceAmountInvalid
			}
			eligible[capacity.TopUpID] = cents
		}
		return eligible, nil
	}

	slices.SortFunc(capacities, func(left, right invoiceTopUpCapacity) int {
		if left.PaidAt != right.PaidAt {
			if left.PaidAt < right.PaidAt {
				return -1
			}
			return 1
		}
		return left.TopUpID - right.TopUpID
	})
	totalPaidQuota := int64(0)
	for _, capacity := range capacities {
		if capacity.PaidQuota > math.MaxInt64-totalPaidQuota {
			return nil, ErrInvoiceAmountInvalid
		}
		totalPaidQuota += capacity.PaidQuota
	}
	remainingPaidQuota := min(max(int64(user.Quota), 0), totalPaidQuota)
	// A balance decrease alone is not proof of usage (manual deductions and
	// refunds also lower quota). Bound legacy reconstruction by settled usage.
	consumedPaidQuota := min(totalPaidQuota-remainingPaidQuota, max(int64(user.UsedQuota), 0))
	for _, capacity := range capacities {
		consumed := min(capacity.PaidQuota, consumedPaidQuota)
		cents, ok := invoiceProratedCents(capacity.PaidCents, consumed, capacity.PaidQuota)
		if !ok {
			return nil, ErrInvoiceAmountInvalid
		}
		eligible[capacity.TopUpID] = cents
		consumedPaidQuota -= consumed
	}
	return eligible, nil
}

func invoiceApplicationNo() string {
	return "INV-" + strings.ToUpper(common.GetUUID()[:16])
}

func appendInvoiceEvent(tx *gorm.DB, applicationID, actorID, actorRole int, action, fromStatus, toStatus, reason string) error {
	return tx.Create(&InvoiceApplicationEvent{
		ApplicationID: applicationID,
		ActorID:       actorID,
		ActorRole:     actorRole,
		Action:        action,
		FromStatus:    fromStatus,
		ToStatus:      toStatus,
		Reason:        strings.TrimSpace(reason),
		CreatedAt:     common.GetTimestamp(),
	}).Error
}

func adjustInvoiceTopUpCounter(tx *gorm.DB, topUpID int, field string, amount int64, increase bool) error {
	if amount <= 0 {
		return ErrInvoiceAmountInvalid
	}
	column := "invoice_reserved_cents"
	if field == "issued" {
		column = "invoice_issued_cents"
	}
	operator := "+"
	if !increase {
		operator = "-"
	}
	query := tx.Model(&TopUp{}).Where("id = ?", topUpID)
	if !increase {
		query = query.Where(column+" >= ?", amount)
	}
	result := query.Update(column, gorm.Expr(column+" "+operator+" ?", amount))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInvoiceSourceExhausted
	}
	return nil
}

func moveInvoiceReservedToIssued(tx *gorm.DB, line InvoiceApplicationLine) error {
	result := tx.Model(&TopUp{}).
		Where("id = ? AND invoice_reserved_cents >= ?", line.TopUpID, line.AllocatedCents).
		Updates(map[string]any{
			"invoice_reserved_cents": gorm.Expr("invoice_reserved_cents - ?", line.AllocatedCents),
			"invoice_issued_cents":   gorm.Expr("invoice_issued_cents + ?", line.AllocatedCents),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInvoiceSourceExhausted
	}
	return nil
}

func CreateInvoiceApplication(userID int, input InvoiceApplicationInput) (*InvoiceApplication, error) {
	if userID <= 0 || strings.TrimSpace(input.PurchaserName) == "" || len(input.PurchaserName) > 255 || strings.TrimSpace(input.TaxNumber) == "" || len(input.TaxNumber) > 64 || strings.TrimSpace(input.ReceiverEmail) == "" || len(input.ReceiverEmail) > 254 || len(input.BankName) > 255 || len(input.BankAccount) > 128 || len(input.Address) > 500 || len(input.Phone) > 64 || len(input.Remark) > 1000 || len(input.Lines) == 0 || len(input.Lines) > 100 {
		return nil, ErrInvoiceAmountInvalid
	}
	seen := make(map[int]struct{}, len(input.Lines))
	for _, line := range input.Lines {
		if line.TopUpID <= 0 || line.AllocatedCents <= 0 {
			return nil, ErrInvoiceAmountInvalid
		}
		if _, ok := seen[line.TopUpID]; ok {
			return nil, ErrInvoiceAmountInvalid
		}
		seen[line.TopUpID] = struct{}{}
	}

	var application *InvoiceApplication
	err := DB.Transaction(func(tx *gorm.DB) error {
		ids := make([]int, 0, len(input.Lines))
		for _, line := range input.Lines {
			ids = append(ids, line.TopUpID)
		}
		slices.Sort(ids)
		var topUps []TopUp
		if err := lockForUpdate(tx).Where("id IN ? AND user_id = ?", ids, userID).Order("id asc").Find(&topUps).Error; err != nil {
			return err
		}
		if len(topUps) != len(ids) {
			return ErrInvoiceSourceUnavailable
		}
		eligibleByTopUp, err := invoiceEligibleCentsByTopUp(tx, userID, topUps, true)
		if err != nil {
			return err
		}
		topUpByID := make(map[int]TopUp, len(topUps))
		for _, topUp := range topUps {
			topUpByID[topUp.Id] = topUp
		}

		total := int64(0)
		lines := make([]InvoiceApplicationLine, 0, len(input.Lines))
		for _, inputLine := range input.Lines {
			topUp, ok := topUpByID[inputLine.TopUpID]
			if !ok {
				return ErrInvoiceSourceUnavailable
			}
			snapshot, paidCents, ok := invoicePaymentCents(&topUp)
			if !ok {
				return ErrInvoiceSourceUnavailable
			}
			eligibleCents := eligibleByTopUp[topUp.Id]
			availableCents, validCounters := invoiceAvailableCents(eligibleCents, topUp.InvoiceReservedCents, topUp.InvoiceIssuedCents)
			if !validCounters {
				return ErrInvoiceSourceUnavailable
			}
			if inputLine.AllocatedCents > availableCents || total > math.MaxInt64-inputLine.AllocatedCents {
				return ErrInvoiceSourceExhausted
			}
			// Keep the balance guard in the write as well as the locked read.
			result := tx.Model(&TopUp{}).
				Where("id = ? AND user_id = ? AND status = ?", topUp.Id, userID, common.TopUpStatusSuccess).
				Where("invoice_reserved_cents >= 0 AND invoice_issued_cents >= 0").
				Where("invoice_reserved_cents <= ? AND invoice_issued_cents <= ? - invoice_reserved_cents", eligibleCents-inputLine.AllocatedCents, eligibleCents-inputLine.AllocatedCents).
				Update("invoice_reserved_cents", gorm.Expr("invoice_reserved_cents + ?", inputLine.AllocatedCents))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrInvoiceSourceExhausted
			}
			total += inputLine.AllocatedCents
			lines = append(lines, InvoiceApplicationLine{
				TopUpID:          topUp.Id,
				SourceTradeNo:    topUp.TradeNo,
				PaidCents:        paidCents,
				AllocatedCents:   inputLine.AllocatedCents,
				CurrencyCode:     "CNY",
				PaymentReference: snapshot.PaymentReference,
				CreatedAt:        common.GetTimestamp(),
			})
		}

		now := common.GetTimestamp()
		application = &InvoiceApplication{
			ApplicationNo: invoiceApplicationNo(),
			UserID:        userID,
			Status:        InvoiceStatusPendingReview,
			CurrencyCode:  "CNY",
			TotalCents:    total,
			PurchaserName: strings.TrimSpace(input.PurchaserName),
			TaxNumber:     strings.TrimSpace(input.TaxNumber),
			ReceiverEmail: strings.TrimSpace(input.ReceiverEmail),
			BankName:      strings.TrimSpace(input.BankName),
			BankAccount:   strings.TrimSpace(input.BankAccount),
			Address:       strings.TrimSpace(input.Address),
			Phone:         strings.TrimSpace(input.Phone),
			Remark:        strings.TrimSpace(input.Remark),
			CreatedAt:     now,
			UpdatedAt:     now,
			EmailStatus:   "pending",
		}
		if err := tx.Create(application).Error; err != nil {
			return err
		}
		for i := range lines {
			lines[i].ApplicationID = application.ID
			if err := tx.Create(&lines[i]).Error; err != nil {
				return err
			}
		}
		application.Lines = lines
		return appendInvoiceEvent(tx, application.ID, userID, common.RoleCommonUser, "submit", "", application.Status, "")
	})
	if err != nil {
		return nil, err
	}
	return application, nil
}

func GetInvoiceSources(userID int) ([]InvoiceSource, error) {
	var topUps []TopUp
	if err := DB.Where("user_id = ? AND status = ?", userID, common.TopUpStatusSuccess).Order("id desc").Find(&topUps).Error; err != nil {
		return nil, err
	}
	eligibleByTopUp, err := invoiceEligibleCentsByTopUp(DB, userID, topUps, false)
	if err != nil {
		return nil, err
	}
	sources := make([]InvoiceSource, 0, len(topUps))
	for i := range topUps {
		_, paidCents, ok := invoicePaymentCents(&topUps[i])
		if !ok {
			continue
		}
		available, validCounters := invoiceAvailableCents(eligibleByTopUp[topUps[i].Id], topUps[i].InvoiceReservedCents, topUps[i].InvoiceIssuedCents)
		if !validCounters {
			continue
		}
		if available <= 0 {
			continue
		}
		sources = append(sources, InvoiceSource{TopUpID: topUps[i].Id, TradeNo: topUps[i].TradeNo, PaidCents: paidCents, AvailableCents: available, CurrencyCode: "CNY", PaidAt: topUps[i].CompleteTime})
	}
	return sources, nil
}

func GetInvoiceSummary(userID int) (*InvoiceSummary, error) {
	var topUps []TopUp
	if err := DB.Where("user_id = ? AND status = ?", userID, common.TopUpStatusSuccess).Find(&topUps).Error; err != nil {
		return nil, err
	}
	eligibleByTopUp, err := invoiceEligibleCentsByTopUp(DB, userID, topUps, false)
	if err != nil {
		return nil, err
	}
	summary := &InvoiceSummary{}
	for i := range topUps {
		_, _, valid := invoicePaymentCents(&topUps[i])
		if !valid {
			continue
		}
		available, validCounters := invoiceAvailableCents(eligibleByTopUp[topUps[i].Id], topUps[i].InvoiceReservedCents, topUps[i].InvoiceIssuedCents)
		var ok bool
		if summary.ReservedCents, ok = addInvoiceCents(summary.ReservedCents, topUps[i].InvoiceReservedCents); !ok {
			return nil, ErrInvoiceAmountInvalid
		}
		if summary.IssuedCents, ok = addInvoiceCents(summary.IssuedCents, topUps[i].InvoiceIssuedCents); !ok {
			return nil, ErrInvoiceAmountInvalid
		}
		if !validCounters {
			continue
		}
		if summary.AvailableCents, ok = addInvoiceCents(summary.AvailableCents, available); !ok {
			return nil, ErrInvoiceAmountInvalid
		}
	}
	return summary, nil
}

func maxInvoiceAvailable(paidCents, reservedCents, issuedCents int64) int64 {
	available, valid := invoiceAvailableCents(paidCents, reservedCents, issuedCents)
	if !valid {
		return 0
	}
	return available
}

func invoiceAvailableCents(eligibleCents, reservedCents, issuedCents int64) (int64, bool) {
	if eligibleCents < 0 || reservedCents < 0 || issuedCents < 0 || reservedCents > eligibleCents || issuedCents > eligibleCents-reservedCents {
		return 0, false
	}
	return eligibleCents - reservedCents - issuedCents, true
}

func addInvoiceCents(total, amount int64) (int64, bool) {
	if total < 0 || amount < 0 || total > math.MaxInt64-amount {
		return 0, false
	}
	return total + amount, true
}

func ListInvoiceApplications(userID int, pageInfo *common.PageInfo, status string) ([]InvoiceApplication, int64, error) {
	query := DB.Model(&InvoiceApplication{}).Where("user_id = ?", userID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var applications []InvoiceApplication
	if err := query.Preload("Lines").Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&applications).Error; err != nil {
		return nil, 0, err
	}
	return applications, total, nil
}

func ListAllInvoiceApplications(pageInfo *common.PageInfo, status string) ([]InvoiceApplication, int64, error) {
	query := DB.Model(&InvoiceApplication{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var applications []InvoiceApplication
	if err := query.Preload("Lines").Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&applications).Error; err != nil {
		return nil, 0, err
	}
	return applications, total, nil
}

func GetInvoiceApplication(id int, userID int, admin bool) (*InvoiceApplication, error) {
	query := DB.Preload("Lines").Where("id = ?", id)
	if !admin {
		query = query.Where("user_id = ?", userID)
	}
	var application InvoiceApplication
	if err := query.First(&application).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvoiceApplicationNotFound
		}
		return nil, err
	}
	return &application, nil
}

func GetInvoiceApplicationEvents(id, userID int, admin bool) ([]InvoiceApplicationEvent, error) {
	query := DB.Model(&InvoiceApplication{}).Where("id = ?", id)
	if !admin {
		query = query.Where("user_id = ?", userID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrInvoiceApplicationNotFound
	}
	var events []InvoiceApplicationEvent
	if err := DB.Where("application_id = ?", id).Order("id asc").Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

func RecordInvoiceDocumentDownload(applicationID, actorID, actorRole int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var application InvoiceApplication
		if err := lockForUpdate(tx).Where("id = ?", applicationID).First(&application).Error; err != nil {
			return ErrInvoiceApplicationNotFound
		}
		if application.Status != InvoiceStatusIssued && application.Status != InvoiceStatusVoidPending && application.Status != InvoiceStatusVoided {
			return ErrInvoiceInvalidStatus
		}
		return appendInvoiceEvent(tx, applicationID, actorID, actorRole, "document_download", application.Status, application.Status, "")
	})
}

func WithdrawInvoiceApplication(userID, applicationID int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var application InvoiceApplication
		if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", applicationID, userID).First(&application).Error; err != nil {
			return ErrInvoiceApplicationNotFound
		}
		if application.Status != InvoiceStatusPendingReview {
			return ErrInvoiceInvalidStatus
		}
		var lines []InvoiceApplicationLine
		if err := lockForUpdate(tx).Where("application_id = ?", application.ID).Find(&lines).Error; err != nil {
			return err
		}
		for _, line := range lines {
			if err := adjustInvoiceTopUpCounter(tx, line.TopUpID, "reserved", line.AllocatedCents, false); err != nil {
				return err
			}
		}
		from := application.Status
		application.Status = InvoiceStatusWithdrawn
		application.UpdatedAt = common.GetTimestamp()
		if err := tx.Save(&application).Error; err != nil {
			return err
		}
		return appendInvoiceEvent(tx, application.ID, userID, common.RoleCommonUser, "withdraw", from, application.Status, "")
	})
}

func ReviewInvoiceApplication(adminID, adminRole, applicationID int, approved bool, reason string) error {
	if len(reason) > 1000 {
		return ErrInvoiceAmountInvalid
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var application InvoiceApplication
		if err := lockForUpdate(tx).Where("id = ?", applicationID).First(&application).Error; err != nil {
			return ErrInvoiceApplicationNotFound
		}
		if application.Status != InvoiceStatusPendingReview {
			return ErrInvoiceInvalidStatus
		}
		if !approved && strings.TrimSpace(reason) == "" {
			return ErrInvoiceReviewReasonMissing
		}
		from := application.Status
		application.Status = InvoiceStatusApproved
		if !approved {
			application.Status = InvoiceStatusRejected
			var lines []InvoiceApplicationLine
			if err := lockForUpdate(tx).Where("application_id = ?", application.ID).Find(&lines).Error; err != nil {
				return err
			}
			for _, line := range lines {
				if err := adjustInvoiceTopUpCounter(tx, line.TopUpID, "reserved", line.AllocatedCents, false); err != nil {
					return err
				}
			}
		}
		application.ReviewReason = strings.TrimSpace(reason)
		application.ReviewerID = adminID
		application.ReviewedAt = common.GetTimestamp()
		application.UpdatedAt = common.GetTimestamp()
		if err := tx.Save(&application).Error; err != nil {
			return err
		}
		action := "review_approve"
		if !approved {
			action = "review_reject"
		}
		return appendInvoiceEvent(tx, application.ID, adminID, adminRole, action, from, application.Status, reason)
	})
}

func IssueInvoiceApplication(adminID, adminRole, applicationID int, input InvoiceIssueInput) error {
	invoiceNumber := strings.TrimSpace(input.InvoiceNumber)
	invoiceCode := strings.TrimSpace(input.InvoiceCode)
	if invoiceNumber == "" || input.InvoiceDate <= 0 || input.InvoiceDate > common.GetTimestamp() || input.AmountCents <= 0 || len(invoiceNumber) > 128 || len(invoiceCode) > 128 {
		return ErrInvoiceAmountInvalid
	}
	if len(input.Data) == 0 {
		return ErrInvoiceDocumentRequired
	}
	if int64(len(input.Data)) > maxInvoiceDocumentBytes {
		return ErrInvoiceDocumentTooLarge
	}
	if len(input.Data) < 5 || string(input.Data[:5]) != "%PDF-" {
		return ErrInvoiceDocumentInvalid
	}
	fileName := strings.TrimSpace(input.FileName)
	if fileName == "" || len(fileName) > 255 || strings.ContainsAny(fileName, "/\\\r\n") {
		return ErrInvoiceDocumentInvalid
	}
	digest := sha256.Sum256(input.Data)
	sha256Hex := hex.EncodeToString(digest[:])
	numberDigest := sha256.Sum256([]byte(strings.ToUpper(invoiceNumber)))
	numberKey := hex.EncodeToString(numberDigest[:])
	return DB.Transaction(func(tx *gorm.DB) error {
		var application InvoiceApplication
		if err := lockForUpdate(tx).Where("id = ?", applicationID).First(&application).Error; err != nil {
			return ErrInvoiceApplicationNotFound
		}
		if application.Status != InvoiceStatusApproved {
			return ErrInvoiceInvalidStatus
		}
		if input.AmountCents != application.TotalCents {
			return ErrInvoiceIssueAmountMismatch
		}
		var duplicateCount int64
		if err := tx.Model(&InvoiceApplication{}).Where("invoice_number_key = ? AND id <> ?", numberKey, application.ID).Count(&duplicateCount).Error; err != nil {
			return err
		}
		if duplicateCount > 0 {
			return ErrInvoiceDuplicateNumber
		}
		var lines []InvoiceApplicationLine
		if err := lockForUpdate(tx).Where("application_id = ?", application.ID).Find(&lines).Error; err != nil {
			return err
		}
		for _, line := range lines {
			if err := moveInvoiceReservedToIssued(tx, line); err != nil {
				return err
			}
		}
		if err := tx.Create(&InvoiceApplicationDocument{
			ApplicationID: application.ID,
			FileName:      fileName,
			ContentType:   "application/pdf",
			Size:          int64(len(input.Data)),
			SHA256:        sha256Hex,
			Data:          input.Data,
			CreatedAt:     common.GetTimestamp(),
		}).Error; err != nil {
			return err
		}
		from := application.Status
		application.Status = InvoiceStatusIssued
		application.InvoiceNumber = invoiceNumber
		application.InvoiceNumberKey = &numberKey
		application.InvoiceCode = invoiceCode
		application.InvoiceDate = input.InvoiceDate
		application.IssuerID = adminID
		application.IssuedAt = common.GetTimestamp()
		application.EmailStatus = "pending"
		application.UpdatedAt = common.GetTimestamp()
		if err := tx.Save(&application).Error; err != nil {
			return err
		}
		return appendInvoiceEvent(tx, application.ID, adminID, adminRole, "issue", from, application.Status, "")
	})
}

func RequestInvoiceVoid(userID, applicationID int, reason string) error {
	if strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return ErrInvoiceReviewReasonMissing
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var application InvoiceApplication
		if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", applicationID, userID).First(&application).Error; err != nil {
			return ErrInvoiceApplicationNotFound
		}
		if application.Status != InvoiceStatusIssued {
			return ErrInvoiceInvalidStatus
		}
		from := application.Status
		application.Status = InvoiceStatusVoidPending
		application.VoidReason = strings.TrimSpace(reason)
		application.UpdatedAt = common.GetTimestamp()
		if err := tx.Save(&application).Error; err != nil {
			return err
		}
		return appendInvoiceEvent(tx, application.ID, userID, common.RoleCommonUser, "void_request", from, application.Status, reason)
	})
}

func ReviewInvoiceVoid(adminID, adminRole, applicationID int, approved bool, reason string) error {
	if strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return ErrInvoiceReviewReasonMissing
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var application InvoiceApplication
		if err := lockForUpdate(tx).Where("id = ?", applicationID).First(&application).Error; err != nil {
			return ErrInvoiceApplicationNotFound
		}
		if application.Status != InvoiceStatusVoidPending {
			return ErrInvoiceInvalidStatus
		}
		from := application.Status
		to := InvoiceStatusIssued
		if approved {
			to = InvoiceStatusVoided
			var lines []InvoiceApplicationLine
			if err := lockForUpdate(tx).Where("application_id = ?", application.ID).Find(&lines).Error; err != nil {
				return err
			}
			for _, line := range lines {
				if err := adjustInvoiceTopUpCounter(tx, line.TopUpID, "issued", line.AllocatedCents, false); err != nil {
					return err
				}
			}
		}
		application.Status = to
		application.ReviewReason = strings.TrimSpace(reason)
		application.UpdatedAt = common.GetTimestamp()
		if err := tx.Save(&application).Error; err != nil {
			return err
		}
		action := "void_reject"
		if approved {
			action = "void_confirm"
		}
		return appendInvoiceEvent(tx, application.ID, adminID, adminRole, action, from, to, reason)
	})
}

func GetInvoiceDocument(applicationID int) (*InvoiceApplication, *InvoiceApplicationDocument, error) {
	var application InvoiceApplication
	if err := DB.Where("id = ?", applicationID).First(&application).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrInvoiceApplicationNotFound
		}
		return nil, nil, err
	}
	if application.Status != InvoiceStatusIssued && application.Status != InvoiceStatusVoidPending && application.Status != InvoiceStatusVoided {
		return nil, nil, ErrInvoiceInvalidStatus
	}
	var document InvoiceApplicationDocument
	if err := DB.Where("application_id = ?", applicationID).First(&document).Error; err != nil {
		return nil, nil, err
	}
	return &application, &document, nil
}

func FormatInvoiceCents(cents int64) string {
	return decimal.NewFromInt(cents).Div(decimal.NewFromInt(100)).StringFixed(2)
}

func UpdateInvoiceEmailStatus(applicationID int, status, lastError string) error {
	if status != "pending" && status != "sent" && status != "failed" {
		return ErrInvoiceInvalidStatus
	}
	if len(lastError) > 500 {
		lastError = lastError[:500]
	}
	updates := map[string]any{
		"email_status":     strings.TrimSpace(status),
		"email_last_error": strings.TrimSpace(lastError),
		"updated_at":       common.GetTimestamp(),
	}
	if status == "sent" {
		updates["email_sent_at"] = common.GetTimestamp()
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var application InvoiceApplication
		if err := lockForUpdate(tx).Where("id = ?", applicationID).First(&application).Error; err != nil {
			return ErrInvoiceApplicationNotFound
		}
		if application.Status != InvoiceStatusIssued && application.Status != InvoiceStatusVoidPending {
			return ErrInvoiceInvalidStatus
		}
		return tx.Model(&application).Updates(updates).Error
	})
}
