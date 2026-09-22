package agencyhub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	AgencyStatusActive     = "active"
	AgencyStatusDisabled   = "disabled"
	OperatorStatusActive   = "active"
	OperatorStatusDisabled = "disabled"
	ActorTypeRoot          = "root"
	ActorTypeOperator      = "agency_operator"
	ActorTypeSystem        = "system"
)

type App struct {
	db                       *gorm.DB
	logDB                    *gorm.DB
	config                   Config
	ready                    atomic.Bool
	ssoPublicKey             []byte
	commandServicePublicKey  []byte
	commandServicePrivateKey []byte
	cursorSecret             []byte
	commandClient            *http.Client
	commandEndpoint          string
}

type Identity struct {
	ActorType            string          `json:"actor_type"`
	ActorID              int64           `json:"actor_id"`
	AgencyID             *int64          `json:"agency_id,omitempty"`
	Username             string          `json:"username,omitempty"`
	MustChangePassword   bool            `json:"must_change_password"`
	ActingAgency         *AgencyIdentity `json:"acting_agency,omitempty"`
	SessionID            int64           `json:"-"`
	SourceSID            string          `json:"-"`
	SourceSessionVersion int64           `json:"-"`
}

// AgencyIdentity is the business-facing identity used when a root session is
// temporarily acting on behalf of an agency. Internal database IDs remain in
// the session only and are not needed in the browser.
type AgencyIdentity struct {
	DisplayName      string `json:"display_name"`
	OperatorUsername string `json:"operator_username"`
}

type apiResponse struct {
	Success   bool      `json:"success"`
	Data      any       `json:"data,omitempty"`
	Error     *apiError `json:"error,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
}
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func New(db, logDB *gorm.DB, config Config) *App {
	if logDB == nil {
		logDB = db
	}
	if config.BasePath == "" {
		config.BasePath = "/agency"
	}
	if config.CookieName == "" {
		config.CookieName = "agency_session"
	}
	cursorSecret := []byte(strings.TrimSpace(config.CursorSecret))
	if len(cursorSecret) < 32 {
		if generated, err := randomToken(32); err == nil {
			cursorSecret = []byte(generated)
		}
	}
	return &App{db: db, logDB: logDB, config: config, cursorSecret: cursorSecret}
}

func (a *App) Migrate() error      { return model.MigrateAgency(a.db) }
func (a *App) SetReady(value bool) { a.ready.Store(value) }

func (a *App) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET(a.config.BasePath, a.index)
	r.GET(a.config.BasePath+"/", a.index)
	r.GET(a.config.BasePath+"/assets/*filepath", a.staticAsset)
	r.GET(a.config.BasePath+"/healthz", a.healthz)
	r.GET(a.config.BasePath+"/livez", a.livez)
	r.GET(a.config.BasePath+"/readyz", a.readyz)
	r.GET(a.config.BasePath+"/api/v1/auth/nonce", a.authNonce)
	r.POST(a.config.BasePath+"/api/v1/auth/login", a.login)
	r.GET(a.config.BasePath+"/sso/start", a.ssoStart)
	r.POST(a.config.BasePath+"/sso/callback", a.ssoCallback)
	r.GET(a.config.BasePath+"/api/v1/public/invitations/:code", a.publicInvitation)
	r.GET(a.config.BasePath+"/api/v1/public/invitations/:code/qr", a.publicInvitationQR)
	api := r.Group(a.config.BasePath + "/api/v1")
	api.Use(a.sessionMiddleware())
	api.Use(a.idempotencyMiddleware())
	api.GET("/auth/me", a.me)
	api.GET("/invitation", a.getOwnInvitation)
	api.POST("/auth/logout", a.logout)
	api.POST("/auth/change-password", a.changePassword)
	api.POST("/auth/verify", a.operatorVerify)
	api.GET("/pricing", a.getOwnPricing)
	api.GET("/pricing/history", a.getOwnPricingHistory)
	api.GET("/pricing/model-sales", a.getOwnModelSales)
	api.GET("/models", a.listPublicModels)
	api.POST("/pricing/sales/preview", a.previewSalesPricing)
	api.POST("/pricing/sales/publish", a.publishSalesPricing)
	api.GET("/customers/:user_id/pricing", a.getCustomerSalesPricing)
	api.PUT("/customers/:user_id/pricing", a.putCustomerSalesPricing)
	api.PUT("/customers/:user_id/pricing/batch", a.putCustomerSalesPricingBatch)
	// The agency UI mutation client intentionally defaults to POST; keep a
	// POST alias while retaining PUT for API clients that use REST semantics.
	api.POST("/customers/:user_id/pricing", a.putCustomerSalesPricing)
	api.POST("/customers/:user_id/pricing/batch", a.putCustomerSalesPricingBatch)
	api.DELETE("/customers/:user_id/pricing", a.deleteCustomerSalesPricing)
	api.POST("/children", a.createChildAgencyHTTP)
	api.GET("/children", a.listChildAgencies)
	api.GET("/hierarchy", a.getAgencyHierarchy)
	api.GET("/children/:id/pricing", a.getChildPricing)
	api.POST("/children/:id/pricing/publish", a.publishChildPricing)
	api.GET("/customers", a.listCustomers)
	api.GET("/customers/:user_id", a.getCustomer)
	api.GET("/customers/:user_id/usage", a.customerUsage)
	api.GET("/customers/:user_id/topups", a.customerTopups)
	api.GET("/commissions/summary", a.commissionSummary)
	api.GET("/commissions/ledger", a.commissionLedger)
	api.GET("/withdrawals", a.listWithdrawals)
	api.POST("/withdrawals", a.createWithdrawal)
	api.POST("/withdrawals/:id/cancel", a.cancelOwnWithdrawal)
	api.GET("/withdrawal-accounts", a.listWithdrawalAccounts)
	api.POST("/withdrawal-accounts", a.createWithdrawalAccount)
	api.PATCH("/withdrawal-accounts/:id", a.updateWithdrawalAccount)
	api.POST("/withdrawal-accounts/:id/disable", a.disableWithdrawalAccount)
	api.GET("/reports/summary", a.reportSummary)
	api.GET("/audit", a.listOwnAudit)
	api.POST("/exports", a.createExport)
	api.GET("/exports", a.listExports)
	api.GET("/exports/:id", a.getExport)
	api.GET("/exports/:id/download", a.downloadExport)

	root := api.Group("/root")
	root.Use(a.requireRoot())
	root.GET("/agencies", a.listAgencies)
	root.GET("/agencies/lookup", a.lookupAgency)
	root.POST("/agencies", a.createAgencyHTTP)
	root.GET("/agencies/:id", a.getAgency)
	root.PATCH("/agencies/:id", a.updateAgency)
	root.POST("/agencies/:id/disable", a.disableAgency)
	root.POST("/agencies/:id/enable", a.enableAgency)
	root.POST("/agencies/:id/reset-password", a.resetPassword)
	root.POST("/deliveries/:delivery_id/ack", a.acknowledgeDeliverySecret)
	root.GET("/agencies/:id/pricing", a.getRootPricing)
	root.GET("/agencies/:id/pricing/history", a.getRootPricingHistory)
	root.GET("/agencies/:id/pricing/model-sales", a.getRootModelSales)
	root.POST("/agencies/:id/pricing/preview", a.previewRootPricing)
	root.POST("/agencies/:id/pricing/publish", a.publishRootPricing)
	root.POST("/agencies/:id/pricing/sales/preview", a.previewRootSalesPricing)
	root.POST("/agencies/:id/pricing/sales/publish", a.publishRootSalesPricing)
	root.GET("/platform-pricing", a.getPlatformPricing)
	root.POST("/platform-pricing/publish", a.publishPlatformPricing)
	root.POST("/agencies/:id/enter", a.enterAgency)
	root.POST("/leave-agency", a.leaveAgency)
	root.GET("/customers", a.listRootCustomers)
	root.GET("/users/management", a.customerManagement)
	root.GET("/users/:user_id/management", a.customerManagement)
	root.POST("/users/:user_id/bind", a.bindExistingUser)
	root.GET("/provisioning/:id", a.getProvisioning)
	root.POST("/provisioning/:id/cancel", a.cancelProvisioning)
	root.GET("/reconciliation/issues", a.listReconciliationIssues)
	root.GET("/reconciliation/issues/:id", a.getReconciliationIssue)
	root.POST("/reconciliation/runs", a.createReconciliationRun)
	root.GET("/reconciliation/runs", a.listReconciliationRuns)
	root.POST("/reconciliation/issues/:id/resolve", a.resolveReconciliationIssue)
	root.GET("/sync/status", a.syncStatus)
	root.GET("/audit", a.listAudit)
	root.POST("/funding/reversals", a.createFundingReversal)
	root.POST("/users/:user_id/transfer", a.transferUser)
	root.GET("/withdrawals", a.listRootWithdrawals)
	root.POST("/withdrawals/:id/review", a.reviewWithdrawal)
	root.POST("/withdrawals/:id/transition", a.transitionWithdrawalCommand)
	root.POST("/withdrawals/:id/mark-paid", a.markWithdrawalPaid)
	root.POST("/withdrawals/:id/reject", a.rejectWithdrawal)
	root.POST("/withdrawal-accounts/:id/reveal", a.revealWithdrawalAccount)
	return r
}

// CommandRouter is mounted only by the gateway's dedicated internal mTLS
// listener. Public Hub and gateway routers must never mount these handlers.
func (a *App) CommandRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.POST("/internal/agency/v1/commands", a.createInternalCommand)
	r.GET("/internal/agency/v1/commands/:id", a.getInternalCommand)
	return r
}

func (a *App) healthz(c *gin.Context) {
	if err := a.pingDatabase(c); err != nil {
		respondError(c, http.StatusServiceUnavailable, "database_unavailable", "database unavailable", nil)
		return
	}
	response := gin.H{"status": "ok", "ready": a.ready.Load()}
	if schema, err := a.agencySchemaStatus(); err == nil {
		response["schema"] = schema
	}
	c.JSON(http.StatusOK, response)
}

func (a *App) livez(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (a *App) readyz(c *gin.Context) {
	if !a.ready.Load() {
		respondError(c, http.StatusServiceUnavailable, "not_ready", "agency hub is not ready", nil)
		return
	}
	if err := a.pingDatabase(c); err != nil {
		respondError(c, http.StatusServiceUnavailable, "database_unavailable", "database unavailable", nil)
		return
	}
	schema, err := a.agencySchemaStatus()
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "schema_check_failed", "agency schema check failed", err.Error())
		return
	}
	if schema["ready"] != true {
		respondError(c, http.StatusServiceUnavailable, "schema_unavailable", "agency schema is incomplete", schema)
		return
	}
	backlog, err := a.agencyBacklogStatus(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "backlog_check_failed", "agency backlog check failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "ready": true, "schema": schema, "capabilities": a.agencyCapabilities(), "backlog": backlog})
}

func (a *App) pingDatabase(c *gin.Context) error {
	if a.db == nil {
		return errors.New("database unavailable")
	}
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	return sqlDB.PingContext(ctx)
}

func (a *App) agencySchemaStatus() (gin.H, error) {
	if a.db == nil {
		return nil, errors.New("database unavailable")
	}
	missing := make([]string, 0)
	missingColumns := make([]string, 0)
	missingIndexes := make([]string, 0)
	for _, item := range model.AgencyModels() {
		statement := &gorm.Statement{DB: a.db}
		if err := statement.Parse(item); err != nil {
			return nil, err
		}
		if !a.db.Migrator().HasTable(item) {
			missing = append(missing, statement.Schema.Table)
		}
	}
	required := map[string][]string{
		(model.Agency{}).TableName():                     {"price_revision", "state_revision", "current_policy_version_id"},
		(model.AgencyPricePolicyVersion{}).TableName():   {"revision", "policy_hash", "created_at_ms"},
		(model.AgencyPlatformPriceState{}).TableName():   {"revision", "current_version_id", "updated_at_ms"},
		(model.AgencyPlatformPriceVersion{}).TableName(): {"revision", "policy_hash", "created_at_ms"},
		(model.AgencyBillingJournal{}).TableName():       {"charge_id", "status", "reserve_quota", "revision"},
		(model.AgencyBillingOperation{}).TableName():     {"charge_id", "event_count", "committed_result"},
		(model.AgencyFundingAccount{}).TableName():       {"paid_available", "nonpaid_available", "money_seq", "reconcile_blocked"},
		(model.AgencyExportJob{}).TableName():            {"error_code", "lease_owner", "lease_until", "attempts"},
		(model.AgencyTopupFact{}).TableName():            {"source_id", "quota_conversion_snapshot", "actual_money", "currency_code", "payment_reference", "expires_at", "expired_quota"},
		(model.AgencyReconciliationIssue{}).TableName():  {"active_key", "resolution_evidence", "repair_event_id"},
		(model.AgencyChargeComponent{}).TableName():      {"component_key", "original_result", "refunded_quota", "reversed_commission_micros"},
		(model.AgencyComponentFunding{}).TableName():     {"charge_component_id", "allocation_id", "restored_paid_quota", "restored_nonpaid_quota", "restored_debt_quota"},
		(model.AgencyUsageFact{}).TableName():            {"component_key"},
		(model.AgencyFundingLot{}).TableName():           {"actor_user_id", "source_snapshot_json", "bonus_debt_repaid", "expires_at", "bonus_expired"},
		(model.AgencyFundingAllocation{}).TableName():    {"source_snapshot_json"},
		(model.AgencyDebtRepayment{}).TableName():        {"source_kind"},
		(model.AgencyFundingDebt{}).TableName():          {"allocation_id"},
		(model.AgencyCommissionLedger{}).TableName():     {"component_key"},
	}
	for table, columns := range required {
		if !a.db.Migrator().HasTable(table) {
			continue
		}
		types, err := a.db.Migrator().ColumnTypes(table)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]struct{}, len(types))
		for _, column := range types {
			seen[strings.ToLower(column.Name())] = struct{}{}
		}
		for _, column := range columns {
			if _, ok := seen[strings.ToLower(column)]; !ok {
				missingColumns = append(missingColumns, table+"."+column)
			}
		}
	}
	if a.db.Migrator().HasTable(&model.AgencyReconciliationIssue{}) &&
		!a.db.Migrator().HasIndex(&model.AgencyReconciliationIssue{}, "uidx_agency_reconcile_active") {
		missingIndexes = append(missingIndexes, "uidx_agency_reconcile_active")
	}
	for _, spec := range []struct {
		item  any
		index string
	}{
		{&model.AgencyUsageFact{}, "uidx_agency_usage_component_key"},
		{&model.AgencyCommissionLedger{}, "uidx_agency_commission_component_key"},
		{&model.AgencyChargeComponent{}, "uidx_agency_charge_component"},
		{&model.AgencyComponentFunding{}, "uidx_agency_component_allocation"},
		{&model.AgencyFundingDebt{}, "idx_agency_debt_allocation"},
		{&model.AgencyFundingLot{}, "idx_agency_funding_lot_expiry"},
	} {
		if a.db.Migrator().HasTable(spec.item) && !a.db.Migrator().HasIndex(spec.item, spec.index) {
			missingIndexes = append(missingIndexes, spec.index)
		}
	}
	return gin.H{"ready": len(missing) == 0 && len(missingColumns) == 0 && len(missingIndexes) == 0, "missing_tables": missing, "missing_columns": missingColumns, "missing_indexes": missingIndexes, "schema_version": "agency-hub-v1"}, nil
}

func (a *App) agencyCapabilities() gin.H {
	return gin.H{
		"agency_durable_v1":    true,
		"pricing_snapshot_v1":  true,
		"outbox_v1":            true,
		"billing_component_v2": true,
		"billing_schemas":      []string{agencycontract.SchemaVersion, agencycontract.ComponentSchemaVersion},
		"onboarding":           common.AgencyOnboardingEnabled(),
		"commission_worker":    a.config.CommissionEnabled,
		"fact_projection":      a.config.FactProjectionEnabled,
		"withdrawals":          a.config.WithdrawalsEnabled,
		"exports":              strings.TrimSpace(a.config.ExportDir) != "",
	}
}

func (a *App) agencyBacklogStatus(ctx context.Context) (gin.H, error) {
	statuses := []string{"pending", "retry", "claimed", "poison"}
	deliveries := gin.H{}
	for _, status := range statuses {
		var count int64
		if err := a.db.WithContext(ctx).Model(&model.AgencyEventDelivery{}).Where("status = ?", status).Count(&count).Error; err != nil {
			return nil, err
		}
		deliveries[status] = count
	}
	var openIssues int64
	if err := a.db.WithContext(ctx).Model(&model.AgencyReconciliationIssue{}).
		Where("status = ? OR (status IN ? AND (resolution_evidence IS NULL OR resolution_evidence = ?))", "open", []string{"ignored", "resolved"}, "").Count(&openIssues).Error; err != nil {
		return nil, err
	}
	var exportsInProgress int64
	if err := a.db.WithContext(ctx).Model(&model.AgencyExportJob{}).Where("status IN ?", []string{"queued", "processing"}).Count(&exportsInProgress).Error; err != nil {
		return nil, err
	}
	commissionJobs := gin.H{}
	for _, status := range []string{"pending", "deferred", "retry", "claimed", "done", "skipped"} {
		var count int64
		if err := a.db.WithContext(ctx).Model(&model.AgencyCommissionJob{}).Where("status = ?", status).Count(&count).Error; err != nil {
			return nil, err
		}
		commissionJobs[status] = count
	}
	var lastProjected int64
	if err := a.db.WithContext(ctx).Model(&model.AgencySourceEvent{}).
		Where("processing_status IN ?", []string{"done", "facts_done", "skipped"}).
		Select("COALESCE(MAX(created_at_ms), 0)").Scan(&lastProjected).Error; err != nil {
		return nil, err
	}
	return gin.H{
		"deliveries":                    deliveries,
		"commission_jobs":               commissionJobs,
		"open_reconciliation_issues":    openIssues,
		"exports_in_progress":           exportsInProgress,
		"fact_projection_enabled":       a.config.FactProjectionEnabled,
		"commission_processing_enabled": a.config.CommissionEnabled,
		"last_projected_at_ms":          lastProjected,
	}, nil
}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, apiResponse{Success: true, Data: data, RequestID: requestID(c)})
}
func respondCreated(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, apiResponse{Success: true, Data: data, RequestID: requestID(c)})
}
func respondError(c *gin.Context, status int, code, message string, details any) {
	c.AbortWithStatusJSON(status, apiResponse{Success: false, Error: &apiError{Code: code, Message: message, Details: details}, RequestID: requestID(c)})
}
func requestID(c *gin.Context) string {
	if c == nil {
		return common.NewRequestId()
	}
	value := strings.TrimSpace(c.GetHeader("X-Request-Id"))
	if value != "" && len(value) <= 191 {
		return value
	}
	return common.NewRequestId()
}

func randomToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", hash[:])
}
func newCode(length int) (string, error) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for i := range raw {
		raw[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(raw), nil
}
func parseID(value string) (int64, error) {
	var result int64
	if _, err := fmt.Sscan(strings.TrimSpace(value), &result); err != nil || result <= 0 {
		return 0, errors.New("invalid id")
	}
	return result, nil
}
