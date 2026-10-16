package opsexport

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func integrityQuery(c *gin.Context, cursorKey string) (int64, int, bool) {
	for key, values := range c.Request.URL.Query() {
		if (key != cursorKey && key != "limit") || len(values) != 1 {
			return 0, 0, false
		}
	}
	cursor := int64(0)
	limit := 500
	if raw, ok := c.GetQuery(cursorKey); ok {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 || strconv.FormatInt(n, 10) != raw {
			return 0, 0, false
		}
		cursor = n
	}
	if raw, ok := c.GetQuery("limit"); ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 || strconv.Itoa(n) != raw {
			return 0, 0, false
		}
		limit = n
	}
	return cursor, limit, true
}

func integrityError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrCNIntegrityCursorAhead):
		cnError(c, 409, "integrity_discovery_cursor_ahead")
	case errors.Is(err, model.ErrCNIntegrityPage):
		cnError(c, 400, "invalid_payload")
	case errors.Is(err, gorm.ErrRecordNotFound):
		cnError(c, 404, "integrity_snapshot_not_found")
	default:
		cnError(c, 503, "source_unavailable")
	}
}

func (s *Server) IntegrityIndex(c *gin.Context) {
	after, limit, ok := integrityQuery(c, "after")
	if !ok {
		cnError(c, 400, "invalid_payload")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	result, err := model.ReadCNIntegrityIndex(ctx, s.DB, s.GatewayID, after, limit)
	if err != nil {
		integrityError(c, err)
		return
	}
	integrityJSON(c, result)
}

func (s *Server) IntegrityManifest(c *gin.Context) {
	if len(c.Request.URL.Query()) != 0 {
		cnError(c, 400, "invalid_payload")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	raw, err := model.ReadCNIntegrityManifest(ctx, s.DB, c.Param("proofId"))
	if err != nil {
		integrityError(c, err)
		return
	}
	c.Data(200, "application/json; charset=utf-8", raw)
}

func (s *Server) IntegrityPage(c *gin.Context) {
	ordinal, limit, ok := integrityQuery(c, "ordinal")
	if !ok {
		cnError(c, 400, "invalid_payload")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	result, err := model.ReadCNIntegrityPage(ctx, s.DB, c.Param("proofId"), c.Param("collection"), ordinal, limit)
	if err != nil {
		integrityError(c, err)
		return
	}
	integrityJSON(c, result)
}

func integrityJSON(c *gin.Context, value any) {
	raw, err := model.CNIntegrityExportJSON(value)
	if err != nil {
		integrityError(c, err)
		return
	}
	c.Data(200, "application/json; charset=utf-8", raw)
}
