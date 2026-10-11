package agencyhub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func creditTestUser(t *testing.T, app *App, username string) model.User {
	t.Helper()
	require.NoError(t, app.db.AutoMigrate(&model.User{}))
	user := model.User{Username: username, Password: "password", Status: common.UserStatusEnabled, BillingMode: model.AgencyDurableBillingMode}
	require.NoError(t, app.db.Create(&user).Error)
	return user
}

func creditTestRequest(method, target, body string, identity *Identity, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = params
	if identity != nil {
		ctx.Set("agency_identity", identity)
	}
	return ctx, recorder
}

func creditSeedDebt(t *testing.T, app *App, userID, amount int64) {
	t.Helper()
	require.NoError(t, app.db.Create(&model.AgencyFundingAccount{UserID: userID, DebtQuota: amount, MoneySeq: 1, Version: 1, UpdatedAt: 1}).Error)
	require.NoError(t, app.db.Create(&model.AgencyFundingDebt{
		UserID:            userID,
		OriginOperationID: fmt.Sprintf("credit-test-debt-%d", userID),
		DebtKind:          model.AgencyDebtKindModelCharge,
		OriginalQuota:     amount,
		OutstandingQuota:  amount,
		CreatedAtMS:       1,
	}).Error)
}

// creditSeedTerms forces email_enabled into the row: the column carries a
// database default of true, so a plain Create with false would be dropped.
func creditSeedTerms(t *testing.T, app *App, userID, limit int64, termDays int, emailEnabled bool) {
	t.Helper()
	terms := model.AgencyCreditTerms{
		UserID:          userID,
		CreditLimit:     limit,
		PaymentTermDays: termDays,
		RepaymentDay:    model.AgencyCreditRepaymentDay,
		Status:          model.AgencyCreditStatusActive,
		EmailEnabled:    emailEnabled,
		CreatedAtMS:     1,
		UpdatedAtMS:     1,
	}
	require.NoError(t, app.db.Select("user_id", "credit_limit", "payment_term_days", "repayment_day", "status", "email_enabled", "updated_by", "created_at_ms", "updated_at_ms").Create(&terms).Error)
	if !emailEnabled {
		require.NoError(t, app.db.Model(&model.AgencyCreditTerms{}).Where("user_id = ?", userID).Update("email_enabled", false).Error)
	}
}

func creditSeedInvoice(t *testing.T, app *App, userID, amount int64, status string) model.AgencyCreditInvoice {
	t.Helper()
	invoice := model.AgencyCreditInvoice{
		UserID:          userID,
		Period:          fmt.Sprintf("2026-%02d", time.Now().Month()),
		PeriodStartMS:   1,
		PeriodEndMS:     2,
		AmountQuota:     amount,
		CurrencyCode:    "CNY",
		Status:          status,
		RepaymentDay:    model.AgencyCreditRepaymentDay,
		PaymentTermDays: 10,
		DueAtMS:         1,
		OverdueAtMS:     2,
		EmailStatus:     model.AgencyCreditEmailStatusDisabled,
		CreatedAtMS:     1,
		UpdatedAtMS:     1,
	}
	require.NoError(t, app.db.Create(&invoice).Error)
	return invoice
}

func TestCreditTermsArePlatformWideAndFixTheRepaymentDay(t *testing.T) {
	app := newAgencyTestApp(t)
	user := creditTestUser(t, app, "credit-terms")
	root := &Identity{ActorType: ActorTypeRoot, ActorID: 7}
	params := gin.Params{{Key: "user_id", Value: strconv.Itoa(user.Id)}}
	ctx, recorder := creditTestRequest(http.MethodPut, "/agency/api/v1/root/users/"+strconv.Itoa(user.Id)+"/credit",
		`{"credit_limit":10000,"payment_term_days":30,"repayment_day":15,"status":"active","email_enabled":false}`, root, params)
	app.putCreditTerms(ctx)
	require.Equal(t, http.StatusOK, ctx.Writer.Status(), recorder.Body.String())

	var terms model.AgencyCreditTerms
	require.NoError(t, app.db.Where("user_id = ?", user.Id).First(&terms).Error)
	assert.Equal(t, model.AgencyCreditRepaymentDay, terms.RepaymentDay)
	assert.Equal(t, 30, terms.PaymentTermDays)
	assert.Equal(t, int64(10000), terms.CreditLimit)
	assert.Equal(t, model.AgencyCreditStatusActive, terms.Status)
	assert.False(t, terms.EmailEnabled)

	var audit model.AgencyAuditLog
	require.NoError(t, app.db.Where("action = ?", "credit.terms.upsert").First(&audit).Error)
	assert.Equal(t, strconv.Itoa(user.Id), audit.ObjectID)

	// Credit is the super administrator's platform-wide book: a root session
	// that entered an agency still manages every customer, because credit is
	// deliberately not an agency-scoped capability.
	otherAgency := int64(4242)
	ctx, recorder = creditTestRequest(http.MethodPut, "/agency/api/v1/root/users/"+strconv.Itoa(user.Id)+"/credit",
		`{"credit_limit":1,"payment_term_days":1,"status":"active"}`, &Identity{ActorType: ActorTypeRoot, ActorID: 7, AgencyID: &otherAgency}, params)
	app.putCreditTerms(ctx)
	require.Equal(t, http.StatusOK, ctx.Writer.Status(), recorder.Body.String())
	require.NoError(t, app.db.Where("user_id = ?", user.Id).First(&terms).Error)
	assert.Equal(t, int64(1), terms.CreditLimit)
}

func TestCreditStatementGenerationIsIdempotentAndTracksOverdue(t *testing.T) {
	app := newAgencyTestApp(t)
	user := creditTestUser(t, app, "credit-statement")
	creditSeedTerms(t, app, int64(user.Id), 10000, 10, false)
	loc := creditLocation()
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, loc)
	periodStart := time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
	require.NoError(t, app.db.Create(&model.AgencyUsageFact{
		EventID: "credit-statement-fact", ComponentID: "default", UserID: int64(user.Id),
		OriginModelName: "deepseek-v4-flash", ModelKey: "deepseek-v4-flash", BusinessStatus: "success",
		DebtQuota: 500, OccurredAtMS: periodStart.Add(time.Hour).UnixMilli(),
	}).Error)

	created, err := app.ProcessCreditInvoices(context.Background(), now)
	require.NoError(t, err)
	assert.Equal(t, 1, created)
	created, err = app.ProcessCreditInvoices(context.Background(), now)
	require.NoError(t, err)
	assert.Equal(t, 0, created, "a restart must not duplicate the monthly statement")

	var invoice model.AgencyCreditInvoice
	require.NoError(t, app.db.Where("user_id = ? AND period = ?", user.Id, "2026-10").First(&invoice).Error)
	assert.Equal(t, int64(500), invoice.AmountQuota)
	assert.Equal(t, model.AgencyCreditInvoiceStatusIssued, invoice.Status)
	assert.Equal(t, model.AgencyCreditEmailStatusDisabled, invoice.EmailStatus)
	assert.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, loc).UnixMilli(), invoice.DueAtMS)
	assert.Equal(t, time.Date(2026, 11, 11, 0, 0, 0, 0, loc).UnixMilli(), invoice.OverdueAtMS)

	changed, err := app.ProcessCreditOverdue(context.Background(), now)
	require.NoError(t, err)
	assert.Equal(t, int64(0), changed)
	changed, err = app.ProcessCreditOverdue(context.Background(), now.AddDate(0, 0, 11))
	require.NoError(t, err)
	assert.Equal(t, int64(1), changed)
	require.NoError(t, app.db.First(&invoice, invoice.ID).Error)
	assert.Equal(t, model.AgencyCreditInvoiceStatusOverdue, invoice.Status)
}

func TestCreditInvoicePartialAndFullSettlement(t *testing.T) {
	app := newAgencyTestApp(t)
	user := creditTestUser(t, app, "credit-settlement")
	creditSeedDebt(t, app, int64(user.Id), 1000)
	invoice := creditSeedInvoice(t, app, int64(user.Id), 1000, model.AgencyCreditInvoiceStatusIssued)

	var updated *model.AgencyCreditInvoice
	err := app.db.Transaction(func(tx *gorm.DB) error {
		result, err := model.MarkAgencyCreditInvoicePaidTx(tx, invoice.ID, 400, 7, "receipt-1")
		updated = result
		return err
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, model.AgencyCreditInvoiceStatusIssued, updated.Status)
	assert.Equal(t, int64(400), updated.PaidQuota)
	assert.Equal(t, "receipt-1", updated.PaymentReference)

	err = app.db.Transaction(func(tx *gorm.DB) error {
		result, err := model.MarkAgencyCreditInvoicePaidTx(tx, invoice.ID, 0, 7, "receipt-2")
		updated = result
		return err
	})
	require.NoError(t, err)
	assert.Equal(t, model.AgencyCreditInvoiceStatusPaid, updated.Status)
	assert.Equal(t, int64(1000), updated.PaidQuota)

	err = app.db.Transaction(func(tx *gorm.DB) error {
		_, err := model.MarkAgencyCreditInvoicePaidTx(tx, invoice.ID, 100, 7, "receipt-3")
		return err
	})
	require.ErrorIs(t, err, model.ErrAgencyCreditInvoiceClosed)

	err = app.db.Transaction(func(tx *gorm.DB) error {
		_, err := model.MarkAgencyCreditInvoicePaidTx(tx, invoice.ID, 2000, 7, "receipt-4")
		return err
	})
	require.ErrorIs(t, err, model.ErrAgencyCreditInvoiceClosed)

	var account model.AgencyFundingAccount
	require.NoError(t, app.db.Where("user_id = ?", user.Id).First(&account).Error)
	assert.Equal(t, int64(0), account.DebtQuota)
	var debt model.AgencyFundingDebt
	require.NoError(t, app.db.Where("user_id = ?", user.Id).First(&debt).Error)
	assert.Equal(t, int64(0), debt.OutstandingQuota)
	var ledgerCount int64
	require.NoError(t, app.db.Model(&model.AgencyFundingLedger{}).Where("user_id = ?", user.Id).Count(&ledgerCount).Error)
	assert.Equal(t, int64(2), ledgerCount)
}

func TestCreditBadDebtRequiresOverdueAndSuspendsCredit(t *testing.T) {
	app := newAgencyTestApp(t)
	user := creditTestUser(t, app, "credit-bad-debt")
	creditSeedDebt(t, app, int64(user.Id), 600)
	creditSeedTerms(t, app, int64(user.Id), 1000, 5, false)
	invoice := creditSeedInvoice(t, app, int64(user.Id), 600, model.AgencyCreditInvoiceStatusIssued)

	root := &Identity{ActorType: ActorTypeRoot, ActorID: 7}
	params := gin.Params{{Key: "id", Value: strconv.FormatInt(invoice.ID, 10)}}
	target := "/agency/api/v1/root/credit-invoices/" + strconv.FormatInt(invoice.ID, 10) + "/mark-bad-debt"
	ctx, recorder := creditTestRequest(http.MethodPost, target, `{"reason":"客户失联"}`, root, params)
	app.markCreditInvoiceBadDebt(ctx)
	require.Equal(t, http.StatusConflict, ctx.Writer.Status(), recorder.Body.String())

	require.NoError(t, app.db.Model(&model.AgencyCreditInvoice{}).Where("id = ?", invoice.ID).Update("status", model.AgencyCreditInvoiceStatusOverdue).Error)
	ctx, recorder = creditTestRequest(http.MethodPost, target, `{"reason":"客户失联"}`, root, params)
	app.markCreditInvoiceBadDebt(ctx)
	require.Equal(t, http.StatusOK, ctx.Writer.Status(), recorder.Body.String())

	var stored model.AgencyCreditInvoice
	require.NoError(t, app.db.First(&stored, invoice.ID).Error)
	assert.Equal(t, model.AgencyCreditInvoiceStatusBadDebt, stored.Status)
	assert.Equal(t, int64(600), stored.BadDebtQuota)
	assert.Equal(t, "客户失联", stored.BadDebtReason)

	var terms model.AgencyCreditTerms
	require.NoError(t, app.db.Where("user_id = ?", user.Id).First(&terms).Error)
	assert.Equal(t, model.AgencyCreditStatusSuspended, terms.Status)

	var debt model.AgencyFundingDebt
	require.NoError(t, app.db.Where("user_id = ?", user.Id).First(&debt).Error)
	assert.Equal(t, int64(600), debt.OutstandingQuota, "written-off debt stays visible as historical evidence")

	var creditInvoiceAudits int64
	require.NoError(t, app.db.Model(&model.AgencyAuditLog{}).Where("action = ?", "credit.invoice.mark_bad_debt").Count(&creditInvoiceAudits).Error)
	assert.Equal(t, int64(1), creditInvoiceAudits)
}

func TestCreditCSVCellEscapesSpreadsheetFormulas(t *testing.T) {
	assert.Equal(t, "'=1+1", csvSafeCell("=1+1"))
	assert.Equal(t, "'+SUM(A1)", csvSafeCell("+SUM(A1)"))
	assert.Equal(t, "'-2+3", csvSafeCell("-2+3"))
	assert.Equal(t, "'@cmd", csvSafeCell("@cmd"))
	assert.Equal(t, "deepseek-v4-flash", csvSafeCell("deepseek-v4-flash"))
}
