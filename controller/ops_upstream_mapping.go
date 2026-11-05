package controller

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"os"
)

func SyncOpsUpstreamMappings(c *gin.Context) {
	if os.Getenv("OPS_UPSTREAM_SYNC_ENABLED") != "true" {
		c.JSON(409, gin.H{"success": false, "message": "请先配置并启用运营平台上游登记"})
		return
	}
	model.NotifyUpstreamMappingChange()
	c.JSON(200, gin.H{"success": true, "data": gin.H{"status": "queued"}})
}
