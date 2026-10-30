package controller

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsmonitor"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpsAcceptanceDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env)
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.Token{}, &model.Log{}, &model.Task{}, &model.Midjourney{}, &model.SystemTask{}, &model.OpsRequestEvent{}, &model.OpsUpstreamAttempt{}, &model.OpsMinuteMetric{}, &model.OpsSystemMetric{}, &model.OpsConcurrencyLimit{}))
			start := time.Now().Unix() - 3600
			users := []model.User{{Id: 701, Username: "ops-alice", AffCode: "ops-alice"}, {Id: 702, Username: "ops-bob", AffCode: "ops-bob"}, {Id: 703, Username: "ops-no-traffic", AffCode: "ops-no-traffic"}}
			require.NoError(t, db.Create(&users).Error)
			tokens := []model.Token{{Id: 801, UserId: 701, Name: "alice-first", Key: "private-alice-first"}, {Id: 802, UserId: 701, Name: "alice-second", Key: "private-alice-second"}, {Id: 803, UserId: 702, Name: "bob-first", Key: "private-bob-first"}, {Id: 804, UserId: 701, Name: "alice-no-traffic", Key: "private-alice-unused"}, {Id: 805, UserId: 703, Name: "new-user-key", Key: "private-unused-user-key"}}
			require.NoError(t, db.Create(&tokens).Error)
			require.NoError(t, db.Create(&model.Model{ModelName: "registered-no-traffic"}).Error)
			require.NoError(t, db.Create(&model.Channel{Name: "disabled-no-traffic", Status: common.ChannelStatusManuallyDisabled, Models: "channel-no-traffic", Group: "channel-only-group"}).Error)
			previousGroups := ratio_setting.GroupRatio2JSONString()
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"configured-only-group":1}`))
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups)) })
			events := []model.OpsRequestEvent{
				{RequestID: "chat-retried", OccurredAtMs: (start + 1) * 1000, UserID: 701, TokenID: 801, ModelName: "deepseek-test", Group: "default", ChannelID: 7, NodeName: "blue", EndpointType: "chat", StatusCode: 200, Success: true, InputTokens: 60, CacheReadTokens: 30, CacheCreationTokens: 10, OutputTokens: 20, DurationMs: 1000, HasTTFT: true, TTFTMs: 200, RetryCount: 1, SwitchCount: 1},
				{RequestID: "video-submit", OccurredAtMs: (start + 2) * 1000, UserID: 701, TokenID: 801, ModelName: "video-test", Group: "default", ChannelID: 7, NodeName: "blue", EndpointType: "video_submit", StatusCode: 200, Success: true, DurationMs: 200, TaskID: "task-alice"},
				{RequestID: "alice-failed", OccurredAtMs: (start + 3) * 1000, UserID: 701, TokenID: 802, ModelName: "deepseek-test", Group: "default", ChannelID: 8, NodeName: "green", EndpointType: "chat", StatusCode: 502, Success: false, ErrorOwner: "provider", InputTokens: 999},
				{RequestID: "bob-video", OccurredAtMs: (start + 4) * 1000, UserID: 702, TokenID: 803, ModelName: "video-test", Group: "other", ChannelID: 8, NodeName: "green", EndpointType: "video_submit", StatusCode: 200, Success: true, TaskID: "task-bob"},
				{RequestID: "outside-window", OccurredAtMs: (start - 1) * 1000, UserID: 701, TokenID: 801, ModelName: "deepseek-test", EndpointType: "chat", Success: true, OutputTokens: 999},
			}
			require.NoError(t, model.CreateOpsRequestEvents(events))
			attempts := []model.OpsUpstreamAttempt{{RequestID: "chat-retried", AttemptIndex: 0, ChannelID: 6, StatusCode: 503}, {RequestID: "chat-retried", AttemptIndex: 1, ChannelID: 7, StatusCode: 200, Success: true}}
			require.NoError(t, model.CreateOpsUpstreamAttempts(attempts))
			tasks := []model.Task{{TaskID: "task-alice", UserId: 701, CreatedAt: start + 2, SubmitTime: start + 2, StartTime: start + 5, FinishTime: start + 20, Status: model.TaskStatusSuccess}, {TaskID: "task-bob", UserId: 702, CreatedAt: start + 4, Status: model.TaskStatusFailure}}
			require.NoError(t, db.Create(&tasks).Error)
			logs := []model.Log{{RequestId: "chat-retried", UserId: 701, TokenId: 801, ModelName: "deepseek-test", ChannelId: 7, Group: "default", CreatedAt: start + 10, Type: model.LogTypeConsume, Quota: 120}, {RequestId: "video-submit", UserId: 701, TokenId: 801, ModelName: "video-test", ChannelId: 7, Group: "default", CreatedAt: start + 11, Type: model.LogTypeConsume, Quota: 50}, {RequestId: "video-submit", UserId: 701, TokenId: 801, ModelName: "video-test", ChannelId: 7, Group: "default", CreatedAt: start + 12, Type: model.LogTypeRefund, Quota: 10}, {UserId: 702, TokenId: 803, CreatedAt: start + 12, Type: model.LogTypeConsume, Quota: 999}}
			require.NoError(t, db.Create(&logs).Error)
			filter := opsmonitor.DashboardFilter{StartTs: start, EndTs: start + 60, UserID: 701, TokenID: 801}
			snapshot, err := opsmonitor.BuildDashboardSnapshot(filter)
			require.NoError(t, err)
			assert.EqualValues(t, 2, snapshot.Overview.RequestCount, "upstream retries do not become downstream requests")
			assert.EqualValues(t, 2, snapshot.Overview.SuccessCount)
			assert.EqualValues(t, 120, snapshot.Overview.TotalTokens)
			assert.Equal(t, 1.0, snapshot.Overview.SuccessRate)
			assert.Equal(t, 2.0, snapshot.Overview.AverageRPM)
			assert.Equal(t, 120.0, snapshot.Overview.AverageTPM)
			assert.Equal(t, 0.5, snapshot.Overview.AverageSwitches)
			assert.EqualValues(t, 600, snapshot.Latency.Duration.Avg)
			assert.EqualValues(t, 200, snapshot.Latency.TTFT.Avg)
			require.Len(t, snapshot.ModelTokenStats, 1)
			assert.Equal(t, "deepseek-test", snapshot.ModelTokenStats[0].ModelName)
			assert.Equal(t, 20.0, snapshot.ModelTokenStats[0].AverageTokensPS)
			assert.EqualValues(t, 1, snapshot.Tasks.Submitted)
			assert.EqualValues(t, 1, snapshot.Tasks.Succeeded)
			assert.Zero(t, snapshot.Tasks.Failed)
			assert.EqualValues(t, 2, snapshot.Billing.ConsumeEntries)
			assert.EqualValues(t, 1, snapshot.Billing.RefundEntries)
			assert.EqualValues(t, 160, snapshot.Billing.NetQuota)
			require.Len(t, snapshot.Options.Users, 3, "directory users must include users without recent activity")
			require.Len(t, snapshot.Options.Tokens, 3, "all of the selected user's keys are listed, including unused keys")
			assert.Contains(t, snapshot.Options.Models, "registered-no-traffic")
			assert.Contains(t, snapshot.Options.Models, "channel-no-traffic")
			assert.Contains(t, snapshot.Options.Groups, "channel-only-group")
			assert.Contains(t, snapshot.Options.Groups, "configured-only-group")
			assert.Contains(t, snapshot.Options.EndpointTypes, "audio", "supported endpoints must not disappear when unused")
			assert.Contains(t, snapshot.Options.EndpointTypes, "video_content")
			assert.Contains(t, snapshot.Options.Models, "video-test", "the directory is independent of the selected user/key")
			assert.Len(t, snapshot.Concurrency.Channels, 1, "disabled channels remain selectable")
			encoded, err := common.Marshal(snapshot)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "private-alice")
			filter.TokenID = 802
			snapshot, err = opsmonitor.BuildDashboardSnapshot(filter)
			require.NoError(t, err)
			assert.EqualValues(t, 1, snapshot.Overview.RequestCount)
			assert.Equal(t, 1.0, snapshot.Overview.FailureRate)
			assert.Zero(t, snapshot.Tasks.Submitted)
			assert.Zero(t, snapshot.Billing.NetQuota)
			filter.TokenID, filter.UserID = 801, 702
			snapshot, err = opsmonitor.BuildDashboardSnapshot(filter)
			require.NoError(t, err)
			assert.Zero(t, snapshot.Overview.RequestCount, "cross-user API Key filters must not widen the query")
			assert.Empty(t, snapshot.RecentErrors)
			assert.Zero(t, snapshot.Tasks.Terminal)
			snapshot, err = opsmonitor.BuildDashboardSnapshot(opsmonitor.DashboardFilter{StartTs: start + 30, EndTs: start + 60, UserID: 703})
			require.NoError(t, err)
			assert.Zero(t, snapshot.Overview.RequestCount)
			assert.Len(t, snapshot.Options.Users, 3)
			assert.Equal(t, []model.OpsIdentityOption{{ID: 805, Name: "new-user-key"}}, snapshot.Options.Tokens)
			assert.Contains(t, snapshot.Options.Models, "registered-no-traffic", "an empty result window must retain the catalog")
			require.NoError(t, db.Create(&model.OpsRequestEvent{RequestID: "mj-submit", OccurredAtMs: (start + 5) * 1000, UserID: 701, TokenID: 801, EndpointType: "async_task", Success: true, StatusCode: 200, TaskID: "mj-alice"}).Error)
			require.NoError(t, db.Create(&[]model.Midjourney{{MjId: "mj-alice", Code: 22, SubmitTime: (start + 5) * 1000}, {MjId: "mj-alice", Code: 1, Status: "SUCCESS", SubmitTime: (start + 5) * 1000, StartTime: (start + 8) * 1000, FinishTime: (start + 20) * 1000}}).Error)
			snapshot, err = opsmonitor.BuildDashboardSnapshot(opsmonitor.DashboardFilter{StartTs: start, EndTs: start + 60, UserID: 701, TokenID: 801, EndpointType: "async_task"})
			require.NoError(t, err)
			assert.EqualValues(t, 1, snapshot.Tasks.Submitted)
			assert.EqualValues(t, 1, snapshot.Tasks.Succeeded, "existing-task responses must not duplicate terminal tasks")
			assert.EqualValues(t, 3000, snapshot.Tasks.AverageQueueMs)
			assert.False(t, snapshot.Billing.Available, "unsupported financial dimensions must not show unfiltered totals")
			var invalid struct {
				Success bool
				Message string
			}
			modelManagementRequest(t, GetOpsDashboardSnapshot, http.MethodGet, "/api/ops/dashboard/snapshot?user_id=-1", nil, &invalid)
			assert.False(t, invalid.Success)
			assert.Contains(t, invalid.Message, "user_id")

			base := "https://user:password@upstream.example/v1?api_key=private-query#private-fragment"
			channels := []model.Channel{{Name: "enabled-provider", Models: "ops-managed", Type: 1, Status: 1, BaseURL: &base, Key: "sk-secret-middle-content-123456"}, {Name: "disabled-provider", Models: "ops-managed", Type: 1, Status: 2, BaseURL: &base, Key: `{"private_key":"never-expose"}`}}
			require.NoError(t, db.Create(&channels).Error)
			connections, err := model.GetConfiguredModelConnections()
			require.NoError(t, err)
			require.Len(t, connections["ops-managed"], 2)
			assert.Equal(t, "https://upstream.example/v1", connections["ops-managed"][0].BaseURL)
			assert.Equal(t, 2, connections["ops-managed"][1].Status)
			assert.Equal(t, "********", connections["ops-managed"][1].MaskedKey)
			encoded, err = common.Marshal(connections)
			require.NoError(t, err)
			for _, secret := range []string{"password", "private-query", "private-fragment", "secret-middle-content", "never-expose"} {
				assert.NotContains(t, string(encoded), secret)
			}
			t.Setenv("FEISHU_ALERT_WEBHOOK_URL", "")
			previousDisable, previousEnable := common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled
			common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled = true, true
			t.Cleanup(func() {
				common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled = previousDisable, previousEnable
			})
			probe := &model.Channel{Name: "unsupported-video-probe", Type: constant.ChannelTypeTaskPlugin, Models: "video-model", Status: common.ChannelStatusAutoDisabled}
			require.NoError(t, db.Create(probe).Error)
			result := testChannelForHealthCheck(context.Background(), probe, 701, true, -1)
			assert.Equal(t, channelTestSummary{Tested: 1, Failed: 1}, result, "unsupported local probes must not count as successful or enable the channel")
			require.NoError(t, db.First(probe, probe.Id).Error)
			assert.Equal(t, common.ChannelStatusAutoDisabled, probe.Status)
		})
	}
}
