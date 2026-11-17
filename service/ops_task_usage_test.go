package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpsTaskTerminalCrashRecoveryAndScope(t *testing.T) {
	truncate(t)
	previous := opsbridge.Default
	dir := filepath.Join(t.TempDir(), "bridge")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"decision":"allowed"}}`))
	}))
	defer server.Close()
	config := opsbridge.Config{URL: server.URL, GatewayID: "test-gateway", Key: "private-key", StateDir: dir, EnforceCalls: true}
	b, err := opsbridge.Open(config)
	require.NoError(t, err)
	opsbridge.Default = b
	t.Cleanup(func() { _ = opsbridge.Default.Close(); opsbridge.Default = previous })
	task := seedPollingTask(t, 1, "task_ops_final", "private-upstream-id")
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Set("id", task.UserId)
	c.Set(common.RequestIdKey, "request-task-final")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDoubaoVideo)
	require.NoError(t, opsbridge.ReserveTask(c, task.TaskID, "seedance", "video_generation"))
	require.NoError(t, opsbridge.BeforeTaskDispatch(c))
	opsbridge.FinishTaskSubmission(c, true)
	task.Properties.OriginModelName = "seedance"
	task.PrivateData.BillingContext = &model.TaskBillingContext{ProviderBilling: &model.TaskProviderBillingSnapshot{HasVideoInput: true}}
	task.Status, task.FinishTime = model.TaskStatusSuccess, 1791936000
	body := []byte(`{"status":"succeeded","usage":{"total_tokens":100,"completion_tokens":80},"duration":1.001,"resolution":"720p","content":{"video_url":"private-media"},"prompt":"private-prompt"}`)
	prepareOpsTaskUsage(task, constant.ChannelTypeDoubaoVideo, body)
	require.True(t, task.OpsUsagePending)
	assert.NotContains(t, string(task.PrivateData.OpsUsage), "private-")
	assert.Contains(t, string(task.PrivateData.OpsUsage), `"duration_ms":1001`)
	won, err := task.UpdateWithStatus(model.TaskStatusInProgress)
	require.NoError(t, err)
	require.True(t, won)
	// Losing the terminal CAS cannot introduce a second payload.
	task.PrivateData.OpsUsage = []byte(`{"event_id":"wrong"}`)
	won, err = task.UpdateWithStatus(model.TaskStatusInProgress)
	require.NoError(t, err)
	assert.False(t, won)
	require.NoError(t, b.Close())
	b, err = opsbridge.Open(config)
	require.NoError(t, err)
	opsbridge.Default = b
	drainOpsTaskUsage(context.Background())
	var stored model.Task
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.False(t, stored.OpsUsagePending)
	// Simulate a crash before the DB acknowledgement; identical replay succeeds.
	require.NoError(t, model.DB.Model(&stored).Update("ops_usage_pending", true).Error)
	drainOpsTaskUsage(context.Background())
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.False(t, stored.OpsUsagePending)
	var record map[string]any
	require.NoError(t, common.Unmarshal(stored.PrivateData.OpsUsage, &record))
	record["bridge_scope"] = "another-gateway"
	encoded, err := common.Marshal(record)
	require.NoError(t, err)
	stored.PrivateData.OpsUsage, stored.OpsUsagePending = encoded, true
	require.NoError(t, model.DB.Save(&stored).Error)
	drainOpsTaskUsage(context.Background())
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.True(t, stored.OpsUsagePending)
}

func TestOpsTaskMissingAndInvalidNativeUsageRemainsRepairable(t *testing.T) {
	previous := opsbridge.Default
	opsbridge.Default = &opsbridge.Bridge{}
	t.Cleanup(func() { opsbridge.Default = previous })
	for _, body := range []string{
		`{"status":"running","usage":{"total_tokens":10}}`,
		`{"status":"succeeded"}`,
		`{"status":"succeeded","usage":{"total_tokens":"10","unknown_secret":"private"}}`,
	} {
		task := &model.Task{TaskID: "task_ops_gap", Status: model.TaskStatusSuccess, FinishTime: 1791936000}
		prepareOpsTaskUsage(task, constant.ChannelTypeDoubaoVideo, []byte(body))
		require.True(t, task.OpsUsagePending)
		assert.NotContains(t, string(task.PrivateData.OpsUsage), "private")
	}
	task := &model.Task{TaskID: "task_ops_failed", Status: model.TaskStatusFailure}
	prepareOpsTaskUsage(task, constant.ChannelTypeDoubaoVideo, []byte(`{"usage":{"total_tokens":10}}`))
	assert.False(t, task.OpsUsagePending)
}
