package opswallet

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSignedAdjustmentRetainsOperationAndValidatesReceipt(t *testing.T) {
	a := Adjustment{RequestID: "adm-test", UserID: "15", Operation: "add", Amount: "0.000001", Operator: "admin", Reason: "test grant"}
	var first []byte
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/test"+Path, r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		expected := opsclient.SignWithNonce("project", "test-adjustment-secret", "POST", Path, body, r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"))
		assert.Equal(t, expected.Get("X-Signature"), r.Header.Get("X-Signature"))
		if count == 0 {
			first = body
		} else {
			assert.Equal(t, first, body)
		}
		count++
		if count == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"requestId":"adm-test","userId":"15","operation":"add","amount":"0.000001","change":"0.000001","balanceBefore":"1","balance":"1.000001","balanceQuota":68493,"ledgerEntryId":7,"recordedAt":"2026-11-12T01:00:00Z","station":{"quotaPerUnit":500000,"usdExchangeRate":"7.3"}}}`))
	}))
	defer server.Close()
	c := opsclient.New(server.URL+"/test", "project", "test-adjustment-secret", time.Second)
	_, err := Adjust(context.Background(), c, a)
	require.Error(t, err)
	assert.False(t, CanEditAfterError(err))
	r, err := Adjust(context.Background(), c, a)
	require.NoError(t, err)
	assert.Equal(t, "1.000001", r.Balance)
	assert.Equal(t, int64(7), r.LedgerEntryID)
	var sent map[string]any
	require.NoError(t, common.Unmarshal(first, &sent))
	assert.NotContains(t, sent, "tenantId")
	assert.NotContains(t, sent, "ledgerTenant")
	a.Amount = "1e12"
	require.Error(t, Validate(a))
	a.Amount = "-1"
	require.Error(t, Validate(a))
}

func TestFundingDeliveryRetainsEventsUntilCompleteAcknowledgement(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:funding-delivery-"+common.GetUUID()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.OPSWalletEvent{}))
	var first []byte
	reply, status := `{}`, http.StatusOK
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, fundingPath, r.URL.Path)
		body, readErr := io.ReadAll(r.Body)
		assert.NoError(t, readErr)
		if first == nil {
			first = body
		} else {
			assert.Equal(t, first, body)
		}
		var batch struct {
			Events []struct {
				Quota int64 `json:"gatewayQuota"`
			} `json:"events"`
		}
		assert.NoError(t, common.Unmarshal(body, &batch))
		assert.Len(t, batch.Events, 2)
		for _, event := range batch.Events {
			assert.EqualValues(t, 9007199254740991, event.Quota)
		}
		expected := opsclient.SignWithNonce("product", "funding-test-secret", "POST", fundingPath, body, r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"))
		assert.Equal(t, expected.Get("X-Signature"), r.Header.Get("X-Signature"))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(server.Close)
	client := opsclient.New(server.URL, "product", "funding-test-secret", time.Second)
	target, err := model.OPSWalletFundingTarget(client.BaseURL, client.KeyID)
	require.NoError(t, err)
	for _, sourceID := range []string{"paid", "gift"} {
		id := fmt.Sprintf("wf-%x", sha256.Sum256([]byte("\x00fixture\x00"+sourceID)))
		intent, marshalErr := common.Marshal(map[string]any{"scope": "", "target": target, "userId": int64(15), "quota": int64(common.MaxWalletQuota), "kind": "gift", "source": "fixture", "sourceId": sourceID, "facts": (*model.OPSFundingFacts)(nil)})
		require.NoError(t, marshalErr)
		body, marshalErr := common.Marshal(map[string]any{"schemaVersion": "1.1", "batchId": id, "events": []any{map[string]any{"requestId": id, "userId": "15", "occurredAt": "1970-01-01T00:00:01Z", "status": "success", "gatewayQuota": int64(common.MaxWalletQuota), "extra": map[string]any{"recordKind": "gift", "direction": "credit", "fundingSource": "fixture", "fundingSourceId": sourceID, "walletFunding": map[string]any{"version": 1, "scope": "", "target": target}}}}})
		require.NoError(t, marshalErr)
		row := model.OPSWalletEvent{EventID: id, Payload: string(body), PayloadHash: fmt.Sprintf("%x", sha256.Sum256(body)), IntentHash: fmt.Sprintf("%x", sha256.Sum256(intent)), CreatedAt: 1}
		require.NoError(t, db.Create(&row).Error)
	}
	for _, wrong := range []*opsclient.Client{
		opsclient.New(server.URL, "another-ledger", client.Secret, time.Second),
		opsclient.New(server.URL+"/test", client.KeyID, client.Secret, time.Second),
	} {
		require.ErrorContains(t, DeliverFunding(t.Context(), db, wrong), "destination changed")
		assert.Zero(t, calls, "credentials or proxy prefix changes must be caught before remote I/O")
	}
	for _, scenario := range []struct {
		name, body string
		status     int
	}{
		{"missing receipt", `{}`, 200},
		{"partial acceptance", `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`, 200},
		{"explicit rejection", `{"data":{"accepted":1,"duplicates":1,"rejected":[{"index":1}]}}`, 200},
		{"negative count", `{"data":{"accepted":3,"duplicates":-1,"rejected":[]}}`, 200},
		{"over acknowledgement", `{"data":{"accepted":3,"duplicates":0,"rejected":[]}}`, 200},
		{"rate limit", `{"error":"rate_limited"}`, 429},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			reply, status = scenario.body, scenario.status
			require.Error(t, DeliverFunding(context.Background(), db, client))
			rows, readErr := model.ReadOPSWalletFunding(db, 100)
			require.NoError(t, readErr)
			assert.Len(t, rows, 2, "unconfirmed funding must remain available for replay")
		})
	}
	// A lost response can be retried as duplicate acceptance without a second credit.
	reply, status = `{"data":{"accepted":0,"duplicates":2,"rejected":[]}}`, 200
	require.NoError(t, DeliverFunding(context.Background(), db, client))
	rows, err := model.ReadOPSWalletFunding(db, 100)
	require.NoError(t, err)
	assert.Empty(t, rows)
	completedCalls := calls
	require.NoError(t, DeliverFunding(context.Background(), db, client))
	assert.Equal(t, completedCalls, calls, "delivered events must not be sent again")
}

type fundingTransport func(*http.Request) (*http.Response, error)

func (f fundingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// Cancellation anywhere in a delivery must retain the entire committed batch,
// including when OPS accepted it before the local acknowledgement failed.
func TestFundingDeliveryCancellationAcrossDialects(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_PROFILE", "product")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	t.Setenv("OPS_PUSH_URL", "https://ops.example.invalid")
	t.Setenv("OPS_PUSH_KEY_ID", "funding-cancellation")
	t.Setenv("OPS_INGEST_BUDGET_ENABLED", "false")
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			if dialect == "sqlite" {
				driver = sqlite.Open(filepath.Join(t.TempDir(), "funding.db"))
			} else {
				dsn := os.Getenv("OPS_WALLET_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if os.Getenv("OPS_WALLET_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("isolated external database not configured")
				}
				if dialect == "mysql" {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			if dialect == "sqlite" {
				var version string
				require.NoError(t, db.Raw("SELECT sqlite_version()").Scan(&version).Error)
				t.Logf("SQLite %s", version)
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.OPSWalletEvent{}))
			user := model.User{Username: "cancel-funding", AffCode: "cancel-funding", Quota: 123}
			require.NoError(t, db.Create(&user).Error)
			for _, stage := range []string{"read", "after acceptance", "during acknowledgement"} {
				t.Run(stage, func(t *testing.T) {
					require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
						for _, source := range []string{"first", "second"} {
							if err := model.QueueOPSWalletFundingTx(tx, int64(user.Id), 100, "gift", "checkin", stage+source, nil); err != nil {
								return err
							}
						}
						return nil
					}))
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					updates, calls := 0, 0
					var original []byte
					if stage == "read" {
						cancel()
					}
					if stage == "during acknowledgement" {
						require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:cancel-funding-ack", func(tx *gorm.DB) {
							if tx.Statement.Table == "ops_wallet_events" && tx.Error == nil && tx.RowsAffected == 1 {
								updates++
								if updates == 1 {
									cancel()
								}
							}
						}))
						t.Cleanup(func() { _ = db.Callback().Update().Remove("test:cancel-funding-ack") })
					}
					client := opsclient.New("https://ops.example.invalid", "funding-cancellation", "synthetic-secret", time.Second)
					client.HTTP.Transport = fundingTransport(func(request *http.Request) (*http.Response, error) {
						calls++
						body, err := io.ReadAll(request.Body)
						require.NoError(t, err)
						accepted, duplicates := 0, 2
						if original == nil {
							original = body
							accepted, duplicates = 2, 0
						} else {
							assert.Equal(t, original, body, "recovery must preserve both event IDs and batch bytes")
						}
						if stage == "after acceptance" && calls == 1 {
							cancel()
						}
						receipt := fmt.Sprintf(`{"data":{"accepted":%d,"duplicates":%d,"rejected":[]}}`, accepted, duplicates)
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(receipt))}, nil
					})
					err = DeliverFunding(ctx, db, client)
					require.ErrorIs(t, err, context.Canceled)
					if stage == "read" {
						assert.Zero(t, calls, "cancelled database reads must not send funding")
					} else {
						assert.Equal(t, 1, calls)
					}
					if stage == "during acknowledgement" {
						assert.Equal(t, 1, updates)
					}
					rows, err := model.ReadOPSWalletFunding(db, 50)
					require.NoError(t, err)
					assert.Len(t, rows, 2, "partial local acknowledgement must roll back")
					require.NoError(t, DeliverFunding(t.Context(), db, client))
					rows, err = model.ReadOPSWalletFunding(db, 50)
					require.NoError(t, err)
					assert.Empty(t, rows)
					finishedCalls := calls
					require.NoError(t, DeliverFunding(t.Context(), db, client))
					assert.Equal(t, finishedCalls, calls)
					var current model.User
					require.NoError(t, db.First(&current, user.Id).Error)
					assert.Equal(t, 123, current.Quota, "delivery cannot mutate the Gateway wallet")
				})
			}
		})
	}
}

func TestAdjustmentRejectsDifferentReceiptAndDefiniteFailures(t *testing.T) {
	a := Adjustment{RequestID: "adm-test", UserID: "15", Operation: "set", Amount: "0", Operator: "admin", Reason: "test reset"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"requestId":"foreign","userId":"15","balance":"0"}}`))
	}))
	defer server.Close()
	_, err := Adjust(context.Background(), opsclient.New(server.URL, "p", "test-adjustment-secret", time.Second), a)
	require.Error(t, err)
	assert.False(t, CanEditAfterError(err))
	assert.True(t, CanEditAfterError(&opsclient.ReadError{Status: 409, Code: "insufficient_balance"}))
	assert.False(t, CanEditAfterError(&opsclient.ReadError{Status: 429}))
	assert.False(t, CanEditAfterError(&opsclient.ReadError{Status: 409, Code: "request_id_conflict"}))
	assert.Equal(t, "运营平台余额不足，无法扣减该金额", ErrorMessage(&opsclient.ReadError{Status: 409, Code: "insufficient_balance"}))
}
