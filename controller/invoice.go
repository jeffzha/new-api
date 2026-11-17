package controller

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const invoiceDocumentMaxBytes int64 = 10 * 1024 * 1024

type invoiceSummaryResponse struct {
	AvailableCents int64  `json:"available_cents"`
	ReservedCents  int64  `json:"reserved_cents"`
	IssuedCents    int64  `json:"issued_cents"`
	Available      string `json:"available"`
	Reserved       string `json:"reserved"`
	Issued         string `json:"issued"`
}

type invoiceSourceResponse struct {
	TopUpID        int    `json:"topup_id"`
	TradeNo        string `json:"trade_no"`
	PaidCents      int64  `json:"paid_cents"`
	AvailableCents int64  `json:"available_cents"`
	Paid           string `json:"paid"`
	Available      string `json:"available"`
	CurrencyCode   string `json:"currency_code"`
	PaidAt         int64  `json:"paid_at"`
}

type invoiceLineResponse struct {
	ID             int    `json:"id"`
	TopUpID        int    `json:"topup_id"`
	SourceTradeNo  string `json:"source_trade_no"`
	PaidCents      int64  `json:"paid_cents"`
	AllocatedCents int64  `json:"allocated_cents"`
	Paid           string `json:"paid"`
	Allocated      string `json:"allocated"`
	CurrencyCode   string `json:"currency_code"`
}

type invoiceApplicationResponse struct {
	ID            int                             `json:"id"`
	ApplicationNo string                          `json:"application_no"`
	UserID        int                             `json:"user_id,omitempty"`
	Status        string                          `json:"status"`
	CurrencyCode  string                          `json:"currency_code"`
	TotalCents    int64                           `json:"total_cents"`
	Total         string                          `json:"total"`
	PurchaserName string                          `json:"purchaser_name"`
	TaxNumber     string                          `json:"tax_number"`
	ReceiverEmail string                          `json:"receiver_email"`
	BankName      string                          `json:"bank_name,omitempty"`
	BankAccount   string                          `json:"bank_account,omitempty"`
	Address       string                          `json:"address,omitempty"`
	Phone         string                          `json:"phone,omitempty"`
	Remark        string                          `json:"remark,omitempty"`
	ReviewReason  string                          `json:"review_reason,omitempty"`
	ReviewerID    int                             `json:"reviewer_id,omitempty"`
	ReviewedAt    int64                           `json:"reviewed_at,omitempty"`
	InvoiceNumber string                          `json:"invoice_number,omitempty"`
	InvoiceCode   string                          `json:"invoice_code,omitempty"`
	InvoiceDate   int64                           `json:"invoice_date,omitempty"`
	IssuerID      int                             `json:"issuer_id,omitempty"`
	IssuedAt      int64                           `json:"issued_at,omitempty"`
	EmailStatus   string                          `json:"email_status"`
	CreatedAt     int64                           `json:"created_at"`
	UpdatedAt     int64                           `json:"updated_at"`
	HasDocument   bool                            `json:"has_document"`
	Lines         []invoiceLineResponse           `json:"lines"`
	Events        []model.InvoiceApplicationEvent `json:"events,omitempty"`
}

type invoiceReviewRequest struct {
	Approved *bool  `json:"approved"`
	Reason   string `json:"reason"`
}

type invoiceVoidRequest struct {
	Approved *bool  `json:"approved"`
	Reason   string `json:"reason"`
}

type invoiceApplicationRequest struct {
	model.InvoiceApplicationInput
}

func invoiceApplicationDTO(application *model.InvoiceApplication, includeUser bool) invoiceApplicationResponse {
	response := invoiceApplicationResponse{
		ID:            application.ID,
		ApplicationNo: application.ApplicationNo,
		Status:        application.Status,
		CurrencyCode:  application.CurrencyCode,
		TotalCents:    application.TotalCents,
		Total:         model.FormatInvoiceCents(application.TotalCents),
		PurchaserName: application.PurchaserName,
		TaxNumber:     application.TaxNumber,
		ReceiverEmail: application.ReceiverEmail,
		BankName:      application.BankName,
		BankAccount:   application.BankAccount,
		Address:       application.Address,
		Phone:         application.Phone,
		Remark:        application.Remark,
		ReviewReason:  application.ReviewReason,
		ReviewerID:    application.ReviewerID,
		ReviewedAt:    application.ReviewedAt,
		InvoiceNumber: application.InvoiceNumber,
		InvoiceCode:   application.InvoiceCode,
		InvoiceDate:   application.InvoiceDate,
		IssuerID:      application.IssuerID,
		IssuedAt:      application.IssuedAt,
		EmailStatus:   application.EmailStatus,
		CreatedAt:     application.CreatedAt,
		UpdatedAt:     application.UpdatedAt,
		HasDocument:   application.Status == model.InvoiceStatusIssued || application.Status == model.InvoiceStatusVoidPending || application.Status == model.InvoiceStatusVoided,
		Lines:         make([]invoiceLineResponse, 0, len(application.Lines)),
	}
	if includeUser {
		response.UserID = application.UserID
	}
	for _, line := range application.Lines {
		response.Lines = append(response.Lines, invoiceLineResponse{
			ID:             line.ID,
			TopUpID:        line.TopUpID,
			SourceTradeNo:  line.SourceTradeNo,
			PaidCents:      line.PaidCents,
			AllocatedCents: line.AllocatedCents,
			Paid:           model.FormatInvoiceCents(line.PaidCents),
			Allocated:      model.FormatInvoiceCents(line.AllocatedCents),
			CurrencyCode:   line.CurrencyCode,
		})
	}
	return response
}

func GetInvoiceSummary(c *gin.Context) {
	summary, err := model.GetInvoiceSummary(c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, invoiceSummaryResponse{
		AvailableCents: summary.AvailableCents,
		ReservedCents:  summary.ReservedCents,
		IssuedCents:    summary.IssuedCents,
		Available:      model.FormatInvoiceCents(summary.AvailableCents),
		Reserved:       model.FormatInvoiceCents(summary.ReservedCents),
		Issued:         model.FormatInvoiceCents(summary.IssuedCents),
	})
}

func GetInvoiceSources(c *gin.Context) {
	sources, err := model.GetInvoiceSources(c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]invoiceSourceResponse, 0, len(sources))
	for _, source := range sources {
		items = append(items, invoiceSourceResponse{
			TopUpID:        source.TopUpID,
			TradeNo:        source.TradeNo,
			PaidCents:      source.PaidCents,
			AvailableCents: source.AvailableCents,
			Paid:           model.FormatInvoiceCents(source.PaidCents),
			Available:      model.FormatInvoiceCents(source.AvailableCents),
			CurrencyCode:   source.CurrencyCode,
			PaidAt:         source.PaidAt,
		})
	}
	common.ApiSuccess(c, items)
}

func GetInvoiceApplications(c *gin.Context) {
	page := common.GetPageQuery(c)
	applications, total, err := model.ListInvoiceApplications(c.GetInt("id"), page, strings.TrimSpace(c.Query("status")))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]invoiceApplicationResponse, 0, len(applications))
	for i := range applications {
		items = append(items, invoiceApplicationDTO(&applications[i], false))
	}
	page.SetTotal(int(total))
	page.SetItems(items)
	common.ApiSuccess(c, page)
}

func GetAdminInvoiceApplications(c *gin.Context) {
	page := common.GetPageQuery(c)
	applications, total, err := model.ListAllInvoiceApplications(page, strings.TrimSpace(c.Query("status")))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]invoiceApplicationResponse, 0, len(applications))
	for i := range applications {
		item := invoiceApplicationDTO(&applications[i], true)
		item.TaxNumber = ""
		item.ReceiverEmail = ""
		item.BankName = ""
		item.BankAccount = ""
		item.Address = ""
		item.Phone = ""
		item.Remark = ""
		items = append(items, item)
	}
	page.SetTotal(int(total))
	page.SetItems(items)
	common.ApiSuccess(c, page)
}

func GetInvoiceApplication(c *gin.Context) {
	applicationID, err := strconv.Atoi(c.Param("id"))
	if err != nil || applicationID <= 0 {
		common.ApiErrorMsg(c, "发票申请不存在")
		return
	}
	application, err := model.GetInvoiceApplication(applicationID, c.GetInt("id"), false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	response := invoiceApplicationDTO(application, false)
	response.Events, err = model.GetInvoiceApplicationEvents(applicationID, c.GetInt("id"), false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, response)
}

func GetAdminInvoiceApplication(c *gin.Context) {
	applicationID, err := strconv.Atoi(c.Param("id"))
	if err != nil || applicationID <= 0 {
		common.ApiErrorMsg(c, "发票申请不存在")
		return
	}
	application, err := model.GetInvoiceApplication(applicationID, c.GetInt("id"), true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	response := invoiceApplicationDTO(application, true)
	response.Events, err = model.GetInvoiceApplicationEvents(applicationID, c.GetInt("id"), true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, response)
}

func CreateInvoiceApplication(c *gin.Context) {
	var request model.InvoiceApplicationInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, "发票申请参数错误")
		return
	}
	parsedEmail, err := mail.ParseAddress(strings.TrimSpace(request.ReceiverEmail))
	if err != nil || parsedEmail.Address != strings.TrimSpace(request.ReceiverEmail) {
		common.ApiErrorMsg(c, "接收邮箱格式错误")
		return
	}
	application, err := model.CreateInvoiceApplication(c.GetInt("id"), request)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, invoiceApplicationDTO(application, false))
}

func WithdrawInvoiceApplication(c *gin.Context) {
	applicationID, err := strconv.Atoi(c.Param("id"))
	if err != nil || applicationID <= 0 {
		common.ApiErrorMsg(c, "发票申请不存在")
		return
	}
	if err = model.WithdrawInvoiceApplication(c.GetInt("id"), applicationID); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func RequestInvoiceVoid(c *gin.Context) {
	applicationID, err := strconv.Atoi(c.Param("id"))
	if err != nil || applicationID <= 0 {
		common.ApiErrorMsg(c, "发票申请不存在")
		return
	}
	var request invoiceVoidRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Reason) == "" {
		common.ApiErrorMsg(c, "请填写作废原因")
		return
	}
	if err := model.RequestInvoiceVoid(c.GetInt("id"), applicationID, request.Reason); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func ReviewInvoiceApplication(c *gin.Context) {
	applicationID, err := strconv.Atoi(c.Param("id"))
	if err != nil || applicationID <= 0 {
		common.ApiErrorMsg(c, "发票申请不存在")
		return
	}
	var request invoiceReviewRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Approved == nil {
		common.ApiErrorMsg(c, "审核参数错误")
		return
	}
	if err := model.ReviewInvoiceApplication(c.GetInt("id"), c.GetInt("role"), applicationID, *request.Approved, request.Reason); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAuditFor(c, invoiceTargetUserID(applicationID), "invoice.review", map[string]any{"application_id": applicationID, "approved": *request.Approved})
	common.ApiSuccess(c, nil)
}

func IssueInvoiceApplication(c *gin.Context) {
	applicationID, err := strconv.Atoi(c.Param("id"))
	if err != nil || applicationID <= 0 {
		common.ApiErrorMsg(c, "发票申请不存在")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, invoiceDocumentMaxBytes+1024*1024)
	file, header, err := c.Request.FormFile("document")
	if err != nil {
		common.ApiErrorMsg(c, "请上传正式发票 PDF")
		return
	}
	defer file.Close()
	if header.Size <= 0 || header.Size > invoiceDocumentMaxBytes {
		common.ApiError(c, model.ErrInvoiceDocumentTooLarge)
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, invoiceDocumentMaxBytes+1))
	if err != nil || int64(len(data)) > invoiceDocumentMaxBytes {
		common.ApiError(c, model.ErrInvoiceDocumentTooLarge)
		return
	}
	if len(data) < 5 || string(data[:5]) != "%PDF-" || http.DetectContentType(data) != "application/pdf" {
		common.ApiErrorMsg(c, "仅支持真实 PDF 发票文件")
		return
	}
	invoiceDate, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("invoice_date")), 10, 64)
	if err != nil || invoiceDate <= 0 {
		common.ApiErrorMsg(c, "开票日期格式错误")
		return
	}
	amountCents, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("amount_cents")), 10, 64)
	if err != nil || amountCents <= 0 {
		common.ApiErrorMsg(c, "开票金额格式错误")
		return
	}
	digest := sha256.Sum256(data)
	if err := model.IssueInvoiceApplication(c.GetInt("id"), c.GetInt("role"), applicationID, model.InvoiceIssueInput{
		InvoiceNumber: strings.TrimSpace(c.PostForm("invoice_number")),
		InvoiceCode:   strings.TrimSpace(c.PostForm("invoice_code")),
		InvoiceDate:   invoiceDate,
		AmountCents:   amountCents,
		FileName:      fmt.Sprintf("invoice-%d.pdf", applicationID),
		ContentType:   "application/pdf",
		SHA256:        fmt.Sprintf("%x", digest),
		Data:          data,
	}); err != nil {
		common.ApiError(c, err)
		return
	}
	emailErr := sendInvoiceEmail(applicationID)
	emailStatus := "sent"
	if emailErr != nil {
		emailStatus = "failed"
		common.SysError("invoice issued but email delivery failed")
	}
	recordManageAuditFor(c, invoiceTargetUserID(applicationID), "invoice.issue", map[string]any{"application_id": applicationID, "email_status": emailStatus})
	common.ApiSuccess(c, nil)
}

func ReviewInvoiceVoid(c *gin.Context) {
	applicationID, err := strconv.Atoi(c.Param("id"))
	if err != nil || applicationID <= 0 {
		common.ApiErrorMsg(c, "发票申请不存在")
		return
	}
	var request invoiceVoidRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Approved == nil || strings.TrimSpace(request.Reason) == "" {
		common.ApiErrorMsg(c, "作废审核参数错误")
		return
	}
	if err := model.ReviewInvoiceVoid(c.GetInt("id"), c.GetInt("role"), applicationID, *request.Approved, request.Reason); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAuditFor(c, invoiceTargetUserID(applicationID), "invoice.void_review", map[string]any{"application_id": applicationID, "approved": *request.Approved})
	common.ApiSuccess(c, nil)
}

func ResendInvoiceEmail(c *gin.Context) {
	applicationID, err := strconv.Atoi(c.Param("id"))
	if err != nil || applicationID <= 0 {
		common.ApiErrorMsg(c, "发票申请不存在")
		return
	}
	err = sendInvoiceEmail(applicationID)
	status := "sent"
	if err != nil {
		status = "failed"
	}
	recordManageAuditFor(c, invoiceTargetUserID(applicationID), "invoice.email_resend", map[string]any{"application_id": applicationID, "email_status": status})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func DownloadInvoiceDocument(c *gin.Context) {
	applicationID, err := strconv.Atoi(c.Param("id"))
	if err != nil || applicationID <= 0 {
		common.ApiErrorMsg(c, "发票申请不存在")
		return
	}
	admin := isAdminContext(c)
	application, err := model.GetInvoiceApplication(applicationID, c.GetInt("id"), admin)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if application.Status != model.InvoiceStatusIssued && application.Status != model.InvoiceStatusVoidPending && application.Status != model.InvoiceStatusVoided {
		common.ApiError(c, model.ErrInvoiceInvalidStatus)
		return
	}
	application, document, err := model.GetInvoiceDocument(applicationID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.RecordInvoiceDocumentDownload(applicationID, c.GetInt("id"), c.GetInt("role")); err != nil {
		common.ApiError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, int64(len(document.Data)), "application/pdf", bytes.NewReader(document.Data), map[string]string{
		"Content-Disposition": fmt.Sprintf(`attachment; filename="%s"`, document.FileName),
	})
}

func isAdminContext(c *gin.Context) bool {
	return c.GetInt("role") >= common.RoleAdminUser
}

func invoiceTargetUserID(applicationID int) int {
	application, err := model.GetInvoiceApplication(applicationID, 0, true)
	if err != nil {
		return 0
	}
	return application.UserID
}

func sendInvoiceEmail(applicationID int) error {
	application, document, err := model.GetInvoiceDocument(applicationID)
	if err != nil {
		return err
	}
	if application.Status != model.InvoiceStatusIssued && application.Status != model.InvoiceStatusVoidPending {
		return model.ErrInvoiceInvalidStatus
	}
	content := fmt.Sprintf("<p>您的发票申请 <strong>%s</strong> 已开具。</p><p>发票号码：%s<br>开票金额：¥%s</p>", html.EscapeString(application.ApplicationNo), html.EscapeString(application.InvoiceNumber), html.EscapeString(model.FormatInvoiceCents(application.TotalCents)))
	err = common.SendEmailWithAttachments("发票已开具 - "+application.ApplicationNo, application.ReceiverEmail, content, []common.EmailAttachment{{Filename: document.FileName, ContentType: "application/pdf", Data: document.Data}})
	if err != nil {
		_ = model.UpdateInvoiceEmailStatus(applicationID, "failed", "email delivery failed")
		return fmt.Errorf("发票邮件发送失败")
	}
	return model.UpdateInvoiceEmailStatus(applicationID, "sent", "")
}
