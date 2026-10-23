package opsexport

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOpsExportFinanceHistoryRedactsSecretsAndPages(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:finance-history-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.AgencyCommissionLedger{}, &model.AgencyCommissionBalance{}, &model.AgencyWithdrawalAccount{}, &model.AgencyWithdrawal{}, &model.AgencyWithdrawalTransition{}, &model.AgencyAuditLog{}))
	token := strings.Repeat("finance-export-", 4)
	Default = &Server{DB: db, GatewayID: "finance-g1", TokenHash: sha256.Sum256([]byte(token))}
	parent := int64(4)
	require.NoError(t, db.Create(&model.AgencyCommissionLedger{ID: 1, EventID: "commission-event-1", ComponentID: "spread", ComponentKey: "agency:4", EntryType: "earned", AgencyID: 4, BindingID: 8, UserID: 9, OriginModelName: "gpt-4.1-mini", CommissionQuota: 30, AmountMicros: 1200, CurrencyCode: "CNY", OccurredAtMS: 100, ParentAgencyID: &parent}).Error)
	require.NoError(t, db.Create(&model.AgencyCommissionBalance{ID: 2, AgencyID: 4, CurrencyCode: "CNY", EarnedMicros: 1200, AvailableMicros: 1200, Version: 2, UpdatedAtMS: 101}).Error)
	require.NoError(t, db.Create(&model.AgencyWithdrawalAccount{ID: 3, AgencyID: 4, Version: 1, Ciphertext: "account-secret", KeyID: "key-1", Last4: "7788", Status: "active", CreatedAtMS: 102}).Error)
	require.NoError(t, db.Create(&model.AgencyWithdrawal{ID: 4, RequestNo: "WD-0001", AgencyID: 4, CurrencyCode: "CNY", AmountMicros: 5000, Status: "paid", Version: 2, AccountID: 3, AccountVersion: 1, AccountSnapshot: "snapshot-secret", AccountSnapshotHash: "snapshot-hash", AccountSnapshotKeyID: "key-1", PaymentChannel: "bank", PaymentReference: "payment-secret", OnHoldReason: "private-reason", CreatedAtMS: 103, UpdatedAtMS: 104}).Error)
	require.NoError(t, db.Create(&model.AgencyWithdrawalTransition{ID: 5, WithdrawalID: 4, OperationID: "wd-op-1", BeforeStatus: "pending", AfterStatus: "paid", ActorType: "root", ActorID: 99, Evidence: "evidence-secret", CreatedAtMS: 105}).Error)
	require.NoError(t, db.Create(&model.AgencyAuditLog{ID: 6, EventID: "audit-event-1", ActorType: "root", ActorID: 99, ActingAgencyID: &parent, Action: "withdrawal.paid", ObjectType: "withdrawal", ObjectID: "4", RequestID: "req-1", Reason: "audit-secret-reason", BeforeJSON: "before-secret", AfterJSON: "after-secret", SourceIP: "10.0.0.1", CreatedAtMS: 106}).Error)
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func(path string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		for _, secret := range []string{"account-secret", "snapshot-secret", "payment-secret", "private-reason", "evidence-secret", "audit-secret-reason", "before-secret", "after-secret", "10.0.0.1"} {
			assert.NotContains(t, w.Body.String(), secret)
		}
		var payload map[string]any
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &payload))
		return payload
	}
	commission := call("/api/ops-export/v1/commission-history?page_size=1")
	assert.Equal(t, "ledgerEntries", commission["resource"])
	assert.NotEmpty(t, commission["nextCursor"])
	commissionBalance := call("/api/ops-export/v1/commission-history?page_size=1&cursor=" + commission["nextCursor"].(string))
	assert.Equal(t, "balances", commissionBalance["resource"])
	assert.Empty(t, commissionBalance["nextCursor"])
	withdrawals := call("/api/ops-export/v1/withdrawals?page_size=1")
	assert.Equal(t, "accounts", withdrawals["resource"])
	assert.NotEmpty(t, withdrawals["nextCursor"])
	withdrawalRows := call("/api/ops-export/v1/withdrawals?page_size=1&cursor=" + withdrawals["nextCursor"].(string))
	assert.Equal(t, "withdrawals", withdrawalRows["resource"])
	assert.NotEmpty(t, withdrawalRows["nextCursor"])
	transitions := call("/api/ops-export/v1/withdrawals?page_size=1&cursor=" + withdrawalRows["nextCursor"].(string))
	assert.Equal(t, "transitions", transitions["resource"])
	assert.Empty(t, transitions["nextCursor"])
	audit := call("/api/ops-export/v1/audit-history?page_size=1")
	assert.Equal(t, "auditLogs", audit["resource"])
	assert.Empty(t, audit["nextCursor"])
	assert.Equal(t, true, audit["consistentSnapshot"])
}

func TestOpsExportFinanceHistoryRejectsNegativeCursorBoundary(t *testing.T) {
	s := &Server{GatewayID: "finance-g1", TokenHash: sha256.Sum256([]byte("cursor-test"))}
	valid := financeHistoryCursor{Gateway: s.GatewayID, Domain: "withdrawals", Until: 4102444800,
		MaxIDs: []int64{1, 2, 3}, Counts: []int64{1, 2, 3}}
	for _, change := range []func(*financeHistoryCursor){
		func(page *financeHistoryCursor) { page.MaxIDs[1] = -1 },
		func(page *financeHistoryCursor) { page.Counts[2] = -1 },
	} {
		page := valid
		page.MaxIDs = append([]int64(nil), valid.MaxIDs...)
		page.Counts = append([]int64(nil), valid.Counts...)
		change(&page)
		_, err := s.parseFinanceHistoryCursor(s.signFinanceHistoryCursor(page), "withdrawals", 3)
		require.Error(t, err)
	}
}

func TestOpsExportFinanceRangeResumesAtFixedBoundary(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:finance-range-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.AgencyCommissionLedger{}))
	token := strings.Repeat("finance-range-", 4)
	Default = &Server{DB: db, GatewayID: "finance-g1", TokenHash: sha256.Sum256([]byte(token))}
	for id := int64(1); id <= 3; id++ {
		require.NoError(t, db.Create(&model.AgencyCommissionLedger{ID: id, EventID: "event-" + strconv.FormatInt(id, 10), ComponentID: "spread", EntryType: "earned", AgencyID: 4, CurrencyCode: "CNY"}).Error)
	}
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func(path, auth string, status int) map[string]any {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, status, w.Code, w.Body.String())
		var payload map[string]any
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &payload))
		return payload
	}
	base := "/api/ops-export/v1/finance-ranges/commission-history/ledgerEntries"
	call(base, "Bearer wrong", 401)
	call("/api/ops-export/v1/finance-ranges/debt-history/repayments", "Bearer "+token, 400)
	first := call(base+"?page_size=1", "Bearer "+token, 200)
	assert.Equal(t, "3", first["through"])
	assert.Equal(t, "1", first["nextAfter"])
	assert.Equal(t, true, first["hasMore"])
	require.NoError(t, db.Create(&model.AgencyCommissionLedger{ID: 4, EventID: "new-event", ComponentID: "spread", EntryType: "earned", AgencyID: 4, CurrencyCode: "CNY"}).Error)
	second := call(base+"?page_size=2&after=1&through=3", "Bearer "+token, 200)
	assert.Equal(t, "3", second["through"])
	assert.Equal(t, false, second["hasMore"])
	items := second["items"].([]any)
	require.Len(t, items, 2)
	assert.Equal(t, "2", items[0].(map[string]any)["externalEntryId"])
	assert.Equal(t, "3", items[1].(map[string]any)["externalEntryId"])
	for _, suffix := range []string{"?after=1", "?through=3&after=4", "?through=-1", "?through=3&page_size=201", "?through=3&bad=1"} {
		call(base+suffix, "Bearer "+token, 400)
	}
}

func TestOpsExportWalletRangeRejectsTamperedBoundaries(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:wallet-range-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.AgencyFundingLedger{}))
	token := strings.Repeat("wallet-range-", 4)
	Default = &Server{DB: db, GatewayID: "finance-g1", TokenHash: sha256.Sum256([]byte(token))}
	for id := int64(1); id <= 3; id++ {
		require.NoError(t, db.Create(&model.AgencyFundingLedger{ID: id, OperationID: "op-" + strconv.FormatInt(id, 10), EntryNo: int(id), UserID: 9, SourceKind: "topup"}).Error)
	}
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func(query string, status int) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/finance-ranges/wallet-history/ledgerEntries"+query, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, status, w.Code, w.Body.String())
		var payload map[string]any
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &payload))
		return payload
	}
	first := call("?page_size=1", 200)
	assert.Equal(t, "3", first["through"])
	assert.Equal(t, "1", first["nextAfter"])
	assert.Equal(t, false, first["consistentSnapshot"])
	require.NoError(t, db.Create(&model.AgencyFundingLedger{ID: 4, OperationID: "new", EntryNo: 4, UserID: 9, SourceKind: "topup"}).Error)
	last := call("?after=1&through=3&page_size=2", 200)
	assert.Equal(t, false, last["hasMore"])
	assert.Len(t, last["items"], 2)
	assert.Empty(t, call("?after=3&through=3", 200)["items"])
	call("?after=0&through=0", 400)
}
