package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func BusinessRequestDetail(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	requestID := c.Param("request_id")
	if requestID == "" || len(requestID) > 64 || !businessapi.ValidText(requestID, 64) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request_id", "message": "Invalid request ID"}})
		return
	}
	log, err := model.GetBusinessRequestLog(c.GetInt("id"), c.GetInt("token_id"), requestID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "request_not_found", "message": "Request not found or not yet recorded"}})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "request_details_unavailable", "message": "Request details are temporarily unavailable"}})
		return
	}
	billing := businessapi.UnavailableBilling()
	var stored struct {
		Billing *businessapi.Billing `json:"billing"`
	}
	if common.UnmarshalJsonStr(log.Other, &stored) == nil && stored.Billing != nil {
		billing = *stored.Billing
	}
	status := "succeeded"
	if log.Type == model.LogTypeError {
		status = "failed"
	}
	// Return an explicit allowlist; full log metadata includes private channel,
	// supplier costs and operator diagnostics that API keys must not receive.
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"request_id": log.RequestId, "model": log.ModelName, "status": status, "created": log.CreatedAt,
		"stream": log.IsStream, "duration_seconds": log.UseTime,
		"usage":   gin.H{"prompt_tokens": log.PromptTokens, "completion_tokens": log.CompletionTokens, "total_tokens": log.PromptTokens + log.CompletionTokens},
		"billing": billing,
	}})
}
