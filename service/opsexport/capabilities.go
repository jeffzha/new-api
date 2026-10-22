package opsexport

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// Capabilities is deliberately a contract probe, not a best-effort inventory.
// A domain is marked supported only when this source exposes an explicit,
// bounded endpoint for it. Existing tables alone are not evidence that a
// domain can be migrated safely.
func (s *Server) Capabilities(c *gin.Context) {
	if len(c.Request.URL.Query()) != 0 {
		cnError(c, 400, "invalid_payload")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	result := map[string]any{
		"schemaVersion": 1,
		"gatewayId":     s.GatewayID,
		"sourceSystem":  "new-api",
		"generatedAt":   time.Now().UTC().Format(model.CNTimeLayout),
		"domains": []map[string]any{
			{"name": "distributor_directory", "supported": s.DB.WithContext(ctx).Migrator().HasTable(&model.Agency{}) && s.DB.WithContext(ctx).Migrator().HasTable(&model.AgencyActiveUserBinding{}), "endpoint": "/api/ops-export/v1/distributors", "schemaVersion": 1, "incremental": true, "consistentSnapshot": false, "reason": "bounded directory keyset; changed rows converge on the next scan"},
			{"name": "agency_identity_metadata", "supported": s.DB.WithContext(ctx).Migrator().HasTable(&model.Agency{}) && s.DB.WithContext(ctx).Migrator().HasTable(&model.AgencyOperatorAccount{}), "endpoint": "/api/ops-export/v1/agency-identities", "schemaVersion": 1, "incremental": false, "consistentSnapshot": false, "reason": "stable invite code and non-secret operator state only; credentials, sessions and payout data are excluded"},
			{"name": "customer_identity", "supported": s.DB.WithContext(ctx).Migrator().HasTable(&model.User{}), "endpoint": "/api/ops-export/v1/inventory", "schemaVersion": 1, "incremental": false, "consistentSnapshot": true, "reason": "identity inventory is read-only and bounded; ownership is imported by TokenOps"},
			{"name": "financial_facts", "supported": s.DB.WithContext(ctx).Migrator().HasTable(&model.CNFinancePublication{}), "endpoint": "/api/ops-export/v2/facts", "schemaVersion": 2, "incremental": true, "consistentSnapshot": false, "reason": "immutable published facts only; this is not a complete agency ledger snapshot"},
			{"name": "price_versions", "supported": priceVersionExportAvailable(s.DB.WithContext(ctx)), "endpoint": "/api/ops-export/v1/price-versions", "schemaVersion": 1, "incremental": false, "consistentSnapshot": false, "reason": "bounded immutable price history plus current customer overrides; mutable overrides may converge on a later scan"},
			{"name": "wallet_history", "supported": walletHistoryAvailable(s.DB.WithContext(ctx)), "endpoint": "/api/ops-export/v1/wallet-history", "schemaVersion": 1, "incremental": false, "consistentSnapshot": false, "reason": "bounded account, lot, allocation, ledger, top-up and reversal scan; mutable rows require later shadow comparison and writer fence"},
			{"name": "debt_history", "supported": debtHistoryAvailable(s.DB.WithContext(ctx)), "endpoint": "/api/ops-export/v1/debt-history", "schemaVersion": 1, "incremental": false, "consistentSnapshot": false, "reason": "bounded debt and repayment scan; mutable balances require shadow comparison before cutover"},
			{"name": "commission_history", "supported": commissionHistoryAvailable(s.DB.WithContext(ctx)), "endpoint": "/api/ops-export/v1/commission-history", "schemaVersion": 1, "incremental": false, "consistentSnapshot": false, "reason": "bounded immutable commission entries plus mutable balance checkpoints; target must reconcile before publication"},
			{"name": "withdrawals", "supported": withdrawalHistoryAvailable(s.DB.WithContext(ctx)), "endpoint": "/api/ops-export/v1/withdrawals", "schemaVersion": 1, "incremental": false, "consistentSnapshot": false, "reason": "bounded payout metadata and transitions; account ciphertext and payment references are never exposed"},
			{"name": "audit_history", "supported": auditHistoryAvailable(s.DB.WithContext(ctx)), "endpoint": "/api/ops-export/v1/audit-history", "schemaVersion": 1, "incremental": false, "consistentSnapshot": true, "reason": "bounded immutable agency audit archive with sensitive payloads represented by hashes"},
			{"name": "writer_fence", "supported": false, "endpoint": "/api/ops-export/v1/writer-fence", "schemaVersion": 1, "incremental": false, "consistentSnapshot": false, "reason": "read-only state and last receipt available; mutation remains on the signed command channel and end-to-end cutover is not certified"},
			{"name": "sensitive_identity", "supported": false, "endpoint": nil, "schemaVersion": 0, "incremental": false, "consistentSnapshot": false, "reason": "password and payout-account migration requires target-key encrypted, short-lived credentials"},
		},
	}
	c.JSON(200, result)
}
