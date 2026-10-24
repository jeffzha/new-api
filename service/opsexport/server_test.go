package opsexport

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type refundHTTPProvider struct{ calls int }

func TestOpsExportAgencyIdentityMetadata(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:agency-identity-export-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.Agency{}, &model.AgencyOperatorAccount{}))
	token := strings.Repeat("identity-export-", 4)
	Default = &Server{DB: db, GatewayID: "g1", TokenHash: sha256.Sum256([]byte(token))}
	require.NoError(t, db.Create(&model.Agency{ID: 1, Code: "first", DisplayName: "First", Status: "active", InviteCode: "INV-FIRST"}).Error)
	require.NoError(t, db.Create(&model.Agency{ID: 2, Code: "second", DisplayName: "Second", Status: "disabled", InviteCode: "INV-SECOND"}).Error)
	require.NoError(t, db.Create(&model.AgencyOperatorAccount{AgencyID: 1, Username: "first-owner", NormalizedUsername: "first-owner", PasswordHash: "private-password-hash", Status: "active", AuthVersion: 3}).Error)
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func(query, credential string, expected int) map[string]any {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/agency-identities"+query, nil)
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, expected, response.Code, response.Body.String())
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		for _, secret := range []string{token, "private-password-hash", "passwordHash", "session", "payout", "payment"} {
			assert.NotContains(t, response.Body.String(), secret)
		}
		var result map[string]any
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
		return result
	}
	call("", "management-token", http.StatusUnauthorized)
	call("?page_size=51", token, http.StatusBadRequest)
	call("?page_size=1&page_size=2", token, http.StatusBadRequest)
	first := call("?page_size=1", token, http.StatusOK)
	assert.Equal(t, "1", first["items"].([]any)[0].(map[string]any)["externalAgencyId"])
	assert.Equal(t, float64(2), first["controlTotals"].(map[string]any)["agencies"])
	assert.Equal(t, float64(1), first["controlTotals"].(map[string]any)["operatorAccounts"])
	cursor := first["nextCursor"].(string)
	call("?cursor="+cursor+"tampered", token, http.StatusBadRequest)
	second := call("?page_size=1&cursor="+cursor, token, http.StatusOK)
	assert.Equal(t, first["snapshotId"], second["snapshotId"])
	assert.Equal(t, "2", second["items"].([]any)[0].(map[string]any)["externalAgencyId"])
	assert.Empty(t, second["nextCursor"])
	assert.NotEqual(t, first["pageHash"], second["pageHash"])
	assert.Equal(t, first["controlTotals"], second["controlTotals"])
	assert.Nil(t, second["items"].([]any)[0].(map[string]any)["operator"])
	expired := Default.signIdentityMetadataCursor(identityMetadataCursor{Gateway: "g1", Until: time.Now().Unix() - 1, MaxAgency: 2, AgencyCount: 2, OperatorCount: 1})
	call("?cursor="+expired, token, http.StatusBadRequest)
}

func TestOpsExportDebtHistory(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:debt-history-export-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.AgencyFundingDebt{}, &model.AgencyDebtRepayment{}))
	token := strings.Repeat("debt-export-", 4)
	Default = &Server{DB: db, GatewayID: "g1", TokenHash: sha256.Sum256([]byte(token))}
	require.NoError(t, db.Create(&model.AgencyFundingDebt{ID: 16, UserID: 7, OriginOperationID: "charge-1", DebtKind: "model_charge", OriginalQuota: 20, OutstandingQuota: 15}).Error)
	lotID := int64(11)
	require.NoError(t, db.Create(&model.AgencyDebtRepayment{ID: 17, DebtID: 16, RepaymentID: "repayment-1", FundingLotID: &lotID, SourceKind: "paid", Quota: 5, RestoredTotal: 5}).Error)
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func(query, credential string, expected int) map[string]any {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/debt-history"+query, nil)
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, expected, response.Code, response.Body.String())
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		assert.NotContains(t, response.Body.String(), token)
		var result map[string]any
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
		return result
	}
	call("", "management-token", http.StatusUnauthorized)
	call("?page_size=51", token, http.StatusBadRequest)
	call("?page_size=1&page_size=2", token, http.StatusBadRequest)
	first := call("?page_size=1", token, http.StatusOK)
	assert.Equal(t, "debts", first["resource"])
	assert.Equal(t, []any{float64(1), float64(1)}, first["controlTotals"])
	cursor := first["nextCursor"].(string)
	call("?cursor="+cursor+"tampered", token, http.StatusBadRequest)
	second := call("?page_size=1&cursor="+cursor, token, http.StatusOK)
	assert.Equal(t, first["snapshotId"], second["snapshotId"])
	assert.Equal(t, "repayments", second["resource"])
	assert.Empty(t, second["nextCursor"])
	item := second["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "7", item["externalUserId"])
	assert.Equal(t, "16", item["externalDebtId"])
	assert.NotEqual(t, first["pageHash"], second["pageHash"])
}

func TestOpsExportWalletHistory(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:wallet-history-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.AgencyFundingAccount{}, &model.AgencyFundingLot{}, &model.AgencyFundingAllocation{}, &model.AgencyFundingLedger{}, &model.AgencyTopupFact{}, &model.AgencyFundingReversal{}))
	token := strings.Repeat("wallet-export-", 4)
	Default = &Server{DB: db, GatewayID: "wallet-g1", TokenHash: sha256.Sum256([]byte(token))}
	require.NoError(t, db.Create(&model.AgencyFundingAccount{UserID: 7, PaidAvailable: 70, NonpaidAvailable: 5, MoneySeq: 2, Version: 3, OpeningSnapshot: "opening-secret", ReconcileReason: "private operator note"}).Error)
	require.NoError(t, db.Create(&model.AgencyFundingLot{ID: 11, UserID: 7, SourceKind: "payment", SourceID: "trade-1", SourceSnapshotJSON: "source-secret", PaidInitial: 100, PaidAvailable: 70, PaidConsumed: 30, MoneySeq: 1, Version: 1}).Error)
	require.NoError(t, db.Create(&model.AgencyFundingAllocation{ID: 12, ChargeID: "charge-1", ComponentID: "default", UserID: 7, LotID: 11, SourceSnapshotJSON: "allocation-secret", Consumed: 30, Version: 1}).Error)
	agencyID, bindingID, lotID := int64(9), int64(10), int64(11)
	require.NoError(t, db.Create(&model.AgencyFundingLedger{ID: 13, OperationID: "charge-1", UserID: 7, MoneySeq: 2, SourceKind: "billing", LotID: &lotID, PaidDelta: -30, PaidAfter: 70, NonpaidAfter: 5, AgencyID: &agencyID, BindingID: &bindingID}).Error)
	require.NoError(t, db.Create(&model.AgencyTopupFact{ID: 14, SourceOperationID: "topup-1", SourceID: "trade-1", UserID: 7, PaymentReference: "payment-reference-secret", ActualMoney: "10.00", CurrencyCode: "CNY", QuotaConversionSnapshot: `{"creditedQuota":100}`, CreditedQuota: 100, PaidQuota: 100, FundingSource: "payment", PaymentStatus: "success"}).Error)
	require.NoError(t, db.Create(&model.AgencyFundingReversal{ID: 15, RefundID: "refund-1", SourceOperationID: "topup-1", UserID: 7, Quota: 10, CurrencyCode: "CNY", PaymentReference: "refund-reference-secret", EvidenceRef: "evidence-secret", Reason: "operator-only reason"}).Error)
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func(query, credential string, expected int) map[string]any {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/wallet-history"+query, nil)
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, expected, response.Code, response.Body.String())
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		for _, secret := range []string{token, "opening-secret", "private operator note", "source-secret", "allocation-secret", "payment-reference-secret", "refund-reference-secret", "evidence-secret", "operator-only reason"} {
			assert.NotContains(t, response.Body.String(), secret)
		}
		var result map[string]any
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
		return result
	}
	call("", "management-token", http.StatusUnauthorized)
	call("?page_size=51", token, http.StatusBadRequest)
	call("?page_size=1&page_size=2", token, http.StatusBadRequest)
	first := call("?page_size=1", token, http.StatusOK)
	assert.Equal(t, "accounts", first["resource"])
	assert.Equal(t, false, first["consistentSnapshot"])
	totals := first["controlTotals"].(map[string]any)
	for _, resource := range []string{"accounts", "lots", "allocations", "ledgerEntries", "topups", "reversals"} {
		assert.Equal(t, float64(1), totals[resource])
	}
	cursor := first["nextCursor"].(string)
	call("?cursor="+cursor+"tampered", token, http.StatusBadRequest)
	for _, expected := range []string{"lots", "allocations", "ledgerEntries", "topups", "reversals"} {
		page := call("?page_size=1&cursor="+cursor, token, http.StatusOK)
		assert.Equal(t, first["snapshotId"], page["snapshotId"])
		assert.Equal(t, expected, page["resource"])
		assert.Len(t, page["pageHash"].(string), 64)
		cursor, _ = page["nextCursor"].(string)
	}
	assert.Empty(t, cursor)
}

func TestOpsExportHistoryCursorsRejectInvalidBoundaries(t *testing.T) {
	s := &Server{GatewayID: "boundary-g1", TokenHash: sha256.Sum256([]byte("boundary-token"))}
	price := priceVersionCursor{Gateway: s.GatewayID, Until: time.Now().Add(time.Hour).Unix(),
		MaxPlatformVersion: 10, PlatformCount: 2, Phase: pricePhasePlatform, After: 1}
	for _, invalid := range []priceVersionCursor{
		{Gateway: s.GatewayID, Until: price.Until, MaxPlatformVersion: -1, Phase: pricePhasePlatform},
		{Gateway: s.GatewayID, Until: price.Until, MaxPlatformVersion: 10, PlatformCount: -1, Phase: pricePhasePlatform},
		{Gateway: s.GatewayID, Until: price.Until, MaxPlatformVersion: 10, Phase: pricePhasePlatform, After: 11},
	} {
		_, err := s.priceCursor(s.signPriceCursor(invalid))
		require.Error(t, err)
	}
	_, err := s.priceCursor(s.signPriceCursor(price))
	require.NoError(t, err)
	wallet := walletHistoryCursor{Gateway: s.GatewayID, Until: price.Until, StartedAt: time.Now().Unix()}
	wallet.MaxIDs[0], wallet.Counts[0] = 10, 1
	for _, change := range []func(*walletHistoryCursor){
		func(page *walletHistoryCursor) { page.MaxIDs[0] = -1 },
		func(page *walletHistoryCursor) { page.Counts[1] = -1 },
		func(page *walletHistoryCursor) { page.After = 11 },
		func(page *walletHistoryCursor) { page.StartedAt = -1 },
	} {
		invalid := wallet
		change(&invalid)
		_, err := s.walletHistoryCursor(s.signWalletHistoryCursor(invalid))
		require.Error(t, err)
	}
	_, err = s.walletHistoryCursor(s.signWalletHistoryCursor(wallet))
	require.NoError(t, err)
}

func TestOpsExportDistributorDirectory(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:distributor-export-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	var sqliteVersion string
	require.NoError(t, db.Raw("SELECT sqlite_version()").Scan(&sqliteVersion).Error)
	t.Logf("directory export database: SQLite %s", sqliteVersion)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.Agency{}, &model.AgencyActiveUserBinding{}))
	token := strings.Repeat("directory-", 8)
	Default = &Server{DB: db, GatewayID: "g1", TokenHash: sha256.Sum256([]byte(token))}
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func(query, credential string, expected int) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/distributors"+query, nil)
		req.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, expected, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), token)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		var result map[string]any
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
		return result
	}
	call("", "management-token", 401)
	call("?page_size=51", token, 400)
	call("?page_size=1&page_size=2", token, 400)
	call("?unknown=1", token, 400)
	empty := call("", token, 200)
	assert.Empty(t, empty["items"])
	assert.Empty(t, empty["nextCursor"])
	parent := model.Agency{ID: 20, Depth: 1, Code: "parent", InviteCode: "secret-parent", DisplayName: "Parent", Status: "active"}
	child := model.Agency{ID: 10, ParentAgencyID: &parent.ID, Depth: 2, Code: "child", InviteCode: "secret-child", DisplayName: "Child", Status: "disabled"}
	require.NoError(t, db.Create(&parent).Error)
	require.NoError(t, db.Create(&child).Error)
	require.NoError(t, db.Create(&model.AgencyActiveUserBinding{UserID: 7, BindingID: 1, AgencyID: child.ID}).Error)
	first := call("?page_size=1", token, 200)
	rows := first["items"].([]any)
	assert.Equal(t, "20", rows[0].(map[string]any)["externalId"])
	assert.NotContains(t, rows[0].(map[string]any), "inviteCode")
	cursor := first["nextCursor"].(string)
	call("?cursor="+cursor+"tampered", token, 400)
	second := call("?page_size=1&cursor="+cursor, token, 200)
	assert.Equal(t, first["snapshotId"], second["snapshotId"])
	row := second["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "10", row["externalId"])
	assert.Equal(t, "20", row["parentExternalId"])
	assert.Equal(t, "inactive", row["status"])
	last := call("?page_size=1&cursor="+second["nextCursor"].(string), token, 200)
	assert.Equal(t, first["snapshotId"], last["snapshotId"])
	assert.Equal(t, "7", last["customers"].([]any)[0].(map[string]any)["externalUserId"])
	assert.Empty(t, last["nextCursor"])
	expired := Default.signDistributorCursor(distributorCursor{Gateway: "g1", Until: time.Now().Unix() - 1})
	call("?cursor="+expired, token, 400)
}

func TestOpsExportBindingHistory(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:binding-history-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.AgencyUserBinding{}, &model.AgencyActiveUserBinding{}))
	token := strings.Repeat("binding-history-", 4)
	Default = &Server{DB: db, GatewayID: "g1", TokenHash: sha256.Sum256([]byte(token))}
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func(query, credential string, status int) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/binding-history"+query, nil)
		req.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		require.Equal(t, status, response.Code, response.Body.String())
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		var result map[string]any
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
		return result
	}
	call("", "management-token", 401)
	call("?page_size=51", token, 400)
	call("?after=1", token, 400)
	call("?unknown=1", token, 400)
	ended := int64(2000)
	require.NoError(t, db.Create(&model.AgencyUserBinding{ID: 1, UserID: 7, AgencyID: 10, Revision: 1, EffectiveAtMS: 1000, EndedAtMS: &ended}).Error)
	require.NoError(t, db.Create(&model.AgencyUserBinding{ID: 2, UserID: 7, AgencyID: 20, Revision: 2, EffectiveAtMS: 2000}).Error)
	require.NoError(t, db.Create(&model.AgencyActiveUserBinding{UserID: 7, BindingID: 2, AgencyID: 20, Revision: 2}).Error)
	first := call("?page_size=1", token, 200)
	assert.Equal(t, false, first["consistentSnapshot"])
	assert.Equal(t, "2", first["through"])
	assert.Equal(t, "1", first["nextAfter"])
	row := first["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "2000", row["endedAtMs"])
	assert.Equal(t, "2", row["current"].(map[string]any)["bindingId"])
	call("?after=2&through=1", token, 400)
	require.NoError(t, db.Create(&model.AgencyUserBinding{ID: 3, UserID: 8, AgencyID: 30, Revision: 1, EffectiveAtMS: 3000}).Error)
	second := call("?after=1&through=2&page_size=1", token, 200)
	assert.Equal(t, "2", second["items"].([]any)[0].(map[string]any)["externalBindingId"])
	assert.Empty(t, second["nextAfter"])
	assert.Equal(t, "2", second["through"])
	assert.Len(t, second["pageHash"].(string), 64)
	next := call("", token, 200)
	assert.Equal(t, "3", next["through"])
	assert.Len(t, next["items"], 3)
}

func TestOpsExportPriceVersions(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:price-export-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(
		&model.Agency{}, &model.AgencyPlatformPriceState{}, &model.AgencyPlatformPriceVersion{},
		&model.AgencyPricePolicyVersion{}, &model.AgencyCustomerSalesOverride{},
	))
	token := strings.Repeat("pricing-", 8)
	Default = &Server{DB: db, GatewayID: "price-g1", TokenHash: sha256.Sum256([]byte(token))}
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func(query, credential string, expected int) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/price-versions"+query, nil)
		req.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, expected, w.Code, w.Body.String())
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		var result map[string]any
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
		return result
	}
	call("", "management-token", http.StatusUnauthorized)
	call("?page_size=51", token, http.StatusBadRequest)
	call("?unknown=1", token, http.StatusBadRequest)

	confirmed := true
	platformOne := agencycontract.PlatformPolicy{Revision: 1, ModelPrices: []agencycontract.PlatformModelPrice{{OriginModelName: "gpt-4.1-mini", AgencyCostBPS: 8000, ChildCostBPS: 8500, DefaultSalesBPS: 10000, PricingConfirmed: &confirmed}}}
	platformTwo := platformOne
	platformTwo.Revision = 2
	platformOneJSON, err := common.Marshal(platformOne)
	require.NoError(t, err)
	platformTwoJSON, err := common.Marshal(platformTwo)
	require.NoError(t, err)
	versions := []model.AgencyPlatformPriceVersion{
		{ID: 11, Revision: 1, PolicyJSON: string(platformOneJSON), PolicyHash: "platform-1", CreatedAtMS: 1000},
		{ID: 12, Revision: 2, PolicyJSON: string(platformTwoJSON), PolicyHash: "platform-2", CreatedAtMS: 2000},
	}
	require.NoError(t, db.Create(&versions).Error)
	require.NoError(t, db.Create(&model.AgencyPlatformPriceState{ID: 1, Revision: 2, CurrentVersionID: 12}).Error)
	agency := model.Agency{ID: 21, Depth: 1, Code: "north", DisplayName: "North", Status: "active"}
	require.NoError(t, db.Create(&agency).Error)
	agencyPolicy := agencycontract.Policy{Revision: 1, DefaultSettlementBPS: 8000, DefaultChildCostBPS: 8500, DefaultSalesBPS: 10000, SalesCapBPS: 30000}
	agencyJSON, err := common.Marshal(agencyPolicy)
	require.NoError(t, err)
	agencyVersion := model.AgencyPricePolicyVersion{ID: 31, AgencyID: agency.ID, Revision: 1, PolicyJSON: string(agencyJSON), PolicyHash: "agency-1", CreatedAtMS: 3000}
	require.NoError(t, db.Create(&agencyVersion).Error)
	require.NoError(t, db.Model(&agency).Updates(map[string]any{"current_policy_version_id": agencyVersion.ID, "price_revision": 1}).Error)
	modelKey, err := agencycontract.ModelKey("gpt-4.1-mini")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyCustomerSalesOverride{ID: 41, AgencyID: agency.ID, UserID: 7, ModelKey: modelKey, OriginModelName: "gpt-4.1-mini", SalesBPS: 11000, Revision: 3, CreatedAtMS: 4000, UpdatedAtMS: 5000}).Error)

	first := call("?page_size=1", token, http.StatusOK)
	require.False(t, first["consistentSnapshot"].(bool))
	require.Len(t, first["pageHash"].(string), 64)
	control := first["controlTotals"].(map[string]any)
	assert.Equal(t, float64(2), control["platformVersions"])
	assert.Equal(t, float64(1), control["agencyVersions"])
	assert.Equal(t, float64(1), control["customerOverrides"])
	item := first["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "platform_price_version", item["recordType"])
	assert.Equal(t, "historical", item["status"])
	assert.NotContains(t, item, "createdById")
	cursor := first["nextCursor"].(string)
	call("?cursor="+cursor+"x", token, http.StatusBadRequest)

	recordTypes := []string{"platform_price_version"}
	statuses := []string{"historical"}
	snapshot := first["snapshotId"].(string)
	for cursor != "" {
		page := call("?page_size=1&cursor="+cursor, token, http.StatusOK)
		assert.Equal(t, snapshot, page["snapshotId"])
		pageItems := page["items"].([]any)
		if len(pageItems) > 0 {
			entry := pageItems[0].(map[string]any)
			recordTypes = append(recordTypes, entry["recordType"].(string))
			statuses = append(statuses, entry["status"].(string))
		}
		cursor, _ = page["nextCursor"].(string)
	}
	assert.Equal(t, []string{"platform_price_version", "platform_price_version", "agency_price_version", "customer_sales_override"}, recordTypes)
	assert.Equal(t, []string{"historical", "published", "published", "current"}, statuses)
}

func TestOpsExportPriceVersionsSkipsEmptyLeadingPhase(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:price-empty-phase?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.Agency{}, &model.AgencyPlatformPriceState{}, &model.AgencyPlatformPriceVersion{},
		&model.AgencyPricePolicyVersion{}, &model.AgencyCustomerSalesOverride{}))
	token := strings.Repeat("empty-phase-", 4)
	Default = &Server{DB: db, GatewayID: "price-empty-g1", TokenHash: sha256.Sum256([]byte(token))}
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func() map[string]any {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/price-versions?page_size=1", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var payload map[string]any
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
		return payload
	}
	policy := agencycontract.Policy{Revision: 1, DefaultSettlementBPS: 8000, DefaultSalesBPS: 10000}
	encoded, err := common.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyPricePolicyVersion{ID: 3, AgencyID: 1, Revision: 1,
		PolicyJSON: string(encoded), CreatedAtMS: 1000}).Error)
	page := call()
	require.Len(t, page["items"], 1)
	assert.Equal(t, "agency_price_version", page["items"].([]any)[0].(map[string]any)["recordType"])
	assert.Empty(t, page["nextCursor"])
	require.NoError(t, db.Delete(&model.AgencyPricePolicyVersion{}, 3).Error)
	empty := call()
	assert.Equal(t, []any{}, empty["items"])
	assert.Empty(t, empty["nextCursor"])
}

func TestOpsExportPriceVersionsSkipsEmptyMiddlePhase(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:price-empty-middle?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.Agency{}, &model.AgencyPlatformPriceState{},
		&model.AgencyPlatformPriceVersion{}, &model.AgencyPricePolicyVersion{}, &model.AgencyCustomerSalesOverride{}))
	token := strings.Repeat("empty-middle-", 4)
	Default = &Server{DB: db, GatewayID: "price-middle-g1", TokenHash: sha256.Sum256([]byte(token))}
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	policy, err := common.Marshal(agencycontract.PlatformPolicy{Revision: 1})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyPlatformPriceVersion{ID: 1, Revision: 1, PolicyJSON: string(policy)}).Error)
	require.NoError(t, db.Create(&model.AgencyCustomerSalesOverride{ID: 2, AgencyID: 3, UserID: 4, ModelKey: "m", SalesBPS: 10000}).Error)
	call := func(cursor string) map[string]any {
		path := "/api/ops-export/v1/price-versions?page_size=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var payload map[string]any
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
		return payload
	}
	first := call("")
	require.Len(t, first["items"], 1)
	assert.Equal(t, "platform_price_version", first["items"].([]any)[0].(map[string]any)["recordType"])
	second := call(first["nextCursor"].(string))
	require.Len(t, second["items"], 1)
	assert.Equal(t, "customer_sales_override", second["items"].([]any)[0].(map[string]any)["recordType"])
	assert.Empty(t, second["nextCursor"])
}

func (p *refundHTTPProvider) Name() string { return "http-simulated" }
func (p *refundHTTPProvider) Submit(ctx context.Context, i model.CNRefundIntent) (model.CNRefundProviderResult, error) {
	p.calls++
	return model.CNRefundProviderResult{Intent: i, Status: "unknown_reconcile"}, nil
}
func (p *refundHTTPProvider) Query(ctx context.Context, i model.CNRefundIntent) (model.CNRefundProviderResult, error) {
	return model.CNRefundProviderResult{Intent: i, Status: "unknown_reconcile"}, nil
}

func TestOpsExportRefundCredentialScopeAndReservation(t *testing.T) {
	previous, oldDB := Default, model.DB
	oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	t.Cleanup(func() {
		Default, model.DB = previous, oldDB
		common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldBatch
		common.SetDatabaseTypes(oldMain, oldLog)
	})
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	db, err := gorm.Open(sqlite.Open("file:refund-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	require.NoError(t, model.MigrateAgency(db))
	exportToken, mutationToken := strings.Repeat("export-", 8), strings.Repeat("refund-", 8)
	exportHash, mutationHash := sha256.Sum256([]byte(exportToken)), sha256.Sum256([]byte(mutationToken))
	t.Setenv("OPS_EXPORT_TOKEN_SHA256", fmt.Sprintf("%x", exportHash))
	t.Setenv("OPS_GATEWAY_ID", "refund-http")
	t.Setenv("UPSTREAM_EVENT_GATEWAY_ID", "")
	t.Setenv("OPS_REFUND_TENANT_ID", "tenant-http")
	t.Setenv("OPS_REFUND_MUTATION_TOKEN_SHA256", fmt.Sprintf("%x", exportHash))
	require.Error(t, StartConfigured(db))
	t.Setenv("OPS_REFUND_MUTATION_TOKEN_SHA256", fmt.Sprintf("%x", mutationHash))
	require.NoError(t, StartConfigured(db))
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	u := model.User{Username: "refund-http", AffCode: "refund-http", BillingMode: model.AgencyDurableBillingMode, Quota: 100}
	require.NoError(t, db.Create(&u).Error)
	require.NoError(t, db.Create(&model.AgencyFundingAccount{UserID: int64(u.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.RecordAgencyTopup(tx, int64(u.Id), "payment", "http-payment", "payment_callback", 100, 0, &model.TopupFundingSnapshot{Payment: &model.TopupPaymentSnapshot{ActualMoney: "80", CurrencyCode: "CNY", PaymentReference: "simulated-payment"}})
	}))
	_, err = model.TryReserveUserQuotaAndAgency(u.Id, 25, "http-charge", 25)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.CaptureCNAgencyFundingTx(tx, agencycontract.BillingEvent{EventID: "http-final", EventType: "agency.billing_finalized", FinancialChargeID: "http-charge", UserID: int64(u.Id), JournalRevision: 1, OperationID: "http-final"})
	}))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterV2(router.Group("/api/ops-export/v2"))
	call := func(method, path, token, key string, body any) *httptest.ResponseRecorder {
		raw, err := common.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(method, "/api/ops-export/v2"+path, strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		}
		assert.NotContains(t, w.Body.String(), mutationToken)
		return w
	}
	artifact := func(name string, body []byte) {
		if dir := os.Getenv("S4R_TEST_ARTIFACT_DIR"); dir != "" {
			require.NoError(t, os.MkdirAll(dir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, name+".json"), body, 0600))
		}
	}
	payment := model.CNIdentity("agency-payment", "http-payment")
	lookup := "/refund-balances?tenantId=tenant-http&gatewayId=refund-http&originalPaymentId=" + payment
	assert.Equal(t, 401, call("GET", lookup, exportToken, "", nil).Code)
	w := call("GET", lookup, mutationToken, "", nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	var balance model.CNRefundBalance
	artifact("refund-balance", w.Body.Bytes())
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &balance))
	assert.Equal(t, "60", balance.AvailableRefundable)
	r := model.CNRefundReserveRequest{TenantID: "tenant-http", GatewayID: "refund-http", OperationID: "http-refund", ApprovalID: "finance-approval", ApprovalHash: strings.Repeat("a", 64), OriginalPaymentID: payment, LotID: balance.LotID, UserID: balance.UserID, Amount: "60", Currency: "CNY", ExpectedFundingVersion: balance.FundingVersion, ExpectedLotVersion: balance.LotVersion}
	assert.Equal(t, 401, call("POST", "/refund-reservations", exportToken, r.OperationID, r).Code)
	assert.Equal(t, 401, call("POST", "/refund-reservations", "management-token", r.OperationID, r).Code)
	assert.Equal(t, 400, call("POST", "/refund-reservations", mutationToken, "wrong-key", r).Code)
	bad := r
	bad.TenantID = "other-tenant"
	assert.Equal(t, 403, call("POST", "/refund-reservations", mutationToken, r.OperationID, bad).Code)
	bad = r
	bad.GatewayID = "other-gateway"
	assert.Equal(t, 403, call("POST", "/refund-reservations", mutationToken, r.OperationID, bad).Code)
	bad = r
	bad.Amount = "61"
	assert.Equal(t, 409, call("POST", "/refund-reservations", mutationToken, r.OperationID, bad).Code)
	w = call("POST", "/refund-reservations", mutationToken, r.OperationID, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	var receipt model.CNRefundReceipt
	artifact("refund-reservation", w.Body.Bytes())
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &receipt))
	assert.Equal(t, "reserved", receipt.Status)
	require.Equal(t, w.Body.String(), call("POST", "/refund-reservations", mutationToken, r.OperationID, r).Body.String())
	c := model.CNRefundCommand{AttemptID: "attempt-1", TenantID: r.TenantID, GatewayID: r.GatewayID, OperationID: r.OperationID, ApprovalID: r.ApprovalID, ApprovalHash: r.ApprovalHash, ExpectedVersion: receipt.Version}
	path := "/refund-reservations/" + r.OperationID + "/execution-intents"
	assert.Equal(t, 503, call("POST", path, mutationToken, r.OperationID, c).Code)
	provider := &refundHTTPProvider{}
	Default.Refund.Provider = provider
	c.ApprovalHash = strings.Repeat("b", 64)
	assert.Equal(t, 409, call("POST", path, mutationToken, r.OperationID, c).Code)
	assert.Zero(t, provider.calls)
	c.ApprovalHash = r.ApprovalHash
	raw, err := common.Marshal(c)
	require.NoError(t, err)
	var injected map[string]any
	require.NoError(t, common.Unmarshal(raw, &injected))
	injected["result"] = "succeeded"
	assert.Equal(t, 400, call("POST", path, mutationToken, r.OperationID, injected).Code)
	assert.Zero(t, provider.calls)
	w = call("POST", path, mutationToken, r.OperationID, c)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "unknown_reconcile")
	assert.Equal(t, 1, provider.calls)
	artifact("refund-unknown", w.Body.Bytes())
	assert.Equal(t, w.Body.String(), call("POST", path, mutationToken, r.OperationID, c).Body.String())
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 404, call("POST", "/refund-reservations/"+r.OperationID+"/release", mutationToken, r.OperationID, c).Code)
	common.RedisEnabled = true
	bad = r
	bad.OperationID = "unsupported-redis"
	assert.Equal(t, 409, call("POST", "/refund-reservations", mutationToken, bad.OperationID, bad).Code)
}

func TestOpsExportCredentialAndCursorBoundary(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	Default = nil
	db, err := gorm.Open(sqlite.Open("file:ops-export-http?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.AgencyBillingOutbox{}, &model.AgencyBillingOperation{}))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	RegisterV2(router.Group("/api/ops-export/v2"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/facts", nil))
	assert.Equal(t, 404, w.Code)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/ops-export/v2/facts", nil))
	assert.Equal(t, 404, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"source_unavailable"`)
	t.Setenv("OPS_EXPORT_TOKEN_SHA256", "invalid")
	require.Error(t, StartConfigured(db))
	token := strings.Repeat("fixture-", 8)
	digest := sha256.Sum256([]byte(token))
	t.Setenv("OPS_EXPORT_TOKEN_SHA256", fmt.Sprintf("%x", digest))
	t.Setenv("OPS_GATEWAY_ID", "g1")
	t.Setenv("UPSTREAM_EVENT_GATEWAY_ID", "g2")
	require.Error(t, StartConfigured(db))
	t.Setenv("UPSTREAM_EVENT_GATEWAY_ID", "g1")
	require.NoError(t, StartConfigured(db))
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &model.User{}, &model.Token{}))
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "inventory", DisplayName: "Inventory user", Password: "s4-password-never-export", Email: "s4-email-never-export@example.test"}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: "s4-token-secret-never-export", Name: "Inventory token"}).Error)
	require.NoError(t, db.Create(&model.Model{ModelName: "s4-hidden", Endpoints: `["openai"]`}).Error)
	require.NoError(t, db.Model(&model.Model{}).Where("model_name = ?", "s4-hidden").Update("status", 0).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 1, Models: "s4-disabled", Status: 2, Key: "s4-channel-secret-never-export"}).Error)
	var fixture struct {
		Items []model.CNFact `json:"items"`
	}
	fixtureJSON, err := os.ReadFile(filepath.Join("..", "..", "model", "testdata", "cn-contract", "finance-v2-facts.json"))
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(fixtureJSON, &fixture))
	for _, fact := range fixture.Items {
		require.NoError(t, model.StageCNFactTx(db, fact))
	}
	for _, tc := range []struct {
		path, credential string
		status           int
	}{
		{"/facts", "", 401}, {"/facts", "Bearer management-token", 401},
		{"/facts", "Bearer " + token + "wrong", 401}, {"/facts", "Bearer " + token, 200},
		{"/facts?limit=1001", "Bearer " + token, 400}, {"/facts?limit=0", "Bearer " + token, 400},
		{"/facts?limit=1&limit=2", "Bearer " + token, 400}, {"/facts?after=bad", "Bearer " + token, 400},
		{"/facts?after=djI6LTE", "Bearer " + token, 400}, {"/daily?date=2026-02-30", "Bearer " + token, 400},
		{"/daily?date=2026-10-14", "Bearer " + token, 200},
		{"/catalog", "Bearer " + token, 200},
		{"/inventory", "", 401}, {"/inventory", "Bearer management-token", 401},
		{"/inventory", "Bearer " + token, 200}, {"/inventory?limit=1", "Bearer " + token, 400},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1"+tc.path, nil)
		request.Header.Set("Authorization", tc.credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		assert.Equal(t, tc.status, w.Code, tc.path)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.NotContains(t, w.Body.String(), token)
		assert.NotContains(t, w.Body.String(), "s4-channel-secret-never-export")
		assert.NotContains(t, w.Body.String(), "s4-password-never-export")
		assert.NotContains(t, w.Body.String(), "s4-email-never-export")
		assert.NotContains(t, w.Body.String(), "s4-token-secret-never-export")
		if directory := os.Getenv("S4_TEST_ARTIFACT_DIR"); directory != "" && (tc.path == "/catalog" || tc.path == "/inventory") && tc.status == 200 {
			require.NoError(t, os.MkdirAll(directory, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(directory, strings.TrimPrefix(tc.path, "/")+".json"), w.Body.Bytes(), 0600))
		}
		if tc.path == "/facts" && tc.status == 200 {
			var result map[string]any
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
			assert.Equal(t, "djI6MA", result["nextCursor"])
			assert.Equal(t, "g1", result["gatewayId"])
		}
	}
	w = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/ops-export/v1/facts", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, request)
	assert.Equal(t, 404, w.Code)
	for _, tc := range []struct {
		path, credential, code string
		status                 int
	}{
		{"/facts", "Bearer management-token", "unauthorized", 401},
		{"/facts?after=djI6MA", "Bearer " + token, "invalid_cursor", 400},
		{"/facts?limit=1&limit=2", "Bearer " + token, "invalid_payload", 400},
		{"/facts?extra=1", "Bearer " + token, "invalid_payload", 400},
		{"/daily?date=2026-02-30", "Bearer " + token, "invalid_payload", 400},
		{"/facts", "Bearer " + token, "", 200},
		{"/refund-capabilities", "Bearer " + token, "", 200},
		{"/daily?date=2026-10-14", "Bearer " + token, "", 200},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v2"+tc.path, nil)
		request.Header.Set("Authorization", tc.credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		require.Equal(t, tc.status, w.Code, w.Body.String())
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.NotContains(t, w.Body.String(), token)
		var body map[string]any
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &body))
		if tc.code != "" {
			failure := body["error"].(map[string]any)
			assert.Equal(t, tc.code, failure["code"])
			assert.Equal(t, false, failure["retryable"])
			assert.Nil(t, failure["operationId"])
		} else if tc.path == "/refund-capabilities" {
			assert.Equal(t, false, body["reservationAvailable"])
			assert.Equal(t, false, body["executionAvailable"])
			assert.Equal(t, false, body["manualVoucherEvidenceAvailable"])
		} else {
			assert.Equal(t, model.CNFinanceSource, body["sourceSystem"])
			if dependencies, ok := body["dependencies"].([]any); ok {
				for _, value := range dependencies {
					dependency := value.(map[string]any)
					from, to := dependency["coverageFrom"].(string), dependency["coverageTo"].(string)
					for _, timestamp := range []string{from, to} {
						parsed, err := time.Parse(model.CNTimeLayout, timestamp)
						require.NoError(t, err)
						assert.Equal(t, timestamp, parsed.UTC().Format(model.CNTimeLayout))
					}
					if from == to {
						assert.NotEqual(t, "complete", dependency["status"])
					}
				}
			}
		}
		if directory := os.Getenv("S4_TEST_ARTIFACT_DIR"); directory != "" && tc.status == 200 {
			require.NoError(t, os.MkdirAll(directory, 0700))
			name := strings.Split(strings.TrimPrefix(tc.path, "/"), "?")[0]
			require.NoError(t, os.WriteFile(filepath.Join(directory, name+".json"), w.Body.Bytes(), 0600))
		}
	}
}

func TestOpsExportSubscriptionActualHTTP(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_SUBSCRIPTION_ENABLED", "true")
	previousDB, previousServer := model.DB, Default
	oldBatch, oldRedis, oldUnit, oldRate := common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit, operation_setting.USDExchangeRate
	oldDisplay := operation_setting.GetGeneralSetting().QuotaDisplayType
	t.Cleanup(func() {
		model.DB, Default = previousDB, previousServer
		common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit, operation_setting.USDExchangeRate = oldBatch, oldRedis, oldUnit, oldRate
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplay
	})
	common.BatchUpdateEnabled, common.RedisEnabled = false, false
	common.QuotaPerUnit, operation_setting.USDExchangeRate = 10, 7
	operation_setting.GetGeneralSetting().QuotaDisplayType = "CNY"
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "subscription-http.db")), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	require.NoError(t, model.MigrateCNFinance(db, "g1-subscription"))
	credential := strings.Repeat("subscription-fixture-", 3)
	Default = &Server{DB: db, GatewayID: "g1-subscription", TokenHash: sha256.Sum256([]byte(credential))}
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "subscription-http", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: "subscription-secret-never-export", RemainQuota: 1000}).Error)
	require.NoError(t, db.Create(&model.SubscriptionPlan{Id: 1, Title: "subscription-http", TotalAmount: 1000, QuotaResetPeriod: model.SubscriptionResetNever}).Error)
	require.NoError(t, db.Create(&model.UserSubscription{Id: 1, UserId: 1, PlanId: 1, AmountTotal: 1000, Status: "active", StartTime: time.Now().Unix(), EndTime: time.Now().Add(24 * time.Hour).Unix()}).Error)
	for _, tc := range []struct {
		request string
		actual  int
		cancel  bool
	}{{"subscription-final-100", 100, false}, {"subscription-actual-zero", 0, false}, {"subscription-cancelled", 0, true}} {
		_, err = model.PreConsumeCNSubscription(tc.request, 1, 100, 1, false, false)
		require.NoError(t, err)
		if tc.cancel {
			require.NoError(t, model.RequestCNSubscriptionCancellation(db, tc.request, 1))
			require.NoError(t, model.CompleteCNSubscriptionCancellation(context.Background(), db, tc.request))
		} else {
			require.NoError(t, model.FinalizeCNSubscription(tc.request, 1, tc.actual))
		}
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterV2(router.Group("/api/ops-export/v2"))
	for _, name := range []string{"facts", "daily"} {
		path := "/api/ops-export/v2/" + name
		if name == "daily" {
			path += "?date=" + time.Now().UTC().Format("2006-01-02")
		}
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.NotContains(t, response.Body.String(), "subscription-secret-never-export")
		assert.NotContains(t, response.Body.String(), credential)
		if name == "facts" {
			var facts struct {
				Items []model.CNFact `json:"items"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &facts))
			require.Len(t, facts.Items, 3)
			for _, fact := range facts.Items {
				assert.Equal(t, "subscription", fact.BillingSource)
				require.NotNil(t, fact.Cumulative.Money)
				assert.Equal(t, "7", fact.Conversion["exchangeRate"])
				assert.Equal(t, "cn_subscription_lifecycle_final", fact.SourceEvidence[0].RecordType)
			}
		}
		if directory := os.Getenv("S4_TEST_ARTIFACT_DIR"); directory != "" {
			require.NoError(t, os.MkdirAll(directory, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "subscription-"+name+".json"), response.Body.Bytes(), 0600))
		}
	}
}
