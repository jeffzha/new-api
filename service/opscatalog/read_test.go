package opscatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const modelsBody = `{"data":{"version":"v1","generatedAt":"2026-11-02T08:00:00Z","items":[{"model":"gpt-4o","callType":"text","catalogKey":"openai/gpt-4o","status":"enabled","onSale":true,"offSaleReason":null,"channels":[]}]}}`
const discountsBody = `{"data":{"version":"d1","generatedAt":"2026-11-02T08:00:00Z","distributors":[{"distributorId":"distributor_1","stationAgencyId":"17","models":[]}],"users":[{"userId":"143","distributorId":"distributor_1","models":[]}]}}`

type stub struct {
	mu       sync.Mutex
	requests []string
	reply    map[string]func(r *http.Request) (int, string, http.Header)
}

func setup(t *testing.T) (*gorm.DB, *stub, *Reader, *time.Time) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "catalog.sqlite3")), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	s := &stub{reply: map[string]func(*http.Request) (int, string, http.Header){}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Len(t, r.Header.Get("X-Signature"), 64)
		s.mu.Lock()
		s.requests = append(s.requests, r.URL.RequestURI())
		reply := s.reply[r.URL.Path]
		s.mu.Unlock()
		status, body, header := reply(r)
		for name, values := range header {
			w.Header()[name] = values
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	reader := New(db, Config{URL: server.URL, KeyID: "gw-test", Secret: "catalog-test-secret-0123"})
	now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	reader.Now = func() time.Time { return now }
	require.NoError(t, reader.Migrate())
	require.NoError(t, reader.Migrate())
	return db, s, reader, &now
}

func snapshot(t *testing.T, db *gorm.DB, kind string) Snapshot {
	var row Snapshot
	require.NoError(t, db.Where("kind = ?", kind).First(&row).Error)
	return row
}

func TestReadStoresVersionsAndUsesConditionalRequests(t *testing.T) {
	db, s, reader, now := setup(t)
	s.reply[ModelsPath] = func(r *http.Request) (int, string, http.Header) {
		if r.URL.Query().Get("since") == "v1" {
			assert.Equal(t, `"v1"`, r.Header.Get("If-None-Match"))
			return http.StatusNotModified, "", http.Header{"Etag": {`"v1"`}}
		}
		return http.StatusOK, modelsBody, http.Header{"Etag": {`"v1"`}}
	}
	s.reply[DiscountsPath] = func(r *http.Request) (int, string, http.Header) {
		return http.StatusOK, discountsBody, http.Header{"Etag": {`"d1"`}}
	}
	reader.Step(context.Background())
	models := snapshot(t, db, KindModels)
	assert.Equal(t, "v1", models.Version)
	assert.Equal(t, 1, models.Items)
	assert.Contains(t, models.Body, `"onSale":true`)
	assert.Equal(t, now.Add(10*time.Minute).Unix(), models.NextAt, "models are read every 10 minutes")
	discounts := snapshot(t, db, KindDiscounts)
	assert.Equal(t, "d1", discounts.Version)
	assert.Equal(t, 2, discounts.Items)
	assert.Equal(t, now.Add(time.Minute).Unix(), discounts.NextAt, "discounts are read every minute")

	// Nothing is due yet.
	reader.Step(context.Background())
	assert.Len(t, s.requests, 2)

	*now = now.Add(11 * time.Minute)
	reader.Step(context.Background())
	assert.Contains(t, s.requests, ModelsPath+"?since=v1")
	assert.Equal(t, 304, snapshot(t, db, KindModels).LastStatus)
	assert.Equal(t, "v1", snapshot(t, db, KindModels).Version)

	rows, err := Current(db)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Empty(t, rows[0].Body, "the status view leaves the copies out")
}

func TestFailedReadsKeepThePreviousCopy(t *testing.T) {
	db, s, reader, now := setup(t)
	s.reply[ModelsPath] = func(*http.Request) (int, string, http.Header) {
		return http.StatusOK, modelsBody, nil
	}
	s.reply[DiscountsPath] = func(*http.Request) (int, string, http.Header) {
		return http.StatusForbidden, `{"error":{"code":"scope_required"}}`, nil
	}
	reader.Step(context.Background())
	discounts := snapshot(t, db, KindDiscounts)
	assert.Equal(t, "scope_required", discounts.LastError)
	assert.Equal(t, now.Add(scopeWait).Unix(), discounts.NextAt, "a missing scope waits quietly")

	*now = now.Add(11 * time.Minute)
	for name, reply := range map[string]string{
		"invalid": `{"data":{"version":"v2","generatedAt":"2026-11-02T08:00:00Z","items":[{"model":""}]}}`,
		"429":     `{"error":{"code":"rate_limited"}}`,
	} {
		s.reply[ModelsPath] = func(*http.Request) (int, string, http.Header) {
			if name == "429" {
				return http.StatusTooManyRequests, reply, http.Header{"Retry-After": {"120"}}
			}
			return http.StatusOK, reply, nil
		}
		require.NoError(t, db.Model(&Snapshot{}).Where("kind = ?", KindModels).Update("next_at", 0).Error)
		reader.Step(context.Background())
		models := snapshot(t, db, KindModels)
		assert.Equal(t, "v1", models.Version, name)
		assert.Contains(t, models.Body, "gpt-4o", name)
		if name == "429" {
			assert.GreaterOrEqual(t, models.NextAt, now.Add(120*time.Second).Unix(), "429 waits at least Retry-After")
		}
	}
}

func TestOnlyOneInstanceReads(t *testing.T) {
	db, s, reader, _ := setup(t)
	s.reply[ModelsPath] = func(*http.Request) (int, string, http.Header) { return http.StatusOK, modelsBody, nil }
	s.reply[DiscountsPath] = func(*http.Request) (int, string, http.Header) { return http.StatusOK, discountsBody, nil }
	reader.Step(context.Background())
	other := New(db, reader.Config)
	other.Now = reader.Now
	require.NoError(t, db.Model(&Snapshot{}).Where("1 = 1").Update("next_at", 0).Error)
	other.Step(context.Background())
	assert.Len(t, s.requests, 2, "the lease holder keeps the reads")
}
