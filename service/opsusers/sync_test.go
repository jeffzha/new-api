package opsusers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testSecret = "user-sync-test-secret-0123456789"

type opsStub struct {
	mu      sync.Mutex
	batches []batch
	reply   func(batch) (int, string)
}

func (o *opsStub) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Spec 3.2 signature over the exact bytes sent.
		key := sha256.Sum256([]byte(testSecret))
		sum := sha256.Sum256(body)
		mac := hmac.New(sha256.New, []byte(hex.EncodeToString(key[:])))
		mac.Write([]byte(r.Method + "\n" + r.URL.RequestURI() + "\n" + r.Header.Get("X-Timestamp") + "\n" + r.Header.Get("X-Nonce") + "\n" + hex.EncodeToString(sum[:])))
		assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Signature"))
		assert.Equal(t, Path, r.URL.Path)
		var received batch
		require.NoError(t, common.Unmarshal(body, &received))
		o.mu.Lock()
		o.batches = append(o.batches, received)
		reply := o.reply
		o.mu.Unlock()
		status, text := http.StatusOK, `{"data":{"accepted":1,"unchanged":0,"rejected":[],"stale":[]}}`
		if reply != nil {
			status, text = reply(received)
		}
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "7")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(text))
	}
}

func (o *opsStub) last() batch {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.batches[len(o.batches)-1]
}

func (o *opsStub) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.batches)
}

func setup(t *testing.T) (*gorm.DB, *opsStub, *Syncer) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "users.sqlite3")), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Agency{}, &model.AgencyActiveUserBinding{}))
	stub := &opsStub{}
	server := httptest.NewServer(stub.handler(t))
	t.Cleanup(server.Close)
	syncer := New(db, Config{URL: server.URL, KeyID: "gw-test", Secret: testSecret})
	now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	syncer.Now = func() time.Time { return now }
	require.NoError(t, syncer.Migrate())
	require.NoError(t, syncer.Migrate(), "a second start keeps the stored state")
	return db, stub, syncer
}

func usersOf(b batch) map[string]map[string]any {
	found := map[string]map[string]any{}
	for _, user := range b.Users {
		found[user["userId"].(string)] = user
	}
	return found
}

func TestFirstPassPushesEveryUserThenOnlyChanges(t *testing.T) {
	db, stub, syncer := setup(t)
	require.NoError(t, db.Create(&model.Agency{ID: 7, Code: "a7", DisplayName: "代理 A", Status: "active", InviteCode: "inv7"}).Error)
	require.NoError(t, db.Create(&model.AgencyActiveUserBinding{UserID: 2, BindingID: 1, Revision: 1, AgencyID: 7}).Error)
	for _, user := range []model.User{
		{Id: 1, Username: "alice", DisplayName: "Alice", Group: "vip", Status: common.UserStatusEnabled, Quota: 2500000, CreatedAt: 1772445600, AffCode: "a1"},
		{Id: 2, Username: "bob", Status: common.UserStatusDisabled, Quota: -10, AffCode: "a2"},
		{Id: 3, Username: "carol", Status: common.UserStatusEnabled, AffCode: "a3"},
	} {
		require.NoError(t, db.Create(&user).Error)
	}
	require.NoError(t, db.Delete(&model.User{}, 3).Error) // soft delete

	syncer.Step()
	require.Equal(t, 1, stub.count())
	first := stub.last()
	assert.Equal(t, "1.1", first.SchemaVersion)
	assert.Regexp(t, `^users-20261113-000001-[0-9a-f]{6}$`, first.BatchId)
	users := usersOf(first)
	require.Len(t, users, 3)
	assert.Equal(t, map[string]any{"userId": "1", "username": "alice", "displayName": "Alice", "group": "vip", "status": "active",
		"registeredAt": "2026-03-02T10:00:00Z", "distributor": nil, "updatedAt": "2026-11-13T08:00:00Z",
		"balance": map[string]any{"quota": float64(2500000), "quotaPerUnit": float64(500000), "usdExchangeRate": "7.3", "at": "2026-11-13T08:00:00Z"}}, users["1"])
	assert.Equal(t, "disabled", users["2"]["status"])
	assert.Equal(t, map[string]any{"id": "7", "name": "代理 A"}, users["2"]["distributor"])
	assert.Equal(t, float64(-10), users["2"]["balance"].(map[string]any)["quota"], "balances may be negative")
	assert.Equal(t, "removed", users["3"]["status"])

	// Nothing changed: no request. A balance change alone is not a profile change.
	require.NoError(t, db.Model(&model.User{}).Where("id = 1").Update("quota", 1).Error)
	syncer.Step()
	assert.Equal(t, 1, stub.count())

	// A profile change, a new attribution and a hard delete are each pushed once.
	require.NoError(t, db.Model(&model.User{}).Where("id = 1").Update("display_name", "Alice B").Error)
	require.NoError(t, db.Create(&model.AgencyActiveUserBinding{UserID: 4, BindingID: 2, Revision: 1, AgencyID: 7}).Error)
	require.NoError(t, db.Create(&model.User{Id: 4, Username: "dave", Status: common.UserStatusEnabled, AffCode: "a4"}).Error)
	require.NoError(t, db.Unscoped().Delete(&model.User{}, 2).Error)
	syncer.Step()
	require.Equal(t, 2, stub.count())
	users = usersOf(stub.last())
	require.Len(t, users, 3)
	assert.Equal(t, "Alice B", users["1"]["displayName"])
	assert.Equal(t, map[string]any{"id": "7", "name": "代理 A"}, users["4"]["distributor"])
	assert.Equal(t, map[string]any{"userId": "2", "status": "removed", "updatedAt": "2026-11-13T08:00:00Z"}, users["2"])
	syncer.Step()
	assert.Equal(t, 2, stub.count())

	// A new resend marker pushes everyone again (the hard-deleted user stays removed).
	syncer.Config.Resend = "opening-20261113"
	syncer.Step()
	require.Equal(t, 3, stub.count())
	assert.Len(t, stub.last().Users, 4)
}

func TestRejectedAndThrottledBatches(t *testing.T) {
	db, stub, syncer := setup(t)
	for id := 1; id <= 3; id++ {
		require.NoError(t, db.Create(&model.User{Id: id, Username: "u" + string(rune('0'+id)), Status: common.UserStatusEnabled, AffCode: "c" + string(rune('0'+id))}).Error)
	}
	stub.reply = func(b batch) (int, string) {
		if len(b.Users) == 3 {
			return http.StatusBadRequest, `{"error":{"code":"invalid_users","retryable":false,"details":{"rejected":[{"index":1,"userId":"2","reason":"invalid_username"}],"rejectedCount":1}}}`
		}
		return http.StatusTooManyRequests, `{"error":{"code":"rate_limited"}}`
	}
	syncer.Step()
	// The same batchId goes again without the rejected user.
	wait := syncer.Step()
	require.Equal(t, 2, stub.count())
	resent := stub.last()
	assert.Equal(t, stub.batches[0].BatchId, resent.BatchId)
	assert.Len(t, resent.Users, 2)
	assert.GreaterOrEqual(t, wait, 7*time.Second, "429 waits at least Retry-After")
	var state State
	require.NoError(t, db.First(&state, 1).Error)
	assert.Equal(t, resent.BatchId, state.BatchId, "a throttled batch is kept for the retry")

	stub.reply = nil
	syncer.Step()
	syncer.Step()
	assert.Equal(t, 3, stub.count(), "the rejected user is not pushed again until it changes")
	var stored []Profile
	require.NoError(t, db.Order("user_id").Find(&stored).Error)
	assert.Len(t, stored, 3)
}

func TestOnlyTheLeaseHolderPushes(t *testing.T) {
	db, stub, syncer := setup(t)
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "alice", Status: common.UserStatusEnabled, AffCode: "x1"}).Error)
	other := New(db, syncer.Config)
	other.Now = syncer.Now
	syncer.Step()
	require.NoError(t, db.Model(&model.User{}).Where("id = 1").Update("display_name", "changed").Error)
	other.Step()
	assert.Equal(t, 1, stub.count(), "the second instance waits for the lease")
}
