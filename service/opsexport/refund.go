package opsexport

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// No real provider is configured by environment in this slice. An accepted
// adapter must be explicitly wired server-side; arbitrary callback JSON is absent.
func (s *Server) configureRefund() error {
	digest := os.Getenv("OPS_REFUND_MUTATION_TOKEN_SHA256")
	if digest == "" {
		return nil
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || subtle.ConstantTimeCompare(decoded, s.TokenHash[:]) == 1 {
		return errors.New("refund mutation credential must be a distinct SHA-256 digest")
	}
	tenant := os.Getenv("OPS_REFUND_TENANT_ID")
	if tenant == "" || len(tenant) > 128 || strings.ContainsAny(tenant, "\r\n \t") {
		return errors.New("refund registered tenant identity required")
	}
	if err := model.MigrateCNRefund(s.DB); err != nil {
		return err
	}
	s.Refund = &model.CNRefundService{DB: s.DB, TenantID: tenant, GatewayID: s.GatewayID}
	copy(s.RefundTokenHash[:], decoded)
	return nil
}

func registerRefundRoutes(group *gin.RouterGroup) {
	mutations := group.Group("")
	mutations.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if Default == nil || Default.Refund == nil {
			cnError(c, 404, "source_unavailable")
			c.Abort()
			return
		}
		token, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		digest := sha256.Sum256([]byte(token))
		if !ok || len(token) < 32 || len(token) > 256 || strings.ContainsAny(token, "\r\n \t") ||
			subtle.ConstantTimeCompare(Default.RefundTokenHash[:], Default.TokenHash[:]) == 1 ||
			subtle.ConstantTimeCompare(digest[:], Default.RefundTokenHash[:]) != 1 {
			cnError(c, 401, "unauthorized")
			c.Abort()
			return
		}
		c.Next()
	})
	mutations.GET("/refund-balances", func(c *gin.Context) {
		query := c.Request.URL.Query()
		if len(query) != 3 || len(query["tenantId"]) != 1 || len(query["gatewayId"]) != 1 {
			cnError(c, 400, "invalid_payload")
			return
		}
		if len(query["originalPaymentId"]) == 1 {
			result, err := Default.Refund.BalanceByPayment(c.Request.Context(), query.Get("tenantId"), query.Get("gatewayId"), query.Get("originalPaymentId"))
			refundResponse(c, result, err)
			return
		}
		if len(query["lotId"]) != 1 {
			cnError(c, 400, "invalid_payload")
			return
		}
		lot, err := strconv.ParseInt(query.Get("lotId"), 10, 64)
		if err != nil || lot <= 0 || strconv.FormatInt(lot, 10) != query.Get("lotId") {
			cnError(c, 400, "invalid_payload")
			return
		}
		result, err := Default.Refund.Balance(c.Request.Context(), query.Get("tenantId"), query.Get("gatewayId"), lot)
		refundResponse(c, result, err)
	})
	mutations.GET("/refund-reservations/:operation", func(c *gin.Context) {
		query := c.Request.URL.Query()
		if len(query) != 2 || len(query["tenantId"]) != 1 || len(query["gatewayId"]) != 1 {
			cnError(c, 400, "invalid_payload")
			return
		}
		result, err := Default.Refund.Get(c.Request.Context(), query.Get("tenantId"), query.Get("gatewayId"), c.Param("operation"))
		refundResponse(c, result, err)
	})
	mutations.POST("/refund-reservations", func(c *gin.Context) {
		var request model.CNRefundReserveRequest
		if !refundDecode(c, &request, "tenantId", "gatewayId", "operationId", "approvalId", "approvalHash", "originalPaymentId", "lotId", "userId", "amount", "currency", "expectedFundingVersion", "expectedLotVersion") {
			return
		}
		if c.GetHeader("Idempotency-Key") != request.OperationID {
			cnError(c, 400, "invalid_payload")
			return
		}
		result, err := Default.Refund.Reserve(c.Request.Context(), request)
		refundResponse(c, result, err)
	})
	for _, action := range []string{"execution-intents", "reconcile"} {
		mutations.POST("/refund-reservations/:operation/"+action, func(c *gin.Context) {
			var request model.CNRefundCommand
			if !refundDecode(c, &request, "tenantId", "gatewayId", "operationId", "approvalId", "approvalHash", "expectedVersion", "attemptId") {
				return
			}
			if request.OperationID != c.Param("operation") || c.GetHeader("Idempotency-Key") != request.OperationID {
				cnError(c, 400, "invalid_payload")
				return
			}
			result, err := Default.Refund.Execute(c.Request.Context(), request, action == "reconcile")
			refundResponse(c, result, err)
		})
	}
}

func refundDecode(c *gin.Context, target any, keys ...string) bool {
	if len(c.Request.URL.Query()) != 0 {
		cnError(c, 400, "invalid_payload")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	if err != nil {
		cnError(c, 400, "invalid_payload")
		return false
	}
	var fields map[string]any
	if common.Unmarshal(body, &fields) != nil || len(fields) != len(keys) {
		cnError(c, 400, "invalid_payload")
		return false
	}
	for _, key := range keys {
		if fields[key] == nil {
			cnError(c, 400, "invalid_payload")
			return false
		}
	}
	if common.Unmarshal(body, target) != nil {
		cnError(c, 400, "invalid_payload")
		return false
	}
	return true
}

func refundResponse(c *gin.Context, result any, err error) {
	if err == nil {
		c.JSON(200, result)
		return
	}
	status, code := 503, "source_unavailable"
	switch {
	case errors.Is(err, model.ErrCNRefundInvalid):
		status, code = 400, err.Error()
	case errors.Is(err, model.ErrCNRefundScope):
		status, code = 403, err.Error()
	case errors.Is(err, gorm.ErrRecordNotFound):
		status, code = 404, "reservation_not_found"
	case errors.Is(err, model.ErrCNRefundUnavailable):
		code = err.Error()
	case errors.Is(err, model.ErrCNRefundStale), errors.Is(err, model.ErrCNRefundConflict), errors.Is(err, model.ErrCNRefundUnsupported), errors.Is(err, model.ErrCNRefundInsufficient), errors.Is(err, model.ErrCNRefundNonintegral):
		status, code = 409, err.Error()
	}
	cnError(c, status, code)
}

func (s *Server) refundCapabilities() map[string]any {
	result := model.CNRefundCapabilities(s.GatewayID)
	if s.Refund == nil || common.RedisEnabled || common.BatchUpdateEnabled {
		return result
	}
	result["reservationAvailable"] = true
	result["executionAvailable"] = s.Refund.Provider != nil
	result["status"] = "limited"
	result["supportedPaths"] = []string{"native_agency_paid_cny_exact_quota"}
	result["blockerCodes"] = []string{"ordinary_wallet_unsupported", "manual_unsupported", "real_provider_unconfigured"}
	return result
}
