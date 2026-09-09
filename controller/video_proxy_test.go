package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupVideoProxyControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRedisEnabled := common.RedisEnabled
	fetchSetting := system_setting.GetFetchSetting()
	originalFetchSetting := *fetchSetting

	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false
	fetchSetting.EnableSSRFProtection = false
	service.InitHttpClient()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}))

	t.Cleanup(func() {
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RedisEnabled = originalRedisEnabled
		*fetchSetting = originalFetchSetting
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}

func TestVideoProxyUsesPrivateNoStoreAndPreservesRangeResponse(t *testing.T) {
	db := setupVideoProxyControllerTestDB(t)

	requestHeaders := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestHeaders <- r.Header.Clone()
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Disposition", `attachment; filename="result.mp4"`)
		w.Header().Set("Content-Range", "bytes 0-3/8")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Server", "upstream-test")
		w.Header().Add("Set-Cookie", "supplier_session=secret; Path=/")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("0123"))
	}))
	defer upstream.Close()

	channel := model.Channel{
		Type:   constant.ChannelTypeSeedanceDomestic,
		Key:    "test-key",
		Status: common.ChannelStatusEnabled,
		Name:   "video-proxy-test",
	}
	require.NoError(t, db.Create(&channel).Error)

	const userID = 17
	task := model.Task{
		TaskID:    "task_video_proxy_private_cache",
		UserId:    userID,
		ChannelId: channel.Id,
		Status:    model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{
			ResultURL: upstream.URL,
		},
	}
	require.NoError(t, db.Create(&task).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/"+task.TaskID+"/content", nil)
	context.Request.Header.Set("Range", "bytes=0-3")
	context.Request.Header.Set("If-Range", `"video-etag"`)
	context.Params = gin.Params{{Key: "task_id", Value: task.TaskID}}
	context.Set("id", userID)

	VideoProxy(context)

	var forwardedHeaders http.Header
	select {
	case forwardedHeaders = <-requestHeaders:
	default:
		require.FailNow(t, "video proxy did not call the upstream server")
	}
	assert.Equal(t, "bytes=0-3", forwardedHeaders.Get("Range"))
	assert.Equal(t, `"video-etag"`, forwardedHeaders.Get("If-Range"))
	assert.Equal(t, http.StatusPartialContent, recorder.Code)
	assert.Equal(t, "0123", recorder.Body.String())
	assert.Equal(t, []string{privateVideoCacheControl}, recorder.Header().Values("Cache-Control"))
	assert.Equal(t, "bytes", recorder.Header().Get("Accept-Ranges"))
	assert.Equal(t, `attachment; filename="result.mp4"`, recorder.Header().Get("Content-Disposition"))
	assert.Equal(t, "bytes 0-3/8", recorder.Header().Get("Content-Range"))
	assert.Empty(t, recorder.Header().Values("Server"))
	assert.Empty(t, recorder.Header().Values("Set-Cookie"))
}

func TestWriteVideoDataURLUsesPrivateNoStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	err := writeVideoDataURL(context, "data:video/mp4;base64,MDEyMw==")

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "0123", recorder.Body.String())
	assert.Equal(t, []string{privateVideoCacheControl}, recorder.Header().Values("Cache-Control"))
}
