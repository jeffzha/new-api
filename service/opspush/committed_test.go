package opspush

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestFastUsageBudgetIsSharedAndCancellationPreservesOutbox(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	oldClient, oldEnabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	t.Cleanup(func() { client.Close(); common.RDB, common.RedisEnabled = oldClient, oldEnabled })
	stub := &opsStub{responses: []func(http.ResponseWriter){reply(200, `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`)}}
	p, db := pusherFixture(t, stub)
	log := model.Log{UserId: 1, Type: model.LogTypeConsume, ModelName: "m", RequestId: "budget", CreatedAt: 10000}
	require.NoError(t, db.Create(&log).Error)
	sum := sha256.Sum256([]byte(p.Config.URL + "\n" + p.Config.KeyID))
	key := "ops-fast-push:" + hex.EncodeToString(sum[:])
	require.NoError(t, client.Set(context.Background(), key, "another-slot", time.Minute).Err())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	require.Error(t, p.sendCommitted(ctx, []int{log.Id}))
	require.Empty(t, stub.bodies, "an occupied fleet budget must not send")
	require.False(t, p.deliveredFast(log.Id))
	server.FastForward(time.Minute)
	require.NoError(t, p.sendCommitted(context.Background(), []int{log.Id}))
	require.Len(t, stub.bodies, 1)
}

func TestProductCommittedUsageDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		for _, spec := range []string{SpecV1, SpecV11} {
			t.Run(dialect+"/"+spec, func(t *testing.T) {
				stub := &opsStub{responses: []func(http.ResponseWriter){reply(200, `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`)}}
				p, db := pusherFixture(t, stub)
				if dialect != "sqlite" {
					dsn := os.Getenv("OPS_PUSH_TEST_POSTGRES_DSN")
					var driver gorm.Dialector = postgres.Open(dsn)
					if dialect == "mysql" {
						dsn = os.Getenv("OPS_PUSH_TEST_MYSQL_DSN")
						if dsn != "" {
							driver = mysql.Open(dsn)
						}
					}
					if dsn == "" {
						t.Skip("isolated database DSN not configured")
					}
					var err error
					db, err = gorm.Open(driver, &gorm.Config{})
					require.NoError(t, err)
					pool, err := db.DB()
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, pool.Close()) })
					require.NoError(t, db.Migrator().DropTable(&State{}, &model.Log{}, &model.Channel{}))
					require.NoError(t, db.AutoMigrate(&model.Log{}, &model.Channel{}))
					p.DB, p.LogDB = db, db
				}
				p.Config.Spec = spec
				require.NoError(t, p.Migrate())
				require.NoError(t, p.Migrate(), "repeated startup preserves cursor")
				require.NoError(t, db.Model(&State{}).Where("id = 1").Update("cursor", 98).Error)
				var before State
				require.NoError(t, db.First(&before, 1).Error)
				cancelled, cancel := context.WithCancel(t.Context())
				cancel()
				_, owned := p.lease(cancelled)
				require.False(t, owned, "cancelled funding work cannot acquire or renew a lease")
				var unchanged State
				require.NoError(t, db.First(&unchanged, 1).Error)
				require.Equal(t, before, unchanged)
				_, owned = p.lease(t.Context())
				require.True(t, owned, "live work retains the shared fleet lease contract")
				fresh := model.Log{Id: 100, UserId: 1, Type: model.LogTypeConsume, ModelName: "m", RequestId: "fast-product", CreatedAt: 10000, Quota: 12, Other: `{"usage_facts":{"input_images":2}}`}
				require.NoError(t, db.Create(&fresh).Error)
				for id, other := range map[int]string{101: `{"billing_scope":"project-uac"}`, 102: `{"billing_scope":42}`, 103: `{"is_task":true}`} {
					foreign := fresh
					foreign.Id, foreign.RequestId, foreign.Other = id, "skip-"+string(rune(id)), other
					require.NoError(t, db.Create(&foreign).Error)
				}
				require.NoError(t, p.sendCommitted(context.Background(), []int{100, 100, 101, 102, 103, 999}))
				require.Len(t, stub.bodies, 1)
				var sent batch
				require.NoError(t, common.UnmarshalJsonStr(stub.bodies[0], &sent))
				require.Len(t, sent.Events, 1)
				expected := Event(&fresh, "")
				schema := "1"
				if spec == SpecV11 {
					expected = EventV11(&fresh, "", nil, CurrentConversion())
					schema = "1.1"
				}
				raw, err := common.Marshal(expected)
				require.NoError(t, err)
				actual, err := common.Marshal(sent.Events[0])
				require.NoError(t, err)
				require.JSONEq(t, string(raw), string(actual))
				require.Equal(t, schema, sent.SchemaVersion)
				var after State
				require.NoError(t, db.First(&after, 1).Error)
				require.Equal(t, before.Cursor, after.Cursor)
				late := fresh
				late.Id, late.RequestId = 99, "late-product"
				require.NoError(t, db.Create(&late).Error)
				p.Now = func() time.Time { return time.Unix(10020, 0) }
				stub.responses = []func(http.ResponseWriter){reply(200, `{"data":{"accepted":2,"duplicates":0,"rejected":[]}}`)}
				require.Zero(t, p.Step())
				require.Contains(t, stub.bodies[1], "late-product")
				require.NotContains(t, stub.bodies[1], "fast-product")
				require.NotContains(t, stub.bodies[1], "project-uac")
				// Hints are optional across restart; stable IDs let OPS deduplicate replay.
				p.fastDelivered = nil
				stub.responses = []func(http.ResponseWriter){reply(200, `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`)}
				require.NoError(t, p.sendCommitted(context.Background(), []int{100}))
				require.Equal(t, stub.bodies[0], stub.bodies[2])
			})
		}
	}
}

func TestProductFastFailureFallsBackToScanner(t *testing.T) {
	for _, ack := range []struct {
		name string
		code int
		body string
	}{
		{"unavailable", 503, `{}`},
		{"incomplete", 200, `{"data":{"accepted":0,"duplicates":0,"rejected":[]}}`},
		{"rejected", 200, `{"data":{"accepted":0,"duplicates":0,"rejected":[{"requestId":"fallback"}]}}`},
	} {
		t.Run(ack.name, func(t *testing.T) {
			stub := &opsStub{responses: []func(http.ResponseWriter){reply(ack.code, ack.body), reply(200, `{"data":{"accepted":1,"duplicates":0,"rejected":[]}}`)}}
			p, db := pusherFixture(t, stub)
			fresh := model.Log{UserId: 1, Type: model.LogTypeConsume, ModelName: "m", RequestId: "fallback", CreatedAt: 10000}
			require.NoError(t, db.Create(&fresh).Error)
			require.Error(t, p.sendCommitted(context.Background(), []int{fresh.Id}))
			require.False(t, p.deliveredFast(fresh.Id))
			// Existing fixture records are also due: isolate this scanner batch.
			require.NoError(t, db.Model(&State{}).Where("id = 1").Update("cursor", fresh.Id-1).Error)
			p.Now = func() time.Time { return time.Unix(10020, 0) }
			require.Zero(t, p.Step())
			require.Contains(t, stub.bodies[1], "fallback")
		})
	}
}
