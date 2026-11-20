package doubao

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLegacyVideoEstimateRetainsActualRequestDimensions(t *testing.T) {
	for _, reference := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		request := relaycommon.TaskSubmitReq{Model: "dreamina-seedance-2-0-mini-filter-off", Resolution: "720p"}
		if reference {
			request.Content = []map[string]any{{"type": "video_url", "video_url": map[string]any{"url": "https://example.invalid/private.mp4"}}}
		}
		info := &relaycommon.RelayInfo{OriginModelName: request.Model, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
		relaycommon.StoreTaskRequest(c, info, "generate", request)
		ratio := (&TaskAdaptor{}).EstimateBilling(c, info)
		value, exists := c.Get("task_billing_dimensions")
		require.True(t, exists)
		dimensions := value.(*model.TaskUsageDimensions)
		require.Equal(t, "720p", dimensions.Resolution)
		require.NotNil(t, dimensions.ReferenceVideo)
		require.Equal(t, reference, *dimensions.ReferenceVideo)
		if reference {
			require.InDelta(t, 0.6, ratio["video_input"], 1e-12)
		}
		task := &model.Task{PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{UsageDimensions: dimensions}}}
		facts := task.BillingLogFacts()
		require.Equal(t, reference, facts["has_video_input"])
		require.Equal(t, "720p", facts["resolution"])
	}
}
