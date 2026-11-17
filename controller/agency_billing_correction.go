package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

type agencyChargeCorrectionRequest struct {
	// ChargeID corrects one charge. Leaving it empty runs the backlog scan.
	ChargeID string `json:"charge_id"`
	Reason   string `json:"reason"`
	Limit    int    `json:"limit"`
}

// ListAgencyChargeCorrections previews the charges whose agency usage record no
// longer matches the funding the customer wallet already paid. It never writes.
func ListAgencyChargeCorrections(c *gin.Context) {
	cursor, _ := strconv.ParseInt(c.Query("cursor"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	targets, next, done, err := model.AgencyChargeCorrectionTargets(cursor, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	eligible, blocked := 0, 0
	for _, target := range targets {
		if target.Eligible {
			eligible++
			continue
		}
		blocked++
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"items": targets, "cursor": next, "done": done, "eligible": eligible, "blocked": blocked,
	}})
}

// ApplyAgencyChargeCorrections runs the audited historical correction as Root.
func ApplyAgencyChargeCorrections(c *gin.Context) {
	var request agencyChargeCorrectionRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid correction request"})
		return
	}
	reason := strings.TrimSpace(request.Reason)
	if utf8.RuneCountInString(reason) < 4 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "an audit reason is required"})
		return
	}
	operatorID := int64(c.GetInt("id"))
	chargeID := strings.TrimSpace(request.ChargeID)
	if chargeID != "" {
		result, err := model.CorrectAgencyTaskCharge(chargeID, operatorID, reason)
		if err != nil {
			writeAgencyCorrectionError(c, err)
			return
		}
		applied := 0
		if !result.AlreadyApplied {
			applied = 1
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
			"applied": applied, "items": []model.AgencyChargeCorrectionResult{result},
		}})
		return
	}
	applied, blocked, err := model.CorrectAgencyChargeBacklog(operatorID, reason, request.Limit)
	if err != nil {
		writeAgencyCorrectionError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"applied": len(applied), "items": applied, "blocked": blocked,
	}})
}

func writeAgencyCorrectionError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, model.ErrAgencyChargeCorrectionUnsupported) {
		status = http.StatusBadRequest
	}
	c.JSON(status, gin.H{"success": false, "message": err.Error()})
}
