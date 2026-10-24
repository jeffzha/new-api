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
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
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

func TestVideoProxyUsesPluginContentRequestDescriptor(t *testing.T) {
	db := setupVideoProxyControllerTestDB(t)

	descriptorRequests := make(chan http.Header, 1)
	descriptorUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		descriptorRequests <- r.Header.Clone()
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("plugin-video"))
	}))
	defer descriptorUpstream.Close()
	fallbackCalled := make(chan struct{}, 1)
	fallbackUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalled <- struct{}{}
		http.Error(w, "stale fallback", http.StatusBadGateway)
	}))
	defer fallbackUpstream.Close()
	baseURL := descriptorUpstream.URL

	channel := model.Channel{
		Type:    constant.ChannelTypeGemini,
		Key:     "plugin-content-key",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
		Name:    "video-plugin-content-test",
	}
	require.NoError(t, db.Create(&channel).Error)

	const userID = 18
	task := model.Task{
		TaskID:    "task_video_proxy_plugin_descriptor",
		Platform:  constant.TaskPlatform("google"),
		UserId:    userID,
		ChannelId: channel.Id,
		Status:    model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{
			ResultURL: fallbackUpstream.URL,
			Execution: &model.TaskExecutionSnapshot{TaskPlugin: &model.TaskPluginSnapshot{
				Key: "google", Name: "Google Veo (Gemini API)", Version: "1.0.0", APIVersion: 1,
			}},
		},
	}
	task.SetData(map[string]any{
		"response": map[string]any{
			"generateVideoResponse": map[string]any{
				"generatedVideos": []any{map[string]any{
					"video": map[string]any{"uri": descriptorUpstream.URL},
				}},
			},
		},
	})
	require.NoError(t, db.Create(&task).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/"+task.TaskID+"/content", nil)
	context.Params = gin.Params{{Key: "task_id", Value: task.TaskID}}
	context.Set("id", userID)

	VideoProxy(context)

	var forwardedHeaders http.Header
	select {
	case forwardedHeaders = <-descriptorRequests:
	default:
		require.FailNow(t, "video proxy did not use the plugin content descriptor")
	}
	assert.Equal(t, "plugin-content-key", forwardedHeaders.Get("x-goog-api-key"))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "plugin-video", recorder.Body.String())
	select {
	case <-fallbackCalled:
		require.FailNow(t, "video proxy used the stale task result URL")
	default:
	}
}

func TestTaskMediaHeadFallsBackOnlyForCredentiallessCDN(t *testing.T) {
	db := setupVideoProxyControllerTestDB(t)
	channel := model.Channel{Type: constant.ChannelTypeTaskPlugin, Key: "test-key", Status: common.ChannelStatusEnabled, Name: "head-test"}
	require.NoError(t, db.Create(&channel).Error)
	task := &model.Task{TaskID: "task_head_cdn", ChannelId: channel.Id}
	for _, credentialless := range []bool{true, false} {
		t.Run(fmt.Sprint(credentialless), func(t *testing.T) {
			var methods []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method)
				if r.Method == http.MethodHead {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				assert.Equal(t, "bytes=0-3", r.Header.Get("Range"))
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Range", "bytes 0-3/8")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("0123"))
			}))
			defer upstream.Close()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodHead, "/v1/videos/task_head_cdn/content", nil)
			c.Request.Header.Set("Range", "bytes=0-3")
			err := proxyTaskMedia(c, task, &relaychannel.TaskContentRequest{URL: upstream.URL, Method: http.MethodHead, Credentialless: credentialless})
			if !credentialless {
				require.Error(t, err)
				assert.Equal(t, []string{http.MethodHead}, methods)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []string{http.MethodHead, http.MethodGet}, methods)
			assert.Equal(t, http.StatusPartialContent, recorder.Code)
			assert.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
			assert.Empty(t, recorder.Body.String())
		})
	}
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
