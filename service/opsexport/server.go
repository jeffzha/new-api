// Package opsexport serves a dedicated, read-only credential surface. It does
// not accept management API tokens or expose customer/key/billing-basis fields.
package opsexport

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Server struct {
	DB              *gorm.DB
	GatewayID       string
	TokenHash       [32]byte
	RefundTokenHash [32]byte
	Refund          *model.CNRefundService
}

var Default *Server

func StartConfigured(db *gorm.DB) error {
	digest := os.Getenv("OPS_EXPORT_TOKEN_SHA256")
	if digest == "" {
		return nil
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("OPS_EXPORT_TOKEN_SHA256 must be a SHA-256 hex digest")
	}
	gateway := os.Getenv("OPS_GATEWAY_ID")
	legacy := os.Getenv("UPSTREAM_EVENT_GATEWAY_ID")
	if gateway == "" {
		gateway = legacy
	}
	if gateway == "" || strings.ContainsAny(gateway, "\r\n") || (legacy != "" && gateway != legacy) {
		return errors.New("Ops export gateway identity is missing or conflicting")
	}
	if err := model.MigrateOpsExport(db); err != nil {
		return err
	}
	if err := model.MigrateCNFinance(db, gateway); err != nil {
		return err
	}
	server := &Server{DB: db, GatewayID: gateway}
	copy(server.TokenHash[:], decoded)
	if err := server.configureRefund(); err != nil {
		return err
	}
	Default = server
	return nil
}

func Register(group *gin.RouterGroup) {
	useExportAuth(group, false)
	group.GET("/facts", func(c *gin.Context) { Default.Facts(c) })
	group.GET("/daily", func(c *gin.Context) { Default.Daily(c) })
	group.GET("/catalog", func(c *gin.Context) { Default.Catalog(c) })
	group.GET("/inventory", func(c *gin.Context) { Default.Inventory(c) })
	group.GET("/distributors", func(c *gin.Context) { Default.Distributors(c) })
	group.GET("/agency-identities", func(c *gin.Context) { Default.AgencyIdentityMetadata(c) })
	group.GET("/price-versions", func(c *gin.Context) { Default.PriceVersions(c) })
	group.GET("/wallet-history", func(c *gin.Context) { Default.WalletHistory(c) })
	group.GET("/debt-history", func(c *gin.Context) { Default.DebtHistory(c) })
	group.GET("/commission-history", func(c *gin.Context) { Default.CommissionHistory(c) })
	group.GET("/withdrawals", func(c *gin.Context) { Default.Withdrawals(c) })
	group.GET("/finance-ranges/:domain/:resource", func(c *gin.Context) { Default.FinanceRange(c) })
	group.GET("/audit-history", func(c *gin.Context) { Default.AuditHistory(c) })
	group.GET("/capabilities", func(c *gin.Context) { Default.Capabilities(c) })
	group.GET("/writer-fence", func(c *gin.Context) { Default.WriterFence(c) })
}

func useExportAuth(group *gin.RouterGroup, structured bool) {
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if Default == nil {
			if structured {
				cnError(c, http.StatusNotFound, "source_unavailable")
				c.Abort()
				return
			}
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		token, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		digest := sha256.Sum256([]byte(token))
		if !ok || len(token) < 32 || len(token) > 256 || strings.ContainsAny(token, "\r\n \t") ||
			subtle.ConstantTimeCompare(digest[:], Default.TokenHash[:]) != 1 {
			if structured {
				cnError(c, http.StatusUnauthorized, "unauthorized")
				c.Abort()
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	})
}

func (s *Server) Facts(c *gin.Context) {
	limit := 500
	var after int64
	for key, values := range c.Request.URL.Query() {
		if (key != "limit" && key != "after") || len(values) != 1 {
			c.JSON(400, gin.H{"error": "invalid page"})
			return
		}
	}
	if value, present := c.GetQuery("limit"); present {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 1000 {
			c.JSON(400, gin.H{"error": "invalid limit"})
			return
		}
		limit = n
	}
	if value, present := c.GetQuery("after"); present {
		raw, err := base64.RawURLEncoding.DecodeString(value)
		text, prefix := strings.CutPrefix(string(raw), "v2:")
		id, parseErr := strconv.ParseInt(text, 10, 64)
		if err != nil || parseErr != nil || !prefix || len(value) > 40 || id < 0 || strconv.FormatInt(id, 10) != text {
			c.JSON(400, gin.H{"error": "invalid cursor"})
			return
		}
		after = id
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	items, next, more, err := model.ReadOpsFacts(ctx, s.DB, after, limit)
	if errors.Is(err, model.ErrOpsExportCursor) {
		c.JSON(400, gin.H{"error": "invalid cursor"})
		return
	}
	if err != nil {
		c.JSON(503, gin.H{"error": "finance source unavailable or requires repair"})
		return
	}
	cursor := base64.RawURLEncoding.EncodeToString([]byte("v2:" + strconv.FormatInt(next, 10)))
	c.JSON(200, gin.H{"gatewayId": s.GatewayID, "items": items, "nextCursor": cursor, "hasMore": more})
}

func (s *Server) Daily(c *gin.Context) {
	query := c.Request.URL.Query()
	day := query.Get("date")
	parsed, err := time.Parse("2006-01-02", day)
	if len(query) != 1 || len(query["date"]) != 1 || err != nil || parsed.Year() < 1 {
		c.JSON(400, gin.H{"error": "invalid date"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	result, err := model.ReadOpsDaily(ctx, s.DB, day)
	if err != nil {
		c.JSON(503, gin.H{"error": "finance source unavailable or requires repair"})
		return
	}
	result["gatewayId"] = s.GatewayID
	c.JSON(200, result)
}
