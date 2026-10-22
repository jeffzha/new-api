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
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	pricePhasePlatform = "platform"
	pricePhaseAgency   = "agency"
	pricePhaseCustomer = "customer"
)

// priceVersionCursor bounds every table at the start of a scan. Immutable
// version rows are stable inside the boundary. Customer overrides are mutable,
// so the response advertises a convergent scan rather than a database snapshot.
type priceVersionCursor struct {
	Gateway             string `json:"gateway"`
	Until               int64  `json:"until"`
	MaxPlatformVersion  int64  `json:"maxPlatformVersion"`
	MaxAgencyVersion    int64  `json:"maxAgencyVersion"`
	MaxCustomerOverride int64  `json:"maxCustomerOverride"`
	PlatformCount       int64  `json:"platformCount"`
	AgencyCount         int64  `json:"agencyCount"`
	CustomerCount       int64  `json:"customerCount"`
	PlatformHash        string `json:"platformHash,omitempty"`
	AgencyHash          string `json:"agencyHash,omitempty"`
	CustomerHash        string `json:"customerHash,omitempty"`
	Phase               string `json:"phase"`
	After               int64  `json:"after"`
}

func priceVersionExportAvailable(db *gorm.DB) bool {
	return db.Migrator().HasTable(&model.Agency{}) &&
		db.Migrator().HasTable(&model.AgencyPricePolicyVersion{}) &&
		db.Migrator().HasTable(&model.AgencyPlatformPriceVersion{}) &&
		db.Migrator().HasTable(&model.AgencyCustomerSalesOverride{})
}

func (s *Server) priceCursor(raw string) (priceVersionCursor, error) {
	var page priceVersionCursor
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 4096 {
		return page, errors.New("invalid cursor")
	}
	mac := hmac.New(sha256.New, s.TokenHash[:])
	mac.Write([]byte(parts[0]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return page, errors.New("invalid cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || common.Unmarshal(data, &page) != nil || page.Gateway != s.GatewayID || page.Until < time.Now().Unix() || page.After < 0 {
		return page, errors.New("expired cursor")
	}
	if page.Phase != pricePhasePlatform && page.Phase != pricePhaseAgency && page.Phase != pricePhaseCustomer {
		return page, errors.New("invalid cursor phase")
	}
	if page.MaxPlatformVersion < 0 || page.MaxAgencyVersion < 0 || page.MaxCustomerOverride < 0 ||
		page.PlatformCount < 0 || page.AgencyCount < 0 || page.CustomerCount < 0 ||
		(page.Phase == pricePhasePlatform && page.After > page.MaxPlatformVersion) ||
		(page.Phase == pricePhaseAgency && page.After > page.MaxAgencyVersion) ||
		(page.Phase == pricePhaseCustomer && page.After > page.MaxCustomerOverride) {
		return page, errors.New("invalid cursor boundary")
	}
	return page, nil
}

func (s *Server) signPriceCursor(page priceVersionCursor) string {
	data, _ := common.Marshal(page)
	raw := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.TokenHash[:])
	mac.Write([]byte(raw))
	return raw + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func priceContent(policy any) (any, string, error) {
	encoded, err := common.Marshal(policy)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(encoded)
	return policy, hex.EncodeToString(digest[:]), nil
}

func extendPriceDomainHash(previous string, page []byte) (string, error) {
	var chained []byte
	if previous != "" {
		decoded, err := hex.DecodeString(previous)
		if err != nil || len(decoded) != sha256.Size {
			return "", errors.New("invalid rolling domain hash")
		}
		chained = append(chained, decoded...)
	}
	chained = append(chained, page...)
	digest := sha256.Sum256(chained)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Server) PriceVersions(c *gin.Context) {
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

	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	db := s.DB.WithContext(ctx)
	if !priceVersionExportAvailable(db) {
		c.JSON(501, gin.H{"error": "price version export unavailable"})
		return
	}

	page := priceVersionCursor{Gateway: s.GatewayID, Until: time.Now().Add(2 * time.Hour).Unix(), Phase: pricePhasePlatform}
	if raw, ok := c.GetQuery("cursor"); ok {
		var err error
		page, err = s.priceCursor(raw)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid cursor"})
			return
		}
	} else if err := initializePriceExportBoundary(db, &page); err != nil {
		c.JSON(503, gin.H{"error": "price source unavailable"})
		return
	}

	identity := page
	identity.Phase, identity.After = pricePhasePlatform, 0
	identity.PlatformHash, identity.AgencyHash, identity.CustomerHash = "", "", ""
	snapshotID := s.signPriceCursor(identity)
	var items []gin.H
	var more bool
	for {
		var err error
		items, more, err = readPriceExportPage(db, &page, limit)
		if err != nil {
			c.JSON(503, gin.H{"error": "price source unavailable or requires repair"})
			return
		}
		if len(items) > 0 || more || !advancePriceExportPhase(&page) {
			break
		}
	}
	if items == nil {
		items = []gin.H{}
	}
	pageJSON, err := common.Marshal(items)
	if err != nil {
		c.JSON(503, gin.H{"error": "price source unavailable or requires repair"})
		return
	}
	pageDigest := sha256.Sum256(pageJSON)
	switch page.Phase {
	case pricePhasePlatform:
		page.PlatformHash, err = extendPriceDomainHash(page.PlatformHash, pageJSON)
	case pricePhaseAgency:
		page.AgencyHash, err = extendPriceDomainHash(page.AgencyHash, pageJSON)
	case pricePhaseCustomer:
		page.CustomerHash, err = extendPriceDomainHash(page.CustomerHash, pageJSON)
	}
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid cursor"})
		return
	}
	next := ""
	if more || (len(items) > 0 && advancePriceExportPhase(&page)) {
		next = s.signPriceCursor(page)
	}
	c.JSON(200, gin.H{
		"schemaVersion": 1, "sourceSystem": "new-api", "gatewayId": s.GatewayID,
		"supported": true, "consistentSnapshot": false, "snapshotId": snapshotID,
		"controlTotals": gin.H{"platformVersions": page.PlatformCount, "agencyVersions": page.AgencyCount, "customerOverrides": page.CustomerCount},
		"pageHash":      hex.EncodeToString(pageDigest[:]), "items": items, "nextCursor": next,
	})
}

func initializePriceExportBoundary(db *gorm.DB, page *priceVersionCursor) error {
	checks := []struct {
		model any
		max   *int64
		count *int64
	}{
		{&model.AgencyPlatformPriceVersion{}, &page.MaxPlatformVersion, &page.PlatformCount},
		{&model.AgencyPricePolicyVersion{}, &page.MaxAgencyVersion, &page.AgencyCount},
		{&model.AgencyCustomerSalesOverride{}, &page.MaxCustomerOverride, &page.CustomerCount},
	}
	for _, check := range checks {
		if err := db.Model(check.model).Select("COALESCE(MAX(id),0)").Scan(check.max).Error; err != nil {
			return err
		}
		if err := db.Model(check.model).Where("id <= ?", *check.max).Count(check.count).Error; err != nil {
			return err
		}
	}
	return nil
}

func advancePriceExportPhase(page *priceVersionCursor) bool {
	switch page.Phase {
	case pricePhasePlatform:
		page.Phase, page.After = pricePhaseAgency, 0
		return page.AgencyCount > 0 || page.CustomerCount > 0
	case pricePhaseAgency:
		page.Phase, page.After = pricePhaseCustomer, 0
		return page.CustomerCount > 0
	default:
		return false
	}
}

func readPriceExportPage(db *gorm.DB, page *priceVersionCursor, limit int) ([]gin.H, bool, error) {
	switch page.Phase {
	case pricePhasePlatform:
		return readPlatformPriceVersions(db, page, limit)
	case pricePhaseAgency:
		return readAgencyPriceVersions(db, page, limit)
	case pricePhaseCustomer:
		return readCustomerPriceOverrides(db, page, limit)
	default:
		return nil, false, errors.New("invalid price export phase")
	}
}

func readPlatformPriceVersions(db *gorm.DB, page *priceVersionCursor, limit int) ([]gin.H, bool, error) {
	var rows []model.AgencyPlatformPriceVersion
	if err := db.Where("id > ? AND id <= ?", page.After, page.MaxPlatformVersion).Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	var state model.AgencyPlatformPriceState
	stateErr := db.Select("current_version_id").Where("id = ?", 1).First(&state).Error
	if stateErr != nil && !errors.Is(stateErr, gorm.ErrRecordNotFound) {
		return nil, false, stateErr
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		var policy agencycontract.PlatformPolicy
		if common.Unmarshal([]byte(row.PolicyJSON), &policy) != nil {
			return nil, false, errors.New("invalid platform price policy")
		}
		policy.Revision = row.Revision
		content, contentHash, err := priceContent(policy)
		if err != nil {
			return nil, false, err
		}
		status := "historical"
		if state.CurrentVersionID == row.ID {
			status = "published"
		}
		items = append(items, gin.H{"recordType": "platform_price_version", "externalVersionId": strconv.FormatInt(row.ID, 10), "revision": row.Revision, "status": status, "timeSemantics": "created_at", "createdAtMs": row.CreatedAtMS, "sourcePolicyHash": row.PolicyHash, "contentHash": contentHash, "policy": content})
		page.After = row.ID
	}
	return items, more, nil
}

func readAgencyPriceVersions(db *gorm.DB, page *priceVersionCursor, limit int) ([]gin.H, bool, error) {
	var rows []model.AgencyPricePolicyVersion
	if err := db.Where("id > ? AND id <= ?", page.After, page.MaxAgencyVersion).Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	agencyIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		agencyIDs = append(agencyIDs, row.AgencyID)
	}
	var agencies []model.Agency
	if len(agencyIDs) > 0 && db.Select("id", "current_policy_version_id").Where("id IN ?", agencyIDs).Find(&agencies).Error != nil {
		return nil, false, errors.New("agency price owner unavailable")
	}
	current := make(map[int64]int64, len(agencies))
	for _, agency := range agencies {
		current[agency.ID] = agency.CurrentPolicyVersionID
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		var policy agencycontract.Policy
		if common.Unmarshal([]byte(row.PolicyJSON), &policy) != nil {
			return nil, false, errors.New("invalid agency price policy")
		}
		policy.Revision = row.Revision
		content, contentHash, err := priceContent(policy)
		if err != nil {
			return nil, false, err
		}
		status := "historical"
		if current[row.AgencyID] == row.ID {
			status = "published"
		}
		items = append(items, gin.H{"recordType": "agency_price_version", "agencyExternalId": strconv.FormatInt(row.AgencyID, 10), "externalVersionId": strconv.FormatInt(row.ID, 10), "revision": row.Revision, "status": status, "timeSemantics": "created_at", "createdAtMs": row.CreatedAtMS, "sourcePolicyHash": row.PolicyHash, "contentHash": contentHash, "policy": content})
		page.After = row.ID
	}
	return items, more, nil
}

func readCustomerPriceOverrides(db *gorm.DB, page *priceVersionCursor, limit int) ([]gin.H, bool, error) {
	var rows []model.AgencyCustomerSalesOverride
	if err := db.Where("id > ? AND id <= ?", page.After, page.MaxCustomerOverride).Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		content := gin.H{"modelKey": row.ModelKey, "originModelName": row.OriginModelName, "salesBps": row.SalesBPS}
		_, contentHash, err := priceContent(content)
		if err != nil {
			return nil, false, err
		}
		items = append(items, gin.H{"recordType": "customer_sales_override", "externalOverrideId": strconv.FormatInt(row.ID, 10), "agencyExternalId": strconv.FormatInt(row.AgencyID, 10), "externalUserId": strconv.FormatInt(row.UserID, 10), "revision": row.Revision, "status": "current", "timeSemantics": "updated_at", "createdAtMs": row.CreatedAtMS, "updatedAtMs": row.UpdatedAtMS, "contentHash": contentHash, "price": content})
		page.After = row.ID
	}
	return items, more, nil
}
