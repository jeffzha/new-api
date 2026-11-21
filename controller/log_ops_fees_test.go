package controller

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestLogFeeBatchOnlyReturnsAuthorizedFees(t *testing.T) {
	db := logFeeDatabaseFixture(t, "FEE_TEST_MAIN_DSN")
	logDB := logFeeDatabaseFixture(t, "FEE_TEST_LOG_DSN")
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, logDB
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })
	require.NoError(t, logDB.AutoMigrate(&model.Log{}))
	require.NoError(t, model.MigrateOpsFees(db))
	model.ConfigureOpsFees(&model.OpsFeeRuntime{Connection: "test"})
	t.Cleanup(func() { model.ConfigureOpsFees(nil) })
	for _, l := range []*model.Log{{Id: 101, UserId: 1, Type: model.LogTypeConsume, RequestId: "mine", ModelName: "m"}, {Id: 102, UserId: 2, Type: model.LogTypeConsume, RequestId: "other", ModelName: "m"}} {
		require.NoError(t, logDB.Create(l).Error)
	}
	amount := "0.42"
	require.NoError(t, db.Create(&model.OpsFee{Connection: "test", RequestID: "mine", UserID: "1", Model: "m", Amount: &amount, Status: "available", Currency: "CNY"}).Error)
	r := gin.New()
	r.POST("/self", func(c *gin.Context) { c.Set("id", 1) }, GetUserLogOpsFees)
	r.POST("/admin", GetAllLogOpsFees)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/self", strings.NewReader(`{"request_ids":["mine","other","missing","mine"]}`)))
	require.Equal(t, 200, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Body.String(), `"amount":"0.42"`)
	require.NotContains(t, w.Body.String(), `"request_id":"other"`)
	require.NotContains(t, w.Body.String(), `"user_id"`)
	require.NotContains(t, w.Body.String(), `"total"`)
	require.Equal(t, 1, strings.Count(w.Body.String(), `"request_id":"mine"`))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/admin", strings.NewReader(`{"request_ids":["other"]}`)))
	require.Contains(t, w.Body.String(), `"status":"waiting"`)
	// The lightweight fee refresh must retain task provenance too. It returns
	// the original visible request ID and the independent final OPS receipt.
	require.NoError(t, logDB.Create(&model.Log{Id: 103, UserId: 1, Type: model.LogTypeConsume, RequestId: "video-original", ModelName: "video", Other: `{"is_task":true,"billing_source":"ops","billing_scope":"overseas","settlement_stage":"preconsume"}`}).Error)
	require.NoError(t, db.Create(&model.OpsFee{Connection: "test", RequestID: opsauthority.TaskRequestIDForScope("overseas", "video-original"), UserID: "1", Model: "video", Amount: &amount, Status: "available", Currency: "CNY"}).Error)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/self", strings.NewReader(`{"request_ids":["video-original","other"]}`)))
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"request_id":"video-original"`)
	require.Contains(t, w.Body.String(), `"amount":"0.42"`)
	require.NotContains(t, w.Body.String(), `"request_id":"other"`)
	for _, body := range []string{`{"request_ids":[]}`, `{"request_ids":[""]}`, `{"request_ids":["   "]}`, `{"request_ids":["` + strings.Repeat("x", 65) + `"]}`, `{"request_ids":[` + strings.Repeat(`"x",`, 100) + `"y"]}`, strings.Repeat(" ", 16385) + `{"request_ids":["mine"]}`, `not-json`} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/self", strings.NewReader(body)))
		require.Equal(t, 400, w.Code)
	}
}

func TestLogFeeDisabledModeDoesNotQueryDatabase(t *testing.T) {
	model.ConfigureOpsFees(nil)
	r := gin.New()
	r.POST("/admin", GetAllLogOpsFees)
	r.POST("/self", GetUserLogOpsFees)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/admin", strings.NewReader(`{"request_ids":["request"]}`)))
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"data":[]`)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/self", strings.NewReader(`{"request_ids":["request"]}`)))
	require.Equal(t, 401, w.Code)
}

// Dedicated local databases only; separate main/log databases protect ownership checks.
func logFeeDatabaseFixture(t *testing.T, variable string) *gorm.DB {
	t.Helper()
	var dialect gorm.Dialector
	dsn := os.Getenv(variable)
	switch os.Getenv("FEE_TEST_ENGINE") {
	case "mysql":
		require.NotEmpty(t, dsn)
		dialect = mysql.Open(dsn)
	case "postgres":
		require.NotEmpty(t, dsn)
		dialect = postgres.Open(dsn)
	default:
		dialect = sqlite.Open("file:" + t.Name() + variable + "?mode=memory&cache=shared")
	}
	db, err := gorm.Open(dialect, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	return db
}
