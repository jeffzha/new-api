package model

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupInvoiceTest(t *testing.T, actualMoney string) (*User, *TopUp) {
	t.Helper()
	user := setupPaymentSnapshotDB(t, "")
	require.NoError(t, DB.AutoMigrate(&InvoiceApplication{}, &InvoiceApplicationLine{}, &InvoiceApplicationEvent{}, &InvoiceApplicationDocument{}))
	snapshot, err := common.Marshal(TopupPaymentSnapshot{ActualMoney: actualMoney, CurrencyCode: "CNY", PaymentReference: "invoice-test-receipt"})
	require.NoError(t, err)
	topUp := &TopUp{UserId: user.Id, TradeNo: "invoice-test-topup", Status: common.TopUpStatusSuccess, PaymentSnapshot: string(snapshot), InvoiceReservedCents: 0, InvoiceIssuedCents: 0}
	require.NoError(t, DB.Create(topUp).Error)
	return user, topUp
}

func validInvoiceInput(topUpID int, cents int64) InvoiceApplicationInput {
	return InvoiceApplicationInput{
		PurchaserName: "Example Buyer Ltd",
		TaxNumber:     "91310000TEST000001",
		ReceiverEmail: "billing@example.com",
		Lines:         []InvoiceApplicationLineInput{{TopUpID: topUpID, AllocatedCents: cents}},
	}
}

func TestInvoiceSourcesRequireVerifiedPositiveCNYPayment(t *testing.T) {
	user, topUp := setupInvoiceTest(t, "12.34")
	otherSnapshot, err := common.Marshal(TopupPaymentSnapshot{PaymentReference: "reference-only"})
	require.NoError(t, err)
	missing := TopUp{UserId: user.Id, TradeNo: "invoice-missing-proof", Status: common.TopUpStatusSuccess, Money: 500, Amount: 50000, PaymentSnapshot: string(otherSnapshot)}
	require.NoError(t, DB.Create(&missing).Error)
	foreign := TopUp{UserId: user.Id + 10, TradeNo: "invoice-foreign", Status: common.TopUpStatusSuccess, PaymentSnapshot: topUp.PaymentSnapshot}
	require.NoError(t, DB.Create(&foreign).Error)
	sources, err := GetInvoiceSources(user.Id)
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, topUp.Id, sources[0].TopUpID)
	assert.Equal(t, int64(1234), sources[0].AvailableCents)
}

func TestInvoiceApplicationRejectAndIssuePreserveAmountLedger(t *testing.T) {
	user, topUp := setupInvoiceTest(t, "100.00")
	application, err := CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 7500))
	require.NoError(t, err)
	assert.Equal(t, InvoiceStatusPendingReview, application.Status)
	firstApplicationID := application.ID

	err = DB.First(topUp, topUp.Id).Error
	require.NoError(t, err)
	assert.Equal(t, int64(7500), topUp.InvoiceReservedCents)

	require.NoError(t, ReviewInvoiceApplication(44, common.RoleAdminUser, application.ID, false, "资料需要补充"))
	require.NoError(t, DB.First(topUp, topUp.Id).Error)
	assert.Zero(t, topUp.InvoiceReservedCents)
	firstEvents, err := GetInvoiceApplicationEvents(firstApplicationID, user.Id, false)
	require.NoError(t, err)
	require.Len(t, firstEvents, 2)
	assert.Equal(t, "review_reject", firstEvents[1].Action)

	application, err = CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 7500))
	require.NoError(t, err)
	require.NoError(t, ReviewInvoiceApplication(44, common.RoleAdminUser, application.ID, true, ""))
	require.NoError(t, IssueInvoiceApplication(45, common.RoleRootUser, application.ID, InvoiceIssueInput{
		InvoiceNumber: "REAL-INVOICE-001",
		InvoiceDate:   common.GetTimestamp(),
		AmountCents:   7500,
		FileName:      "invoice-1.pdf",
		SHA256:        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Data:          []byte("%PDF-1.7 test"),
	}))
	require.NoError(t, DB.First(topUp, topUp.Id).Error)
	assert.Zero(t, topUp.InvoiceReservedCents)
	assert.Equal(t, int64(7500), topUp.InvoiceIssuedCents)

	require.NoError(t, RequestInvoiceVoid(user.Id, application.ID, "重复申请"))
	require.NoError(t, ReviewInvoiceVoid(45, common.RoleRootUser, application.ID, true, "已完成外部作废"))
	require.NoError(t, DB.First(topUp, topUp.Id).Error)
	assert.Zero(t, topUp.InvoiceIssuedCents)
	require.ErrorIs(t, UpdateInvoiceEmailStatus(application.ID, "sent", ""), ErrInvoiceInvalidStatus)

	events, err := GetInvoiceApplicationEvents(application.ID, user.Id, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"submit", "review_approve", "issue", "void_request", "void_confirm"}, func() []string {
		actions := make([]string, 0, len(events))
		for _, event := range events {
			actions = append(actions, event.Action)
		}
		return actions
	}())
}

func TestInvoiceIssueRejectsDuplicateNumberAndRecomputesDigest(t *testing.T) {
	user, firstTopUp := setupInvoiceTest(t, "100.00")
	first, err := CreateInvoiceApplication(user.Id, validInvoiceInput(firstTopUp.Id, 5000))
	require.NoError(t, err)
	require.NoError(t, ReviewInvoiceApplication(44, common.RoleAdminUser, first.ID, true, ""))
	pdf := []byte("%PDF-1.7 first invoice")
	issueInput := InvoiceIssueInput{
		InvoiceNumber: "INV-2026-001",
		InvoiceDate:   common.GetTimestamp(),
		AmountCents:   4999,
		FileName:      "invoice-1.pdf",
		Data:          pdf,
	}
	require.ErrorIs(t, IssueInvoiceApplication(45, common.RoleRootUser, first.ID, issueInput), ErrInvoiceIssueAmountMismatch)
	issueInput.AmountCents = 5000
	require.NoError(t, IssueInvoiceApplication(45, common.RoleRootUser, first.ID, InvoiceIssueInput{
		InvoiceNumber: issueInput.InvoiceNumber,
		InvoiceDate:   issueInput.InvoiceDate,
		AmountCents:   issueInput.AmountCents,
		FileName:      issueInput.FileName,
		SHA256:        "not-the-document-digest",
		Data:          issueInput.Data,
	}))
	_, firstDocument, err := GetInvoiceDocument(first.ID)
	require.NoError(t, err)
	digest := sha256.Sum256(pdf)
	assert.Equal(t, hex.EncodeToString(digest[:]), firstDocument.SHA256)

	snapshot, err := common.Marshal(TopupPaymentSnapshot{ActualMoney: "100.00", CurrencyCode: "CNY", PaymentReference: "invoice-test-receipt-2"})
	require.NoError(t, err)
	secondTopUp := &TopUp{UserId: user.Id, TradeNo: "invoice-test-topup-2", Status: common.TopUpStatusSuccess, PaymentSnapshot: string(snapshot)}
	require.NoError(t, DB.Create(secondTopUp).Error)
	second, err := CreateInvoiceApplication(user.Id, validInvoiceInput(secondTopUp.Id, 5000))
	require.NoError(t, err)
	require.NoError(t, ReviewInvoiceApplication(44, common.RoleAdminUser, second.ID, true, ""))
	err = IssueInvoiceApplication(45, common.RoleRootUser, second.ID, InvoiceIssueInput{
		InvoiceNumber: " inv-2026-001 ",
		InvoiceDate:   common.GetTimestamp(),
		AmountCents:   5000,
		FileName:      "invoice-2.pdf",
		Data:          []byte("%PDF-1.7 second invoice"),
	})
	require.ErrorIs(t, err, ErrInvoiceDuplicateNumber)
}

func TestInvoiceIssueRejectsFutureDateAndInvalidSourceCounters(t *testing.T) {
	user, topUp := setupInvoiceTest(t, "100.00")
	application, err := CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 5000))
	require.NoError(t, err)
	require.NoError(t, ReviewInvoiceApplication(44, common.RoleAdminUser, application.ID, true, ""))
	err = IssueInvoiceApplication(45, common.RoleRootUser, application.ID, InvoiceIssueInput{
		InvoiceNumber: "INV-2026-FUTURE",
		InvoiceDate:   common.GetTimestamp() + 1,
		AmountCents:   5000,
		FileName:      "invoice.pdf",
		Data:          []byte("%PDF-1.7 invoice"),
	})
	require.ErrorIs(t, err, ErrInvoiceAmountInvalid)

	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", topUp.Id).Update("invoice_reserved_cents", int64(math.MinInt64)).Error)
	sources, err := GetInvoiceSources(user.Id)
	require.NoError(t, err)
	assert.Empty(t, sources)
	_, err = CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 1))
	require.ErrorIs(t, err, ErrInvoiceSourceUnavailable)
}

func TestInvoiceApplicationsCannotReserveMoreThanVerifiedPayment(t *testing.T) {
	user, topUp := setupInvoiceTest(t, "100.00")
	_, err := CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 10001))
	require.ErrorIs(t, err, ErrInvoiceSourceExhausted)

	application, err := CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 10000))
	require.NoError(t, err)
	assert.Equal(t, int64(10000), application.TotalCents)
	_, err = CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 1))
	require.ErrorIs(t, err, ErrInvoiceSourceExhausted)

	require.NoError(t, WithdrawInvoiceApplication(user.Id, application.ID))
	require.NoError(t, DB.First(topUp, topUp.Id).Error)
	assert.Zero(t, topUp.InvoiceReservedCents)
}

func TestConcurrentInvoiceApplicationsCannotDoubleReserveSource(t *testing.T) {
	user, topUp := setupInvoiceTest(t, "100.00")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			_, errs[i] = CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 7500))
		})
	}
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	assert.Equal(t, 1, successes)
	require.NoError(t, DB.First(topUp, topUp.Id).Error)
	assert.LessOrEqual(t, topUp.InvoiceReservedCents+topUp.InvoiceIssuedCents, int64(10000))
}

func TestInvoiceCentsRejectFractionsAndInvalidValues(t *testing.T) {
	for _, value := range []string{"", "0", "-1.00", "1.001", "not-money"} {
		_, err := invoiceCentsFromDecimal(value)
		require.Error(t, err, value)
	}
	cents, err := invoiceCentsFromDecimal("1234.56")
	require.NoError(t, err)
	assert.Equal(t, int64(123456), cents)
	assert.Equal(t, "1234.56", FormatInvoiceCents(cents))
}

func TestInvoiceSummaryRejectsCentsOverflow(t *testing.T) {
	user, _ := setupInvoiceTest(t, "92233720368547758.07")
	snapshot, err := common.Marshal(TopupPaymentSnapshot{
		ActualMoney:      "92233720368547758.07",
		CurrencyCode:     "CNY",
		PaymentReference: "invoice-overflow-receipt",
	})
	require.NoError(t, err)
	secondTopUp := &TopUp{
		UserId:          user.Id,
		TradeNo:         "invoice-overflow-topup-2",
		Status:          common.TopUpStatusSuccess,
		PaymentSnapshot: string(snapshot),
	}
	require.NoError(t, DB.Create(secondTopUp).Error)

	_, err = GetInvoiceSummary(user.Id)
	require.ErrorIs(t, err, ErrInvoiceAmountInvalid)
}

func TestInvoiceOwnershipAndStateGuardsPreserveReservations(t *testing.T) {
	user, topUp := setupInvoiceTest(t, "10.00")
	application, err := CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 1000))
	require.NoError(t, err)
	_, err = GetInvoiceApplication(application.ID, user.Id+1, false)
	require.ErrorIs(t, err, ErrInvoiceApplicationNotFound)
	require.ErrorIs(t, WithdrawInvoiceApplication(user.Id+1, application.ID), ErrInvoiceApplicationNotFound)
	_, err = GetInvoiceApplicationEvents(application.ID, user.Id+1, false)
	require.ErrorIs(t, err, ErrInvoiceApplicationNotFound)
	_, _, err = GetInvoiceDocument(application.ID)
	require.ErrorIs(t, err, ErrInvoiceInvalidStatus)
	require.ErrorIs(t, ReviewInvoiceApplication(44, common.RoleAdminUser, application.ID, false, ""), ErrInvoiceReviewReasonMissing)
	require.NoError(t, ReviewInvoiceApplication(44, common.RoleAdminUser, application.ID, true, ""))
	require.ErrorIs(t, WithdrawInvoiceApplication(user.Id, application.ID), ErrInvoiceInvalidStatus)
	require.ErrorIs(t, ReviewInvoiceApplication(44, common.RoleAdminUser, application.ID, true, ""), ErrInvoiceInvalidStatus)
	require.NoError(t, DB.First(topUp, topUp.Id).Error)
	assert.Equal(t, int64(1000), topUp.InvoiceReservedCents)
}

func TestInvoiceDocumentRejectsUnsafeNamesAndFilesWithoutConsumingReservation(t *testing.T) {
	user, topUp := setupInvoiceTest(t, "10.00")
	application, err := CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 1000))
	require.NoError(t, err)
	require.NoError(t, ReviewInvoiceApplication(44, common.RoleAdminUser, application.ID, true, ""))
	input := InvoiceIssueInput{InvoiceNumber: "REAL-42", InvoiceDate: common.GetTimestamp(), AmountCents: 1000, FileName: "invoice.pdf", Data: []byte("%PDF-1.7 test")}
	for _, name := range []string{"../invoice.pdf", "bad\rname.pdf", "bad\nname.pdf", "C:\\invoice.pdf"} {
		input.FileName = name
		require.ErrorIs(t, IssueInvoiceApplication(44, common.RoleAdminUser, application.ID, input), ErrInvoiceDocumentInvalid)
	}
	input.FileName = "invoice.pdf"
	input.Data = []byte("<html>not a PDF</html>")
	require.ErrorIs(t, IssueInvoiceApplication(44, common.RoleAdminUser, application.ID, input), ErrInvoiceDocumentInvalid)
	input.Data = make([]byte, maxInvoiceDocumentBytes+1)
	require.ErrorIs(t, IssueInvoiceApplication(44, common.RoleAdminUser, application.ID, input), ErrInvoiceDocumentTooLarge)
	require.NoError(t, DB.First(topUp, topUp.Id).Error)
	assert.Equal(t, int64(1000), topUp.InvoiceReservedCents)
	assert.Zero(t, topUp.InvoiceIssuedCents)
}

func TestInvoiceEmailFailureAndVoidRejectionRetainIssuedAmount(t *testing.T) {
	user, topUp := setupInvoiceTest(t, "10.00")
	application, err := CreateInvoiceApplication(user.Id, validInvoiceInput(topUp.Id, 1000))
	require.NoError(t, err)
	require.NoError(t, ReviewInvoiceApplication(44, common.RoleAdminUser, application.ID, true, ""))
	require.NoError(t, IssueInvoiceApplication(44, common.RoleAdminUser, application.ID, InvoiceIssueInput{InvoiceNumber: "REAL-43", InvoiceDate: common.GetTimestamp(), AmountCents: 1000, FileName: "invoice.pdf", Data: []byte("%PDF-1.7 test")}))
	require.NoError(t, UpdateInvoiceEmailStatus(application.ID, "failed", "delivery failed"))
	require.NoError(t, RequestInvoiceVoid(user.Id, application.ID, "Incorrect buyer"))
	require.NoError(t, ReviewInvoiceVoid(44, common.RoleAdminUser, application.ID, false, "External invoice remains valid"))
	saved, err := GetInvoiceApplication(application.ID, user.Id, false)
	require.NoError(t, err)
	assert.Equal(t, InvoiceStatusIssued, saved.Status)
	assert.Equal(t, "failed", saved.EmailStatus)
	require.NoError(t, UpdateInvoiceEmailStatus(application.ID, "sent", ""))
	require.NoError(t, DB.First(topUp, topUp.Id).Error)
	assert.Equal(t, int64(1000), topUp.InvoiceIssuedCents)
}

func TestInvoiceSQLiteUpgradeIsIdempotentAndPreservesExistingPayment(t *testing.T) {
	user, topUp := setupInvoiceTest(t, "12.34")
	require.NoError(t, DB.Migrator().DropColumn(&TopUp{}, "InvoiceReservedCents"))
	require.NoError(t, DB.Migrator().DropColumn(&TopUp{}, "InvoiceIssuedCents"))
	for range 2 {
		require.NoError(t, DB.AutoMigrate(&TopUp{}, &InvoiceApplication{}, &InvoiceApplicationLine{}, &InvoiceApplicationEvent{}, &InvoiceApplicationDocument{}))
	}
	require.NoError(t, DB.First(topUp, topUp.Id).Error)
	assert.Zero(t, topUp.InvoiceReservedCents)
	assert.Zero(t, topUp.InvoiceIssuedCents)
	sources, err := GetInvoiceSources(user.Id)
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, int64(1234), sources[0].AvailableCents)
	assert.True(t, DB.Migrator().HasIndex(&InvoiceApplication{}, "idx_invoice_number_key"))
}
