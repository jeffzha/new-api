package doubao

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskAdaptorUsesChannelEndpoints(t *testing.T) {
	service.InitHttpClient()

	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"upstream-task","status":"succeeded"}`)
	}))
	t.Cleanup(server.Close)

	adaptor := &TaskAdaptor{}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:    constant.ChannelTypeDoubaoVideo,
		ChannelBaseUrl: server.URL,
		ApiKey:         "upstream-secret",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			DoubaoVideoEndpoints: &dto.DoubaoVideoEndpointSettings{
				SubmitPath: "/v1/seedance/video/generations",
				FetchPath:  "/v1/seedance/video/generations/{task_id}",
			},
		},
	}})

	submitURL, err := adaptor.BuildRequestURL(nil)
	require.NoError(t, err)
	assert.Equal(t, server.URL+"/v1/seedance/video/generations", submitURL)

	resp, err := adaptor.FetchTask(server.URL, "upstream-secret", map[string]any{
		"task_id": "task-123",
	}, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	request := <-requests
	assert.Equal(t, http.MethodGet, request.Method)
	assert.Equal(t, "/v1/seedance/video/generations/task-123", request.URL.Path)
	assert.Equal(t, "Bearer upstream-secret", request.Header.Get("Authorization"))

	snapshot := adaptor.TaskEndpointSnapshot()
	require.NotNil(t, snapshot)
	assert.Equal(t, server.URL, snapshot.BaseURL)
	assert.Equal(t, "/v1/seedance/video/generations/{task_id}", snapshot.FetchPath)
}

func TestTaskAdaptorKeepsOfficialEndpointsByDefault(t *testing.T) {
	adaptor := &TaskAdaptor{}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:    constant.ChannelTypeDoubaoVideo,
		ChannelBaseUrl: "https://ark.example.com/",
	}})

	submitURL, err := adaptor.BuildRequestURL(nil)
	require.NoError(t, err)
	assert.Equal(t, "https://ark.example.com/api/v3/contents/generations/tasks", submitURL)
	assert.Equal(t, defaultFetchPath, adaptor.TaskEndpointSnapshot().FetchPath)
}

func TestTaskAdaptorIgnoresDoubaoEndpointOverrideForVolcEngine(t *testing.T) {
	adaptor := &TaskAdaptor{}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:    constant.ChannelTypeVolcEngine,
		ChannelBaseUrl: "https://ark.example.com",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			DoubaoVideoEndpoints: &dto.DoubaoVideoEndpointSettings{
				SubmitPath: "/custom/submit",
				FetchPath:  "/custom/fetch/{task_id}",
			},
		},
	}})

	submitURL, err := adaptor.BuildRequestURL(nil)
	require.NoError(t, err)
	assert.Equal(t, "https://ark.example.com"+defaultSubmitPath, submitURL)
}

func TestTaskAdaptorParsesStringSeedFromCompatibleUpstream(t *testing.T) {
	adaptor := &TaskAdaptor{}
	taskResult, err := adaptor.ParseTaskResult([]byte(`{
		"id":"upstream-task",
		"status":"succeeded",
		"seed":"10785",
		"content":{"video_url":"https://example.com/video.mp4"},
		"usage":{"completion_tokens":48400,"total_tokens":48400}
	}`))

	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusSuccess, taskResult.Status)
	assert.Equal(t, 48400, taskResult.TotalTokens)
	assert.Equal(t, "https://example.com/video.mp4", taskResult.Url)
}
