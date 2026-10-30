package agencyhub

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const shanghaiCreditLocation = "Asia/Shanghai"

// creditFailedEmailRetryDelay throttles repeated SMTP attempts for one
// statement so a broken mailbox or SMTP outage cannot turn into a mail flood.
const creditFailedEmailRetryDelay = 15 * time.Minute

// CreditBookRoutes mounts the platform credit book under a group that is already
// authenticated as the super administrator. The gateway uses it to serve the
// book straight from the main site admin console, where the platform root
// session is the proof. The Agency Center has no management routes for this
// platform-wide capability.
func (a *App) CreditBookRoutes(book *gin.RouterGroup) {
	book.GET("/terms", a.listCreditTerms)
	book.GET("/resolve-user", a.resolveCreditUser)
	book.GET("/users/:user_id", a.getCreditTerms)
	book.PUT("/users/:user_id", a.putCreditTerms)
	book.GET("/statements", a.listCreditInvoices)
	book.POST("/statements/:id/mark-paid", a.markCreditInvoicePaid)
	book.POST("/statements/:id/mark-bad-debt", a.markCreditInvoiceBadDebt)
	book.POST("/statements/:id/resend-email", a.resendCreditInvoiceEmail)
}

// PlatformRootIdentity describes the super administrator of the main site when
// the gateway serves the credit book there. Both the audit trail and the
// updated_by column record this actor, and without it every mutation would fail
// closed because the audit helper refuses an anonymous writer.
func PlatformRootIdentity(actorID int64) *Identity {
	return &Identity{ActorType: ActorTypeRoot, ActorID: actorID}
}

func creditLocation() *time.Location {
	loc, err := time.LoadLocation(shanghaiCreditLocation)
	if err != nil {
		return time.FixedZone(shanghaiCreditLocation, 8*60*60)
	}
	return loc
}

func parseCreditUserID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || id <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的用户ID", nil)
		return 0, false
	}
	return id, true
}

func parseCreditInvoiceID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的账单ID", nil)
		return 0, false
	}
	return id, true
}

// loadCreditInvoice reads the statement a root operator is about to mutate.
func (a *App) loadCreditInvoice(c *gin.Context, id int64) (*model.AgencyCreditInvoice, bool) {
	var invoice model.AgencyCreditInvoice
	if err := a.db.Where("id = ?", id).First(&invoice).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusNotFound, "not_found", "账单不存在", nil)
			return nil, false
		}
		respondError(c, http.StatusInternalServerError, "database_error", "读取账单失败", nil)
		return nil, false
	}
	return &invoice, true
}

type creditAccountItem struct {
	UserID            string `json:"user_id"`
	Username          string `json:"username"`
	AccountName       string `json:"account_name"`
	Configured        bool   `json:"configured"`
	Status            string `json:"status"`
	CreditLimit       int64  `json:"credit_limit"`
	UsedQuota         int64  `json:"used_quota"`
	AvailableQuota    int64  `json:"available_quota"`
	PaymentTermDays   int    `json:"payment_term_days"`
	RepaymentDay      int    `json:"repayment_day"`
	EmailEnabled      bool   `json:"email_enabled"`
	NextStatementAtMS int64  `json:"next_statement_at_ms"`
	UpdatedAtMS       int64  `json:"updated_at_ms"`
}

func (a *App) listCreditTerms(c *gin.Context) {
	// Platform-wide by design: the super administrator owns the credit book,
	// so an acting-agency session does not narrow it.
	query := a.db.WithContext(c.Request.Context()).Model(&model.AgencyCreditTerms{})
	if keyword := strings.TrimSpace(c.Query("query")); keyword != "" {
		users := a.db.Model(&model.User{}).Select("id")
		like := "%" + strings.ToLower(keyword) + "%"
		if userID, err := strconv.ParseInt(keyword, 10, 64); err == nil && userID > 0 {
			users = users.Where("id = ? OR LOWER(username) LIKE ? OR LOWER(display_name) LIKE ?", userID, like, like)
		} else {
			users = users.Where("LOWER(username) LIKE ? OR LOWER(display_name) LIKE ?", like, like)
		}
		query = query.Where("user_id IN (?)", users)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取授信账户数量失败", nil)
		return
	}
	// Legacy Agency Hub callers still receive their unpaged (bounded) list.
	page, pageSize := 1, 500
	if c.Query("p") != "" || c.Query("page_size") != "" {
		pageInfo := common.GetPageQuery(c)
		page, pageSize = max(1, min(pageInfo.Page, 1000000)), max(1, pageInfo.PageSize)
	}
	var terms []model.AgencyCreditTerms
	if err := query.Order("user_id ASC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&terms).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取授信条款失败", nil)
		return
	}
	ids := make([]int64, 0, len(terms))
	for _, term := range terms {
		ids = append(ids, term.UserID)
	}
	overviews, err := model.GetAgencyCreditOverviews(ids)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取授信余额失败", nil)
		return
	}
	users := make(map[int64]model.User, len(ids))
	if len(ids) > 0 {
		var rows []model.User
		if err := a.db.Select("id, username, display_name").Where("id IN ?", ids).Find(&rows).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", "读取客户信息失败", nil)
			return
		}
		for _, user := range rows {
			users[int64(user.Id)] = user
		}
	}
	items := make([]creditAccountItem, 0, len(terms))
	for _, term := range terms {
		overview := overviews[term.UserID]
		user := users[term.UserID]
		items = append(items, creditAccountItem{
			UserID:            strconv.FormatInt(term.UserID, 10),
			Username:          user.Username,
			AccountName:       userAccountName(user),
			Configured:        true,
			Status:            term.Status,
			CreditLimit:       term.CreditLimit,
			UsedQuota:         overview.UsedQuota,
			AvailableQuota:    overview.AvailableQuota,
			PaymentTermDays:   term.PaymentTermDays,
			RepaymentDay:      model.AgencyCreditRepaymentDay,
			EmailEnabled:      term.EmailEnabled,
			NextStatementAtMS: overview.NextStatementAtMS,
			UpdatedAtMS:       term.UpdatedAtMS,
		})
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

func (a *App) resolveCreditUser(c *gin.Context) {
	// Any platform user can receive credit; the lookup is not agency-bound.
	raw := strings.TrimSpace(c.Query("query"))
	if raw == "" {
		respondError(c, http.StatusBadRequest, "invalid_query", "请输入客户账号或用户 ID", nil)
		return
	}
	var user model.User
	query := a.db.Select("id, username, display_name")
	if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
		query = query.Where("id = ?", id)
	} else {
		query = query.Where("username = ? OR email = ?", raw, raw)
	}
	if err := query.First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusNotFound, "user_not_found", "未找到该客户", nil)
			return
		}
		respondError(c, http.StatusInternalServerError, "database_error", "查询客户失败", nil)
		return
	}
	overview, err := model.GetAgencyCreditOverview(int64(user.Id))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取授信余额失败", nil)
		return
	}
	// The main-site book opens this result directly after a search. Return the
	// persisted contract fields too, rather than making the browser infer them
	// from the balance overview and accidentally reset existing terms on save.
	if !overview.Configured {
		respondOK(c, gin.H{
			"user_id":              strconv.FormatInt(int64(user.Id), 10),
			"username":             user.Username,
			"account_name":         userAccountName(user),
			"configured":           false,
			"status":               model.AgencyCreditStatusSuspended,
			"credit_limit":         0,
			"used_quota":           overview.UsedQuota,
			"available_quota":      0,
			"payment_term_days":    0,
			"repayment_day":        model.AgencyCreditRepaymentDay,
			"email_enabled":        false,
			"next_statement_at_ms": overview.NextStatementAtMS,
		})
		return
	}
	var terms model.AgencyCreditTerms
	err = a.db.Where("user_id = ?", user.Id).First(&terms).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusInternalServerError, "database_error", "读取授信条款失败", nil)
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusConflict, "credit_terms_inconsistent", "授信条款状态异常，请稍后重试", nil)
		return
	}
	respondOK(c, gin.H{
		"user_id":              strconv.FormatInt(int64(user.Id), 10),
		"username":             user.Username,
		"account_name":         userAccountName(user),
		"configured":           true,
		"status":               terms.Status,
		"credit_limit":         terms.CreditLimit,
		"used_quota":           overview.UsedQuota,
		"available_quota":      overview.AvailableQuota,
		"payment_term_days":    terms.PaymentTermDays,
		"repayment_day":        model.AgencyCreditRepaymentDay,
		"email_enabled":        terms.EmailEnabled,
		"next_statement_at_ms": overview.NextStatementAtMS,
		"updated_at_ms":        terms.UpdatedAtMS,
	})
}

func (a *App) getCreditTerms(c *gin.Context) {
	userID, ok := parseCreditUserID(c)
	if !ok {
		return
	}
	overview, err := model.GetAgencyCreditOverview(userID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取授信条款失败", nil)
		return
	}
	var terms model.AgencyCreditTerms
	if err := a.db.Where("user_id = ?", userID).First(&terms).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusInternalServerError, "database_error", "读取授信条款失败", nil)
			return
		}
		respondOK(c, gin.H{
			"user_id":           strconv.FormatInt(userID, 10),
			"configured":        false,
			"credit_limit":      0,
			"payment_term_days": 0,
			"repayment_day":     model.AgencyCreditRepaymentDay,
			"status":            model.AgencyCreditStatusSuspended,
			"email_enabled":     false,
			"used_quota":        overview.UsedQuota,
			"available_quota":   0,
		})
		return
	}
	respondOK(c, gin.H{
		"user_id":              strconv.FormatInt(userID, 10),
		"configured":           true,
		"credit_limit":         terms.CreditLimit,
		"payment_term_days":    terms.PaymentTermDays,
		"repayment_day":        model.AgencyCreditRepaymentDay,
		"status":               terms.Status,
		"email_enabled":        terms.EmailEnabled,
		"used_quota":           overview.UsedQuota,
		"available_quota":      overview.AvailableQuota,
		"next_statement_at_ms": overview.NextStatementAtMS,
		"updated_at_ms":        terms.UpdatedAtMS,
	})
}

type creditTermsRequest struct {
	CreditLimit     int64  `json:"credit_limit"`
	PaymentTermDays int    `json:"payment_term_days"`
	Status          string `json:"status"`
	EmailEnabled    bool   `json:"email_enabled"`
}

func (a *App) putCreditTerms(c *gin.Context) {
	userID, ok := parseCreditUserID(c)
	if !ok {
		return
	}
	identity := currentIdentity(c)
	var request creditTermsRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.CreditLimit < 0 || request.PaymentTermDays < 0 || request.PaymentTermDays > 365 {
		respondError(c, http.StatusBadRequest, "invalid_credit_terms", "授信额度或还款期限无效", nil)
		return
	}
	if request.Status == "" {
		request.Status = model.AgencyCreditStatusActive
	}
	if request.Status != model.AgencyCreditStatusActive && request.Status != model.AgencyCreditStatusSuspended {
		respondError(c, http.StatusBadRequest, "invalid_credit_status", "授信状态无效", nil)
		return
	}
	var user model.User
	if err := a.db.Select("id, username").First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusNotFound, "user_not_found", "未找到该客户", nil)
			return
		}
		respondError(c, http.StatusInternalServerError, "database_error", "读取客户失败", nil)
		return
	}
	now := time.Now().UnixMilli()
	actorID := int64(0)
	if identity != nil {
		actorID = identity.ActorID
	}
	var stored model.AgencyCreditTerms
	err := a.db.Transaction(func(tx *gorm.DB) error {
		var existing model.AgencyCreditTerms
		found := true
		if err := model.AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&existing).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			found = false
		}
		before := any(nil)
		if found {
			before = existing
			if err := tx.Model(&model.AgencyCreditTerms{}).Where("user_id = ?", userID).Updates(map[string]any{
				"credit_limit":      request.CreditLimit,
				"payment_term_days": request.PaymentTermDays,
				"repayment_day":     model.AgencyCreditRepaymentDay,
				"status":            request.Status,
				"email_enabled":     request.EmailEnabled,
				"updated_by":        actorID,
				"updated_at_ms":     now,
			}).Error; err != nil {
				return err
			}
			existing.CreditLimit = request.CreditLimit
			existing.PaymentTermDays = request.PaymentTermDays
			existing.RepaymentDay = model.AgencyCreditRepaymentDay
			existing.Status = request.Status
			existing.EmailEnabled = request.EmailEnabled
			existing.UpdatedBy = actorID
			existing.UpdatedAtMS = now
			stored = existing
		} else {
			stored = model.AgencyCreditTerms{
				UserID:          userID,
				CreditLimit:     request.CreditLimit,
				PaymentTermDays: request.PaymentTermDays,
				RepaymentDay:    model.AgencyCreditRepaymentDay,
				Status:          request.Status,
				EmailEnabled:    request.EmailEnabled,
				UpdatedBy:       actorID,
				CreatedAtMS:     now,
				UpdatedAtMS:     now,
			}
			// email_enabled carries a database default of true, so an explicit
			// false has to be written field by field.
			if err := tx.Select("user_id", "credit_limit", "payment_term_days", "repayment_day", "status", "email_enabled", "updated_by", "created_at_ms", "updated_at_ms").Create(&stored).Error; err != nil {
				return err
			}
			if !request.EmailEnabled {
				// GORM drops a zero value for a default-tagged column, so the
				// explicit false has to be written after the insert.
				if err := tx.Model(&model.AgencyCreditTerms{}).Where("user_id = ?", userID).Update("email_enabled", false).Error; err != nil {
					return err
				}
			}
		}
		after := map[string]any{
			"credit_limit":      stored.CreditLimit,
			"payment_term_days": stored.PaymentTermDays,
			"repayment_day":     stored.RepaymentDay,
			"status":            stored.Status,
			"email_enabled":     stored.EmailEnabled,
		}
		return recordAuditTx(tx, c, identity, "credit.terms.upsert", "agency_credit_terms", strconv.FormatInt(userID, 10), "设置客户授信额度与还款期限", before, after)
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "保存授信条款失败", nil)
		return
	}
	respondOK(c, gin.H{
		"user_id":           strconv.FormatInt(userID, 10),
		"username":          user.Username,
		"configured":        true,
		"credit_limit":      stored.CreditLimit,
		"payment_term_days": stored.PaymentTermDays,
		"repayment_day":     model.AgencyCreditRepaymentDay,
		"status":            stored.Status,
		"email_enabled":     stored.EmailEnabled,
		"updated_at_ms":     stored.UpdatedAtMS,
	})
}

type creditInvoiceItem struct {
	model.AgencyCreditInvoice
	UserIDText     string `json:"user_id_text"`
	Username       string `json:"username"`
	AccountName    string `json:"account_name"`
	RemainingQuota int64  `json:"remaining_quota"`
}

func (a *App) listCreditInvoices(c *gin.Context) {
	query := a.db.Model(&model.AgencyCreditInvoice{})
	if userID := strings.TrimSpace(c.Query("user_id")); userID != "" {
		query = query.Where("user_id = ?", userID)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	var invoices []model.AgencyCreditInvoice
	if err := query.Order("due_at_ms DESC, id DESC").Limit(200).Find(&invoices).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取账单失败", nil)
		return
	}
	users := make(map[int64]model.User, len(invoices))
	if len(invoices) > 0 {
		ids := make([]int64, 0, len(invoices))
		for _, invoice := range invoices {
			ids = append(ids, invoice.UserID)
		}
		var rows []model.User
		if err := a.db.Select("id, username, display_name").Where("id IN ?", ids).Find(&rows).Error; err == nil {
			for _, user := range rows {
				users[int64(user.Id)] = user
			}
		}
	}
	items := make([]creditInvoiceItem, 0, len(invoices))
	for _, invoice := range invoices {
		user := users[invoice.UserID]
		items = append(items, creditInvoiceItem{
			AgencyCreditInvoice: invoice,
			UserIDText:          strconv.FormatInt(invoice.UserID, 10),
			Username:            user.Username,
			AccountName:         userAccountName(user),
			RemainingQuota:      invoice.AmountQuota - invoice.PaidQuota - invoice.BadDebtQuota,
		})
	}
	respondOK(c, gin.H{"items": items})
}

func (a *App) markCreditInvoicePaid(c *gin.Context) {
	id, ok := parseCreditInvoiceID(c)
	if !ok {
		return
	}
	identity := currentIdentity(c)
	invoice, ok := a.loadCreditInvoice(c, id)
	if !ok {
		return
	}
	var input struct {
		AmountQuota      int64  `json:"amount_quota"`
		PaymentReference string `json:"payment_reference"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求无效", nil)
		return
	}
	if strings.TrimSpace(input.PaymentReference) == "" {
		respondError(c, http.StatusBadRequest, "invalid_reference", "请填写收款凭证编号", nil)
		return
	}
	// payment_reference is stored in a 191 character column. Reject an
	// overlong value here so a long bank slip number cannot fail in the
	// middle of the settlement transaction.
	if len(strings.TrimSpace(input.PaymentReference)) > 191 {
		respondError(c, http.StatusBadRequest, "invalid_reference", "收款凭证编号过长", nil)
		return
	}
	actorID := int64(0)
	if identity != nil {
		actorID = identity.ActorID
	}
	var updated *model.AgencyCreditInvoice
	err := a.db.Transaction(func(tx *gorm.DB) error {
		result, err := model.MarkAgencyCreditInvoicePaidTx(tx, id, input.AmountQuota, actorID, input.PaymentReference)
		if err != nil {
			return err
		}
		updated = result
		return recordAuditTx(tx, c, identity, "credit.invoice.mark_paid", "agency_credit_invoice", strconv.FormatInt(id, 10), "登记授信账单收款", gin.H{
			"status":     invoice.Status,
			"paid_quota": invoice.PaidQuota,
		}, gin.H{
			"status":            result.Status,
			"paid_quota":        result.PaidQuota,
			"payment_reference": result.PaymentReference,
		})
	})
	if err != nil {
		respondCreditMutationError(c, err, "登记收款失败")
		return
	}
	respondOK(c, gin.H{
		"id":                updated.ID,
		"status":            updated.Status,
		"paid_quota":        updated.PaidQuota,
		"remaining_quota":   updated.AmountQuota - updated.PaidQuota - updated.BadDebtQuota,
		"payment_reference": updated.PaymentReference,
	})
}

func (a *App) markCreditInvoiceBadDebt(c *gin.Context) {
	id, ok := parseCreditInvoiceID(c)
	if !ok {
		return
	}
	identity := currentIdentity(c)
	invoice, ok := a.loadCreditInvoice(c, id)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Reason) == "" {
		respondError(c, http.StatusBadRequest, "invalid_request", "请填写坏账原因", nil)
		return
	}
	actorID := int64(0)
	if identity != nil {
		actorID = identity.ActorID
	}
	var updated *model.AgencyCreditInvoice
	err := a.db.Transaction(func(tx *gorm.DB) error {
		result, err := model.MarkAgencyCreditInvoiceBadDebtTx(tx, id, actorID, input.Reason)
		if err != nil {
			return err
		}
		updated = result
		return recordAuditTx(tx, c, identity, "credit.invoice.mark_bad_debt", "agency_credit_invoice", strconv.FormatInt(id, 10), strings.TrimSpace(input.Reason), gin.H{
			"status":         invoice.Status,
			"bad_debt_quota": invoice.BadDebtQuota,
		}, gin.H{
			"status":           result.Status,
			"bad_debt_quota":   result.BadDebtQuota,
			"suspended_credit": true,
		})
	})
	if err != nil {
		respondCreditMutationError(c, err, "标记坏账失败")
		return
	}
	respondOK(c, gin.H{
		"id":             updated.ID,
		"status":         updated.Status,
		"bad_debt_quota": updated.BadDebtQuota,
	})
}

func (a *App) resendCreditInvoiceEmail(c *gin.Context) {
	id, ok := parseCreditInvoiceID(c)
	if !ok {
		return
	}
	identity := currentIdentity(c)
	invoice, ok := a.loadCreditInvoice(c, id)
	if !ok {
		return
	}
	if err := a.sendCreditInvoice(invoice.UserID, *invoice); err != nil {
		respondError(c, http.StatusBadGateway, "email_failed", "发送授信账单邮件失败："+err.Error(), nil)
		return
	}
	if identity != nil {
		err := a.db.Transaction(func(tx *gorm.DB) error {
			return recordAuditTx(tx, c, identity, "credit.invoice.resend_email", "agency_credit_invoice", strconv.FormatInt(id, 10), "重发授信账单邮件", nil, gin.H{"email_status": model.AgencyCreditEmailStatusSent})
		})
		if err != nil {
			common.SysError("agency credit invoice audit failed: " + err.Error())
		}
	}
	respondOK(c, gin.H{"id": id, "email_status": model.AgencyCreditEmailStatusSent})
}

func respondCreditMutationError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, model.ErrAgencyCreditInvoiceClosed):
		respondError(c, http.StatusConflict, "invoice_closed", "账单已结清或已核销，无法再次变更", nil)
	case errors.Is(err, model.ErrAgencyCreditSettlementExceedsDebt):
		respondError(c, http.StatusConflict, "settlement_exceeds_debt", "还款金额超过未结清授信余额或已超出账单余额", nil)
	case errors.Is(err, model.ErrAgencyCreditSettlementInvalid):
		respondError(c, http.StatusBadRequest, "invalid_request", "还款金额、操作人或收款凭证无效", nil)
	default:
		common.SysError("agency credit mutation failed: " + err.Error())
		respondError(c, http.StatusInternalServerError, "database_error", fallback, nil)
	}
}

// ProcessCreditInvoices issues the previous natural month's statement for
// every active credit contract that actually used credit. It is idempotent:
// the (user_id, period) uniqueness plus an existence probe keep a restart or a
// delayed day-1 run from creating a duplicate statement.
func (a *App) ProcessCreditInvoices(ctx context.Context, now time.Time) (int, error) {
	loc := creditLocation()
	local := now.In(loc)
	periodTime := local.AddDate(0, -1, 0)
	period := periodTime.Format("2006-01")
	start := time.Date(periodTime.Year(), periodTime.Month(), 1, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 1, 0)
	var terms []model.AgencyCreditTerms
	if err := a.db.WithContext(ctx).Where("status = ? AND credit_limit > 0", model.AgencyCreditStatusActive).Find(&terms).Error; err != nil {
		return 0, err
	}
	created := 0
	for _, term := range terms {
		var existing int64
		if err := a.db.WithContext(ctx).Model(&model.AgencyCreditInvoice{}).Where("user_id = ? AND period = ?", term.UserID, period).Count(&existing).Error; err != nil {
			return created, err
		}
		if existing > 0 {
			continue
		}
		var total int64
		if err := a.db.WithContext(ctx).Model(&model.AgencyUsageFact{}).Where("user_id = ? AND occurred_at_ms >= ? AND occurred_at_ms < ? AND debt_quota > 0", term.UserID, start.UnixMilli(), end.UnixMilli()).Select("COALESCE(SUM(debt_quota), 0)").Scan(&total).Error; err != nil {
			return created, err
		}
		if total <= 0 {
			continue
		}
		due := time.Date(local.Year(), local.Month(), model.AgencyCreditRepaymentDay, 0, 0, 0, 0, loc)
		overdue := due.AddDate(0, 0, term.PaymentTermDays)
		emailStatus := model.AgencyCreditEmailStatusPending
		if !term.EmailEnabled {
			emailStatus = model.AgencyCreditEmailStatusDisabled
		}
		invoice := model.AgencyCreditInvoice{
			UserID:          term.UserID,
			Period:          period,
			PeriodStartMS:   start.UnixMilli(),
			PeriodEndMS:     end.UnixMilli(),
			AmountQuota:     total,
			CurrencyCode:    "CNY",
			Status:          model.AgencyCreditInvoiceStatusIssued,
			RepaymentDay:    model.AgencyCreditRepaymentDay,
			PaymentTermDays: term.PaymentTermDays,
			DueAtMS:         due.UnixMilli(),
			OverdueAtMS:     overdue.UnixMilli(),
			EmailStatus:     emailStatus,
			CreatedAtMS:     local.UnixMilli(),
			UpdatedAtMS:     local.UnixMilli(),
		}
		result := a.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "period"}},
			DoNothing: true,
		}).Create(&invoice)
		if result.Error != nil {
			return created, result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		created++
		if term.EmailEnabled {
			if err := a.sendCreditInvoice(term.UserID, invoice); err != nil {
				common.SysError("agency credit invoice email failed: " + err.Error())
			}
		}
	}
	return created, nil
}

// ProcessCreditOverdue moves due statements into the overdue state so finance
// can write them off and the customer can see the real collection status.
func (a *App) ProcessCreditOverdue(ctx context.Context, now time.Time) (int64, error) {
	local := now.In(creditLocation())
	result := a.db.WithContext(ctx).Model(&model.AgencyCreditInvoice{}).
		Where("status = ? AND overdue_at_ms <= ?", model.AgencyCreditInvoiceStatusIssued, local.UnixMilli()).
		Updates(map[string]any{"status": model.AgencyCreditInvoiceStatusOverdue, "updated_at_ms": local.UnixMilli()})
	return result.RowsAffected, result.Error
}

// RetryCreditInvoiceEmails re-sends statements whose first delivery failed or
// was never attempted. Failed attempts are throttled so a broken SMTP path
// cannot flood a customer's mailbox.
func (a *App) RetryCreditInvoiceEmails(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	cutoff := time.Now().Add(-creditFailedEmailRetryDelay).UnixMilli()
	var invoices []model.AgencyCreditInvoice
	if err := a.db.WithContext(ctx).
		// Only statements that are still open are retried: a paid or
		// written-off statement must never be re-sent to the customer by
		// the automatic retry loop.
		Where("status IN ?", []string{model.AgencyCreditInvoiceStatusIssued, model.AgencyCreditInvoiceStatusOverdue}).
		Where("email_status = ? OR (email_status = ? AND updated_at_ms <= ?)", model.AgencyCreditEmailStatusPending, model.AgencyCreditEmailStatusFailed, cutoff).
		Order("id ASC").Limit(limit).Find(&invoices).Error; err != nil {
		return 0, err
	}
	sent := 0
	for _, invoice := range invoices {
		var term model.AgencyCreditTerms
		if err := a.db.WithContext(ctx).Where("user_id = ?", invoice.UserID).First(&term).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				now := time.Now().UnixMilli()
				_ = a.db.WithContext(ctx).Model(&model.AgencyCreditInvoice{}).Where("id = ?", invoice.ID).Updates(map[string]any{
					"email_status":     model.AgencyCreditEmailStatusDisabled,
					"email_last_error": "credit contract no longer exists",
					"updated_at_ms":    now,
				})
			}
			continue
		}
		if !term.EmailEnabled {
			now := time.Now().UnixMilli()
			_ = a.db.WithContext(ctx).Model(&model.AgencyCreditInvoice{}).Where("id = ?", invoice.ID).Updates(map[string]any{
				"email_status":  model.AgencyCreditEmailStatusDisabled,
				"updated_at_ms": now,
			})
			continue
		}
		if err := a.sendCreditInvoice(invoice.UserID, invoice); err == nil {
			sent++
		}
	}
	return sent, nil
}

func (a *App) sendCreditInvoice(userID int64, invoice model.AgencyCreditInvoice) error {
	var user model.User
	if err := a.db.Select("id, username, email").First(&user, userID).Error; err != nil {
		return a.recordCreditEmailFailure(invoice.ID, err)
	}
	email := strings.TrimSpace(user.Email)
	if email == "" {
		return a.recordCreditEmailFailure(invoice.ID, errors.New("客户邮箱为空"))
	}
	var rows []model.AgencyUsageFact
	if err := a.db.Where("user_id = ? AND occurred_at_ms >= ? AND occurred_at_ms < ? AND debt_quota > 0", userID, invoice.PeriodStartMS, invoice.PeriodEndMS).Order("occurred_at_ms ASC").Find(&rows).Error; err != nil {
		return a.recordCreditEmailFailure(invoice.ID, err)
	}
	var data bytes.Buffer
	writer := csv.NewWriter(&data)
	_ = writer.Write([]string{"时间", "模型", "接口", "授信额度用量", "状态"})
	for _, row := range rows {
		_ = writer.Write([]string{
			time.UnixMilli(row.OccurredAtMS).In(creditLocation()).Format("2006-01-02 15:04:05"),
			csvSafeCell(row.OriginModelName),
			csvSafeCell(row.Endpoint),
			strconv.FormatInt(row.DebtQuota, 10),
			csvSafeCell(row.BusinessStatus),
		})
	}
	writer.Flush()
	attachmentName := "credit-invoice-" + invoice.Period + ".csv"
	content := fmt.Sprintf(
		"<p>您好，%s：</p><p>%s 账期授信账单已生成，应还授信额度：<strong>%d</strong>。</p><p>统一还款日：每月 %d 日；还款期限：账单日后 %d 天。</p><p>账单明细见附件。</p>",
		html.EscapeString(user.Username),
		html.EscapeString(invoice.Period),
		invoice.AmountQuota,
		model.AgencyCreditRepaymentDay,
		invoice.PaymentTermDays,
	)
	err := common.SendEmailWithAttachments("授信账单 "+invoice.Period, email, content, []common.EmailAttachment{{
		Filename:    attachmentName,
		ContentType: "text/csv; charset=UTF-8",
		Data:        data.Bytes(),
	}})
	if err != nil {
		return a.recordCreditEmailFailure(invoice.ID, err)
	}
	now := time.Now().UnixMilli()
	if err := a.db.Model(&model.AgencyCreditInvoice{}).Where("id = ?", invoice.ID).Updates(map[string]any{
		"email_status":     model.AgencyCreditEmailStatusSent,
		"email_sent_at_ms": now,
		"email_last_error": "",
		"attachment_name":  attachmentName,
		"updated_at_ms":    now,
	}).Error; err != nil {
		return err
	}
	return nil
}

func (a *App) recordCreditEmailFailure(invoiceID int64, cause error) error {
	message := cause.Error()
	if len(message) > 900 {
		message = message[:900]
	}
	if invoiceID > 0 {
		_ = a.db.Model(&model.AgencyCreditInvoice{}).Where("id = ?", invoiceID).Updates(map[string]any{
			"email_status":     model.AgencyCreditEmailStatusFailed,
			"email_last_error": message,
			"updated_at_ms":    time.Now().UnixMilli(),
		})
	}
	return cause
}

// csvSafeCell prevents a spreadsheet from executing customer- or
// provider-controlled text such as a model name as a formula.
func csvSafeCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t")
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + value
	}
	return value
}
