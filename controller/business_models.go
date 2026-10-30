package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/relaykit/dto"
	businessapi "github.com/QuantumNous/new-api/service/businessapi"
	"github.com/gin-gonic/gin"
)

type businessModel struct {
	dto.OpenAIModels
	DisplayName string              `json:"display_name"`
	Pricing     businessapi.Pricing `json:"pricing"`
}

func writeBusinessModels(c *gin.Context, models []dto.OpenAIModels) {
	names := make([]string, 0, len(models))
	for _, item := range models {
		names = append(names, item.Id)
	}
	prices, err := businessapi.PlatformPricing(names)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "server_error", "code": "pricing_unavailable", "message": "Platform pricing is temporarily unavailable"}})
		return
	}
	items := make([]businessModel, 0, len(models))
	for _, item := range models {
		// The current catalog has no separate display-name field. Keep the
		// documented ID fallback, rather than presenting a description as a name.
		items = append(items, businessModel{OpenAIModels: item, DisplayName: item.Id, Pricing: prices[item.Id]})
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "object": "list", "data": items})
}
