package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSeedanceDomesticTaskFetchReturnsPublicContractOnly(t *testing.T) {
	setupRelayTaskFetchTestDB(t)
	task := &model.Task{
		CreatedAt:  100,
		UpdatedAt:  200,
		TaskID:     "task_public",
		Platform:   constant.TaskPlatform(fmt.Sprintf("%d", constant.ChannelTypeSeedanceDomestic)),
		UserId:     42,
		Group:      "sensitive-group",
		ChannelId:  59,
		Quota:      274982,
		Action:     constant.TaskActionGenerate,
		Status:     model.TaskStatusSuccess,
		FailReason: "",
		SubmitTime: 110,
		StartTime:  120,
		FinishTime: 190,
		Progress:   "100%",
		Properties: model.Properties{
			Input:             "sensitive-input",
			OriginModelName:   "doubao-seedance-2-0-260128",
			UpstreamModelName: "private-upstream-model",
		},
		PrivateData: model.TaskPrivateData{
			Key:            "supplier-secret",
			UpstreamTaskID: "1200",
			ResultURL:      "https://cdn.example.com/result.mp4",
			TokenId:        17,
		},
	}
	task.SetData(map[string]any{"private_provider_payload": "must-not-leak"})
	require.NoError(t, model.DB.Create(task).Error)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/task_public", nil)
	c.Params = gin.Params{{Key: "task_id", Value: "task_public"}}
	c.Set("id", 42)

	taskErr := RelayTaskFetch(c, relayconstant.RelayModeVideoFetchByID)

	require.Nil(t, taskErr)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.ElementsMatch(t, []string{"code", "data"}, mapKeys(response))
	assert.Equal(t, dto.TaskSuccessCode, response["code"])
	data, ok := response["data"].(map[string]any)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{
		"task_id", "status", "fail_reason", "result_url",
		"submit_time", "start_time", "finish_time", "progress",
	}, mapKeys(data))
	assert.Equal(t, "task_public", data["task_id"])
	assert.Equal(t, string(model.TaskStatusSuccess), data["status"])
	assert.Equal(t, "", data["fail_reason"])
	assert.Equal(t, "https://cdn.example.com/result.mp4", data["result_url"])
	assert.EqualValues(t, 110, data["submit_time"])
	assert.EqualValues(t, 120, data["start_time"])
	assert.EqualValues(t, 190, data["finish_time"])
	assert.Equal(t, "100%", data["progress"])
	assert.NotContains(t, recorder.Body.String(), "supplier-secret")
	assert.NotContains(t, recorder.Body.String(), "must-not-leak")
	assert.NotContains(t, recorder.Body.String(), "sensitive-group")
	assert.NotContains(t, recorder.Body.String(), "private-upstream-model")
}

func TestSeedanceDomesticPendingTaskFetchIncludesNullableContractFields(t *testing.T) {
	setupRelayTaskFetchTestDB(t)
	task := &model.Task{
		TaskID:     "task_pending",
		Platform:   constant.TaskPlatform(fmt.Sprintf("%d", constant.ChannelTypeSeedanceDomestic)),
		UserId:     42,
		Status:     model.TaskStatusQueued,
		SubmitTime: 110,
		Progress:   "10%",
	}
	require.NoError(t, model.DB.Create(task).Error)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/task_pending", nil)
	c.Params = gin.Params{{Key: "task_id", Value: "task_pending"}}
	c.Set("id", 42)

	taskErr := RelayTaskFetch(c, relayconstant.RelayModeVideoFetchByID)

	require.Nil(t, taskErr)
	var response struct {
		Code string `json:"code"`
		Data struct {
			ResultURL  *string `json:"result_url"`
			StartTime  *int64  `json:"start_time"`
			FinishTime *int64  `json:"finish_time"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, dto.TaskSuccessCode, response.Code)
	assert.Nil(t, response.Data.ResultURL)
	assert.Nil(t, response.Data.StartTime)
	assert.Nil(t, response.Data.FinishTime)
	assert.Contains(t, recorder.Body.String(), `"result_url":null`)
	assert.Contains(t, recorder.Body.String(), `"start_time":null`)
	assert.Contains(t, recorder.Body.String(), `"finish_time":null`)
}

func TestSeedanceDomesticOpenAIVideoFetchRemainsOpenAIFormat(t *testing.T) {
	setupRelayTaskFetchTestDB(t)
	task := &model.Task{
		CreatedAt: 100,
		UpdatedAt: 200,
		TaskID:    "task_openai",
		Platform:  constant.TaskPlatform(fmt.Sprintf("%d", constant.ChannelTypeSeedanceDomestic)),
		UserId:    42,
		Status:    model.TaskStatusSuccess,
		Progress:  "100%",
		Properties: model.Properties{
			OriginModelName: "doubao-seedance-2-0-260128",
		},
		PrivateData: model.TaskPrivateData{
			ResultURL: "https://cdn.example.com/result.mp4",
		},
	}
	require.NoError(t, model.DB.Create(task).Error)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/task_openai", nil)
	c.Params = gin.Params{{Key: "task_id", Value: "task_openai"}}
	c.Set("id", 42)

	taskErr := RelayTaskFetch(c, relayconstant.RelayModeVideoFetchByID)

	require.Nil(t, taskErr)
	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "task_openai", response["id"])
	assert.Equal(t, "task_openai", response["task_id"])
	assert.Equal(t, "completed", response["status"])
	assert.Equal(t, "doubao-seedance-2-0-260128", response["model"])
	assert.NotContains(t, response, "code")
	assert.NotContains(t, response, "data")
}

func setupRelayTaskFetchTestDB(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalMainType := common.MainDatabaseType()
	originalLogType := common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			require.NoError(t, sqlDB.Close())
		}
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.SetDatabaseTypes(originalMainType, originalLogType)
	})
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
