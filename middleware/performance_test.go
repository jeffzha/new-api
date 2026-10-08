package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func overloadTestConfig() common.PerformanceMonitorConfig {
	return common.PerformanceMonitorConfig{
		Enabled:         true,
		CPUThreshold:    90,
		MemoryThreshold: 90,
		DiskThreshold:   95,
	}
}

func TestEvaluateOverloadClassifiesBreachedMetric(t *testing.T) {
	config := overloadTestConfig()
	tests := []struct {
		name          string
		status        common.SystemStatus
		wantReason    string
		wantCode      types.ErrorCode
		wantValue     float64
		wantThreshold int
	}{
		{
			name:          "cpu",
			status:        common.SystemStatus{CPUUsage: 93.5, MemoryUsage: 10, DiskUsage: 10},
			wantReason:    overloadReasonCPU,
			wantCode:      ErrorCodeSystemOverloadCPUShed,
			wantValue:     93.5,
			wantThreshold: 90,
		},
		{
			name:          "memory",
			status:        common.SystemStatus{CPUUsage: 10, MemoryUsage: 91.5, DiskUsage: 10},
			wantReason:    overloadReasonMemory,
			wantCode:      ErrorCodeSystemOverloadMemory,
			wantValue:     91.5,
			wantThreshold: 90,
		},
		{
			name:          "disk",
			status:        common.SystemStatus{CPUUsage: 10, MemoryUsage: 10, DiskUsage: 96},
			wantReason:    overloadReasonDisk,
			wantCode:      ErrorCodeSystemOverloadDisk,
			wantValue:     96,
			wantThreshold: 95,
		},
		{
			name:   "healthy",
			status: common.SystemStatus{CPUUsage: 10, MemoryUsage: 10, DiskUsage: 10},
		},
		{
			name:   "at_threshold_is_not_a_breach",
			status: common.SystemStatus{CPUUsage: 90, MemoryUsage: 90, DiskUsage: 95},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			breach := evaluateOverload(tt.status, config)
			if tt.wantCode == "" {
				require.Nil(t, breach)
				return
			}
			require.NotNil(t, breach)
			assert.Equal(t, tt.wantReason, breach.reason)
			assert.Equal(t, tt.wantCode, breach.code)
			assert.Equal(t, tt.wantValue, breach.value)
			assert.Equal(t, tt.wantThreshold, breach.threshold)
		})
	}
}

func TestEvaluateSystemOverloadRejectsWithOverloadCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	systemOverloadGuard = newOverloadGuard()
	t.Cleanup(func() { systemOverloadGuard = newOverloadGuard() })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	err := evaluateSystemOverload(c, common.SystemStatus{CPUUsage: 12, MemoryUsage: 20, DiskUsage: 97}, overloadTestConfig())

	require.NotNil(t, err)
	assert.Equal(t, ErrorCodeSystemOverloadDisk, err.GetErrorCode())
	assert.Equal(t, http.StatusServiceUnavailable, err.StatusCode)

	c.JSON(err.StatusCode, gin.H{"error": err.ToOpenAIError()})
	assert.Contains(t, recorder.Body.String(), "system_overload_disk")
	assert.Contains(t, recorder.Body.String(), "system disk overloaded")
}

func TestOverloadGuardCountsRejectedRequestsUntilRecovery(t *testing.T) {
	guard := newOverloadGuard()
	config := overloadTestConfig()
	breached := common.SystemStatus{CPUUsage: 10, MemoryUsage: 10, DiskUsage: 97}
	recovered := common.SystemStatus{CPUUsage: 10, MemoryUsage: 10, DiskUsage: 60}
	breach := evaluateOverload(breached, config)
	require.NotNil(t, breach)

	guard.observeSample(breached, config)
	guard.observeSample(breached, config)
	for _, requestID := range []string{"req-1", "req-2", "req-3"} {
		guard.recordRejection(breach, breached, config, "/v1/chat/completions", requestID)
	}

	guard.mu.Lock()
	episode := guard.active
	consecutiveWhileBreaching := guard.consecutive[overloadReasonDisk]
	peakConsecutiveWhileBreaching := guard.active.peakConsecutive[overloadReasonDisk]
	guard.mu.Unlock()
	require.NotNil(t, episode)
	assert.EqualValues(t, 3, episode.rejected)
	assert.Equal(t, 2, consecutiveWhileBreaching)
	assert.Equal(t, 2, peakConsecutiveWhileBreaching)

	guard.observeSample(recovered, config)

	guard.mu.Lock()
	closedEpisode := guard.active
	consecutiveAfterRecovery := guard.consecutive[overloadReasonDisk]
	guard.mu.Unlock()
	assert.Nil(t, closedEpisode)
	assert.Equal(t, 0, consecutiveAfterRecovery)
}
