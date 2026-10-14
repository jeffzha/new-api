package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/gin-gonic/gin"
	"net/http"
)

// RegisterOpsBridgeRoutes is mounted behind the existing Root authentication,
// request audit, rate limiting and cache controls.
func RegisterOpsBridgeRoutes(group *gin.RouterGroup) {
	group.GET("", func(c *gin.Context) {
		if opsbridge.Default == nil {
			c.JSON(200, gin.H{"enabled": false})
			return
		}
		c.JSON(200, opsbridge.Default.Status())
	})
	group.Use(func(c *gin.Context) {
		if opsbridge.Default == nil {
			c.AbortWithStatusJSON(409, gin.H{"error": "Ops bridge is disabled"})
			return
		}
		c.Next()
	})
	group.POST("/imports/preview", func(c *gin.Context) {
		plan, err := opsbridge.Default.PreviewImport(c.Request.Context(), service.OpsPricingStore{})
		if err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, plan)
	})
	group.POST("/imports/:id/apply", func(c *gin.Context) {
		var input struct {
			Models    []string `json:"models"`
			Overwrite []string `json:"overwrite_models"`
		}
		if err := common.DecodeJsonStrict(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20), &input); err != nil {
			c.JSON(400, gin.H{"error": "invalid import selection"})
			return
		}
		if err := opsbridge.Default.ApplyImport(c.Request.Context(), service.OpsPricingStore{}, c.Param("id"), input.Models, input.Overwrite); err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"status": "applied", "confirmation": "queued"})
	})
	group.POST("/deliveries/:id/retry", func(c *gin.Context) {
		if err := opsbridge.Default.Retry(c.Param("id")); err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"status": "pending"})
	})
	group.POST("/notifications/refresh", func(c *gin.Context) {
		if err := opsbridge.Default.RefreshNotifications(c.Request.Context()); err != nil {
			c.JSON(503, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"status": "ack_queued"})
	})
	group.POST("/objects/sync", func(c *gin.Context) {
		counts, err := service.SyncOpsObjects(c.Request.Context(), opsbridge.Default)
		if err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"status": "queued", "counts": counts})
	})
	group.POST("/archive", func(c *gin.Context) {
		count, err := opsbridge.Default.ArchiveDelivered()
		if err != nil {
			c.JSON(503, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"archived": count})
	})
	group.POST("/pricing/sync", func(c *gin.Context) {
		count, err := service.SyncOpsPricing(c.Request.Context(), opsbridge.Default)
		if err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"status": "queued", "models": count})
	})
}
