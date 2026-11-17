package controller

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetUserLogOpsFees(c *gin.Context) { getLogOpsFees(c, true) }
func GetAllLogOpsFees(c *gin.Context)  { getLogOpsFees(c, false) }

// A fee refresh never searches the log list, counts rows or waits for OPS.
// Self logs use display IDs, so authorization is resolved by request ID in LOG_DB.
func getLogOpsFees(c *gin.Context, self bool) {
	var body struct {
		RequestIDs []string `json:"request_ids"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if c.ShouldBindJSON(&body) != nil || len(body.RequestIDs) == 0 || len(body.RequestIDs) > 100 {
		c.JSON(400, gin.H{"success": false, "message": "Provide 1 to 100 request IDs"})
		return
	}
	ids := make([]string, 0, len(body.RequestIDs))
	seen := map[string]bool{}
	for _, id := range body.RequestIDs {
		if len(id) > 64 || strings.TrimSpace(id) == "" {
			c.JSON(400, gin.H{"success": false, "message": "Invalid request ID"})
			return
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if self && c.GetInt("id") <= 0 {
		c.Status(401)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	userID := 0
	if self {
		userID = c.GetInt("id")
	}
	logs, err := model.LogFeeLogs(ctx, ids, userID)
	if err != nil || model.AttachOpsFees(ctx, logs) != nil {
		c.JSON(503, gin.H{"success": false, "message": "Fees temporarily unavailable"})
		return
	}
	items := make([]gin.H, 0, len(logs))
	for _, log := range logs {
		items = append(items, gin.H{"request_id": log.RequestId, "ops_fee": log.OpsFee})
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"success": true, "data": items})
}
