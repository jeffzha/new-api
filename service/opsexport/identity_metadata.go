package opsexport

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// Agency identity metadata is intentionally separate from the directory export.
// It contains only stable invite and account state needed for a review-only
// migration. Password hashes, sessions and payout data never cross this endpoint.
type identityMetadataCursor struct {
	Gateway       string `json:"gateway"`
	Until         int64  `json:"until"`
	MaxAgency     int64  `json:"maxAgency"`
	AgencyCount   int64  `json:"agencyCount"`
	OperatorCount int64  `json:"operatorCount"`
	AgencyID      int64  `json:"agencyId"`
}

func (s *Server) identityMetadataCursor(raw string) (identityMetadataCursor, error) {
	var page identityMetadataCursor
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 2048 {
		return page, errors.New("invalid cursor")
	}
	mac := hmac.New(sha256.New, s.TokenHash[:])
	mac.Write([]byte(parts[0]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return page, errors.New("invalid signature")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || common.Unmarshal(data, &page) != nil || page.Gateway != s.GatewayID ||
		page.Until < time.Now().Unix() || page.MaxAgency < 0 || page.AgencyID < 0 ||
		page.AgencyCount < 0 || page.OperatorCount < 0 || page.OperatorCount > page.AgencyCount ||
		page.AgencyID > page.MaxAgency {
		return page, errors.New("expired cursor")
	}
	return page, nil
}

func (s *Server) signIdentityMetadataCursor(page identityMetadataCursor) string {
	data, _ := common.Marshal(page)
	raw := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.TokenHash[:])
	mac.Write([]byte(raw))
	return raw + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) AgencyIdentityMetadata(c *gin.Context) {
	limit := 50
	for key, values := range c.Request.URL.Query() {
		if (key != "page_size" && key != "cursor") || len(values) != 1 {
			c.JSON(400, gin.H{"error": "invalid page"})
			return
		}
	}
	if raw, ok := c.GetQuery("page_size"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			c.JSON(400, gin.H{"error": "invalid page size"})
			return
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !db.Migrator().HasTable(&model.Agency{}) || !db.Migrator().HasTable(&model.AgencyOperatorAccount{}) {
		c.JSON(501, gin.H{"error": "agency identity metadata export unavailable"})
		return
	}
	page := identityMetadataCursor{Gateway: s.GatewayID, Until: time.Now().Add(2 * time.Hour).Unix()}
	if raw, ok := c.GetQuery("cursor"); ok {
		var err error
		page, err = s.identityMetadataCursor(raw)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid cursor"})
			return
		}
	} else {
		if db.Model(&model.Agency{}).Select("COALESCE(MAX(id),0)").Scan(&page.MaxAgency).Error != nil {
			c.JSON(503, gin.H{"error": "agency identity metadata unavailable"})
			return
		}
		if db.Model(&model.Agency{}).Where("id <= ?", page.MaxAgency).Count(&page.AgencyCount).Error != nil ||
			db.Model(&model.AgencyOperatorAccount{}).Where("agency_id <= ?", page.MaxAgency).Count(&page.OperatorCount).Error != nil ||
			page.OperatorCount > page.AgencyCount {
			c.JSON(503, gin.H{"error": "agency identity metadata unavailable"})
			return
		}
	}
	identity := page
	identity.AgencyID = 0
	snapshot := s.signIdentityMetadataCursor(identity)
	var rows []model.Agency
	if db.Select("id", "invite_code", "status").Where("id <= ? AND id > ?", page.MaxAgency, page.AgencyID).
		Order("id ASC").Limit(limit+1).Find(&rows).Error != nil {
		c.JSON(503, gin.H{"error": "agency identity metadata unavailable"})
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	operators := map[int64]model.AgencyOperatorAccount{}
	if len(ids) > 0 {
		var accounts []model.AgencyOperatorAccount
		if db.Select("agency_id", "username", "status", "must_change_password", "auth_version").
			Where("agency_id IN ?", ids).Find(&accounts).Error != nil {
			c.JSON(503, gin.H{"error": "agency identity metadata unavailable"})
			return
		}
		for _, account := range accounts {
			operators[account.AgencyID] = account
		}
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		item := gin.H{
			"externalAgencyId": strconv.FormatInt(row.ID, 10),
			"inviteCode":       row.InviteCode,
			"agencyStatus":     mapAgencyStatus(row.Status),
			"operator":         nil,
		}
		if account, ok := operators[row.ID]; ok {
			item["operator"] = gin.H{
				"username":           account.Username,
				"status":             mapAgencyStatus(account.Status),
				"mustChangePassword": account.MustChangePassword,
				"authVersion":        account.AuthVersion,
			}
		}
		items = append(items, item)
		page.AgencyID = row.ID
	}
	next := ""
	if more {
		next = s.signIdentityMetadataCursor(page)
	}
	pageHashBytes, err := common.Marshal(items)
	if err != nil {
		c.JSON(503, gin.H{"error": "agency identity metadata unavailable"})
		return
	}
	pageHash := sha256.Sum256(pageHashBytes)
	c.JSON(200, gin.H{
		"schemaVersion": 1, "sourceSystem": "new-api", "gatewayId": s.GatewayID,
		"supported": true, "consistentSnapshot": false, "snapshotId": snapshot,
		"controlTotals": gin.H{"agencies": page.AgencyCount, "operatorAccounts": page.OperatorCount},
		"pageHash":      hex.EncodeToString(pageHash[:]), "items": items, "nextCursor": next,
	})
}

func mapAgencyStatus(status string) string {
	if status == "active" {
		return "active"
	}
	return "inactive"
}
