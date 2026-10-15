package opsexport

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func RegisterV2(group *gin.RouterGroup) {
	useExportAuth(group, true)
	group.GET("/facts", func(c *gin.Context) { Default.FactsV2(c) })
	group.GET("/daily", func(c *gin.Context) { Default.DailyV2(c) })
	group.GET("/refund-capabilities", func(c *gin.Context) {
		if len(c.Request.URL.Query()) != 0 {
			cnError(c, 400, "invalid_payload")
			return
		}
		c.JSON(200, model.CNRefundCapabilities(Default.GatewayID))
	})
}

func cnError(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "retryable": status == 503, "operationId": nil, "details": gin.H{}}})
}

func (s *Server) Catalog(c *gin.Context) {
	if len(c.Request.URL.Query()) != 0 {
		cnError(c, 400, "invalid_payload")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	payload, err := model.ReadCNCatalog(ctx, s.DB, s.GatewayID)
	if errors.Is(err, model.ErrCNCatalogTooLarge) {
		cnError(c, 413, "catalog_snapshot_too_large")
		return
	}
	if err != nil {
		cnError(c, 503, "source_unavailable")
		return
	}
	c.Data(200, "application/json; charset=utf-8", payload)
}

func (s *Server) Inventory(c *gin.Context) {
	if len(c.Request.URL.Query()) != 0 {
		cnError(c, 400, "invalid_payload")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	payload, err := model.ReadOpsInventory(ctx, s.DB, s.GatewayID)
	if errors.Is(err, model.ErrOpsInventoryTooLarge) {
		cnError(c, 413, "inventory_snapshot_too_large")
		return
	}
	if err != nil {
		cnError(c, 503, "source_unavailable")
		return
	}
	c.Data(200, "application/json; charset=utf-8", payload)
}

func (s *Server) FactsV2(c *gin.Context) {
	limit := 500
	for key, values := range c.Request.URL.Query() {
		if (key != "limit" && key != "after") || len(values) != 1 {
			cnError(c, 400, "invalid_payload")
			return
		}
	}
	if value, present := c.GetQuery("limit"); present {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 1000 {
			cnError(c, 400, "invalid_payload")
			return
		}
		limit = n
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	result, err := model.ReadCNFacts(ctx, s.DB, c.Query("after"), limit)
	if errors.Is(err, model.ErrOpsExportCursor) {
		cnError(c, 400, "invalid_cursor")
		return
	}
	if err != nil {
		cnError(c, 503, "source_unavailable")
		return
	}
	c.JSON(200, result)
}

func (s *Server) DailyV2(c *gin.Context) {
	for key, values := range c.Request.URL.Query() {
		if (key != "date" && key != "through") || len(values) != 1 {
			cnError(c, 400, "invalid_payload")
			return
		}
	}
	if date, err := time.Parse("2006-01-02", c.Query("date")); err != nil || date.Year() < 1 {
		cnError(c, 400, "invalid_payload")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	result, err := model.ReadCNDaily(ctx, s.DB, c.Query("date"), c.Query("through"))
	if errors.Is(err, model.ErrOpsExportCursor) {
		cnError(c, 400, "invalid_cursor")
		return
	}
	if err != nil {
		cnError(c, 503, "source_unavailable")
		return
	}
	c.JSON(200, result)
}
