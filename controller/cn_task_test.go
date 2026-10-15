package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestCNTaskControllerEntrances(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_TASK_ENABLED", "true")
	dialect := common.DatabaseType(os.Getenv("TEST_TASK_DB_DIALECT"))
	var driver gorm.Dialector
	switch dialect {
	case "", common.DatabaseTypeSQLite:
		dialect, driver = common.DatabaseTypeSQLite, sqlite.Open(":memory:")
	case common.DatabaseTypeMySQL:
		require.NotEmpty(t, os.Getenv("TEST_MYSQL_DSN"))
		driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
	case common.DatabaseTypePostgreSQL:
		require.NotEmpty(t, os.Getenv("TEST_POSTGRES_DSN"))
		driver = postgres.New(postgres.Config{DSN: os.Getenv("TEST_POSTGRES_DSN"), PreferSimpleProtocol: true})
	default:
		t.Fatalf("unsupported dialect %s", dialect)
	}
	prefix := fmt.Sprintf("cntask_%d_", time.Now().UnixNano())
	db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: prefix}})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	if dialect == common.DatabaseTypeSQLite {
		pool.SetMaxOpenConns(1)
	}
	t.Cleanup(func() {
		tables, err := db.Migrator().GetTables()
		require.NoError(t, err)
		for _, table := range tables {
			if strings.HasPrefix(table, prefix) {
				require.NoError(t, db.Migrator().DropTable(table))
			}
		}
		_ = pool.Close()
	})
	oldDB, oldLog := model.DB, model.LOG_DB
	oldMain, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	oldRedis, oldBatch, oldMemory, oldUnit, oldRetries := common.RedisEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled, common.QuotaPerUnit, common.RetryTimes
	oldPrices, oldRatios := ratio_setting.ModelPrice2JSONString(), ratio_setting.GroupRatio2JSONString()
	oldFactory := service.GetTaskAdaptorFunc
	oldQueryLimit := constant.TaskQueryLimit
	constant.TaskQueryLimit = 100
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(dialect, dialect)
	common.RedisEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled, common.QuotaPerUnit, common.RetryTimes = false, false, false, 1000, 3
	service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor { return relay.GetTaskAdaptor(platform) }
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"wan2.6-t2i":0.1,"wan2.6-t2v":0.1}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLog
		common.SetDatabaseTypes(oldMain, oldLogType)
		common.RedisEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled, common.QuotaPerUnit, common.RetryTimes = oldRedis, oldBatch, oldMemory, oldUnit, oldRetries
		service.GetTaskAdaptorFunc = oldFactory
		constant.TaskQueryLimit = oldQueryLimit
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatios))
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}, &model.TaskBillingReconciliation{}, &model.Log{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}))
	require.NoError(t, model.MigrateCNFinance(db, "cn-task-controller"))
	allowPrivateTaskMediaTest(t)
	plugin, exists := pluginruntime.DefaultRegistry.Generation().Get("alibaba")
	require.True(t, exists, "exercise the actually registered factory plugin")
	for i, name := range []string{"image-immediate", "video", "native", "unknown", "accepted-insert-failed", "acceptance-payload-failed", "native-insert-failed", "native-payload-failed", "caller-cancelled", "responses-sync", "responses-background", "responses-stream", "submit-timeout", "submit-conflict"} {
		t.Run(name, func(t *testing.T) {
			id := 700 + i
			user := model.User{Id: id, Username: name, AffCode: name, Quota: 1000000, Group: "default"}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, db.Create(&model.Token{Id: id, UserId: id, Key: "token-" + name, RemainQuota: 1000000}).Error)
			var posts, gets atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					gets.Add(1)
					if strings.HasPrefix(name, "native") {
						assert.Equal(t, "/v1/video/generations/provider-native", r.URL.Path)
						_, _ = w.Write([]byte(`{"id":"provider-native","status":"succeeded","usage":{"total_tokens":100000},"content":{"video_url":"https://cdn.example/native.mp4"}}`))
						return
					}
					assert.Equal(t, "/api/v1/tasks/provider-video", r.URL.Path)
					_, _ = w.Write([]byte(`{"output":{"task_id":"provider-video","task_status":"SUCCEEDED","video_url":"https://cdn.example/video.mp4"}}`))
					return
				}
				posts.Add(1)
				if name == "submit-timeout" || name == "submit-conflict" {
					status := http.StatusRequestTimeout
					if name == "submit-conflict" {
						status = http.StatusConflict
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":"provider acceptance unknown"}`))
					return
				}
				if name == "unknown" {
					_, _ = w.Write([]byte(`{"malformed":true}`))
					return
				}
				if name == "image-immediate" || strings.HasPrefix(name, "responses-") {
					_, _ = w.Write([]byte(`{"request_id":"image-request","output":{"choices":[{"message":{"content":[{"image":"https://cdn.example/image.png"}]}}]},"usage":{"image_count":1}}`))
				} else if strings.HasPrefix(name, "native") {
					_, _ = w.Write([]byte(`{"id":"provider-native","status":"queued"}`))
				} else {
					_, _ = w.Write([]byte(`{"output":{"task_id":"provider-video","task_status":"PENDING"}}`))
				}
			}))
			t.Cleanup(upstream.Close)
			channel := model.Channel{Id: id, Name: name, Type: constant.ChannelTypeAli, Status: common.ChannelStatusEnabled, Key: "fixture-key", BaseURL: &upstream.URL}
			modelName, body := "wan2.6-t2v", `{"model":"wan2.6-t2v","prompt":"A blue square","duration":5}`
			if name == "image-immediate" || strings.HasPrefix(name, "responses-") {
				modelName, body = "wan2.6-t2i", `{"model":"wan2.6-t2i","prompt":"A blue square","metadata":{"upstream_mode":"sync","parameters":{"n":1}}}`
			}
			if strings.HasPrefix(name, "native") {
				channel.Type = constant.ChannelTypeOpenAISeedance
				modelName, body = "doubao-seedance-2-0-260128", `{"model":"doubao-seedance-2-0-260128","prompt":"A blue square","duration":5}`
			}
			require.NoError(t, db.Create(&channel).Error)
			c := taskSubmissionTestContext()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, common.RequestIdKey, "cn-controller-"+name)
			common.SetContextKey(c, constant.ContextKeyUserId, id)
			common.SetContextKey(c, constant.ContextKeyUserQuota, user.Quota)
			common.SetContextKey(c, constant.ContextKeyOriginalModel, modelName)
			common.SetContextKey(c, constant.ContextKeyTokenId, id)
			common.SetContextKey(c, constant.ContextKeyTokenKey, "token-"+name)
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			if !strings.HasPrefix(name, "native") {
				c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Plugin: plugin})
			}
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, &channel, modelName))
			info := &relaycommon.RelayInfo{UserId: id, TokenId: id, RequestId: "cn-controller-" + name, UserQuota: user.Quota, UserGroup: "default", UsingGroup: "default", OriginModelName: modelName, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task-cn-" + name, LockedChannel: &channel}}
			info.UserSetting.BillingPreference = "wallet_only"
			if name == "accepted-insert-failed" || name == "native-insert-failed" {
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("cn-insert-fail", func(tx *gorm.DB) {
					if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Task" {
						tx.AddError(errors.New("injected accepted task insert failure"))
					}
				}))
				t.Cleanup(func() { _ = db.Callback().Create().Remove("cn-insert-fail") })
			}
			if name == "acceptance-payload-failed" || name == "native-payload-failed" {
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("cn-payload-fail", func(tx *gorm.DB) {
					if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CNTaskBilling" {
						if values, ok := tx.Statement.Dest.(map[string]any); ok && values["accepted_task"] != nil {
							tx.AddError(errors.New("injected acceptance payload failure"))
						}
					}
				}))
				t.Cleanup(func() { _ = db.Callback().Update().Remove("cn-payload-fail") })
			}
			submit := relay.RelayTaskSubmit
			if name == "caller-cancelled" {
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				submit = func(c *gin.Context, info *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
					result, err := relay.RelayTaskSubmit(c, info)
					cancel()
					return result, err
				}
			}
			var outcome *taskSubmissionOutcome
			var taskErr *dto.TaskError
			if strings.HasPrefix(name, "responses-") {
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set("resolved_task_model", modelName)
				requestBody := map[string]any{}
				require.NoError(t, common.UnmarshalJsonStr(body, &requestBody))
				requestBody["input"], requestBody["background"] = "A blue square", name == "responses-background"
				pinned := pluginruntime.PinnedEndpoint{Generation: pluginruntime.DefaultRegistry.Generation(), Plugin: plugin, Protocol: "openai_responses", Operation: pluginruntime.HostProtocolOperation{Name: "create"}, Model: modelName, MappedModel: modelName}
				c.Set(pluginruntime.ContextKeyPinnedEndpoint, pinned)
				c.Set(pluginruntime.ContextKeyProtocolRequest, pluginruntime.ProtocolRequestContext{RouteRequestContext: pluginruntime.RouteRequestContext{Path: "/v1/responses", Method: http.MethodPost, Body: map[string]any{"kind": "json", "value": requestBody}, RequestBody: requestBody}, Protocol: "openai_responses", Model: modelName, UpstreamModel: modelName, Stream: name == "responses-stream"})
				deps := pluginProtocolTestDeps()
				deps.submit = func(c *gin.Context, generated *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
					generated.LockedChannel = &channel
					outcome, taskErr = executeTaskSubmission(c, generated)
					return outcome, taskErr
				}
				serveTaskPluginProtocol(c, pinned, deps)
				require.Nil(t, taskErr)
				require.NotNil(t, outcome)
				assert.Equal(t, http.StatusOK, c.Writer.Status())
				if name == "responses-stream" {
					assert.Equal(t, "text/event-stream", c.Writer.Header().Get("Content-Type"))
				}
			} else {
				outcome, taskErr = executeTaskSubmissionWith(c, info, submit)
			}
			if name == "video" {
				summary := service.RunTaskPollingOnce(context.Background(), nil)
				assert.Positive(t, summary.PlatformsScanned)
			}
			assert.Equal(t, int32(1), posts.Load())
			var row model.CNTaskBilling
			require.NoError(t, db.Where("request_id = ?", info.RequestId).First(&row).Error)
			var wallet model.CNWalletLifecycle
			require.ErrorIs(t, db.Where("request_id = ?", info.RequestId).First(&wallet).Error, gorm.ErrRecordNotFound)
			unknown := name == "unknown" || name == "submit-timeout" || name == "submit-conflict"
			if unknown {
				require.NotNil(t, taskErr)
				assert.Equal(t, "unknown", row.State)
				assert.Zero(t, row.TaskID)
				require.Error(t, service.ProbeCNTaskSubmission(context.Background(), row.Key))
				assert.Zero(t, gets.Load())
				require.ErrorIs(t, model.RequestCNTaskCancellation(row.Key), model.ErrCNConflict)
			} else if name == "accepted-insert-failed" || name == "native-insert-failed" {
				require.NotNil(t, taskErr)
				require.True(t, row.Pending)
				require.NotEmpty(t, row.AcceptedTask)
				require.NoError(t, db.Callback().Create().Remove("cn-insert-fail"))
				require.NoError(t, model.CompleteCNTaskIntent(context.Background(), db, row.Key))
			} else if name == "acceptance-payload-failed" || name == "native-payload-failed" {
				require.NotNil(t, taskErr)
				assert.False(t, row.Pending)
				require.NotEmpty(t, row.UpstreamID)
				require.NoError(t, db.Callback().Update().Remove("cn-payload-fail"))
				if name == "native-payload-failed" {
					require.NoError(t, db.Model(&channel).Update("key", "rotated-fixture-key").Error)
					require.NoError(t, db.Model(&row).Update("next_attempt_at", 0).Error)
					require.ErrorContains(t, service.RunCNTaskSubmissionProbes(context.Background(), 10), "credential_changed")
					assert.Zero(t, gets.Load(), "changed credentials must not query an accepted task")
					require.NoError(t, db.Model(&channel).Update("key", "fixture-key").Error)
				}
				require.NoError(t, db.Model(&row).Update("next_attempt_at", 0).Error)
				require.True(t, model.HasDueCNTaskSubmissions())
				require.NoError(t, service.RunCNTaskSubmissionProbes(context.Background(), 10))
				assert.Equal(t, int32(1), gets.Load())
			} else {
				require.Nil(t, taskErr)
				require.NotNil(t, outcome)
			}
			if strings.HasPrefix(name, "native") {
				var task model.Task
				require.NoError(t, db.Where("cn_billing_key = ?", row.Key).First(&task).Error)
				if name != "native" {
					require.True(t, task.BillingReconciliationPending, "acceptance recovery must retain the provider-bill enqueue intent")
				}
				_, err := model.EnqueuePendingTaskBillingReconciliations(100)
				require.NoError(t, err)
				var record model.TaskBillingReconciliation
				require.NoError(t, db.Where("task_id = ?", task.ID).First(&record).Error)
				if task.Status != model.TaskStatusSuccess {
					service.DispatchPlatformUpdate(context.Background(), task.Platform, map[int][]string{channel.Id: {task.GetUpstreamTaskID()}}, map[string]*model.Task{task.GetUpstreamTaskID(): &task})
				}
				require.NoError(t, db.First(&task, task.ID).Error)
				assert.EqualValues(t, model.TaskStatusSuccess, task.Status)
				summary := service.RunTaskBillingReconciliationOnce(context.Background(), 100)
				require.Positive(t, summary.Settled)
				require.NoError(t, db.First(&record, record.ID).Error)
				assert.Equal(t, model.TaskBillingReconciliationSettled, record.Status)
			}
			assert.Equal(t, int32(1), posts.Load(), "recovery may only query, never resubmit")
			require.NoError(t, db.First(&user, id).Error)
			require.NoError(t, db.First(&row, map[string]any{"key": row.Key}).Error)
			assert.Equal(t, 1000000-row.HeldQuota, user.Quota)
			assert.Equal(t, row.Actual, user.UsedQuota)
			if !unknown {
				assert.Equal(t, 1, user.RequestCount)
				assert.Positive(t, row.TaskID)
			}
			if name == "image-immediate" || name == "acceptance-payload-failed" || name == "video" || strings.HasPrefix(name, "responses-") || strings.HasPrefix(name, "native") {
				assert.Equal(t, "final", row.State)
			}
		})
	}
}
