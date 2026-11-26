package modelhealth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay/channel/task/ali"
	"github.com/QuantumNous/new-api/relay/channel/task/happyhorse"
	"github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	seedancepricing "github.com/QuantumNous/new-api/setting/seedance_video_pricing"
	"github.com/gin-gonic/gin"
	"github.com/go-audio/wav"
	"github.com/stretchr/testify/require"
)

func TestKnownMediaProfilesAreProviderSpecificAndBounded(t *testing.T) {
	saved := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"wan3.0-video":0.0821917808219178,"wan3.0-video-prime":0.1232876712328767,"qwen-image-2.0":0.0273972602739726}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(saved)) })
	for _, tc := range []struct {
		name                 string
		channel              int
		plugin               string
		endpoint, resolution string
		ready                bool
	}{
		{"doubao-seedance-2.5", 1000, "", "openai-video", "480p", true},
		{"doubao-seedance-2-5-260628", 54, "", "openai-video", "480p", true},
		{"wan3.0-video", 17, "", "openai-video", "480P", true},
		{"wan3.0-video-prime", 17, "", "openai-video", "480P", true},
		{"HappyHorse1.1", 1001, "", "openai-video", "480P", true},
		{"qwen-image-2.0", 17, "", "image-generation", "", true},
		{"qwen-image-3.0", 17, "", "image-generation", "", true},
		{"fun-asr-flash-2026-06-15", 17, "", "audio_transcription", "", true},
		{"wan3.0-video", 1, "", "openai-video", "", false},
		{"MiniMax-H3", 61, "unknown-plugin", "openai-video", "", false},
	} {
		t.Run(tc.name+tc.plugin+tc.endpoint+strconv.Itoa(tc.channel), func(t *testing.T) {
			ch := model.Channel{Id: 7, Type: tc.channel, Status: 1, Name: "fixture", Models: tc.name, BaseURL: common.GetPointer("https://provider.example")}
			ch.Setting = common.GetPointer(`{"task_plugin_key":"` + tc.plugin + `"}`)
			target := targetFor(&ch, tc.name)
			p := builtinTemplate(target, tc.endpoint, AutoTemplatePolicy{Credential: "MODEL_HEALTH_KEY_FIXTURE", MaxMicro: 10000})
			require.Equal(t, tc.ready, p.MinimumConfirmed)
			if !tc.ready {
				return
			}
			require.LessOrEqual(t, p.MaxMicro, int64(5000000))
			require.Greater(t, p.MaxMicro, int64(0))
			require.True(t, safeTemplateValue(p.Body))
			if tc.resolution != "" {
				require.Equal(t, tc.resolution, p.Body["resolution"])
			}
			if tc.endpoint == "openai-video" {
				require.Equal(t, "", p.ResultField)
			}
			require.True(t, p.validFor(target, tc.endpoint))
			if tc.channel == constant.ChannelTypeAli && tc.endpoint == "image-generation" {
				require.Equal(t, "1328x1328", p.Body["size"])
			}
		})
	}
}

func TestMediaProbeNativeVideoConversion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       int
		seconds    int
		resolution string
	}{
		{"wan3.0-video", constant.ChannelTypeAli, 2, "480P"},
		{"HappyHorse1.1", constant.ChannelTypeHappyHorse, 3, "480P"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := model.Channel{Id: 1, Type: tc.kind, Status: 1, Models: tc.name}
			p := builtinTemplate(targetFor(&ch, tc.name), "openai-video", AutoTemplatePolicy{MaxMicro: 10000})
			p.Body["model"] = tc.name
			encoded, err := common.Marshal(p.Body)
			require.NoError(t, err)
			var request relaycommon.TaskSubmitReq
			require.NoError(t, common.Unmarshal(encoded, &request))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(encoded))
			info := &relaycommon.RelayInfo{OriginModelName: tc.name, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: tc.name}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			relaycommon.StoreTaskRequest(c, info, constant.TaskActionGenerate, request)
			var body io.Reader
			if tc.kind == constant.ChannelTypeAli {
				body, err = (&ali.TaskAdaptor{}).BuildRequestBody(c, info)
			} else {
				body, err = (&happyhorse.TaskAdaptor{}).BuildRequestBody(c, info)
			}
			require.NoError(t, err)
			var result struct {
				Parameters struct {
					Duration   int
					Resolution string
				}
			}
			require.NoError(t, common.DecodeJson(body, &result))
			require.Equal(t, tc.seconds, result.Parameters.Duration)
			require.Equal(t, tc.resolution, result.Parameters.Resolution)
		})
	}
}

func TestMediaProbePluginConversionAndPublicTaskIdentity(t *testing.T) {
	for _, key := range []string{"oiioii", "cn-locopal-video"} {
		t.Run(key, func(t *testing.T) {
			var source string
			var err error
			name, mapped := "doubao-seedance-2.5", "doubao-seedance-2-5-260628"
			if key == "cn-locopal-video" {
				data, e := os.ReadFile("../../scripts/task-plugins/cn-locopal-video.js")
				require.NoError(t, e)
				source = string(data)
				name, mapped = "MiniMax-H3", "h3-video"
			} else {
				source, err = plugins.Source(key)
				require.NoError(t, err)
			}
			plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
			require.NoError(t, err)
			target := Target{Model: name, Upstream: mapped, channelType: constant.ChannelTypeTaskPlugin, pluginKey: key}
			digest := sha256.Sum256([]byte(source))
			target.pluginHash = fmt.Sprintf("%x", digest)
			p := builtinTemplate(target, "openai-video", AutoTemplatePolicy{MaxMicro: 10000})
			p.Body["model"] = name
			value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"body": map[string]any{"kind": "json", "value": p.Body}})
			require.NoError(t, err)
			encoded, err := common.Marshal(value)
			require.NoError(t, err)
			var intent struct {
				RequestBody map[string]any `json:"requestBody"`
			}
			require.NoError(t, common.Unmarshal(encoded, &intent))
			require.NotEmpty(t, intent.RequestBody)
			_, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"model": name, "upstreamModel": mapped, "requestBody": intent.RequestBody, "baseUrl": "https://fixture.example", "apiKey": "fixture-only"})
			require.NoError(t, err)
			task := &model.Task{TaskID: "task_health_public", Status: model.TaskStatusSuccess, Data: []byte(`{"id":"provider_private","status":"succeeded"}`)}
			public, err := jsplugin.New(plugin).ConvertToOpenAIVideo(task)
			require.NoError(t, err)
			var out map[string]any
			require.NoError(t, common.Unmarshal(public, &out))
			require.Equal(t, "task_health_public", out["id"])
			require.Equal(t, "completed", out["status"])
		})
	}
}

func TestAudioProbeUploadsOwnedBoundedPCM(t *testing.T) {
	p := Template{Selection: Selection{Endpoint: "audio_transcription"}, Path: "/v1/audio/transcriptions", InputFixture: "speech-hello-v1", Body: map[string]any{"model": "fun-asr-flash-2026-06-15", "response_format": "json"}}
	data, ct, err := probePayload(p)
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, p.Path, bytes.NewReader(data))
	c.Request.Header.Set("Content-Type", ct)
	audio, err := relayhelper.GetAndValidAudioRequest(c, relayconstant.RelayModeAudioTranscription)
	require.NoError(t, err)
	require.Equal(t, p.Body["model"], audio.Model)
	require.Equal(t, "json", audio.ResponseFormat)
	t.Cleanup(func() {
		if c.Request.MultipartForm != nil {
			_ = c.Request.MultipartForm.RemoveAll()
		}
	})
	media, params, err := mime.ParseMediaType(ct)
	require.NoError(t, err)
	require.Equal(t, "multipart/form-data", media)
	reader := multipart.NewReader(bytes.NewReader(data), params["boundary"])
	form, err := reader.ReadForm(1 << 20)
	require.NoError(t, err)
	defer form.RemoveAll()
	require.Equal(t, []string{p.Body["model"].(string)}, form.Value["model"])
	require.Len(t, form.File["file"], 1)
	file, err := form.File["file"][0].Open()
	require.NoError(t, err)
	defer file.Close()
	clip, err := io.ReadAll(file)
	require.NoError(t, err)
	require.Equal(t, speechHello, clip)
	decoder := wav.NewDecoder(bytes.NewReader(clip))
	require.True(t, decoder.IsValidFile())
	pcm, err := decoder.FullPCMBuffer()
	require.NoError(t, err)
	require.Greater(t, len(pcm.Data), 16000)
	require.LessOrEqual(t, len(pcm.Data), 5*16000)
	require.EqualValues(t, 16000, decoder.SampleRate)
	require.EqualValues(t, 1, decoder.NumChans)
	require.True(t, strings.Contains(evidenceHeaders(http.Header{"Content-Type": {ct}}, int64(len(data)))["Content-Type"], "multipart/form-data"))
	p.Body["file"] = "https://untrusted.example/audio.wav"
	_, _, err = probePayload(p)
	require.Error(t, err)
	p.InputFixture = "unknown"
	_, _, err = probePayload(p)
	require.Error(t, err)
}

func TestTemplateBindingIgnoresDiscoveryButTracksProtocol(t *testing.T) {
	ch := model.Channel{Id: 1, Type: 54, Models: "doubao-seedance-2-0-260128", BaseURL: common.GetPointer("https://fixture.example")}
	before := templateBinding(targetFor(&ch, ch.Models), "openai-video")
	ch.OtherSettings = `{"upstream_model_update_last_check_time":100,"upstream_model_update_last_detected_models":["new"]}`
	require.Equal(t, before, templateBinding(targetFor(&ch, ch.Models), "openai-video"))
	ch.OtherSettings = `{"doubao_video_endpoints":{"submit_path":"/custom/tasks"}}`
	require.NotEqual(t, before, templateBinding(targetFor(&ch, ch.Models), "openai-video"))
	ch.OtherSettings = ""
	ch.Setting = common.GetPointer(`{"task_plugin_key":"oiioii"}`)
	require.NotEqual(t, before, templateBinding(targetFor(&ch, ch.Models), "openai-video"))
}

func TestLegacyManualTextBindingRemainsValid(t *testing.T) {
	ch := model.Channel{Id: 1, Type: 1, Models: "glm-fixture", BaseURL: common.GetPointer("https://fixture.example")}
	target := targetFor(&ch, ch.Models)
	p := Template{Binding: hash(target.ID, target.base, target.Upstream, "openai"), MinimumConfirmed: true, MaxMicro: 100}
	require.True(t, p.validFor(target, "openai"))
	ch.ModelMapping = common.GetPointer(`{"glm-fixture":"another-model"}`)
	require.False(t, p.validFor(targetFor(&ch, ch.Models), "openai"))
}

func TestPluginSourceChangeRequiresNewMediaConfirmation(t *testing.T) {
	db := healthDB(t)
	channel := model.Channel{Type: 61, Models: "MiniMax-H3", BaseURL: common.GetPointer("https://fixture.example"), Setting: common.GetPointer(`{"task_plugin_key":"cn-locopal-video"}`), ModelMapping: common.GetPointer(`{"MiniMax-H3":"h3-video"}`)}
	require.NoError(t, db.Create(&channel).Error)
	plugin := model.TaskPlugin{Key: "cn-locopal-video", Version: "1.0.10", APIVersion: 1, Source: "fixture", SourceHash: "80062690d040212074f39b605d925ae8663cca8f2d8ca658ce408c7fbbf7a1f6", Active: true, Enabled: true}
	require.NoError(t, db.Create(&plugin).Error)
	before, err := findTarget(db, channel.Id, channel.Models, "openai-video")
	require.NoError(t, err)
	p := Template{Binding: templateBinding(before, "openai-video"), MinimumConfirmed: true, MaxMicro: 100}
	require.True(t, p.validFor(before, "openai-video"))
	require.NoError(t, db.Model(&plugin).Update("source_hash", "changed").Error)
	after, err := findTarget(db, channel.Id, channel.Models, "openai-video")
	require.NoError(t, err)
	require.False(t, p.validFor(after, "openai-video"))
	draft := builtinTemplate(after, "openai-video", AutoTemplatePolicy{MaxMicro: 100})
	require.False(t, draft.MinimumConfirmed)
}

func TestSynchronousImageHasBoundedLongRequestAndExclusiveLease(t *testing.T) {
	db := healthDB(t)
	migrateHealth(t, db)
	ctx := context.Background()
	channel := model.Channel{Type: 1, Status: 1, Models: "qwen-image-3.0", BaseURL: common.GetPointer("https://provider.example")}
	require.NoError(t, db.Create(&channel).Error)
	t.Setenv("MODEL_HEALTH_KEY_FIXTURE", "fixture-monitoring-key")
	t.Setenv("MODEL_HEALTH_KEY_FIXTURE_ORIGIN", "https://provider.example")
	selection := Selection{Channel: channel.Id, Model: channel.Models, Endpoint: "image-generation"}
	p := Template{Selection: selection, Path: "/v1/images/generations", Credential: "MODEL_HEALTH_KEY_FIXTURE", Auth: "bearer", Body: map[string]any{"prompt": "A red circle", "n": float64(1)}, ResultField: "data.0.b64_json", MaxTokens: 128, MaxMicro: 100, TimeoutSeconds: 180, MinimumConfirmed: true}
	require.NoError(t, SaveTemplate(ctx, p))
	require.NoError(t, SaveBudget(ctx, Budget{Run: 100, Day: 200, Month: 1000, Concurrent: 1, ChannelRPM: 1, ChannelTPM: 256}))
	bindings, err := workerBindings(db)
	require.NoError(t, err)
	owned, err := acquireWorker(ctx, "fixture", bindings)
	require.NoError(t, err)
	require.True(t, owned)
	plan, err := GetPlan(ctx, selection)
	require.NoError(t, err)
	require.True(t, plan.Ready, "%+v", plan.Blocked)
	run, err := Enqueue(ctx, selection, plan.Revision, plan.Template.MaxMicro)
	require.NoError(t, err)
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	posts := 0
	client := &http.Client{Transport: mockProvider(func(req *http.Request) (*http.Response, error) {
		posts++
		deadline, ok := req.Context().Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 170*time.Second)
		require.LessOrEqual(t, time.Until(deadline), 180*time.Second)
		var active model.ModelHealthRun
		require.NoError(t, db.First(&active, run.ID).Error)
		require.Greater(t, active.LeaseUntil, time.Now().Unix()+180)
		// Another executor after the old 90s lease must not reclaim this POST.
		other, err := claim(ctx, time.Now().Unix()+91)
		require.NoError(t, err)
		require.Zero(t, other.ID)
		return reply(200, `{"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(encoded.Bytes())+`"}]}`), nil
	})}
	require.NoError(t, work(ctx, client, time.Now().Unix()))
	require.NoError(t, db.First(&run, run.ID).Error)
	require.Equal(t, "succeeded", run.State)
	require.Equal(t, 1, posts)
	require.Less(t, workerCycleLimit, workerLease)
	require.Less(t, workerLease, probeLease)
}

func TestMediaProfilePreservesAdministratorPriceAndBudget(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	t.Cleanup(func() { *settings = saved })
	*settings = billing_setting.BillingSetting{BillingMode: map[string]string{"qwen-image-3.0": "tiered_expr"}, BillingExpr: map[string]string{"qwen-image-3.0": `tier("custom", fixed(1)) * image_count`}}
	ch := model.Channel{Id: 1, Type: 17, Models: "qwen-image-3.0"}
	expression, exists := billing_setting.ResolveTaskBillingExpr("", ch.Models, ch.Models)
	require.True(t, exists)
	_, _, expressionErr := billingexpr.RunExprWithRequest(expression, billingexpr.TokenParams{}, billingexpr.RequestInput{ImageCount: common.GetPointer(1), ReferenceImageCount: common.GetPointer(0)})
	require.NoError(t, expressionErr)
	p := builtinTemplate(targetFor(&ch, ch.Models), "image-generation", AutoTemplatePolicy{MaxMicro: 10000})
	require.True(t, p.MinimumConfirmed, "target=%+v template=%+v", targetFor(&ch, ch.Models), p)
	require.Greater(t, p.MaxMicro, int64(5000000))
	settings.BillingExpr["qwen-image-3.0"] = `-1`
	require.False(t, builtinTemplate(targetFor(&ch, ch.Models), "image-generation", AutoTemplatePolicy{MaxMicro: 10000}).MinimumConfirmed)
}

func TestSanitizedDomesticMediaContracts(t *testing.T) {
	data, err := os.ReadFile("testdata/domestic-media-contracts-20261126.json")
	require.NoError(t, err)
	var fixture struct {
		Channels []struct {
			ID       int    `json:"id"`
			Type     int    `json:"type"`
			Status   int    `json:"status"`
			Models   string `json:"models"`
			Base     string `json:"base_url"`
			Mapping  string `json:"model_mapping"`
			Setting  string `json:"setting"`
			Settings string `json:"settings"`
			Targets  []struct {
				Model    string `json:"model"`
				Endpoint string `json:"endpoint"`
			} `json:"targets"`
		} `json:"channels"`
		Prices []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"prices"`
	}
	require.NoError(t, common.Unmarshal(data, &fixture))
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved, savedPrices := *settings, ratio_setting.ModelPrice2JSONString()
	*settings = billing_setting.BillingSetting{}
	seedance := config.GlobalConfig.Get("seedance_video_pricing").(*seedancepricing.Setting)
	savedSeedance := *seedance
	*seedance = seedancepricing.Setting{RequestPricesCNY: seedancepricing.DefaultRequestPricesCNY()}
	t.Cleanup(func() {
		*settings = saved
		*seedance = savedSeedance
		require.NoError(t, seedancepricing.RebuildPriceIndex())
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	for _, option := range fixture.Prices {
		switch option.Key {
		case "ModelPrice":
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(option.Value))
		case "billing_setting.billing_expr":
			require.NoError(t, common.UnmarshalJsonStr(option.Value, &settings.BillingExpr))
		case "billing_setting.billing_mode":
			require.NoError(t, common.UnmarshalJsonStr(option.Value, &settings.BillingMode))
		case "billing_setting.plugin_billing_expr":
			require.NoError(t, common.UnmarshalJsonStr(option.Value, &settings.PluginBillingExpr))
		case seedancepricing.OptionKey:
			require.NoError(t, common.UnmarshalJsonStr(option.Value, &seedance.PricesCNY))
			require.NoError(t, seedancepricing.RebuildPriceIndex())
		}
	}
	active, missing, over := 0, 0, 0
	for _, ch := range fixture.Channels {
		channel := model.Channel{Id: ch.ID, Type: ch.Type, Status: ch.Status, Models: ch.Models, Group: "default", BaseURL: common.GetPointer(ch.Base), ModelMapping: common.GetPointer(ch.Mapping), Setting: common.GetPointer(ch.Setting), OtherSettings: ch.Settings}
		for _, selection := range ch.Targets {
			target := targetFor(&channel, selection.Model)
			if target.pluginKey == "cn-locopal-video" {
				target.pluginHash = "80062690d040212074f39b605d925ae8663cca8f2d8ca658ce408c7fbbf7a1f6"
			} else if target.pluginKey == "oiioii" {
				target.pluginHash = "9f6af9cbaa58075d7bfbaa6c6628b8fc782069fd956de2b6fe3c15353099f224"
			}
			p := builtinTemplate(target, selection.Endpoint, AutoTemplatePolicy{Credential: "MODEL_HEALTH_KEY_FIXTURE", MaxMicro: 10000})
			t.Logf("channel=%d model=%s paused=%t confirmed=%t reserve=%.6f", ch.ID, selection.Model, target.Paused, p.MinimumConfirmed, float64(p.MaxMicro)/1e6)
			if target.Paused {
				continue
			}
			active++
			if !p.MinimumConfirmed {
				missing++
			}
			if p.MaxMicro > 5000000 {
				over++
			}
			require.True(t, p.MinimumConfirmed, "channel %d / %s", ch.ID, selection.Model)
			require.LessOrEqual(t, p.MaxMicro, int64(5000000), "channel %d / %s", ch.ID, selection.Model)
		}
	}
	t.Logf("active=%d missing=%d over=%d", active, missing, over)
}
