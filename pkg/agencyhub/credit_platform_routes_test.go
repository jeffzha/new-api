package agencyhub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCreditBookConfiguredAccountSearchAndPagination(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			if dialect == "sqlite" {
				driver = sqlite.Open(filepath.Join(t.TempDir(), "credit.sqlite"))
			} else {
				dsn := os.Getenv("AGENCY_HUB_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if os.Getenv("AGENCY_HUB_RUN_EXTERNAL_DB_TESTS") != "1" || dsn == "" {
					t.Skip("isolated external test database is not configured")
				}
				if dialect == "mysql" {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.Open(dsn)
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			require.NoError(t, model.MigrateAgency(db))
			require.NoError(t, db.AutoMigrate(&model.User{}))
			db = db.Begin()
			require.NoError(t, db.Error)
			t.Cleanup(func() { require.NoError(t, db.Rollback().Error) })
			previous := model.DB
			model.DB = db
			t.Cleanup(func() { model.DB = previous })
			app := New(db, db, Config{})
			prefix := "credit-pagination"
			users := []model.User{
				{Username: prefix + "-one", AffCode: prefix + "-one", Status: common.UserStatusEnabled},
				{Username: prefix + "-two", AffCode: prefix + "-two", Status: common.UserStatusEnabled},
				{Username: prefix + "-none", AffCode: prefix + "-none", Status: common.UserStatusEnabled},
			}
			require.NoError(t, db.Create(&users).Error)
			first, second, unconfigured := users[0], users[1], users[2]
			creditSeedTerms(t, app, int64(first.Id), 10000, 30, false)
			creditSeedTerms(t, app, int64(second.Id), 20000, 10, false)
			router := creditPlatformRouter(t, app, PlatformRootIdentity(42))
			for _, tc := range []struct {
				query  string
				page   int
				wantID int
				total  int
			}{
				{prefix, 1, first.Id, 2}, {prefix, 2, second.Id, 2},
				{strconv.Itoa(second.Id), 1, second.Id, 1},
				{unconfigured.Username, 1, 0, 0}, {prefix, 3, 0, 2},
			} {
				request := httptest.NewRequest(http.MethodGet, "/api/credit/terms?query="+url.QueryEscape(tc.query)+"&p="+strconv.Itoa(tc.page)+"&page_size=1", nil)
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				var response struct {
					Data struct {
						Items []creditAccountItem
						Total int
						Page  int
					}
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				assert.Equal(t, tc.total, response.Data.Total)
				assert.Equal(t, tc.page, response.Data.Page)
				if tc.wantID == 0 {
					assert.Empty(t, response.Data.Items)
				} else {
					require.Len(t, response.Data.Items, 1)
					assert.Equal(t, strconv.Itoa(tc.wantID), response.Data.Items[0].UserID)
				}
			}
		})
	}
}

// creditPlatformRouter mirrors the gateway wiring for the main site admin
// console: the book hangs off an already authenticated group, and the only
// thing telling the handlers who is writing is that group's identity.
func creditPlatformRouter(t *testing.T, app *App, identity *Identity) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	book := router.Group("/api/credit")
	if identity != nil {
		book.Use(func(c *gin.Context) {
			c.Set("agency_identity", identity)
			c.Next()
		})
	}
	app.CreditBookRoutes(book)
	return router
}

func creditPlatformPut(router *gin.Engine, userID int, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPut, "/api/credit/users/"+strconv.Itoa(userID), strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestCreditBookRoutesAttributeMainSiteWritesToTheRootActor(t *testing.T) {
	app := newAgencyTestApp(t)
	user := creditTestUser(t, app, "credit-platform-route")
	router := creditPlatformRouter(t, app, PlatformRootIdentity(42))

	recorder := creditPlatformPut(router, user.Id, `{"credit_limit":250000,"payment_term_days":30,"status":"active","email_enabled":true}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var terms model.AgencyCreditTerms
	require.NoError(t, app.db.Where("user_id = ?", user.Id).First(&terms).Error)
	assert.Equal(t, int64(250000), terms.CreditLimit)
	assert.Equal(t, int64(42), terms.UpdatedBy)

	var audit model.AgencyAuditLog
	require.NoError(t, app.db.Where("action = ? AND object_id = ?", "credit.terms.upsert", strconv.Itoa(user.Id)).First(&audit).Error)
	assert.Equal(t, ActorTypeRoot, audit.ActorType)
	assert.Equal(t, int64(42), audit.ActorID)
}

func TestCreditBookRoutesRefuseWritesWithoutAnAuditIdentity(t *testing.T) {
	app := newAgencyTestApp(t)
	user := creditTestUser(t, app, "credit-platform-anonymous")
	router := creditPlatformRouter(t, app, nil)

	recorder := creditPlatformPut(router, user.Id, `{"credit_limit":250000,"payment_term_days":30,"status":"active"}`)
	assert.NotEqual(t, http.StatusOK, recorder.Code)

	var count int64
	require.NoError(t, app.db.Model(&model.AgencyCreditTerms{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count, "an anonymous writer must not be able to open a credit line")
}

func TestCreditBookRoutesResolveReturnsExistingTerms(t *testing.T) {
	app := newAgencyTestApp(t)
	user := creditTestUser(t, app, "credit-platform-resolve")
	creditSeedTerms(t, app, int64(user.Id), 250000, 30, false)
	router := creditPlatformRouter(t, app, PlatformRootIdentity(42))

	request := httptest.NewRequest(http.MethodGet, "/api/credit/resolve-user?query="+user.Username, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Configured      bool  `json:"configured"`
			CreditLimit     int64 `json:"credit_limit"`
			PaymentTermDays int   `json:"payment_term_days"`
			EmailEnabled    bool  `json:"email_enabled"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.True(t, response.Data.Configured)
	assert.Equal(t, int64(250000), response.Data.CreditLimit)
	assert.Equal(t, 30, response.Data.PaymentTermDays)
	assert.False(t, response.Data.EmailEnabled)
}
