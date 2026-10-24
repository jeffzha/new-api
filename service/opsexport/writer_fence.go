package opsexport

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// WriterFence exposes only the committed state and the last immutable receipt.
// Mutations remain on the signed command channel with its compare-and-swap gate.
func (s *Server) WriterFence(c *gin.Context) {
	query := c.Request.URL.Query()
	if len(query) > 1 || (len(query) == 1 && len(query["command_id"]) != 1) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query"})
		return
	}
	commandID := query.Get("command_id")
	if len(query) != 0 && (commandID == "" || len(commandID) > 191 || strings.TrimSpace(commandID) != commandID || strings.ContainsAny(commandID, "\r\n")) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !db.Migrator().HasTable(&model.AgencyOpsWriterState{}) || !db.Migrator().HasTable(&model.AgencyOpsWriterTransition{}) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "writer fence unavailable"})
		return
	}
	var state model.AgencyOpsWriterState
	if err := db.First(&state, 1).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "writer fence unavailable"})
		return
	}
	var receipt model.AgencyOpsWriterTransition
	lookupID := state.LastCommandID
	if commandID != "" {
		lookupID = commandID
	}
	var err error
	if lookupID != "" {
		err = db.Where("command_id = ?", lookupID).First(&receipt).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) && commandID != "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "writer receipt not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "writer receipt unavailable"})
		return
	}
	var lastReceipt any
	if lookupID != "" {
		lastReceipt = gin.H{
			"commandId": receipt.CommandID, "targetMode": receipt.TargetMode,
			"expectedFenceVersion":  receipt.ExpectedFenceVersion,
			"resultingFenceVersion": receipt.ResultingFenceVersion,
			"manifestVersion":       receipt.ManifestVersion, "createdAtMs": receipt.CreatedAtMS,
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"schemaVersion": 1, "gatewayId": s.GatewayID,
		"writerMode": state.Mode, "fenceVersion": state.FenceVersion,
		"manifestVersion": state.ManifestVersion, "updatedAtMs": state.UpdatedAtMS,
		"lastReceipt": lastReceipt,
	})
}
