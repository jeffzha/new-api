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

// Directory cursors are short lived, credential-bound keysets, not financial
// snapshots. Changed identities converge on the following daily directory scan.
type distributorCursor struct {
	Gateway   string `json:"gateway"`
	Until     int64  `json:"until"`
	MaxAgency int64  `json:"maxAgency"`
	MaxUser   int64  `json:"maxUser"`
	Depth     int    `json:"depth"`
	After     int64  `json:"after"`
	Customers bool   `json:"customers"`
}

func (s *Server) distributorCursor(raw string) (distributorCursor, error) {
	var page distributorCursor
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 2048 {
		return page, errors.New("invalid cursor")
	}
	mac := hmac.New(sha256.New, s.TokenHash[:])
	mac.Write([]byte(parts[0]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return page, errors.New("invalid cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || common.Unmarshal(data, &page) != nil || page.Gateway != s.GatewayID || page.Until < time.Now().Unix() || page.After < 0 {
		return page, errors.New("expired cursor")
	}
	return page, nil
}

func (s *Server) signDistributorCursor(page distributorCursor) string {
	data, _ := common.Marshal(page)
	raw := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.TokenHash[:])
	mac.Write([]byte(raw))
	return raw + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) Distributors(c *gin.Context) {
	limit := 50
	for key, values := range c.Request.URL.Query() {
		if (key != "page_size" && key != "cursor") || len(values) != 1 {
			c.JSON(400, gin.H{"error": "invalid page"})
			return
		}
	}
	if raw, ok := c.GetQuery("page_size"); ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			c.JSON(400, gin.H{"error": "invalid page size"})
			return
		}
		limit = n
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !db.Migrator().HasTable(&model.Agency{}) || !db.Migrator().HasTable(&model.AgencyActiveUserBinding{}) {
		c.JSON(501, gin.H{"error": "distributor export unavailable"})
		return
	}
	page := distributorCursor{Gateway: s.GatewayID, Until: time.Now().Add(2 * time.Hour).Unix()}
	if raw, ok := c.GetQuery("cursor"); ok {
		var err error
		page, err = s.distributorCursor(raw)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid cursor"})
			return
		}
	} else {
		if db.Model(&model.Agency{}).Select("COALESCE(MAX(id),0)").Scan(&page.MaxAgency).Error != nil || db.Model(&model.AgencyActiveUserBinding{}).Select("COALESCE(MAX(user_id),0)").Scan(&page.MaxUser).Error != nil {
			c.JSON(503, gin.H{"error": "directory unavailable"})
			return
		}
	}
	identity := page
	identity.Depth, identity.After, identity.Customers = 0, 0, false
	snapshot := s.signDistributorCursor(identity)
	items, customers := []gin.H{}, []gin.H{}
	next := ""
	if !page.Customers {
		var rows []model.Agency
		if db.Select("id", "parent_agency_id", "depth", "display_name", "status").Where("id <= ? AND (depth > ? OR (depth = ? AND id > ?))", page.MaxAgency, page.Depth, page.Depth, page.After).Order("depth ASC, id ASC").Limit(limit+1).Find(&rows).Error != nil {
			c.JSON(503, gin.H{"error": "directory unavailable"})
			return
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		for _, row := range rows {
			var parent any
			if row.ParentAgencyID != nil {
				parent = strconv.FormatInt(*row.ParentAgencyID, 10)
			}
			status := "inactive"
			if row.Status == "active" {
				status = "active"
			}
			items = append(items, gin.H{"externalId": strconv.FormatInt(row.ID, 10), "parentExternalId": parent, "name": row.DisplayName, "status": status})
			page.Depth, page.After = row.Depth, row.ID
		}
		if more {
			next = s.signDistributorCursor(page)
		} else if page.MaxAgency > 0 && page.MaxUser > 0 {
			page.Customers, page.After = true, 0
			next = s.signDistributorCursor(page)
		}
	} else {
		var rows []model.AgencyActiveUserBinding
		if db.Select("user_id", "agency_id").Where("user_id > ? AND user_id <= ?", page.After, page.MaxUser).Order("user_id ASC").Limit(limit+1).Find(&rows).Error != nil {
			c.JSON(503, gin.H{"error": "directory unavailable"})
			return
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		for _, row := range rows {
			customers = append(customers, gin.H{"externalUserId": strconv.FormatInt(row.UserID, 10), "agencyExternalId": strconv.FormatInt(row.AgencyID, 10)})
			page.After = row.UserID
		}
		if more {
			next = s.signDistributorCursor(page)
		}
	}
	c.JSON(200, gin.H{"schemaVersion": 1, "gatewayId": s.GatewayID, "supported": true, "snapshotId": snapshot, "items": items, "customers": customers, "nextCursor": next})
}

// BindingHistory is a bounded reconciliation feed, not an atomic ownership
// snapshot. A later pass must compare mutable end times and active pointers.
func (s *Server) BindingHistory(c *gin.Context) {
	for key, values := range c.Request.URL.Query() {
		if (key != "after" && key != "through" && key != "page_size") || len(values) != 1 {
			c.JSON(400, gin.H{"error": "invalid range"})
			return
		}
	}
	parseID := func(key string) (int64, bool, bool) {
		raw, present := c.GetQuery(key)
		if !present {
			return 0, false, true
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		return value, true, err == nil && value >= 0 && strconv.FormatInt(value, 10) == raw
	}
	after, _, validAfter := parseID("after")
	through, bounded, validThrough := parseID("through")
	limit := 50
	if raw, present := c.GetQuery("page_size"); present {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			c.JSON(400, gin.H{"error": "invalid page size"})
			return
		}
		limit = value
	}
	if !validAfter || !validThrough || bounded && (through == 0 || after > through) || !bounded && after != 0 {
		c.JSON(400, gin.H{"error": "invalid range boundary"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !db.Migrator().HasTable(&model.AgencyUserBinding{}) || !db.Migrator().HasTable(&model.AgencyActiveUserBinding{}) {
		c.JSON(501, gin.H{"error": "binding history unavailable"})
		return
	}
	if !bounded {
		if err := db.Model(&model.AgencyUserBinding{}).Select("COALESCE(MAX(id),0)").Scan(&through).Error; err != nil {
			c.JSON(503, gin.H{"error": "binding history unavailable"})
			return
		}
	}
	var rows []model.AgencyUserBinding
	if err := db.Select("id", "user_id", "agency_id", "revision", "effective_at_ms", "ended_at_ms").
		Where("id > ? AND id <= ?", after, through).Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		c.JSON(503, gin.H{"error": "binding history unavailable"})
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	users := make([]int64, 0, len(rows))
	for _, row := range rows {
		users = append(users, row.UserID)
	}
	active := map[int64]model.AgencyActiveUserBinding{}
	if len(users) > 0 {
		var pointers []model.AgencyActiveUserBinding
		if err := db.Select("user_id", "binding_id", "revision", "agency_id").Where("user_id IN ?", users).Find(&pointers).Error; err != nil {
			c.JSON(503, gin.H{"error": "binding history unavailable"})
			return
		}
		for _, pointer := range pointers {
			active[pointer.UserID] = pointer
		}
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		var ended any
		if row.EndedAtMS != nil {
			ended = strconv.FormatInt(*row.EndedAtMS, 10)
		}
		var current any
		if pointer, ok := active[row.UserID]; ok {
			current = gin.H{"bindingId": strconv.FormatInt(pointer.BindingID, 10),
				"agencyExternalId": strconv.FormatInt(pointer.AgencyID, 10), "revision": pointer.Revision}
		}
		items = append(items, gin.H{"externalBindingId": strconv.FormatInt(row.ID, 10),
			"externalUserId":   strconv.FormatInt(row.UserID, 10),
			"agencyExternalId": strconv.FormatInt(row.AgencyID, 10), "revision": row.Revision,
			"effectiveAtMs": strconv.FormatInt(row.EffectiveAtMS, 10), "endedAtMs": ended,
			"current": current})
	}
	encoded, err := common.Marshal(items)
	if err != nil {
		c.JSON(503, gin.H{"error": "binding history unavailable"})
		return
	}
	next := ""
	if more {
		next = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	digest := sha256.Sum256(encoded)
	c.JSON(200, gin.H{"schemaVersion": 1, "gatewayId": s.GatewayID, "through": strconv.FormatInt(through, 10),
		"after": strconv.FormatInt(after, 10), "nextAfter": next, "hasMore": more,
		"consistentSnapshot": false, "items": items, "pageHash": hex.EncodeToString(digest[:])})
}
