package agencyhub

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const shanghaiCreditLocation = "Asia/Shanghai"

func (a *App) creditRoutes(api *gin.RouterGroup) {
	root := api.Group("/root")
	root.Use(a.requireRoot())
	root.GET("/users/:user_id/credit", a.getCreditTerms)
	root.PUT("/users/:user_id/credit", a.putCreditTerms)
	root.GET("/credit-invoices", a.listCreditInvoices)
	root.POST("/credit-invoices/:id/mark-bad-debt", a.markCreditBadDebt)
}

func parseCreditUserID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || id <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的用户ID", nil)
		return 0, false
	}
	return id, true
}

func (a *App) getCreditTerms(c *gin.Context) {
	userID, ok := parseCreditUserID(c)
	if !ok {
		return
	}
	var terms model.AgencyCreditTerms
	err := a.db.Where("user_id = ?", userID).First(&terms).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondOK(c, gin.H{"user_id": strconv.FormatInt(userID, 10), "credit_limit": "0", "payment_term_days": 0, "repayment_day": 1, "status": "disabled", "email_enabled": false})
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取授信条款失败", nil)
		return
	}
	respondOK(c, terms)
}

type creditTermsRequest struct {
	CreditLimit     int64  `json:"credit_limit"`
	PaymentTermDays int    `json:"payment_term_days"`
	RepaymentDay    int    `json:"repayment_day"`
	Status          string `json:"status"`
	EmailEnabled    bool   `json:"email_enabled"`
}

func (a *App) putCreditTerms(c *gin.Context) {
	userID, ok := parseCreditUserID(c)
	if !ok {
		return
	}
	var request creditTermsRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.CreditLimit < 0 || request.PaymentTermDays < 0 || request.PaymentTermDays > 365 || request.RepaymentDay < 1 || request.RepaymentDay > 28 {
		respondError(c, http.StatusBadRequest, "invalid_credit_terms", "授信额度、账期或还款日无效", nil)
		return
	}
	if request.Status == "" {
		request.Status = model.AgencyCreditStatusActive
	}
	if request.Status != model.AgencyCreditStatusActive && request.Status != model.AgencyCreditStatusSuspended {
		respondError(c, http.StatusBadRequest, "invalid_credit_status", "授信状态无效", nil)
		return
	}
	now := time.Now().UnixMilli()
	identity := currentIdentity(c)
	actorID := int64(0)
	if identity != nil {
		actorID = identity.ActorID
	}
	terms := model.AgencyCreditTerms{UserID: userID, CreditLimit: request.CreditLimit, PaymentTermDays: request.PaymentTermDays, RepaymentDay: request.RepaymentDay, Status: request.Status, EmailEnabled: request.EmailEnabled, UpdatedBy: actorID, CreatedAtMS: now, UpdatedAtMS: now}
	result := a.db.Where("user_id = ?", userID).Assign(map[string]any{"credit_limit": terms.CreditLimit, "payment_term_days": terms.PaymentTermDays, "repayment_day": terms.RepaymentDay, "status": terms.Status, "email_enabled": terms.EmailEnabled, "updated_by": terms.UpdatedBy, "updated_at_ms": now}).FirstOrCreate(&terms)
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "保存授信条款失败", nil)
		return
	}
	respondOK(c, terms)
}

func (a *App) listCreditInvoices(c *gin.Context) {
	var invoices []model.AgencyCreditInvoice
	query := a.db.Order("due_at_ms DESC, id DESC")
	if userID := strings.TrimSpace(c.Query("user_id")); userID != "" {
		query = query.Where("user_id = ?", userID)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	if err := query.Limit(200).Find(&invoices).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取账单失败", nil)
		return
	}
	respondOK(c, gin.H{"items": invoices})
}

func (a *App) markCreditBadDebt(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的账单ID", nil)
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求无效", nil)
		return
	}
	now := time.Now().UnixMilli()
	result := a.db.Model(&model.AgencyCreditInvoice{}).Where("id = ? AND status IN ?", id, []string{"issued", "overdue"}).Updates(map[string]any{"status": "bad_debt", "bad_debt_quota": gorm.Expr("amount_quota - paid_quota"), "bad_debt_at_ms": now, "bad_debt_reason": strings.TrimSpace(input.Reason), "updated_at_ms": now})
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "标记坏账失败", nil)
		return
	}
	if result.RowsAffected != 1 {
		respondError(c, http.StatusNotFound, "not_found", "账单不存在或状态不允许变更", nil)
		return
	}
	respondOK(c, gin.H{"id": id, "status": "bad_debt"})
}

func (a *App) ProcessCreditInvoices(ctx context.Context, now time.Time) (int, error) {
	loc, err := time.LoadLocation(shanghaiCreditLocation)
	if err != nil {
		loc = time.FixedZone(shanghaiCreditLocation, 8*60*60)
	}
	local := now.In(loc)
	if local.Day() != 1 {
		return 0, nil
	}
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
		var existing model.AgencyCreditInvoice
		if err := a.db.WithContext(ctx).Where("user_id = ? AND period = ?", term.UserID, period).First(&existing).Error; err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return created, err
		}
		var total int64
		if err := a.db.WithContext(ctx).Model(&model.AgencyUsageFact{}).Where("user_id = ? AND occurred_at_ms >= ? AND occurred_at_ms < ? AND debt_quota > 0", term.UserID, start.UnixMilli(), end.UnixMilli()).Select("COALESCE(SUM(debt_quota), 0)").Scan(&total).Error; err != nil {
			return created, err
		}
		due := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 0, term.PaymentTermDays)
		invoice := model.AgencyCreditInvoice{UserID: term.UserID, Period: period, PeriodStartMS: start.UnixMilli(), PeriodEndMS: end.UnixMilli(), AmountQuota: total, CurrencyCode: "CNY", Status: "issued", RepaymentDay: 1, PaymentTermDays: term.PaymentTermDays, DueAtMS: due.UnixMilli(), OverdueAtMS: due.UnixMilli(), EmailStatus: "pending", CreatedAtMS: now.UnixMilli(), UpdatedAtMS: now.UnixMilli()}
		if err := a.db.WithContext(ctx).Create(&invoice).Error; err != nil {
			return created, err
		}
		created++
		if term.EmailEnabled {
			_ = a.sendCreditInvoice(term.UserID, invoice)
		}
	}
	return created, nil
}

func (a *App) sendCreditInvoice(userID int64, invoice model.AgencyCreditInvoice) error {
	var user model.User
	if err := a.db.Select("id, username, email").First(&user, userID).Error; err != nil || strings.TrimSpace(user.Email) == "" {
		return err
	}
	var rows []model.AgencyUsageFact
	if err := a.db.Where("user_id = ? AND occurred_at_ms >= ? AND occurred_at_ms < ? AND debt_quota > 0", userID, invoice.PeriodStartMS, invoice.PeriodEndMS).Order("occurred_at_ms ASC").Find(&rows).Error; err != nil {
		return err
	}
	var data bytes.Buffer
	w := csv.NewWriter(&data)
	_ = w.Write([]string{"时间", "模型", "接口", "授信额度用量", "状态"})
	for _, row := range rows {
		_ = w.Write([]string{time.UnixMilli(row.OccurredAtMS).Format("2006-01-02 15:04:05"), row.OriginModelName, row.Endpoint, strconv.FormatInt(row.DebtQuota, 10), row.BusinessStatus})
	}
	w.Flush()
	content := fmt.Sprintf("<p>您好，%s：</p><p>%s 账期授信账单已生成，应还授信额度：<strong>%d</strong>，统一还款日：每月 1 日（还款期限：%d 天）。账单明细见附件。</p>", user.Username, invoice.Period, invoice.AmountQuota, invoice.PaymentTermDays)
	err := common.SendEmailWithAttachments("授信账单 "+invoice.Period, user.Email, content, []common.EmailAttachment{{Filename: "credit-invoice-" + invoice.Period + ".csv", ContentType: "text/csv; charset=UTF-8", Data: data.Bytes()}})
	if err == nil {
		now := time.Now().UnixMilli()
		_ = a.db.Model(&model.AgencyCreditInvoice{}).Where("id = ?", invoice.ID).Updates(map[string]any{"email_status": "sent", "email_sent_at_ms": now, "attachment_name": "credit-invoice-" + invoice.Period + ".csv", "updated_at_ms": now})
	}
	return err
}
