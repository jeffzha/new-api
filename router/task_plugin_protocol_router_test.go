package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestHostProtocolRegistryDrivesProtocolRoutesOnce(t *testing.T) {
	engine := gin.New()
	SetTaskPluginProtocolRouter(engine)

	expected := []string{
		"POST /v1/responses",
		"GET /v1/responses/:response_id",
		"POST /v1/videos",
		"GET /v1/videos/:task_id",
		"GET /v1/videos/:task_id/content",
		"HEAD /v1/videos/:task_id/content",
	}
	actual := make([]string, 0, len(engine.Routes()))
	for _, route := range engine.Routes() {
		actual = append(actual, fmt.Sprintf("%s %s", route.Method, route.Path))
	}
	sort.Strings(expected)
	sort.Strings(actual)
	assert.Equal(t, expected, actual)
}

func TestPinnedPluginVideoRetrievalUsesOwnedTaskWithoutSubmitting(t *testing.T) {
	require.NoError(t, i18n.Init())
	previousDB := model.DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Task{}, &model.Channel{}))
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })
	_, err = jsplugin.DefaultRegistry.Register(`
export const meta = {apiVersion: 1, key: "health-retrieve-fixture", name: "Fixture", version: "1.0.0", author: {name: "Test"}, models: ["health-video"], fetchMode: "per_task", protocols: ["openai_video"]};
export const protocols = {openai_video: {decodeRequest: function() {throw new Error("must not submit");}, render: function() {return {};}}};
export function buildSubmitRequest() {throw new Error("must not submit");}
export function parseSubmitResponse() {return {taskId: "unused"};}
export function buildQueryRequest() {throw new Error("must not resubmit or query upstream");}
export function parseTaskResult() {return {status: "SUCCESS"};}
export function listArtifacts() {return [];}
export function buildContentRequest() {throw new Error("must not fetch content");}
`, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("health-retrieve-fixture") })
	channel := model.Channel{Id: 71, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled}
	require.NoError(t, database.Create(&channel).Error)
	task := model.Task{TaskID: "task_health_retrieve", UserId: 273, ChannelId: channel.Id, Platform: "health-retrieve-fixture", Status: model.TaskStatusSuccess, Progress: "100%", Quota: 123, Properties: model.Properties{OriginModelName: "health-video"}}
	require.NoError(t, database.Create(&task).Error)
	handlers, err := taskPluginProtocolHandlers("openai_video", "retrieve")
	require.NoError(t, err)
	for _, testCase := range []struct {
		name    string
		userID  int
		limited bool
		allowed bool
		status  int
	}{
		{"owner pinned", 273, false, false, http.StatusOK},
		{"owner model allowed", 273, true, true, http.StatusOK},
		{"owner model forbidden", 273, true, false, http.StatusForbidden},
		{"other user", 274, false, false, http.StatusBadRequest},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			engine := gin.New()
			engine.GET("/v1/videos/:task_id", append([]gin.HandlerFunc{func(c *gin.Context) {
				c.Set("id", testCase.userID)
				common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, testCase.limited)
				common.SetContextKey(c, constant.ContextKeyTokenModelLimit, map[string]bool{"health-video": testCase.allowed})
				service.GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceToken, Rank: dto.PinRankToken})
				c.Next()
			}}, handlers[2:]...)...)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/videos/task_health_retrieve", nil))
			require.Equal(t, testCase.status, recorder.Code, recorder.Body.String())
			if testCase.status == http.StatusOK {
				assert.Contains(t, recorder.Body.String(), `"status":"completed"`)
				assert.Contains(t, recorder.Body.String(), `"id":"task_health_retrieve"`)
			}
		})
	}
	var saved model.Task
	require.NoError(t, database.First(&saved, task.ID).Error)
	assert.Equal(t, task.Quota, saved.Quota)
	assert.Equal(t, task.Status, saved.Status)
}
