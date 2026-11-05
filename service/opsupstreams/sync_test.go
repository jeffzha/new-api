package opsupstreams

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/upstreammap"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/opspush"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestUpstreamMappingDatabaseMatrix(t *testing.T) {
	for _, engine := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			dsn := os.Getenv("UPSTREAM_TEST_" + strings.ToUpper(engine) + "_DSN")
			if engine != "sqlite" && dsn == "" {
				t.Skip("isolated database DSN required")
			}
			var dialect gorm.Dialector
			switch engine {
			case "mysql":
				dialect = mysql.Open(dsn)
			case "postgres":
				dialect = postgres.Open(dsn)
			default:
				dialect = sqlite.Open(filepath.Join(t.TempDir(), "mapping.sqlite3"))
			}
			db, err := gorm.Open(dialect, &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			// Fresh installation and a second startup must both be safe.
			require.NoError(t, upstreammap.Migrate(db))
			require.NoError(t, upstreammap.Migrate(db))
			require.NoError(t, db.Migrator().DropTable(&upstreammap.Binding{}, &upstreammap.Mapping{}))
			// Upgrade the existing durable Midjourney receipt without losing money facts.
			require.NoError(t, db.AutoMigrate(&model.CNMidjourneyBilling{}))
			require.NoError(t, db.Migrator().DropColumn(&model.CNMidjourneyBilling{}, "UpstreamBaseURL"))
			require.NoError(t, db.Table("cn_midjourney_billings").Create(map[string]any{"key": "old-receipt", "task_id": 71, "user_id": 1, "mj_id": "old-task", "state": "charged", "quota": 19, "created_at": "2026-11-01"}).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.CNMidjourneyBilling{}))
			}
			var receipt model.CNMidjourneyBilling
			require.NoError(t, db.First(&receipt, map[string]any{"key": "old-receipt"}).Error)
			assert.Equal(t, 19, receipt.Quota)
			assert.Empty(t, receipt.UpstreamBaseURL)
			// Existing released channel data is created before the new tables.
			require.NoError(t, db.AutoMigrate(&model.Channel{}))
			base := "https://UPSTREAM.example:443/api/"
			require.NoError(t, db.Create(&model.Channel{Id: 31, Type: 1, BaseURL: &base, Name: "original", Key: "private-never-exported"}).Error)
			require.NoError(t, upstreammap.Migrate(db))
			require.NoError(t, upstreammap.Migrate(db))
			logDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "logs.sqlite3")), &gorm.Config{})
			require.NoError(t, err)
			lp, err := logDB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lp.Close()) })
			require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			calls := 0
			fail := true
			var usageBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				assert.NotContains(t, string(raw), "private-never-exported")
				key := sha256.Sum256([]byte("test-secret-at-least-sixteen"))
				bodyHash := sha256.Sum256(raw)
				mac := hmac.New(sha256.New, []byte(hex.EncodeToString(key[:])))
				mac.Write([]byte(r.Method + "\n" + r.URL.RequestURI() + "\n" + r.Header.Get("X-Timestamp") + "\n" + r.Header.Get("X-Nonce") + "\n" + hex.EncodeToString(bodyHash[:])))
				assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Signature"))
				if r.URL.Path == Path {
					calls++
					if fail {
						w.WriteHeader(503)
						return
					}
					_, _ = w.Write([]byte(`{"data":{"ops_upstream_id":71,"base_url":"https://upstream.example/api"}}`))
					return
				}
				require.NoError(t, common.Unmarshal(raw, &usageBody))
				_, _ = w.Write([]byte(`{"data":{"accepted":1}}`))
			}))
			defer server.Close()
			c := Config{URL: server.URL, KeyID: "gateway-test", Secret: "test-secret-at-least-sixteen", Enabled: true}
			s := New(db, c)
			require.NoError(t, s.Discover(context.Background()))
			require.NoError(t, s.Discover(context.Background()))
			assert.Error(t, s.SyncPending(context.Background()))
			row, opsID, err := upstreammap.Resolve(db, base, c.Target())
			require.NoError(t, err)
			assert.Nil(t, opsID)
			fail = false
			require.NoError(t, s.SyncPending(context.Background()))
			require.NoError(t, s.SyncPending(context.Background()))
			assert.Equal(t, 2, calls, "registered addresses are not sent again")
			row, opsID, err = upstreammap.Resolve(db, base, c.Target())
			require.NoError(t, err)
			require.NotNil(t, opsID)
			assert.Equal(t, int64(71), *opsID)
			var group sync.WaitGroup
			ids := make(chan int64, 8)
			errors := make(chan error, 8)
			for range 8 {
				group.Go(func() {
					r, err := upstreammap.Ensure(db, base)
					if err != nil {
						errors <- err
						return
					}
					errors <- upstreammap.EnsureBinding(db, r.ID, c.Target())
					ids <- r.ID
				})
			}
			group.Wait()
			close(ids)
			close(errors)
			for err := range errors {
				require.NoError(t, err)
			}
			for id := range ids {
				assert.Equal(t, row.ID, id)
			}
			require.NoError(t, upstreammap.Refresh(db, c.Target()))
			other := model.NewLogOther()
			model.CaptureUpstreamLogReference(other, base)
			var captured map[string]any
			require.NoError(t, common.UnmarshalJsonStr(other.JSONString(), &captured))
			assert.NotContains(t, captured, "base_url")
			assert.Contains(t, captured, "admin_info")
			changed := "https://other.example/api"
			require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 31).Update("base_url", changed).Error)
			require.NoError(t, s.Discover(context.Background()))
			var count int64
			require.NoError(t, db.Model(&upstreammap.Mapping{}).Count(&count).Error)
			assert.Equal(t, int64(2), count)
			require.NoError(t, db.Delete(&model.Channel{}, 31).Error)
			require.NoError(t, s.Discover(context.Background()))
			require.NoError(t, db.Model(&upstreammap.Mapping{}).Count(&count).Error)
			assert.Equal(t, int64(2), count, "removed channels do not delete upstream identities")
			_, otherID, err := upstreammap.Resolve(db, base, upstreammap.Target(server.URL+"/test", c.KeyID))
			require.NoError(t, err)
			assert.Nil(t, otherID, "different OPS targets never share IDs")
			t.Setenv("OPS_UPSTREAM_SYNC_ENABLED", "true")
			t.Setenv("OPS_UPSTREAM_URL", server.URL)
			p := opspush.New(db, logDB, opspush.Config{URL: server.URL, KeyID: c.KeyID, Secret: c.Secret, Spec: opspush.SpecV11})
			require.NoError(t, p.Migrate())
			require.NoError(t, logDB.Create(&model.Log{UserId: 1, CreatedAt: time.Now().Add(-time.Minute).Unix(), Type: model.LogTypeConsume, ModelName: "m", ChannelId: 31, RequestId: "old-address-call", Other: other.JSONString()}).Error)
			p.Step()
			require.NotNil(t, usageBody)
			events := usageBody["events"].([]any)
			extra := events[0].(map[string]any)["extra"].(map[string]any)
			assert.Equal(t, "https://upstream.example/api", extra["base_url"])
			assert.Equal(t, float64(row.ID), extra["gateway_upstream_id"])
			assert.Equal(t, float64(71), extra["ops_upstream_id"])
			assert.Equal(t, "usage", extra["recordKind"])
			assert.Equal(t, "debit", extra["direction"])
		})
	}
}

func TestTaskUpstreamSnapshotDoesNotChangePollingEndpoint(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://original.example"}}
	task := model.InitTask(constant.TaskPlatform("test"), info)
	assert.Nil(t, task.PrivateData.Endpoint)
	info.ChannelBaseUrl = "https://edited.example"
	encoded, err := task.PrivateData.Value()
	require.NoError(t, err)
	var restored model.TaskPrivateData
	require.NoError(t, restored.Scan(encoded))
	assert.Equal(t, "https://original.example", restored.UpstreamBaseURL)
}

func TestNormalizeUpstreamURL(t *testing.T) {
	for _, raw := range []string{"https://host/path?key=secret", "https://user:secret@host", "https://host/#fragment", "file:///tmp/a", "", "https://host/a b"} {
		_, err := upstreammap.Normalize(raw)
		assert.Error(t, err, raw)
	}
	for raw, want := range map[string]string{" https://API.example:443/v1/ ": "https://api.example/v1", "http://localhost:80/": "http://localhost", "https://host/V1": "https://host/V1"} {
		got, err := upstreammap.Normalize(raw)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}
