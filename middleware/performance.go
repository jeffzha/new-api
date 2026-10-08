package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/opsmonitor"
	"github.com/gin-gonic/gin"
)

// SystemPerformanceCheck 检查系统性能中间件
func SystemPerformanceCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		err := checkSystemPerformance(c)
		if err == nil {
			c.Next()
			return
		}
		// 仅检查 Relay 接口 (/v1, /v1beta 等)
		// 这里简单判断路径前缀，可以根据实际路由调整
		if strings.HasPrefix(c.Request.URL.Path, "/v1/messages") {
			c.JSON(err.StatusCode, gin.H{
				"error": err.ToClaudeError(),
			})
		} else {
			c.JSON(err.StatusCode, gin.H{
				"error": err.ToOpenAIError(),
			})
		}
		c.Abort()
	}
}

// checkSystemPerformance 检查系统性能是否超过阈值
func checkSystemPerformance(c *gin.Context) *types.NewAPIError {
	config := common.GetPerformanceMonitorConfig()
	if !config.Enabled {
		systemOverloadGuard.closeEpisode(common.GetSystemStatus(), config, "monitor_disabled")
		return nil
	}
	return evaluateSystemOverload(c, common.GetSystemStatus(), config)
}

// evaluateSystemOverload keeps the host metric gate unchanged and records which
// metric tripped, so the rejection is traceable in the gateway log and in the
// operational request store.
func evaluateSystemOverload(c *gin.Context, status common.SystemStatus, config common.PerformanceMonitorConfig) *types.NewAPIError {
	breach := evaluateOverload(status, config)
	if breach == nil {
		systemOverloadGuard.closeEpisode(status, config, "recovered")
		return nil
	}

	path := ""
	requestID := ""
	if c != nil {
		requestID = c.GetString(common.RequestIdKey)
		if c.Request != nil {
			path = c.Request.URL.Path
		}
	}
	systemOverloadGuard.recordRejection(breach, status, config, path, requestID)

	message := fmt.Sprintf("system %s overloaded (current: %.1f%%, threshold: %d%%)", breach.reason, breach.value, breach.threshold)
	if c != nil {
		opsmonitor.ObserveGatewayRejection(c, http.StatusServiceUnavailable, "openai_error", string(breach.code), message)
	}
	return types.NewErrorWithStatusCode(errors.New(message), breach.code, http.StatusServiceUnavailable)
}
