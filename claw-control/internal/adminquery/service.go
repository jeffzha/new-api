package adminquery

import (
	"time"

	credentialpkg "github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/pagination"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"gorm.io/gorm"
)

type Service struct {
	db *gorm.DB
}

type Dashboard struct {
	CustomersTotal     int64              `json:"customers_total"`
	CustomersActive    int64              `json:"customers_active"`
	AppsActive         int64              `json:"apps_active"`
	PlansExpiring      int64              `json:"plans_expiring"`
	UsageAuditsPending int64              `json:"usage_audits_pending"`
	PendingOutbox      int64              `json:"pending_outbox"`
	RecentAudits       []model.AdminAudit `json:"recent_audits"`
}

type AppConfigView struct {
	ID                         uint64               `json:"id"`
	ConfigVersion              int64                `json:"config_version"`
	Status                     string               `json:"status"`
	Region                     string               `json:"region"`
	SpaceID                    string               `json:"space_id"`
	TemplateAgentID            string               `json:"template_agent_id"`
	CredentialProfileID        *uint64              `json:"credential_profile_id,omitempty"`
	CredentialChangeApprovalID *uint64              `json:"credential_change_approval_id,omitempty"`
	RowVersion                 int64                `json:"row_version"`
	Limits                     productpolicy.Limits `json:"limits"`
	Capabilities               []string             `json:"capabilities"`
	CreatedBy                  string               `json:"created_by"`
	VerifiedAt                 *time.Time           `json:"verified_at,omitempty"`
	CreatedAt                  time.Time            `json:"created_at"`
}

type CustomerDetail struct {
	Customer      model.Customer          `json:"customer"`
	Members       []model.CustomerMember  `json:"members"`
	Identities    []model.IdentityBinding `json:"identities,omitempty"`
	App           *model.CustomerApp      `json:"app,omitempty"`
	CurrentConfig *AppConfigView          `json:"current_config,omitempty"`
	PendingConfig *AppConfigView          `json:"pending_config,omitempty"`
	Verifications []model.AppVerification `json:"verifications"`
	PlanPeriods   []model.PlanPeriod      `json:"plan_periods"`
	Invoices      []model.CustomerInvoice `json:"invoices"`
	UsageAudits   []model.UsageAudit      `json:"usage_audits,omitempty"`
}

type PlanVersionView struct {
	ID              uint64               `json:"id"`
	PlanID          uint64               `json:"plan_id"`
	PlanCode        string               `json:"plan_code"`
	DisplayName     string               `json:"display_name"`
	Version         int64                `json:"version"`
	MonthlyPriceCNY string               `json:"monthly_price_cny"`
	Currency        string               `json:"currency"`
	Capabilities    []string             `json:"capabilities"`
	Limits          productpolicy.Limits `json:"limits"`
	Status          string               `json:"status"`
	ValidFrom       time.Time            `json:"valid_from"`
	ValidTo         *time.Time           `json:"valid_to,omitempty"`
}

type CredentialView struct {
	ID                  uint64     `json:"id"`
	OwnerScope          string     `json:"owner_scope"`
	CustomerID          *uint64    `json:"customer_id,omitempty"`
	ProviderEnvironment string     `json:"provider_environment"`
	Name                string     `json:"name"`
	Fingerprint         string     `json:"fingerprint"`
	FingerprintVersion  int        `json:"fingerprint_version"`
	Status              string     `json:"status"`
	Version             int64      `json:"version"`
	RowVersion          int64      `json:"row_version"`
	RotatedAt           *time.Time `json:"rotated_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type AppDraftResult struct {
	App     *model.CustomerApp `json:"app"`
	Version *AppConfigView     `json:"config_version"`
}

type AppVerificationView struct {
	VerificationID      string    `json:"verification_id"`
	AppConfigVersionID  uint64    `json:"app_config_version_id"`
	Result              string    `json:"result"`
	AppMode             int       `json:"app_mode"`
	ReleaseStatus       string    `json:"release_status"`
	TemplateAgentStatus string    `json:"template_agent_status"`
	DynamicAgentConfig  bool      `json:"dynamic_agent_config"`
	ErrorCode           string    `json:"error_code,omitempty"`
	ErrorMessage        string    `json:"error_message,omitempty"`
	VerifiedAt          time.Time `json:"verified_at"`
}

type CustomerAppListView struct {
	model.CustomerApp
	CurrentConfigVersion *int64               `json:"current_config_version,omitempty"`
	PendingConfigVersion *int64               `json:"pending_config_version,omitempty"`
	LatestConfig         *AppConfigView       `json:"latest_config,omitempty"`
	LatestVerification   *AppVerificationView `json:"latest_verification,omitempty"`
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Dashboard(limit int) (*Dashboard, error) {
	limit = normalizedLimit(limit, 20)
	now := time.Now().UTC()
	result := &Dashboard{}
	queries := []struct {
		model any
		where string
		args  []any
		count *int64
	}{
		{model: &model.Customer{}, count: &result.CustomersTotal},
		{model: &model.Customer{}, where: "status = ?", args: []any{model.CustomerStatusActive}, count: &result.CustomersActive},
		{model: &model.CustomerApp{}, where: "status = ?", args: []any{model.AppStatusActive}, count: &result.AppsActive},
		{model: &model.PlanPeriod{}, where: "status = ? AND end_at > ? AND end_at <= ?", args: []any{model.PeriodStatusActive, now, now.Add(7 * 24 * time.Hour)}, count: &result.PlansExpiring},
		{model: &model.ControlOutbox{}, where: "status = ?", args: []any{model.OutboxStatusPending}, count: &result.PendingOutbox},
		{model: &model.UsageAudit{}, where: "status = ?", args: []any{model.UsageAuditStatusDraft}, count: &result.UsageAuditsPending},
	}
	for _, query := range queries {
		db := s.db.Model(query.model)
		if query.where != "" {
			db = db.Where(query.where, query.args...)
		}
		if err := db.Count(query.count).Error; err != nil {
			return nil, err
		}
	}
	if err := s.db.Order("id desc").Limit(limit).Find(&result.RecentAudits).Error; err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) CustomerDetail(customerID uint64) (*CustomerDetail, error) {
	if customerID == 0 {
		return nil, domain.Invalid("customer_id is required")
	}
	result := &CustomerDetail{}
	if err := s.db.First(&result.Customer, customerID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, domain.NotFound("customer not found")
		}
		return nil, err
	}
	if err := s.db.Where("customer_id = ?", customerID).Order("id asc").Find(&result.Members).Error; err != nil {
		return nil, err
	}
	if err := s.db.Where("customer_id = ?", customerID).Order("id asc").Find(&result.Identities).Error; err != nil {
		return nil, err
	}
	var stable model.CustomerApp
	appQuery := s.db.Where("customer_id = ? AND slot = ?", customerID, "primary").First(&stable)
	if appQuery.Error != nil && appQuery.Error != gorm.ErrRecordNotFound {
		return nil, appQuery.Error
	}
	if appQuery.Error == nil {
		result.App = &stable
		var configurations []model.AppConfigVersion
		if err := s.db.Where("customer_app_id = ?", stable.ID).Order("config_version desc").Find(&configurations).Error; err != nil {
			return nil, err
		}
		for _, configuration := range configurations {
			view, err := projectAppConfig(configuration)
			if err != nil {
				return nil, err
			}
			if stable.CurrentConfigVersionID != nil && *stable.CurrentConfigVersionID == configuration.ID {
				result.CurrentConfig = view
			}
			if stable.PendingConfigVersionID != nil && *stable.PendingConfigVersionID == configuration.ID {
				result.PendingConfig = view
			}
		}
		if err := s.db.Where("customer_app_id = ?", stable.ID).Order("id desc").Limit(50).Find(&result.Verifications).Error; err != nil {
			return nil, err
		}
	}
	if err := s.db.Where("customer_id = ?", customerID).Order("start_at desc").Limit(60).Find(&result.PlanPeriods).Error; err != nil {
		return nil, err
	}
	if err := s.db.Where("customer_id = ?", customerID).Order("id desc").Limit(60).Find(&result.Invoices).Error; err != nil {
		return nil, err
	}
	if err := s.db.Where("customer_id = ?", customerID).Order("id desc").Limit(60).Find(&result.UsageAudits).Error; err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) PlanCatalog(limit int) ([]PlanVersionView, error) {
	page, err := s.PlanCatalogPage(0, limit)
	return page.Items, err
}

func (s *Service) PlanCatalogPage(beforeID uint64, limit int) (pagination.Page[PlanVersionView], error) {
	limit = pagination.Limit(limit)
	query := s.db.Order("id desc").Limit(limit + 1)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	var versions []model.PlanVersion
	if err := query.Find(&versions).Error; err != nil {
		return pagination.Page[PlanVersionView]{}, err
	}
	page := pagination.Trim(versions, limit, func(value model.PlanVersion) uint64 { return value.ID })
	result := make([]PlanVersionView, 0, len(page.Items))
	for _, version := range page.Items {
		var plan model.Plan
		if err := s.db.First(&plan, version.PlanID).Error; err != nil {
			return pagination.Page[PlanVersionView]{}, err
		}
		var capabilities []string
		var limits productpolicy.Limits
		if err := jsonx.Unmarshal([]byte(version.CapabilitiesJSON), &capabilities); err != nil {
			return pagination.Page[PlanVersionView]{}, err
		}
		if err := jsonx.Unmarshal([]byte(version.LimitsJSON), &limits); err != nil {
			return pagination.Page[PlanVersionView]{}, err
		}
		result = append(result, PlanVersionView{
			ID: version.ID, PlanID: plan.ID, PlanCode: plan.PlanCode, DisplayName: version.Name,
			Version: version.Version, MonthlyPriceCNY: version.MonthlyPriceCNY, Currency: version.Currency,
			Capabilities: capabilities, Limits: limits, Status: version.Status,
			ValidFrom: version.ValidFrom, ValidTo: version.ValidTo,
		})
	}
	return pagination.Page[PlanVersionView]{Items: result, NextBeforeID: page.NextBeforeID}, nil
}

func (s *Service) CredentialProfiles(limit int, customerID *uint64) ([]CredentialView, error) {
	page, err := s.CredentialProfilesPage(0, limit, customerID)
	return page.Items, err
}

func (s *Service) CredentialProfilesPage(beforeID uint64, limit int, customerID *uint64) (pagination.Page[CredentialView], error) {
	limit = pagination.Limit(limit)
	var profiles []model.CredentialProfile
	query := s.db.Order("id desc").Limit(limit + 1)
	if customerID != nil {
		if *customerID == 0 {
			return pagination.Page[CredentialView]{}, domain.Invalid("customer_id must be positive")
		}
		query = query.Where("owner_scope = ? OR (owner_scope = ? AND customer_id = ?)",
			credentialpkg.PlatformOwnerScope, credentialpkg.CustomerOwnerScope(*customerID), *customerID)
	}
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	if err := query.Find(&profiles).Error; err != nil {
		return pagination.Page[CredentialView]{}, err
	}
	page := pagination.Trim(profiles, limit, func(value model.CredentialProfile) uint64 { return value.ID })
	result := make([]CredentialView, 0, len(page.Items))
	for _, profile := range page.Items {
		result = append(result, ProjectCredential(profile))
	}
	return pagination.Page[CredentialView]{Items: result, NextBeforeID: page.NextBeforeID}, nil
}

func (s *Service) Apps(customerID uint64) ([]model.CustomerApp, error) {
	page, err := s.AppsPage(customerID, 0, pagination.MaxLimit)
	return page.Items, err
}

func (s *Service) AppsPage(customerID, beforeID uint64, limit int) (pagination.Page[model.CustomerApp], error) {
	if customerID == 0 {
		return pagination.Page[model.CustomerApp]{}, domain.Invalid("customer_id is required")
	}
	limit = pagination.Limit(limit)
	var result []model.CustomerApp
	query := s.db.Where("customer_id = ?", customerID).Order("id desc").Limit(limit + 1)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	if err := query.Find(&result).Error; err != nil {
		return pagination.Page[model.CustomerApp]{}, err
	}
	return pagination.Trim(result, limit, func(value model.CustomerApp) uint64 { return value.ID }), nil
}

func (s *Service) AppViewsPage(customerID, beforeID uint64, limit int) (pagination.Page[CustomerAppListView], error) {
	apps, err := s.AppsPage(customerID, beforeID, limit)
	if err != nil {
		return pagination.Page[CustomerAppListView]{}, err
	}
	appIDs := make([]uint64, 0, len(apps.Items))
	for index := range apps.Items {
		appIDs = append(appIDs, apps.Items[index].ID)
	}
	versions := make(map[uint64]model.AppConfigVersion)
	latestConfigs := make(map[uint64]*AppConfigView)
	latestVerifications := make(map[uint64]*AppVerificationView)
	if len(appIDs) > 0 {
		var configurations []model.AppConfigVersion
		if err := s.db.Where("customer_app_id IN ?", appIDs).Order("customer_app_id asc, config_version desc").Find(&configurations).Error; err != nil {
			return pagination.Page[CustomerAppListView]{}, err
		}
		for index := range configurations {
			configuration := configurations[index]
			versions[configuration.ID] = configuration
			if latestConfigs[configuration.CustomerAppID] == nil {
				view, err := projectAppConfig(configuration)
				if err != nil {
					return pagination.Page[CustomerAppListView]{}, err
				}
				latestConfigs[configuration.CustomerAppID] = view
			}
		}
		var verifications []model.AppVerification
		if err := s.db.Where("customer_app_id IN ?", appIDs).Order("customer_app_id asc, id desc").Find(&verifications).Error; err != nil {
			return pagination.Page[CustomerAppListView]{}, err
		}
		for index := range verifications {
			verification := verifications[index]
			if latestVerifications[verification.CustomerAppID] == nil {
				latestVerifications[verification.CustomerAppID] = &AppVerificationView{
					VerificationID: verification.PublicID, AppConfigVersionID: verification.AppConfigVersionID,
					Result: verification.Result, AppMode: verification.AppMode, ReleaseStatus: verification.ReleaseStatus,
					TemplateAgentStatus: verification.TemplateAgentStatus, DynamicAgentConfig: verification.DynamicAgentConfig,
					ErrorCode: verification.ErrorCode, ErrorMessage: verification.ErrorMessage, VerifiedAt: verification.VerifiedAt,
				}
			}
		}
	}
	items := make([]CustomerAppListView, 0, len(apps.Items))
	for index := range apps.Items {
		view := CustomerAppListView{
			CustomerApp: apps.Items[index], LatestConfig: latestConfigs[apps.Items[index].ID],
			LatestVerification: latestVerifications[apps.Items[index].ID],
		}
		if apps.Items[index].CurrentConfigVersionID != nil {
			configuration, ok := versions[*apps.Items[index].CurrentConfigVersionID]
			if !ok {
				return pagination.Page[CustomerAppListView]{}, domain.Conflict("current App configuration is unavailable")
			}
			value := configuration.ConfigVersion
			view.CurrentConfigVersion = &value
		}
		if apps.Items[index].PendingConfigVersionID != nil {
			configuration, ok := versions[*apps.Items[index].PendingConfigVersionID]
			if !ok {
				return pagination.Page[CustomerAppListView]{}, domain.Conflict("pending App configuration is unavailable")
			}
			value := configuration.ConfigVersion
			view.PendingConfigVersion = &value
		}
		items = append(items, view)
	}
	return pagination.Page[CustomerAppListView]{Items: items, NextBeforeID: apps.NextBeforeID}, nil
}

func ProjectAppDraft(stable *model.CustomerApp, configuration *model.AppConfigVersion) (*AppDraftResult, error) {
	if stable == nil || configuration == nil {
		return nil, domain.Invalid("App draft is incomplete")
	}
	version, err := projectAppConfig(*configuration)
	if err != nil {
		return nil, err
	}
	return &AppDraftResult{App: stable, Version: version}, nil
}

func ProjectCredential(profile model.CredentialProfile) CredentialView {
	return CredentialView{
		ID: profile.ID, OwnerScope: profile.OwnerScope, CustomerID: profile.CustomerID,
		ProviderEnvironment: profile.ProviderEnvironment, Name: profile.Name,
		Fingerprint: profile.Fingerprint, FingerprintVersion: profile.FingerprintVersion,
		Status: profile.Status, Version: profile.Version,
		RowVersion: profile.RowVersion,
		RotatedAt:  profile.RotatedAt, CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt,
	}
}

func (s *Service) Audits(limit int, customerID *uint64) ([]model.AdminAudit, error) {
	page, err := s.AuditsPage(0, limit, customerID)
	return page.Items, err
}

func (s *Service) AuditsPage(beforeID uint64, limit int, customerID *uint64) (pagination.Page[model.AdminAudit], error) {
	limit = pagination.Limit(limit)
	query := s.db.Order("id desc").Limit(limit + 1)
	if customerID != nil {
		query = query.Where("customer_id = ?", *customerID)
	}
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	var audits []model.AdminAudit
	if err := query.Find(&audits).Error; err != nil {
		return pagination.Page[model.AdminAudit]{}, err
	}
	return pagination.Trim(audits, limit, func(value model.AdminAudit) uint64 { return value.ID }), nil
}

func projectAppConfig(configuration model.AppConfigVersion) (*AppConfigView, error) {
	var limits productpolicy.Limits
	var capabilities []string
	if err := jsonx.Unmarshal([]byte(configuration.LimitsJSON), &limits); err != nil {
		return nil, err
	}
	if err := jsonx.Unmarshal([]byte(configuration.CapabilitiesJSON), &capabilities); err != nil {
		return nil, err
	}
	return &AppConfigView{
		ID: configuration.ID, ConfigVersion: configuration.ConfigVersion, Status: configuration.Status,
		Region: configuration.Region, SpaceID: configuration.SpaceID, TemplateAgentID: configuration.TemplateAgentID,
		CredentialProfileID:        configuration.CredentialProfileID,
		CredentialChangeApprovalID: configuration.CredentialChangeApprovalID, RowVersion: configuration.RowVersion,
		Limits: limits, Capabilities: capabilities, CreatedBy: configuration.CreatedBy,
		VerifiedAt: configuration.VerifiedAt, CreatedAt: configuration.CreatedAt,
	}, nil
}

func normalizedLimit(limit, fallback int) int {
	if limit <= 0 || limit > 500 {
		return fallback
	}
	return limit
}
