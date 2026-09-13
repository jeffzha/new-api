package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAgencyTaskResponseReleaseWaitsForPersistence(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	buffer := newAgencyTaskResponseBuffer(ctx.Writer)
	buffer.Header().Set("Content-Type", "application/json")
	buffer.WriteHeader(http.StatusAccepted)
	_, err := buffer.WriteString(`{"id":"public-task"}`)
	require.NoError(t, err)
	ctx.Set(agencyTaskResponseReleaseKey, func(commit bool) {
		if commit {
			buffer.commit()
		}
	})
	require.Empty(t, recorder.Body.String(), "the adapter response must remain private until the task row exists")
	ReleaseAgencyTaskResponse(ctx, true)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.JSONEq(t, `{"id":"public-task"}`, recorder.Body.String())
	ReleaseAgencyTaskResponse(ctx, true)
	require.JSONEq(t, `{"id":"public-task"}`, recorder.Body.String(), "release is consumed exactly once")
}

func TestAgencyTaskResponseDiscardAllowsRecoverableError(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	buffer := newAgencyTaskResponseBuffer(ctx.Writer)
	_, err := buffer.WriteString(`{"id":"public-task"}`)
	require.NoError(t, err)
	ctx.Set(agencyTaskResponseReleaseKey, func(commit bool) {
		if commit {
			buffer.commit()
		}
	})
	ReleaseAgencyTaskResponse(ctx, false)
	ctx.JSON(http.StatusServiceUnavailable, gin.H{"code": "task_persistence_failed", "public_task_id": "public-task"})
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.JSONEq(t, `{"code":"task_persistence_failed","public_task_id":"public-task"}`, recorder.Body.String())
}
