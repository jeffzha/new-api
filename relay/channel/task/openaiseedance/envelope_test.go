package openaiseedance

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

const wrappedInProgress = `{"code":"success","message":"","data":{"id":6216,"task_id":"task_abc","status":"IN_PROGRESS","result_url":"","fail_reason":"","progress":"50%","data":{"id":"task_upstream","model":"doubao-seedance-2-0-260128","status":"running","content":{"video_url":""},"usage":{"completion_tokens":0,"total_tokens":0}}}}`

const wrappedSuccess = `{"code":"success","message":"","data":{"id":6216,"task_id":"task_abc","status":"SUCCESS","result_url":"https://cdn.example.com/video.mp4","fail_reason":"","progress":"100%","data":{"id":"task_upstream","model":"doubao-seedance-2-0-260128","status":"succeeded","content":{"video_url":"https://cdn.example.com/video.mp4"},"usage":{"completion_tokens":87300,"total_tokens":87300}}}}`

const wrappedFailure = `{"code":"success","message":"","data":{"id":6216,"task_id":"task_abc","status":"FAILURE","result_url":"","fail_reason":"content moderation rejected","progress":"100%","data":{"id":"task_upstream","model":"doubao-seedance-2-0-260128","status":"failed"}}}`

func TestOpenAISeedanceParsesWrappedInProgressEnvelope(t *testing.T) {
	adaptor := &TaskAdaptor{}
	result, err := adaptor.ParseTaskResult(&model.Task{}, nil, []byte(wrappedInProgress))
	require.NoError(t, err)
	require.Equal(t, model.TaskStatusInProgress, result.Status)
	require.Equal(t, "50%", result.Progress)
}

func TestOpenAISeedanceParsesWrappedSuccessEnvelope(t *testing.T) {
	adaptor := &TaskAdaptor{}
	result, err := adaptor.ParseTaskResult(&model.Task{}, nil, []byte(wrappedSuccess))
	require.NoError(t, err)
	require.Equal(t, model.TaskStatusSuccess, result.Status)
	require.Equal(t, "https://cdn.example.com/video.mp4", result.Url)
	require.Equal(t, "100%", result.Progress)
}

func TestOpenAISeedanceParsesWrappedFailureEnvelope(t *testing.T) {
	adaptor := &TaskAdaptor{}
	result, err := adaptor.ParseTaskResult(&model.Task{}, nil, []byte(wrappedFailure))
	require.NoError(t, err)
	require.Equal(t, model.TaskStatusFailure, result.Status)
	require.Equal(t, "content moderation rejected", result.Reason)
}

func TestOpenAISeedanceStillParsesUnwrappedStatus(t *testing.T) {
	adaptor := &TaskAdaptor{}
	body := []byte(`{"id":"task_abc","status":"completed","progress":100,"result_url":"https://cdn.example.com/unwrapped.mp4"}`)
	result, err := adaptor.ParseTaskResult(&model.Task{}, nil, body)
	require.NoError(t, err)
	require.Equal(t, model.TaskStatusSuccess, result.Status)
	require.Equal(t, "https://cdn.example.com/unwrapped.mp4", result.Url)
}

func TestApplyTaskEnvelopeReadsNestedUsageForBilling(t *testing.T) {
	var task responseTask
	require.True(t, applyTaskEnvelope([]byte(wrappedSuccess), &task))
	require.Equal(t, 87300, task.Usage.TotalTokens)
	require.Equal(t, "completed", task.Status)
	require.Equal(t, "https://cdn.example.com/video.mp4", task.ResultURL)
}
