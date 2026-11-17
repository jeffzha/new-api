package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"net/http/httptest"
	"testing"
)

func TestTaskLogUpstreamIsNotReplacedByPollingContext(t *testing.T) {
	other := NewLogOther()
	CaptureUpstreamLogReference(other, "https://submitted.example")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://polling.example")
	result := captureRequestUpstream(c, other)
	admin := result.Snapshot()["admin_info"].(map[string]any)
	assert.Equal(t, "https://submitted.example", admin["upstream"].(map[string]any)["base_url"])
	assert.NotContains(t, result.Snapshot(), "base_url")
}
