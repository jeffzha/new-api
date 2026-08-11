package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	credentialpkg "github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/newapi"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

type Service struct {
	db               *gorm.DB
	secretResolver   secrets.Resolver
	identityVerifier newapi.IdentityVerifier
	entryTicketTTL   time.Duration
	ssoTicketTTL     time.Duration
	sessionTTL       time.Duration
	adminSessionTTL  time.Duration
	contextTTL       time.Duration
}

const SelectionNonceTTL = 2 * time.Minute

type IssueEntryTicketCommand struct {
	NewAPIUserID    int64
	IdentityVersion string
	Surface         string
	IsSuperAdmin    bool
}

type IssueEntryTicketResult struct {
	Ticket    string `json:"ticket"`
	ExpiresAt int64  `json:"expires_at"`
}

type EnterCommand struct {
	Ticket   string
	DeferSSO bool
}

type EnterResult struct {
	Surface                 string            `json:"surface"`
	SelectionRequired       bool              `json:"selection_required,omitempty"`
	Selections              []SelectionOption `json:"selections,omitempty"`
	ControlSessionToken     string            `json:"-"`
	ControlCSRFToken        string            `json:"-"`
	ControlSessionExpiresAt time.Time         `json:"control_session_expires_at,omitempty"`
	ADPSSOTicket            string            `json:"-"`
	ADPSSOTicketExpiresAt   time.Time         `json:"adp_sso_ticket_expires_at,omitempty"`
	SSOBrowserBinding       string            `json:"-"`
	AdminSessionToken       string            `json:"-"`
	AdminCSRFToken          string            `json:"-"`
	AdminSessionExpiresAt   time.Time         `json:"admin_session_expires_at,omitempty"`
}

type SelectionOption struct {
	SelectionToken      string    `json:"selection_token"`
	ExpiresAt           time.Time `json:"expires_at"`
	CustomerCode        string    `json:"customer_code"`
	CustomerDisplayName string    `json:"customer_display_name"`
	Role                string    `json:"role"`
	AppSelector         string    `json:"app_selector"`
	AppAlias            string    `json:"app_alias"`
	AppDisplayName      string    `json:"app_display_name"`
	AppStatus           string    `json:"app_status"`
	IsDefault           bool      `json:"is_default"`
	AccessMode          string    `json:"access_mode"`
}

type SelectionResult struct {
	ADPSSOTicket          string    `json:"-"`
	ADPSSOTicketExpiresAt time.Time `json:"adp_sso_ticket_expires_at"`
	SSOBrowserBinding     string    `json:"-"`
}

type ConsumeTicketCommand struct {
	Ticket          string
	BrowserBinding  string
	ConsumerService string
}

type IdentityContext struct {
	BindingID        string `json:"binding_id"`
	CanonicalSubject string `json:"canonical_subject"`
	CustomerID       uint64 `json:"customer_id"`
	NewAPIUserID     int64  `json:"new_api_user_id"`
	AuthEpoch        int64  `json:"auth_epoch"`
	DisplayName      string `json:"display_name"`
	ApplicationID    string `json:"application_id"`
	AppProfileID     uint64 `json:"app_profile_id"`
	ConfigVersion    int64  `json:"config_version"`
	Role             string `json:"role,omitempty"`
	AccessMode       string `json:"access_mode"`
	Allowed          bool   `json:"allowed"`
	ExpiresAt        int64  `json:"expires_at,omitempty"`
}

type AuthzCommand struct {
	BindingID        string
	CanonicalSubject string
	AuthEpoch        int64
	CustomerID       uint64
	AppProfileID     uint64
	ConfigVersion    int64
	Method           string
	ResourcePath     string
}

type AppContextCommand struct {
	BindingID              string
	CanonicalSubject       string
	AuthEpoch              int64
	RequestedAppProfileID  uint64
	RequestedConfigVersion int64
	CurrentAppProfileID    uint64
	CurrentConfigVersion   int64
	Purpose                string
}

type AppContext struct {
	CustomerID          uint64               `json:"customer_id"`
	ApplicationID       string               `json:"application_id"`
	AppProfileID        uint64               `json:"app_profile_id"`
	ConfigVersion       int64                `json:"config_version"`
	AuthEpoch           int64                `json:"auth_epoch"`
	Vendor              string               `json:"vendor"`
	ServiceVendor       string               `json:"service_vendor"`
	ProviderEnvironment string               `json:"provider_environment"`
	Region              string               `json:"region"`
	AppID               string               `json:"app_id"`
	AppKey              string               `json:"app_key"`
	SpaceID             string               `json:"space_id"`
	TemplateAgentID     string               `json:"template_agent_id"`
	ProviderAppMode     *int                 `json:"provider_app_mode,omitempty"`
	RuntimeProfile      *string              `json:"runtime_profile,omitempty"`
	ExecutionEnabled    *bool                `json:"execution_enabled,omitempty"`
	SecretID            string               `json:"secret_id"`
	SecretKey           string               `json:"secret_key"`
	Capabilities        []string             `json:"capabilities"`
	Limits              productpolicy.Limits `json:"limits"`
	ExpiresAt           int64                `json:"expires_at"`
}

type BrowserConfig struct {
	CustomerCode        string               `json:"customer_code"`
	CustomerDisplayName string               `json:"customer_display_name"`
	Role                string               `json:"role"`
	AccessMode          string               `json:"access_mode"`
	AppDisplayName      string               `json:"app_display_name"`
	AppSelector         string               `json:"app_selector"`
	AppAlias            string               `json:"app_alias"`
	IsDefault           bool                 `json:"is_default"`
	AppStatus           string               `json:"app_status"`
	Capabilities        []string             `json:"capabilities"`
	Limits              productpolicy.Limits `json:"limits"`
}

type BrowserPlan struct {
	PeriodID      uint64         `json:"period_id"`
	StartAt       time.Time      `json:"start_at"`
	EndAt         time.Time      `json:"end_at"`
	Status        string         `json:"status"`
	PaymentStatus string         `json:"payment_status"`
	AmountCNY     string         `json:"amount_cny"`
	Snapshot      map[string]any `json:"snapshot"`
}

type resolvedAccess struct {
	identity     model.IdentityBinding
	member       model.CustomerMember
	customer     model.Customer
	app          model.CustomerApp
	config       model.AppConfigVersion
	period       *model.PlanPeriod
	capabilities []string
	limits       productpolicy.Limits
	mode         string
	epoch        int64
}

type planPolicySnapshot struct {
	Capabilities []string             `json:"capabilities"`
	Limits       productpolicy.Limits `json:"limits"`
}

func New(db *gorm.DB, resolver secrets.Resolver, verifier newapi.IdentityVerifier, entryTicketTTL, ssoTicketTTL, sessionTTL, adminSessionTTL, contextTTL time.Duration) *Service {
	return &Service{
		db: db, secretResolver: resolver, identityVerifier: verifier,
		entryTicketTTL: entryTicketTTL, ssoTicketTTL: ssoTicketTTL,
		sessionTTL: sessionTTL, adminSessionTTL: adminSessionTTL, contextTTL: contextTTL,
	}
}

func (s *Service) IssueEntryTicket(command IssueEntryTicketCommand) (*IssueEntryTicketResult, error) {
	command.IdentityVersion = strings.TrimSpace(command.IdentityVersion)
	command.Surface = strings.ToLower(strings.TrimSpace(command.Surface))
	if command.Surface == "" {
		command.Surface = "workbench"
	}
	if command.NewAPIUserID <= 0 || command.IdentityVersion == "" || len(command.IdentityVersion) > 128 {
		return nil, domain.Invalid("new_api_user_id and identity_version are required")
	}
	if s.entryTicketTTL <= 0 {
		return nil, fmt.Errorf("entry ticket TTL must be positive")
	}
	result := &IssueEntryTicketResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if command.Surface == "admin" {
			if !command.IsSuperAdmin {
				return domain.Forbidden("admin entry ticket requires a trusted super-admin assertion")
			}
			token, err := randomOpaqueToken()
			if err != nil {
				return err
			}
			expiresAt := time.Now().UTC().Add(s.entryTicketTTL)
			hash := sha256.Sum256([]byte(token))
			if err := tx.Create(&model.EntryTicket{
				TokenHash: hex.EncodeToString(hash[:]), NewAPIUserID: command.NewAPIUserID,
				IdentityVersion: command.IdentityVersion, Surface: "admin", IsSuperAdmin: true,
				ExpiresAt: expiresAt,
			}).Error; err != nil {
				return err
			}
			result.Ticket = token
			result.ExpiresAt = expiresAt.Unix()
			return nil
		}
		if command.Surface != "workbench" {
			return domain.Invalid("surface must be workbench or admin")
		}
		var bindings []model.IdentityBinding
		if err := database.ForUpdate(tx).Where("new_api_user_id = ? AND status IN ?", command.NewAPIUserID, []string{
			model.IdentityStatusProvisioning, model.IdentityStatusActive,
		}).Order("id asc").Find(&bindings).Error; err != nil {
			return err
		}
		if len(bindings) == 0 {
			return domain.NotFound("identity binding not found for new-api user")
		}
		for index := range bindings {
			if bindings[index].IdentityVersion == command.IdentityVersion {
				continue
			}
			before := bindings[index]
			bindings[index].IdentityVersion = command.IdentityVersion
			bindings[index].AuthEpoch++
			bindings[index].RowVersion++
			if err := tx.Save(&bindings[index]).Error; err != nil {
				return err
			}
			if err := support.Enqueue(tx, &bindings[index].CustomerID, "SESSION_REVOKE", fmt.Sprintf("identity-version:%d:%d", bindings[index].ID, bindings[index].AuthEpoch), map[string]any{
				"binding_id": bindings[index].PublicID, "new_api_user_id": bindings[index].NewAPIUserID, "auth_epoch": bindings[index].AuthEpoch,
			}); err != nil {
				return err
			}
			if err := support.Audit(tx, &bindings[index].CustomerID, "new-api-core", "identity.version.sync", "identity_binding", bindings[index].PublicID, &before, &bindings[index], "", ""); err != nil {
				return err
			}
		}
		token, err := randomOpaqueToken()
		if err != nil {
			return err
		}
		expiresAt := time.Now().UTC().Add(s.entryTicketTTL)
		hash := sha256.Sum256([]byte(token))
		ticket := &model.EntryTicket{
			TokenHash: hex.EncodeToString(hash[:]), NewAPIUserID: command.NewAPIUserID,
			IdentityVersion: command.IdentityVersion, Surface: "workbench", ExpiresAt: expiresAt,
		}
		if err := tx.Create(ticket).Error; err != nil {
			return err
		}
		result.Ticket = token
		result.ExpiresAt = expiresAt.Unix()
		return nil
	})
	return result, err
}

func (s *Service) Enter(ctx context.Context, command EnterCommand) (*EnterResult, error) {
	command.Ticket = strings.TrimSpace(command.Ticket)
	if command.Ticket == "" {
		return nil, domain.Invalid("entry ticket is required")
	}
	if s.identityVerifier == nil || s.sessionTTL <= 0 || s.adminSessionTTL <= 0 || s.ssoTicketTTL <= 0 {
		return nil, fmt.Errorf("browser entry service is not configured")
	}
	hash := sha256.Sum256([]byte(command.Ticket))
	var entry model.EntryTicket
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).Where("token_hash = ?", hex.EncodeToString(hash[:])).First(&entry).Error; err != nil {
			return domain.NotFound("entry ticket not found")
		}
		now := time.Now().UTC()
		if entry.ConsumedAt != nil {
			return domain.Conflict("entry ticket has already been consumed")
		}
		if !now.Before(entry.ExpiresAt) {
			return domain.Forbidden("entry ticket has expired")
		}
		entry.ConsumedAt = &now
		return tx.Save(&entry).Error
	}); err != nil {
		return nil, err
	}
	if entry.Surface == "admin" {
		if !entry.IsSuperAdmin {
			return nil, domain.Forbidden("admin entry ticket lacks a trusted assertion")
		}
		if err := s.identityVerifier.VerifyAdmin(ctx, entry.NewAPIUserID, entry.IdentityVersion); err != nil {
			return nil, err
		}
		return s.createAdminSession(entry)
	}
	if entry.Surface != "workbench" {
		return nil, domain.Forbidden("entry ticket surface is invalid")
	}
	if err := s.identityVerifier.Verify(ctx, entry.NewAPIUserID, entry.IdentityVersion); err != nil {
		return nil, err
	}
	result := &EnterResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		candidates, err := s.availableCandidates(tx, entry.NewAPIUserID, entry.IdentityVersion, true)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return domain.Forbidden("identity has no workbench access")
		}
		sessionToken, err := randomOpaqueToken()
		if err != nil {
			return err
		}
		csrfToken, err := randomOpaqueToken()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		sessionHash := sha256.Sum256([]byte(sessionToken))
		csrfHash := sha256.Sum256([]byte(csrfToken))
		session := &model.ControlSession{
			TokenHash: hex.EncodeToString(sessionHash[:]), CSRFTokenHash: hex.EncodeToString(csrfHash[:]), NewAPIUserID: entry.NewAPIUserID,
			IdentityVersion: entry.IdentityVersion,
			ExpiresAt:       now.Add(s.sessionTTL), LastSeenAt: now,
		}
		if command.DeferSSO || len(candidates) == 1 {
			selected := &candidates[0]
			if selected.app.Slot != "primary" {
				return domain.Conflict("customer has no explicit default App")
			}
			session.SelectionState = model.ControlSessionStateSelected
			applyResolvedSession(session, selected)
		} else {
			session.SelectionState = model.ControlSessionStateSelectionPending
			session.AccessMode = model.ControlSessionStateSelectionPending
		}
		if err := tx.Create(session).Error; err != nil {
			return err
		}
		result.ControlSessionToken = sessionToken
		result.ControlCSRFToken = csrfToken
		result.ControlSessionExpiresAt = session.ExpiresAt
		result.Surface = "workbench"
		if session.SelectionState == model.ControlSessionStateSelectionPending {
			options, err := s.issueSelectionOptions(tx, session, candidates, now)
			if err != nil {
				return err
			}
			result.SelectionRequired = true
			result.Selections = options
			return nil
		}
		if command.DeferSSO {
			return nil
		}
		sso, err := s.createSSOTicket(tx, session, &candidates[0], now)
		if err != nil {
			return err
		}
		result.ADPSSOTicket = sso.ADPSSOTicket
		result.ADPSSOTicketExpiresAt = sso.ADPSSOTicketExpiresAt
		result.SSOBrowserBinding = sso.SSOBrowserBinding
		return nil
	})
	return result, err
}

type SessionPrincipal struct {
	ControlSessionID  uint64
	CustomerID        uint64
	NewAPIUserID      int64
	IdentityBindingID uint64
	IdentityVersion   string
	Role              string
}

// AuthorizeControlSession returns only the server-side authorization scope.
// It never exposes provider identifiers and optionally enforces the hashed
// double-submit CSRF token created with the browser session.
func (s *Service) AuthorizeControlSession(ctx context.Context, token, csrfToken string, requireCSRF bool) (*SessionPrincipal, error) {
	resolved, session, err := s.resolveSession(token, false)
	if err != nil {
		return nil, err
	}
	if requireCSRF {
		csrfToken = strings.TrimSpace(csrfToken)
		if csrfToken == "" || session.CSRFTokenHash == "" {
			return nil, domain.Forbidden("control session CSRF token is required")
		}
		digest := sha256.Sum256([]byte(csrfToken))
		expected, decodeErr := hex.DecodeString(session.CSRFTokenHash)
		if decodeErr != nil || subtle.ConstantTimeCompare(expected, digest[:]) != 1 {
			return nil, domain.Forbidden("control session CSRF token is invalid")
		}
	}
	if s.identityVerifier == nil {
		return nil, fmt.Errorf("new-api identity verifier is unavailable")
	}
	if err := s.identityVerifier.Verify(ctx, resolved.identity.NewAPIUserID, resolved.identity.IdentityVersion); err != nil {
		return nil, err
	}
	return &SessionPrincipal{
		ControlSessionID: session.ID, CustomerID: resolved.customer.ID,
		NewAPIUserID: resolved.identity.NewAPIUserID, IdentityBindingID: resolved.identity.ID,
		IdentityVersion: resolved.identity.IdentityVersion, Role: resolved.member.Role,
	}, nil
}

// IssueAppSelection creates the same single-use nonce used by the existing
// context selector, but only after Agent Store authorization has selected the
// server-owned CustomerApp. The browser never supplies the provider AppId.
func (s *Service) IssueAppSelection(ctx context.Context, sessionToken string, customerAppID uint64, ttl time.Duration) (string, error) {
	if customerAppID == 0 || ttl <= 0 || ttl > time.Minute {
		return "", domain.Invalid("customer App and a launch TTL of at most 60 seconds are required")
	}
	principal, err := s.AuthorizeControlSession(ctx, sessionToken, "", false)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(strings.TrimSpace(sessionToken)))
	var result string
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var session model.ControlSession
		if err := database.ForUpdate(tx).Where("token_hash = ?", hex.EncodeToString(hash[:])).First(&session).Error; err != nil {
			return domain.Forbidden("control session is invalid")
		}
		now := time.Now().UTC()
		if session.ID != principal.ControlSessionID || session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
			return domain.Forbidden("control session is stale")
		}
		var identity model.IdentityBinding
		if err := tx.First(&identity, principal.IdentityBindingID).Error; err != nil {
			return domain.Forbidden("control session identity is unavailable")
		}
		resolved, err := s.resolveExact(tx, identity.PublicID, customerAppID, true)
		if err != nil {
			return err
		}
		if resolved.customer.ID != principal.CustomerID || resolved.identity.NewAPIUserID != principal.NewAPIUserID || (resolved.mode != "active" && resolved.mode != "readonly") {
			return domain.Forbidden("Agent Store application context is unavailable")
		}
		result, err = randomOpaqueToken()
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(result))
		expiresAt := now.Add(ttl)
		if expiresAt.After(session.ExpiresAt) {
			expiresAt = session.ExpiresAt
		}
		return tx.Create(&model.ContextSelectionNonce{
			TokenHash: hex.EncodeToString(digest[:]), ControlSessionID: session.ID,
			NewAPIUserID: resolved.identity.NewAPIUserID, IdentityBindingID: resolved.identity.ID,
			CustomerMemberID: resolved.member.ID, CustomerID: resolved.customer.ID,
			CustomerAppID: resolved.app.ID, AppConfigVersionID: resolved.config.ID,
			IdentityVersion: resolved.identity.IdentityVersion, IdentityAuthEpoch: resolved.identity.AuthEpoch,
			MemberAuthEpoch: resolved.member.AuthEpoch, AppAuthEpoch: resolved.app.AuthEpoch,
			ExpiresAt: expiresAt,
		}).Error
	})
	return result, err
}

func (s *Service) createAdminSession(entry model.EntryTicket) (*EnterResult, error) {
	sessionToken, err := randomOpaqueToken()
	if err != nil {
		return nil, err
	}
	csrfToken, err := randomOpaqueToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sessionHash := sha256.Sum256([]byte(sessionToken))
	csrfHash := sha256.Sum256([]byte(csrfToken))
	session := &model.AdminSession{
		TokenHash: hex.EncodeToString(sessionHash[:]), CSRFTokenHash: hex.EncodeToString(csrfHash[:]),
		NewAPIUserID: entry.NewAPIUserID, IdentityVersion: entry.IdentityVersion,
		ExpiresAt: now.Add(s.adminSessionTTL), LastSeenAt: now,
	}
	if err := s.db.Create(session).Error; err != nil {
		return nil, err
	}
	return &EnterResult{
		Surface: "admin", AdminSessionToken: sessionToken,
		AdminCSRFToken: csrfToken, AdminSessionExpiresAt: session.ExpiresAt,
	}, nil
}

func (s *Service) ListSelectionOptions(ctx context.Context, sessionToken string) ([]SelectionOption, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return nil, domain.Forbidden("control session is required")
	}
	hash := sha256.Sum256([]byte(sessionToken))
	var session model.ControlSession
	if err := s.db.Where("token_hash = ?", hex.EncodeToString(hash[:])).First(&session).Error; err != nil {
		return nil, domain.Forbidden("control session is invalid")
	}
	if session.RevokedAt != nil || !time.Now().UTC().Before(session.ExpiresAt) {
		return nil, domain.Forbidden("control session is expired or revoked")
	}
	if session.SelectionState != model.ControlSessionStateSelectionPending && session.SelectionState != model.ControlSessionStateSelected {
		return nil, domain.Forbidden("control session state does not allow context selection")
	}
	if err := s.identityVerifier.Verify(ctx, session.NewAPIUserID, session.IdentityVersion); err != nil {
		return nil, err
	}
	var result []SelectionOption
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := database.ForUpdate(tx).First(&session, session.ID).Error; err != nil {
			return domain.Forbidden("control session is unavailable")
		}
		candidates, err := s.availableCandidates(tx, session.NewAPIUserID, session.IdentityVersion, true)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return domain.Forbidden("identity has no selectable workbench context")
		}
		result, err = s.issueSelectionOptions(tx, &session, candidates, time.Now().UTC())
		return err
	})
	return result, err
}

func (s *Service) SelectContext(ctx context.Context, sessionToken, selectionToken string) (*SelectionResult, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	selectionToken = strings.TrimSpace(selectionToken)
	if sessionToken == "" || selectionToken == "" {
		return nil, domain.Invalid("control session and selection token are required")
	}
	sessionHash := sha256.Sum256([]byte(sessionToken))
	var asserted model.ControlSession
	if err := s.db.Where("token_hash = ?", hex.EncodeToString(sessionHash[:])).First(&asserted).Error; err != nil {
		return nil, domain.Forbidden("control session is invalid")
	}
	if err := s.identityVerifier.Verify(ctx, asserted.NewAPIUserID, asserted.IdentityVersion); err != nil {
		return nil, err
	}
	nonceHash := sha256.Sum256([]byte(selectionToken))
	var result *SelectionResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var session model.ControlSession
		if err := database.ForUpdate(tx).Where("token_hash = ?", hex.EncodeToString(sessionHash[:])).First(&session).Error; err != nil {
			return domain.Forbidden("control session is invalid")
		}
		now := time.Now().UTC()
		if session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
			return domain.Forbidden("control session is expired or revoked")
		}
		if session.SelectionState != model.ControlSessionStateSelectionPending && session.SelectionState != model.ControlSessionStateSelected {
			return domain.Forbidden("control session state does not allow context selection")
		}
		if session.NewAPIUserID != asserted.NewAPIUserID || session.IdentityVersion != asserted.IdentityVersion {
			return domain.Forbidden("control session identity changed")
		}
		var nonce model.ContextSelectionNonce
		if err := database.ForUpdate(tx).Where("token_hash = ?", hex.EncodeToString(nonceHash[:])).First(&nonce).Error; err != nil {
			return domain.NotFound("selection token not found")
		}
		if nonce.ConsumedAt != nil {
			return domain.Conflict("selection token has already been consumed")
		}
		if !now.Before(nonce.ExpiresAt) {
			return domain.Forbidden("selection token has expired")
		}
		if nonce.ControlSessionID != session.ID || nonce.NewAPIUserID != session.NewAPIUserID || nonce.IdentityVersion != session.IdentityVersion {
			return domain.Forbidden("selection token is bound to a different session or identity")
		}
		var agentStoreDeployment *model.CustomerAgentDeployment
		if nonce.Purpose == "agent_store_launch" {
			var item model.AgentCatalogItem
			if err := database.ForUpdate(tx).Where("id = ?", nonce.AgentCatalogItemID).First(&item).Error; err != nil ||
				item.Status != model.AgentCatalogStatusPublished || item.CurrentVersionID == nil ||
				*item.CurrentVersionID != nonce.CatalogVersionID || item.RowVersion != nonce.CatalogRowVersion {
				return domain.Forbidden("Agent Store catalog selection is stale")
			}
			var deployment model.CustomerAgentDeployment
			if err := database.ForUpdate(tx).Where("id = ? AND item_id = ? AND customer_id = ?", nonce.AgentDeploymentID, item.ID, nonce.CustomerID).First(&deployment).Error; err != nil ||
				deployment.Status != model.AgentDeploymentStatusActive || !deployment.ExecutionEnabled ||
				deployment.RowVersion != nonce.DeploymentVersion || deployment.CustomerAppID != nonce.CustomerAppID ||
				deployment.VerifiedConfigVersionID == nil || *deployment.VerifiedConfigVersionID != nonce.AppConfigVersionID ||
				deployment.VerifiedAppAuthEpoch != nonce.AppAuthEpoch || deployment.ProviderAppMode < 1 || deployment.ProviderAppMode > 4 || deployment.RuntimeProfile == "" {
				return domain.Forbidden("Agent Store deployment selection is stale")
			}
			agentStoreDeployment = &deployment
		}
		var identity model.IdentityBinding
		if err := tx.First(&identity, nonce.IdentityBindingID).Error; err != nil {
			return domain.Forbidden("selected identity is unavailable")
		}
		resolved, err := s.resolveExact(tx, identity.PublicID, nonce.CustomerAppID, true)
		if err != nil {
			return err
		}
		if resolved.customer.ID != nonce.CustomerID || resolved.member.ID != nonce.CustomerMemberID ||
			resolved.config.ID != nonce.AppConfigVersionID || resolved.identity.NewAPIUserID != nonce.NewAPIUserID ||
			resolved.identity.AuthEpoch != nonce.IdentityAuthEpoch || resolved.member.AuthEpoch != nonce.MemberAuthEpoch ||
			resolved.app.AuthEpoch != nonce.AppAuthEpoch || resolved.identity.IdentityVersion != nonce.IdentityVersion ||
			(resolved.mode != "active" && resolved.mode != "readonly") {
			return domain.Forbidden("selection token context is stale")
		}
		if nonce.Purpose == "agent_store_launch" {
			var period model.PlanPeriod
			if err := tx.Where("customer_id = ? AND status = ? AND payment_status = ? AND start_at <= ? AND end_at > ?", nonce.CustomerID, model.PeriodStatusActive, model.PaymentStatusPaid, now, now).
				Order("start_at desc").First(&period).Error; err != nil {
				return domain.Forbidden("Agent Store paid plan is unavailable")
			}
			var entitlements []model.AgentCatalogEntitlement
			if err := tx.Where("deployment_id = ? AND status = ? AND valid_from <= ? AND (valid_until IS NULL OR valid_until > ?)", agentStoreDeployment.ID, model.AgentEntitlementStatusActive, now, now).Find(&entitlements).Error; err != nil {
				return err
			}
			authorized := false
			for _, entitlement := range entitlements {
				switch entitlement.SubjectType {
				case "customer":
					authorized = entitlement.SubjectRef == strconv.FormatUint(nonce.CustomerID, 10)
				case "user":
					authorized = entitlement.SubjectRef == strconv.FormatInt(nonce.NewAPIUserID, 10)
				case "role":
					authorized = entitlement.SubjectRef == resolved.member.Role
				case "plan":
					authorized = entitlement.SubjectRef == strconv.FormatUint(period.PlanVersionID, 10)
				}
				if authorized {
					break
				}
			}
			if !authorized {
				return domain.Forbidden("Agent Store entitlement is unavailable")
			}
		}
		consumed := tx.Model(&model.ContextSelectionNonce{}).Where("id = ? AND consumed_at IS NULL", nonce.ID).Update("consumed_at", now)
		if consumed.Error != nil {
			return consumed.Error
		}
		if consumed.RowsAffected != 1 {
			return domain.Conflict("selection token has already been consumed")
		}
		if err := tx.Model(&model.SSOTicket{}).Where("control_session_id = ? AND consumed_at IS NULL", session.ID).Updates(map[string]any{
			"consumed_at": now, "consumed_by_service": "control-context-switch",
		}).Error; err != nil {
			return err
		}
		before := session
		session.SelectionState = model.ControlSessionStateSelected
		applyResolvedSession(&session, resolved)
		session.LastSeenAt = now
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		result, err = s.createSSOTicket(tx, &session, resolved, now)
		if err != nil {
			return err
		}
		return support.Audit(tx, &resolved.customer.ID, fmt.Sprintf("user:%d", session.NewAPIUserID), "context.selection", "control_session", support.ResourceID(session.ID), &before, &session, "", "")
	})
	return result, err
}

func (s *Service) availableCandidates(db *gorm.DB, userID int64, identityVersion string, allowProvisioning bool) ([]resolvedAccess, error) {
	statuses := []string{model.IdentityStatusActive}
	if allowProvisioning {
		statuses = append(statuses, model.IdentityStatusProvisioning)
	}
	var identities []model.IdentityBinding
	if err := db.Where("new_api_user_id = ? AND status IN ?", userID, statuses).Order("customer_id asc, id asc").Find(&identities).Error; err != nil {
		return nil, err
	}
	result := make([]resolvedAccess, 0)
	for index := range identities {
		if identities[index].IdentityVersion != identityVersion {
			continue
		}
		var apps []model.CustomerApp
		if err := db.Where("customer_id = ? AND (slot = ? OR slot LIKE ?)", identities[index].CustomerID, "primary", "app:%").Order("id asc").Find(&apps).Error; err != nil {
			return nil, err
		}
		sort.SliceStable(apps, func(left, right int) bool {
			if (apps[left].Slot == "primary") != (apps[right].Slot == "primary") {
				return apps[left].Slot == "primary"
			}
			return apps[left].ID < apps[right].ID
		})
		for appIndex := range apps {
			resolved, err := s.resolveExact(db, identities[index].PublicID, apps[appIndex].ID, allowProvisioning)
			if err != nil {
				continue
			}
			if resolved.mode == "active" || resolved.mode == "readonly" {
				result = append(result, *resolved)
			}
		}
	}
	return result, nil
}

func (s *Service) issueSelectionOptions(tx *gorm.DB, session *model.ControlSession, candidates []resolvedAccess, now time.Time) ([]SelectionOption, error) {
	result := make([]SelectionOption, 0, len(candidates))
	for index := range candidates {
		token, err := randomOpaqueToken()
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256([]byte(token))
		expiresAt := now.Add(SelectionNonceTTL)
		if expiresAt.After(session.ExpiresAt) {
			expiresAt = session.ExpiresAt
		}
		candidate := &candidates[index]
		nonce := model.ContextSelectionNonce{
			TokenHash: hex.EncodeToString(hash[:]), ControlSessionID: session.ID,
			NewAPIUserID: candidate.identity.NewAPIUserID, IdentityBindingID: candidate.identity.ID,
			CustomerMemberID: candidate.member.ID, CustomerID: candidate.customer.ID,
			CustomerAppID: candidate.app.ID, AppConfigVersionID: candidate.config.ID,
			IdentityVersion:   candidate.identity.IdentityVersion,
			IdentityAuthEpoch: candidate.identity.AuthEpoch, MemberAuthEpoch: candidate.member.AuthEpoch,
			AppAuthEpoch: candidate.app.AuthEpoch, ExpiresAt: expiresAt,
		}
		if err := tx.Create(&nonce).Error; err != nil {
			return nil, err
		}
		result = append(result, SelectionOption{
			SelectionToken: token, ExpiresAt: expiresAt,
			CustomerCode: candidate.customer.CustomerCode, CustomerDisplayName: candidate.customer.DisplayName,
			Role: candidate.member.Role, AppSelector: candidate.app.Selector, AppAlias: candidate.app.Alias,
			AppDisplayName: candidate.app.DisplayName, AppStatus: candidate.app.Status,
			IsDefault: candidate.app.Slot == "primary", AccessMode: candidate.mode,
		})
	}
	return result, nil
}

func (s *Service) createSSOTicket(tx *gorm.DB, session *model.ControlSession, resolved *resolvedAccess, now time.Time) (*SelectionResult, error) {
	ssoToken, err := randomOpaqueToken()
	if err != nil {
		return nil, err
	}
	browserBinding, err := randomOpaqueToken()
	if err != nil {
		return nil, err
	}
	ssoHash := sha256.Sum256([]byte(ssoToken))
	browserBindingHash := sha256.Sum256([]byte(browserBinding))
	expiresAt := now.Add(s.ssoTicketTTL)
	if expiresAt.After(session.ExpiresAt) {
		expiresAt = session.ExpiresAt
	}
	ticket := &model.SSOTicket{
		TokenHash: hex.EncodeToString(ssoHash[:]), BrowserBindingHash: hex.EncodeToString(browserBindingHash[:]),
		ControlSessionID: session.ID, IdentityBindingID: resolved.identity.ID,
		CustomerID: resolved.customer.ID, NewAPIUserID: resolved.identity.NewAPIUserID,
		CustomerAppID: resolved.app.ID, AppConfigVersionID: resolved.config.ID,
		AccessMode: resolved.mode, AuthEpoch: resolved.epoch, IdentityVersion: resolved.identity.IdentityVersion,
		ExpiresAt: expiresAt,
	}
	if err := tx.Create(ticket).Error; err != nil {
		return nil, err
	}
	return &SelectionResult{ADPSSOTicket: ssoToken, ADPSSOTicketExpiresAt: ticket.ExpiresAt, SSOBrowserBinding: browserBinding}, nil
}

func applyResolvedSession(session *model.ControlSession, resolved *resolvedAccess) {
	session.IdentityBindingID = resolved.identity.ID
	session.CustomerID = resolved.customer.ID
	session.NewAPIUserID = resolved.identity.NewAPIUserID
	session.CustomerAppID = resolved.app.ID
	session.AppConfigVersionID = resolved.config.ID
	session.AccessMode = resolved.mode
	session.AuthEpoch = resolved.epoch
	session.IdentityVersion = resolved.identity.IdentityVersion
}

func (s *Service) AuthorizeAdminSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (int64, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	csrfToken = strings.TrimSpace(csrfToken)
	if sessionToken == "" || (requireCSRF && csrfToken == "") {
		return 0, domain.Forbidden("admin session and CSRF token are required")
	}
	sessionHash := sha256.Sum256([]byte(sessionToken))
	var session model.AdminSession
	if err := s.db.Where("token_hash = ?", hex.EncodeToString(sessionHash[:])).First(&session).Error; err != nil {
		return 0, domain.Forbidden("admin session is invalid")
	}
	now := time.Now().UTC()
	if session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
		return 0, domain.Forbidden("admin session is expired or revoked")
	}
	if requireCSRF {
		csrfHash := sha256.Sum256([]byte(csrfToken))
		expected, err := hex.DecodeString(session.CSRFTokenHash)
		if err != nil || subtle.ConstantTimeCompare(expected, csrfHash[:]) != 1 {
			return 0, domain.Forbidden("admin CSRF token is invalid")
		}
	}
	if s.identityVerifier == nil {
		return 0, fmt.Errorf("new-api admin identity verifier is unavailable")
	}
	if err := s.identityVerifier.VerifyAdmin(ctx, session.NewAPIUserID, session.IdentityVersion); err != nil {
		return 0, err
	}
	return session.NewAPIUserID, nil
}

func (s *Service) AuthorizeRecentAdminSession(ctx context.Context, sessionToken string, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, domain.Forbidden("recent administrator authentication is required")
	}
	userID, err := s.AuthorizeAdminSession(ctx, sessionToken, "", false)
	if err != nil {
		return 0, err
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(sessionToken)))
	var session model.AdminSession
	if err := s.db.Where("token_hash = ?", hex.EncodeToString(digest[:])).First(&session).Error; err != nil {
		return 0, domain.Forbidden("admin session is invalid")
	}
	if time.Since(session.CreatedAt.UTC()) > maxAge || time.Now().UTC().Before(session.CreatedAt.UTC().Add(-time.Minute)) {
		return 0, domain.Forbidden("recent administrator authentication is required")
	}
	return userID, nil
}

func (s *Service) ConsumeTicket(command ConsumeTicketCommand) (*IdentityContext, error) {
	command.Ticket = strings.TrimSpace(command.Ticket)
	command.BrowserBinding = strings.TrimSpace(command.BrowserBinding)
	command.ConsumerService = strings.TrimSpace(command.ConsumerService)
	if command.Ticket == "" || command.BrowserBinding == "" || command.ConsumerService == "" {
		return nil, domain.Invalid("ticket, browser binding, and consumer service are required")
	}
	hash := sha256.Sum256([]byte(command.Ticket))
	var result IdentityContext
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var ticket model.SSOTicket
		if err := database.ForUpdate(tx).Where("token_hash = ?", hex.EncodeToString(hash[:])).First(&ticket).Error; err != nil {
			return domain.NotFound("SSO ticket not found")
		}
		now := time.Now().UTC()
		if ticket.ConsumedAt != nil {
			return domain.Conflict("SSO ticket has already been consumed")
		}
		if !now.Before(ticket.ExpiresAt) {
			return domain.Forbidden("SSO ticket has expired")
		}
		bindingHash := sha256.Sum256([]byte(command.BrowserBinding))
		expectedBindingHash, err := hex.DecodeString(ticket.BrowserBindingHash)
		if err != nil || len(expectedBindingHash) != sha256.Size || subtle.ConstantTimeCompare(expectedBindingHash, bindingHash[:]) != 1 {
			return domain.Forbidden("SSO browser binding is invalid")
		}
		var binding model.IdentityBinding
		if err := tx.First(&binding, ticket.IdentityBindingID).Error; err != nil {
			return domain.NotFound("identity binding not found")
		}
		resolved, err := s.resolveExact(tx, binding.PublicID, ticket.CustomerAppID, true)
		if err != nil {
			return err
		}
		if resolved.epoch != ticket.AuthEpoch || resolved.identity.IdentityVersion != ticket.IdentityVersion || resolved.customer.ID != ticket.CustomerID || resolved.app.ID != ticket.CustomerAppID || resolved.config.ID != ticket.AppConfigVersionID || resolved.mode != ticket.AccessMode {
			return domain.Forbidden("SSO ticket context is stale")
		}
		if ticket.ControlSessionID > 0 {
			var session model.ControlSession
			if err := tx.First(&session, ticket.ControlSessionID).Error; err != nil || session.SelectionState != model.ControlSessionStateSelected || session.RevokedAt != nil ||
				session.NewAPIUserID != ticket.NewAPIUserID || session.IdentityBindingID != ticket.IdentityBindingID ||
				session.CustomerID != ticket.CustomerID || session.CustomerAppID != ticket.CustomerAppID || session.AppConfigVersionID != ticket.AppConfigVersionID ||
				session.AuthEpoch != ticket.AuthEpoch || session.IdentityVersion != ticket.IdentityVersion || !now.Before(session.ExpiresAt) {
				return domain.Forbidden("SSO ticket control session is stale")
			}
		}
		consumed := tx.Model(&model.SSOTicket{}).
			Where("id = ? AND consumed_at IS NULL", ticket.ID).
			Updates(map[string]any{
				"consumed_at": now, "consumed_by_service": command.ConsumerService,
			})
		if consumed.Error != nil {
			return consumed.Error
		}
		if consumed.RowsAffected != 1 {
			return domain.Conflict("SSO ticket has already been consumed")
		}
		result = identityContext(resolved)
		result.ExpiresAt = ticket.ExpiresAt.Unix()
		return nil
	})
	return &result, err
}

func (s *Service) Authorize(ctx context.Context, command AuthzCommand) (*IdentityContext, error) {
	command.BindingID = strings.TrimSpace(command.BindingID)
	command.CanonicalSubject = strings.TrimSpace(command.CanonicalSubject)
	command.Method = strings.ToUpper(strings.TrimSpace(command.Method))
	command.ResourcePath = strings.TrimSpace(command.ResourcePath)
	if command.BindingID == "" || command.CanonicalSubject == "" || command.AuthEpoch <= 0 || command.Method == "" || !validResourcePath(command.ResourcePath) {
		return nil, domain.Invalid("binding_id, canonical_subject, auth_epoch, method, and a valid resource_path are required")
	}
	hasContextTuple := command.CustomerID > 0 || command.AppProfileID > 0 || command.ConfigVersion > 0
	if hasContextTuple && (command.CustomerID == 0 || command.AppProfileID == 0 || command.ConfigVersion <= 0) {
		return nil, domain.Invalid("customer_id, app_profile_id, and config_version must be provided together")
	}
	var resolved *resolvedAccess
	var err error
	if command.AppProfileID > 0 {
		resolved, err = s.resolveExact(s.db, command.BindingID, command.AppProfileID, false)
	} else {
		resolved, err = s.resolve(s.db, command.BindingID, false)
	}
	if err != nil {
		return nil, err
	}
	if resolved.identity.CanonicalSubject != command.CanonicalSubject || resolved.epoch != command.AuthEpoch ||
		(command.CustomerID > 0 && resolved.customer.ID != command.CustomerID) ||
		(command.ConfigVersion > 0 && resolved.config.ConfigVersion != command.ConfigVersion) {
		return nil, domain.Forbidden("identity tuple or auth_epoch is stale")
	}
	if s.identityVerifier == nil {
		return nil, fmt.Errorf("new-api identity verifier is unavailable")
	}
	if err := s.identityVerifier.Verify(ctx, resolved.identity.NewAPIUserID, resolved.identity.IdentityVersion); err != nil {
		return nil, err
	}
	result := identityContext(resolved)
	result.Allowed = resolved.mode == "active" || (resolved.mode == "readonly" && readOnlyMethod(command.Method))
	return &result, nil
}

func (s *Service) AppContext(ctx context.Context, command AppContextCommand) (*AppContext, error) {
	command.BindingID = strings.TrimSpace(command.BindingID)
	command.CanonicalSubject = strings.TrimSpace(command.CanonicalSubject)
	command.Purpose = strings.ToLower(strings.TrimSpace(command.Purpose))
	if command.BindingID == "" || command.CanonicalSubject == "" || command.AuthEpoch <= 0 ||
		command.RequestedAppProfileID == 0 || command.RequestedConfigVersion <= 0 || command.Purpose == "" {
		return nil, domain.Invalid("binding_id, canonical_subject, auth_epoch, requested_app_profile_id, requested_config_version, and purpose are required")
	}
	if command.Purpose != "interactive" && command.Purpose != "scheduled_task" && command.Purpose != "integration_refresh" && command.Purpose != "history_read" {
		return nil, domain.Invalid("unsupported App context purpose")
	}
	resolveProfileID := command.RequestedAppProfileID
	if command.Purpose == "history_read" {
		if command.CurrentAppProfileID == 0 || command.CurrentConfigVersion <= 0 {
			return nil, domain.Invalid("current_app_profile_id and current_config_version are required for history_read")
		}
		resolveProfileID = command.CurrentAppProfileID
	}
	resolved, err := s.resolveExact(s.db, command.BindingID, resolveProfileID, false)
	if err != nil {
		return nil, err
	}
	expectedCurrentVersion := command.RequestedConfigVersion
	if command.Purpose == "history_read" {
		expectedCurrentVersion = command.CurrentConfigVersion
	}
	if resolved.identity.CanonicalSubject != command.CanonicalSubject || resolved.epoch != command.AuthEpoch ||
		resolved.config.ConfigVersion != expectedCurrentVersion || (resolved.mode != "active" && resolved.mode != "readonly") {
		return nil, domain.Forbidden("App context is unavailable or auth_epoch is stale")
	}
	if command.Purpose != "interactive" && command.Purpose != "history_read" && resolved.mode != "active" {
		return nil, domain.Forbidden("offline App context requires active access")
	}
	appRecord := resolved.app
	configRecord := resolved.config
	if command.Purpose == "history_read" {
		var lineage model.AppMigrationLineage
		if err := s.db.Where(
			"customer_id = ? AND source_customer_app_id = ? AND source_config_version = ? AND target_customer_app_id = ? AND target_config_version = ?",
			resolved.customer.ID, command.RequestedAppProfileID, command.RequestedConfigVersion,
			resolved.app.ID, resolved.config.ConfigVersion,
		).First(&lineage).Error; err != nil {
			return nil, domain.NotFound("historical App context not found")
		}
		appRecord = model.CustomerApp{}
		if err := s.db.First(&appRecord, lineage.SourceCustomerAppID).Error; err != nil ||
			appRecord.CustomerID != resolved.customer.ID || appRecord.Status != model.AppStatusArchived ||
			appRecord.AppID != lineage.SourceApplicationID {
			return nil, domain.NotFound("historical App context not found")
		}
		configRecord = model.AppConfigVersion{}
		if err := s.db.First(&configRecord, lineage.SourceAppConfigVersionID).Error; err != nil ||
			configRecord.CustomerAppID != appRecord.ID || configRecord.ConfigVersion != lineage.SourceConfigVersion {
			return nil, domain.NotFound("historical App context not found")
		}
	}
	requiredCapability := ""
	if command.Purpose == "scheduled_task" {
		requiredCapability = productpolicy.CapabilityScheduledTasks
	} else if command.Purpose == "integration_refresh" {
		requiredCapability = productpolicy.CapabilityOAuth
	}
	if requiredCapability != "" {
		allowed := false
		for _, capability := range resolved.capabilities {
			if capability == requiredCapability {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, domain.Forbidden("App context purpose is not enabled by the effective plan")
		}
	}
	if s.identityVerifier == nil {
		return nil, fmt.Errorf("new-api identity verifier is unavailable")
	}
	if err := s.identityVerifier.VerifyFresh(ctx, resolved.identity.NewAPIUserID, resolved.identity.IdentityVersion); err != nil {
		return nil, err
	}
	if configRecord.CredentialProfileID == nil {
		return nil, fmt.Errorf("verified App config has no credential profile")
	}
	var credential model.CredentialProfile
	if err := s.db.First(&credential, *configRecord.CredentialProfileID).Error; err != nil {
		return nil, fmt.Errorf("load credential profile: %w", err)
	}
	if !credentialpkg.RuntimeEligible(credential.Status) || credential.ProviderEnvironment != appRecord.ProviderEnvironment ||
		!credentialpkg.AvailableToCustomer(credential, resolved.customer.ID) {
		return nil, domain.Forbidden("credential profile is inactive or mismatched")
	}
	appKey, err := secrets.ResolveAppKey(s.secretResolver, configRecord.AppKeySecretRef, configRecord.AppKeyFingerprint, configRecord.AppKeyFingerprintVersion)
	if err != nil {
		return nil, domain.Unavailable("provider AppKey integrity verification failed")
	}
	pair, err := secrets.ResolveCredentialPair(s.secretResolver, credential.SecretIDRef, credential.SecretKeyRef, credential.Fingerprint, credential.FingerprintVersion)
	if err != nil {
		return nil, domain.Unavailable("provider credential integrity verification failed")
	}
	serviceVendor, supported := providerServiceVendor(appRecord.ProviderEnvironment)
	if !supported {
		return nil, domain.Forbidden("App provider environment is unsupported")
	}
	var providerAppMode *int
	var runtimeProfile *string
	var executionEnabled *bool
	if command.Purpose != "history_read" {
		var deployment model.CustomerAgentDeployment
		query := s.db.Where("customer_app_id = ?", appRecord.ID).First(&deployment)
		if query.Error == nil {
			var item model.AgentCatalogItem
			if err := s.db.First(&item, "id = ?", deployment.ItemID).Error; err != nil ||
				item.Status != model.AgentCatalogStatusPublished || deployment.Status != model.AgentDeploymentStatusActive ||
				deployment.VerifiedConfigVersionID == nil || *deployment.VerifiedConfigVersionID != configRecord.ID ||
				deployment.VerifiedConfigVersion != configRecord.ConfigVersion || deployment.VerifiedAppAuthEpoch != appRecord.AuthEpoch ||
				deployment.ProviderAppMode < 1 || deployment.ProviderAppMode > 4 || !validRuntimeProfile(deployment.RuntimeProfile, deployment.ProviderAppMode, deployment.DynamicAgentConfig) {
				return nil, domain.Forbidden("Agent Store deployment is stale or unavailable")
			}
			if deployment.RuntimeProfile == "claw_dynamic_v2" && strings.TrimSpace(configRecord.TemplateAgentID) == "" {
				return nil, domain.Forbidden("dynamic Claw deployment has no verified template Agent")
			}
			providerAppMode = &deployment.ProviderAppMode
			runtimeProfile = &deployment.RuntimeProfile
			executionEnabled = &deployment.ExecutionEnabled
			if !deployment.ExecutionEnabled && command.Purpose != "history_read" {
				return nil, domain.Forbidden("Agent Store runtime profile is not enabled")
			}
		} else if query.Error != gorm.ErrRecordNotFound {
			return nil, query.Error
		}
	}
	return &AppContext{
		CustomerID:    resolved.customer.ID,
		ApplicationID: appRecord.AppID, AppProfileID: appRecord.ID,
		ConfigVersion: configRecord.ConfigVersion, AuthEpoch: resolved.epoch,
		Vendor: "Tencent", ServiceVendor: serviceVendor,
		ProviderEnvironment: appRecord.ProviderEnvironment, Region: configRecord.Region,
		AppID: appRecord.AppID, AppKey: appKey, SpaceID: configRecord.SpaceID,
		TemplateAgentID: configRecord.TemplateAgentID, SecretID: pair.SecretID, SecretKey: pair.SecretKey,
		ProviderAppMode: providerAppMode, RuntimeProfile: runtimeProfile, ExecutionEnabled: executionEnabled,
		Capabilities: resolved.capabilities, Limits: resolved.limits, ExpiresAt: time.Now().UTC().Add(s.contextTTL).Unix(),
	}, nil
}

func validRuntimeProfile(profile string, mode int, dynamic bool) bool {
	switch mode {
	case 1:
		return profile == "standard_v2" && !dynamic
	case 2:
		return profile == "multi_agent_v2" && !dynamic
	case 3:
		return profile == "workflow_v2" && !dynamic
	case 4:
		return (!dynamic && profile == "claw_static_v2") || (dynamic && profile == "claw_dynamic_v2")
	default:
		return false
	}
}

func (s *Service) ConfigForSession(ctx context.Context, token string) (*BrowserConfig, error) {
	resolved, _, err := s.resolveSession(token, false)
	if err != nil {
		return nil, err
	}
	if s.identityVerifier == nil {
		return nil, fmt.Errorf("new-api identity verifier is unavailable")
	}
	if err := s.identityVerifier.Verify(ctx, resolved.identity.NewAPIUserID, resolved.identity.IdentityVersion); err != nil {
		return nil, err
	}
	return &BrowserConfig{
		CustomerCode: resolved.customer.CustomerCode, CustomerDisplayName: resolved.customer.DisplayName,
		Role: resolved.member.Role, AccessMode: resolved.mode,
		AppDisplayName: resolved.app.DisplayName, AppStatus: resolved.app.Status,
		AppSelector: resolved.app.Selector, AppAlias: resolved.app.Alias, IsDefault: resolved.app.Slot == "primary",
		Capabilities: resolved.capabilities, Limits: resolved.limits,
	}, nil
}

func (s *Service) PlanForSession(ctx context.Context, token string) (*BrowserPlan, error) {
	resolved, _, err := s.resolveSession(token, false)
	if err != nil {
		return nil, err
	}
	if s.identityVerifier == nil {
		return nil, fmt.Errorf("new-api identity verifier is unavailable")
	}
	if err := s.identityVerifier.Verify(ctx, resolved.identity.NewAPIUserID, resolved.identity.IdentityVersion); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var period model.PlanPeriod
	query := s.db.Where("customer_id = ? AND payment_status = ? AND start_at <= ? AND end_at > ?", resolved.customer.ID, model.PaymentStatusPaid, now, now).
		Order("start_at desc").First(&period)
	if query.Error != nil {
		return nil, domain.NotFound("current plan period not found")
	}
	var snapshot map[string]any
	if err := jsonx.Unmarshal([]byte(period.SnapshotJSON), &snapshot); err != nil {
		return nil, err
	}
	return &BrowserPlan{
		PeriodID: period.ID, StartAt: period.StartAt, EndAt: period.EndAt,
		Status: period.Status, PaymentStatus: period.PaymentStatus,
		AmountCNY: period.AmountCNY, Snapshot: snapshot,
	}, nil
}

// AuthorizeSSOPreflight validates the short-lived selected control session
// before Caddy forwards the one-time SSO ticket to ADP. Provisioning identities
// are allowed only on this bootstrap boundary; ordinary browser config and plan
// reads continue to require a confirmed active identity.
func (s *Service) AuthorizeSSOPreflight(ctx context.Context, token string) error {
	resolved, _, err := s.resolveSession(token, true)
	if err != nil {
		return err
	}
	if s.identityVerifier == nil {
		return fmt.Errorf("new-api identity verifier is unavailable")
	}
	return s.identityVerifier.Verify(ctx, resolved.identity.NewAPIUserID, resolved.identity.IdentityVersion)
}

func (s *Service) resolveSession(token string, allowProvisioning bool) (*resolvedAccess, *model.ControlSession, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil, domain.Forbidden("control session is required")
	}
	hash := sha256.Sum256([]byte(token))
	var session model.ControlSession
	if err := s.db.Where("token_hash = ?", hex.EncodeToString(hash[:])).First(&session).Error; err != nil {
		return nil, nil, domain.Forbidden("control session is invalid")
	}
	now := time.Now().UTC()
	if session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
		return nil, nil, domain.Forbidden("control session is expired or revoked")
	}
	if session.SelectionState != model.ControlSessionStateSelected {
		return nil, nil, domain.Forbidden("control session requires a workbench context selection")
	}
	var binding model.IdentityBinding
	if err := s.db.First(&binding, session.IdentityBindingID).Error; err != nil {
		return nil, nil, domain.Forbidden("control session identity is unavailable")
	}
	resolved, err := s.resolveExact(s.db, binding.PublicID, session.CustomerAppID, allowProvisioning)
	if err != nil {
		return nil, nil, err
	}
	if resolved.epoch != session.AuthEpoch || resolved.identity.IdentityVersion != session.IdentityVersion ||
		resolved.customer.ID != session.CustomerID || resolved.app.ID != session.CustomerAppID ||
		resolved.config.ID != session.AppConfigVersionID || resolved.mode != session.AccessMode {
		return nil, nil, domain.Forbidden("control session context is stale")
	}
	return resolved, &session, nil
}

func (s *Service) resolve(db *gorm.DB, bindingID string, allowProvisioning bool) (*resolvedAccess, error) {
	var identity model.IdentityBinding
	if err := db.Where("public_id = ?", bindingID).First(&identity).Error; err != nil {
		return nil, domain.NotFound("identity binding not found")
	}
	var appIDs []uint64
	if err := db.Model(&model.CustomerApp{}).Where(
		"customer_id = ? AND (slot = ? OR slot LIKE ?) AND current_config_version_id IS NOT NULL",
		identity.CustomerID, "primary", "app:%",
	).Order("id asc").Pluck("id", &appIDs).Error; err != nil {
		return nil, err
	}
	if len(appIDs) == 0 {
		return nil, domain.NotFound("customer App not found")
	}
	if len(appIDs) > 1 {
		return nil, domain.Forbidden("App context is ambiguous; app_profile_id and config_version are required")
	}
	return s.resolveExact(db, bindingID, appIDs[0], allowProvisioning)
}

func (s *Service) resolveExact(db *gorm.DB, bindingID string, appProfileID uint64, allowProvisioning bool) (*resolvedAccess, error) {
	var result resolvedAccess
	if err := db.Where("public_id = ?", bindingID).First(&result.identity).Error; err != nil {
		return nil, domain.NotFound("identity binding not found")
	}
	if result.identity.Status != model.IdentityStatusActive && !(allowProvisioning && result.identity.Status == model.IdentityStatusProvisioning) {
		return nil, domain.Forbidden("identity binding is not active")
	}
	if err := db.Where("customer_id = ? AND new_api_user_id = ?", result.identity.CustomerID, result.identity.NewAPIUserID).First(&result.member).Error; err != nil {
		return nil, domain.NotFound("customer membership not found")
	}
	if !model.ActiveMembership(result.member) {
		return nil, domain.Forbidden("customer membership is not active")
	}
	if err := db.First(&result.customer, result.identity.CustomerID).Error; err != nil {
		return nil, domain.NotFound("customer not found")
	}
	if result.customer.Status != model.CustomerStatusActive {
		return nil, domain.Forbidden("customer is not active")
	}
	if err := db.Where("id = ? AND customer_id = ?", appProfileID, result.customer.ID).First(&result.app).Error; err != nil {
		return nil, domain.NotFound("customer App not found")
	}
	if result.app.Slot != "primary" && !strings.HasPrefix(result.app.Slot, "app:") {
		return nil, domain.Forbidden("customer App is not selectable")
	}
	if result.app.CurrentConfigVersionID == nil {
		return nil, domain.Forbidden("customer App has no verified configuration")
	}
	if err := db.First(&result.config, *result.app.CurrentConfigVersionID).Error; err != nil {
		return nil, domain.NotFound("App config version not found")
	}
	now := time.Now().UTC()
	var policyPeriod model.PlanPeriod
	currentPeriod := db.Where(
		"customer_id = ? AND status = ? AND payment_status = ? AND start_at <= ? AND end_at > ?",
		result.customer.ID, model.PeriodStatusActive, model.PaymentStatusPaid, now, now,
	).Order("start_at desc").First(&policyPeriod)
	hasCurrentPeriod := currentPeriod.Error == nil
	if currentPeriod.Error != nil && currentPeriod.Error != gorm.ErrRecordNotFound {
		return nil, currentPeriod.Error
	}
	if !hasCurrentPeriod {
		latestPaidPeriod := db.Where("customer_id = ? AND payment_status = ?", result.customer.ID, model.PaymentStatusPaid).
			Order("end_at desc").First(&policyPeriod)
		if latestPaidPeriod.Error != nil && latestPaidPeriod.Error != gorm.ErrRecordNotFound {
			return nil, latestPaidPeriod.Error
		}
		if latestPaidPeriod.Error == nil {
			result.period = &policyPeriod
		}
	} else {
		result.period = &policyPeriod
	}
	result.mode = effectiveAccessMode(result.app.Status, result.member.Role, hasCurrentPeriod)
	if result.period != nil && result.mode != "disabled" {
		var appCapabilities []string
		var appLimits productpolicy.Limits
		var planSnapshot planPolicySnapshot
		if err := jsonx.Unmarshal([]byte(result.config.CapabilitiesJSON), &appCapabilities); err != nil {
			return nil, err
		}
		if err := jsonx.Unmarshal([]byte(result.config.LimitsJSON), &appLimits); err != nil {
			return nil, err
		}
		if err := jsonx.Unmarshal([]byte(result.period.SnapshotJSON), &planSnapshot); err != nil {
			return nil, err
		}
		if err := productpolicy.ValidateLimits(appLimits); err != nil {
			return nil, err
		}
		if err := productpolicy.ValidateLimits(planSnapshot.Limits); err != nil {
			return nil, err
		}
		result.capabilities = productpolicy.IntersectCapabilities(appCapabilities, planSnapshot.Capabilities)
		result.limits = productpolicy.MinimumLimits(appLimits, planSnapshot.Limits)
	}
	result.epoch = result.identity.AuthEpoch + result.member.AuthEpoch + result.app.AuthEpoch
	return &result, nil
}

func effectiveAccessMode(appStatus, memberRole string, hasCurrentPeriod bool) string {
	if appStatus == model.AppStatusActive && hasCurrentPeriod {
		if memberRole == "viewer" {
			return "readonly"
		}
		return "active"
	}
	if appStatus == model.AppStatusSuspended || appStatus == model.AppStatusActive {
		return "readonly"
	}
	return "disabled"
}

func identityContext(resolved *resolvedAccess) IdentityContext {
	return IdentityContext{
		BindingID: resolved.identity.PublicID, CanonicalSubject: resolved.identity.CanonicalSubject,
		CustomerID: resolved.customer.ID, NewAPIUserID: resolved.identity.NewAPIUserID,
		AuthEpoch: resolved.epoch, DisplayName: resolved.customer.DisplayName,
		ApplicationID: resolved.app.AppID, AppProfileID: resolved.app.ID,
		ConfigVersion: resolved.config.ConfigVersion, Role: resolved.member.Role,
		AccessMode: resolved.mode, Allowed: resolved.mode != "disabled",
	}
}

func validResourcePath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.Contains(value, "..") && len(value) <= 2048
}

func readOnlyMethod(method string) bool {
	return method == "GET" || method == "HEAD" || method == "OPTIONS"
}

func randomOpaqueToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate opaque credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func providerServiceVendor(environment string) (string, bool) {
	switch environment {
	case model.ProviderChinaTencentCloud:
		return "ChinaTencentCloud", true
	case model.ProviderChinaTencentADP:
		return "ChinaTencentADP", true
	default:
		return "", false
	}
}
