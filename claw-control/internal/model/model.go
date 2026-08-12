package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	CustomerStatusActive    = "active"
	CustomerStatusSuspended = "suspended"
	CustomerStatusDisabled  = "disabled"
	CustomerStatusArchived  = "archived"

	MemberStatusActive   = "active"
	MemberStatusDisabled = "disabled"

	IdentityStatusProvisioning = "provisioning"
	IdentityStatusActive       = "active"
	IdentityStatusDisabled     = "disabled"

	CredentialStatusActive   = "active"
	CredentialStatusStaged   = "staged"
	CredentialStatusRetiring = "retiring"
	CredentialStatusDisabled = "disabled"
	CredentialStatusRetired  = "retired"

	AppStatusDraft     = "draft"
	AppStatusVerified  = "verified"
	AppStatusActive    = "active"
	AppStatusSuspended = "suspended"
	AppStatusDisabled  = "disabled"
	AppStatusArchived  = "archived"

	AppConfigStatusDraft      = "draft"
	AppConfigStatusVerified   = "verified"
	AppConfigStatusInvalid    = "invalid"
	AppConfigStatusSuperseded = "superseded"

	PlanStatusPublished = "published"
	PlanStatusRetired   = "retired"

	PeriodStatusPendingPayment = "pending_payment"
	PeriodStatusScheduled      = "scheduled"
	PeriodStatusActive         = "active"
	PeriodStatusPastDue        = "past_due"
	PeriodStatusExpired        = "expired"
	PeriodStatusCanceled       = "canceled"

	PaymentModeOfflineManual = "offline_manual"
	PaymentStatusPending     = "pending"
	PaymentStatusPaid        = "paid"

	InvoiceStatusPaid = "paid"
	InvoiceStatusVoid = "void"

	UsageAuditStatusDraft      = "draft"
	UsageAuditStatusLocked     = "locked"
	UsageAuditStatusSuperseded = "superseded"
	EvidenceStatusActive       = "active"

	BillingImportStatusPending      = "pending"
	BillingImportStatusRunning      = "running"
	BillingImportStatusDraftCreated = "draft_created"
	BillingImportStatusFailed       = "failed"

	AllocationAppExact            = "app_exact"
	AllocationEstimatedAllocation = "estimated_allocation"
	AllocationAccountOnly         = "account_only"
	AllocationUnverified          = "unverified"

	OutboxStatusPending   = "pending"
	OutboxStatusDelivered = "delivered"

	ResourceBindingStatusActive = "active"

	NotificationStatusUnread = "unread"
	NotificationStatusRead   = "read"

	RetentionPolicyStatusActive = "active"
	RetentionRunStatusPlanned   = "planned"
	RetentionRunStatusPending   = "pending_external"
	RetentionRunStatusInvalid   = "invalidated"
	RetentionRunStatusCompleted = "completed"
	RetentionDeliveryPending    = "pending"
	RetentionDeliveryBlocked    = "blocked"
	RetentionDeliveryDelivered  = "delivered"

	ApprovalStatusPending  = "pending"
	ApprovalStatusApproved = "approved"
	ApprovalStatusRejected = "rejected"
	ApprovalStatusExecuted = "executed"
	ApprovalStatusExpired  = "expired"

	ApprovalActionAppDisable          = "app_disable"
	ApprovalActionAppIDMigration      = "app_id_migration"
	ApprovalActionAppCredentialChange = "app_credential_change"
	ApprovalActionCredentialRotate    = "credential_rotation"
	ApprovalActionCredentialRollback  = "credential_rollback"

	AppMigrationJobStatusPending    = "pending"
	AppMigrationJobStatusRunning    = "running"
	AppMigrationJobStatusReady      = "ready"
	AppMigrationJobStatusFailed     = "failed"
	AppMigrationJobStatusSuperseded = "superseded"

	AppMigrationMemberStatusPending   = "pending"
	AppMigrationMemberStatusRunning   = "running"
	AppMigrationMemberStatusSucceeded = "succeeded"
	AppMigrationMemberStatusFailed    = "failed"

	ControlSessionStateSelectionPending = "selection_pending"
	ControlSessionStateSelected         = "selected"

	AgentCatalogStatusDraft       = "draft"
	AgentCatalogStatusVerifying   = "verifying"
	AgentCatalogStatusRejected    = "rejected"
	AgentCatalogStatusVerified    = "verified"
	AgentCatalogStatusPublished   = "published"
	AgentCatalogStatusUnpublished = "unpublished"
	AgentCatalogStatusDisabled    = "disabled"
	AgentCatalogStatusArchived    = "archived"

	AgentDeploymentStatusDraft     = "draft"
	AgentDeploymentStatusVerified  = "verified"
	AgentDeploymentStatusActive    = "active"
	AgentDeploymentStatusSuspended = "suspended"
	AgentDeploymentStatusDisabled  = "disabled"

	AgentEntitlementStatusActive   = "active"
	AgentEntitlementStatusDisabled = "disabled"

	ProviderChinaTencentCloud = "china_tencent_cloud"
	ProviderChinaTencentADP   = "china_tencent_adp"
)

type Customer struct {
	ID            uint64     `json:"id" gorm:"primaryKey"`
	CustomerCode  string     `json:"customer_code" gorm:"type:varchar(80);uniqueIndex;not null"`
	DisplayName   string     `json:"display_name" gorm:"type:varchar(160);not null"`
	Status        string     `json:"status" gorm:"type:varchar(24);index;not null"`
	BillingUserID *int64     `json:"billing_user_id,omitempty" gorm:"index"`
	RowVersion    int64      `json:"row_version" gorm:"not null"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ArchivedAt    *time.Time `json:"archived_at,omitempty" gorm:"index"`
}

func (Customer) TableName() string { return "claw_customers" }

type CustomerMember struct {
	ID             uint64     `json:"id" gorm:"primaryKey"`
	CustomerID     uint64     `json:"customer_id" gorm:"uniqueIndex:idx_claw_member_customer_user,priority:1;index;not null"`
	NewAPIUserID   int64      `json:"new_api_user_id" gorm:"uniqueIndex:idx_claw_member_customer_user,priority:2;uniqueIndex:idx_claw_member_user_slot,priority:1;index;not null"`
	Role           string     `json:"role" gorm:"type:varchar(24);not null"`
	Status         string     `json:"status" gorm:"type:varchar(24);index;not null"`
	MembershipSlot string     `json:"membership_slot" gorm:"type:varchar(80);uniqueIndex:idx_claw_member_user_slot,priority:2;index;not null"`
	AuthEpoch      int64      `json:"auth_epoch" gorm:"not null"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DisabledAt     *time.Time `json:"disabled_at,omitempty"`
}

func (CustomerMember) TableName() string { return "claw_customer_members" }

func MembershipSlot(customerID uint64) string { return fmt.Sprintf("customer:%d", customerID) }

func ActiveMembership(member CustomerMember) bool {
	return member.Status == MemberStatusActive && (member.MembershipSlot == MembershipSlot(member.CustomerID) || member.MembershipSlot == "primary")
}

type IdentityBinding struct {
	ID                uint64     `json:"id" gorm:"primaryKey"`
	PublicID          string     `json:"binding_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	CustomerID        uint64     `json:"customer_id" gorm:"uniqueIndex:idx_claw_identity_customer_user,priority:1;index;not null"`
	NewAPIUserID      int64      `json:"new_api_user_id" gorm:"uniqueIndex:idx_claw_identity_customer_user,priority:2;index;not null"`
	CanonicalSubject  string     `json:"canonical_subject" gorm:"type:varchar(191);uniqueIndex;not null"`
	ADPAccountID      string     `json:"adp_account_id,omitempty" gorm:"type:varchar(128);index"`
	ADPAccountVersion int64      `json:"adp_account_version" gorm:"not null"`
	IdentityVersion   string     `json:"identity_version,omitempty" gorm:"type:varchar(128);index"`
	Status            string     `json:"status" gorm:"type:varchar(24);index;not null"`
	AuthEpoch         int64      `json:"auth_epoch" gorm:"not null"`
	RowVersion        int64      `json:"row_version" gorm:"not null"`
	ConfirmedAt       *time.Time `json:"confirmed_at,omitempty"`
	DisabledAt        *time.Time `json:"disabled_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func (IdentityBinding) TableName() string { return "claw_identity_bindings" }

type ADPAccountBinding struct {
	ID                uint64    `json:"id" gorm:"primaryKey"`
	IdentityBindingID uint64    `json:"identity_binding_id" gorm:"uniqueIndex;not null"`
	ADPAccountID      string    `json:"adp_account_id" gorm:"type:varchar(128);uniqueIndex;not null"`
	CustomerID        uint64    `json:"customer_id" gorm:"index;not null"`
	NewAPIUserID      int64     `json:"new_api_user_id" gorm:"index;not null"`
	CreatedAt         time.Time `json:"created_at"`
}

func (ADPAccountBinding) TableName() string { return "claw_adp_account_bindings" }

// ResourceBinding is the control-plane mirror of an ADP-owned resource. Hash
// keys keep the global uniqueness constraints portable to MySQL 5.7 while the
// original identifiers remain available for audit and parent-chain checks.
type ResourceBinding struct {
	ID                      uint64    `json:"id" gorm:"primaryKey"`
	PublicID                string    `json:"resource_binding_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	ResourceKeyHash         string    `json:"-" gorm:"type:char(64);uniqueIndex;not null"`
	SourceEventKeyHash      string    `json:"-" gorm:"type:char(64);uniqueIndex;not null"`
	IdentityBindingID       uint64    `json:"identity_binding_id" gorm:"index;not null"`
	CustomerID              uint64    `json:"customer_id" gorm:"index;not null"`
	CustomerAppID           uint64    `json:"customer_app_id" gorm:"index;not null"`
	AppConfigVersionID      uint64    `json:"app_config_version_id" gorm:"index;not null"`
	ResourceType            string    `json:"resource_type" gorm:"type:varchar(32);index;not null"`
	ResourceID              string    `json:"resource_id" gorm:"type:varchar(255);not null"`
	ParentResourceBindingID *uint64   `json:"parent_resource_binding_id,omitempty" gorm:"index"`
	ParentResourceType      string    `json:"parent_resource_type,omitempty" gorm:"type:varchar(32)"`
	ParentResourceID        string    `json:"parent_resource_id,omitempty" gorm:"type:varchar(255)"`
	SourceService           string    `json:"source_service" gorm:"type:varchar(64);not null"`
	SourceEventID           string    `json:"source_event_id" gorm:"type:varchar(128);not null"`
	SourceVersion           int64     `json:"source_version" gorm:"not null"`
	SourcePayloadHash       string    `json:"-" gorm:"type:char(64);not null"`
	Status                  string    `json:"status" gorm:"type:varchar(24);index;not null"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

func (ResourceBinding) TableName() string { return "claw_resource_bindings" }

// CredentialProfile stores only external secret references. OwnerScope is
// always either "platform" or "customer:<CustomerID>"; it is included in the
// version uniqueness key so nullable CustomerID semantics cannot collapse or
// duplicate credential histories differently across database engines.
type CredentialProfile struct {
	ID                  uint64     `json:"id" gorm:"primaryKey"`
	OwnerScope          string     `json:"owner_scope" gorm:"type:varchar(191);uniqueIndex:idx_claw_credential_owner_version,priority:1;index;not null;check:chk_claw_credential_owner_scope,owner_scope <> ''"`
	CustomerID          *uint64    `json:"customer_id,omitempty" gorm:"index"`
	ProviderEnvironment string     `json:"provider_environment" gorm:"type:varchar(48);uniqueIndex:idx_claw_credential_owner_version,priority:2;index;not null"`
	Name                string     `json:"name" gorm:"type:varchar(120);uniqueIndex:idx_claw_credential_owner_version,priority:3;not null"`
	SecretIDRef         string     `json:"secret_id_ref" gorm:"type:varchar(255);not null"`
	SecretKeyRef        string     `json:"secret_key_ref" gorm:"type:varchar(255);not null"`
	Fingerprint         string     `json:"fingerprint" gorm:"type:varchar(128);not null"`
	FingerprintVersion  int        `json:"fingerprint_version" gorm:"not null"`
	Status              string     `json:"status" gorm:"type:varchar(24);index;not null"`
	Version             int64      `json:"version" gorm:"uniqueIndex:idx_claw_credential_owner_version,priority:4;not null"`
	PreviousProfileID   *uint64    `json:"previous_profile_id,omitempty" gorm:"index"`
	RowVersion          int64      `json:"row_version"`
	ActivatedAt         *time.Time `json:"activated_at,omitempty"`
	RotatedAt           *time.Time `json:"rotated_at,omitempty"`
	RetiredAt           *time.Time `json:"retired_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func (CredentialProfile) TableName() string { return "claw_credential_profiles" }

// ProviderSecret stores write-only provider material encrypted by the
// deployment-owned provider vault key. PublicID is the only value referenced
// by application configuration; ciphertext and its nonce are never projected
// through an HTTP response or audit payload.
type ProviderSecret struct {
	ID          uint64     `json:"-" gorm:"primaryKey"`
	PublicID    string     `json:"-" gorm:"type:varchar(64);uniqueIndex;not null"`
	CustomerID  uint64     `json:"-" gorm:"index;not null"`
	Kind        string     `json:"-" gorm:"type:varchar(32);index;not null"`
	Ciphertext  string     `json:"-" gorm:"type:text;not null"`
	Fingerprint string     `json:"-" gorm:"type:varchar(128);not null"`
	KeyVersion  int        `json:"-" gorm:"not null"`
	CreatedBy   string     `json:"-" gorm:"type:varchar(128);not null"`
	RevokedAt   *time.Time `json:"-" gorm:"index"`
	CreatedAt   time.Time  `json:"-"`
	UpdatedAt   time.Time  `json:"-"`
}

func (ProviderSecret) TableName() string { return "claw_provider_secrets" }

type CustomerApp struct {
	ID                     uint64     `json:"id" gorm:"primaryKey"`
	CustomerID             uint64     `json:"customer_id" gorm:"uniqueIndex:idx_claw_app_customer_slot,priority:1;uniqueIndex:idx_claw_app_customer_alias,priority:1;index;not null"`
	Slot                   string     `json:"slot" gorm:"type:varchar(80);uniqueIndex:idx_claw_app_customer_slot,priority:2;not null"`
	Selector               string     `json:"selector" gorm:"type:varchar(64);uniqueIndex"`
	Alias                  string     `json:"alias" gorm:"type:varchar(80);uniqueIndex:idx_claw_app_customer_alias,priority:2"`
	ProviderEnvironment    string     `json:"provider_environment" gorm:"type:varchar(48);uniqueIndex:idx_claw_app_provider_id,priority:1;not null"`
	AppID                  string     `json:"app_id" gorm:"type:varchar(128);uniqueIndex:idx_claw_app_provider_id,priority:2;not null"`
	DisplayName            string     `json:"display_name" gorm:"type:varchar(160);not null"`
	CurrentConfigVersionID *uint64    `json:"current_config_version_id,omitempty" gorm:"index"`
	PendingConfigVersionID *uint64    `json:"pending_config_version_id,omitempty" gorm:"index"`
	Status                 string     `json:"status" gorm:"type:varchar(24);index;not null"`
	AuthEpoch              int64      `json:"auth_epoch" gorm:"not null"`
	RowVersion             int64      `json:"row_version" gorm:"not null"`
	VerifiedAt             *time.Time `json:"verified_at,omitempty"`
	EnabledAt              *time.Time `json:"enabled_at,omitempty"`
	SuspendedAt            *time.Time `json:"suspended_at,omitempty"`
	DisabledAt             *time.Time `json:"disabled_at,omitempty"`
	ArchivedAt             *time.Time `json:"archived_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

func (CustomerApp) TableName() string { return "claw_customer_apps" }

func (app *CustomerApp) BeforeCreate(tx *gorm.DB) error {
	randomID := strings.ReplaceAll(uuid.NewString(), "-", "")
	var selectorCount int64
	if app.Selector != "" {
		if err := tx.Model(&CustomerApp{}).Where("selector = ?", app.Selector).Count(&selectorCount).Error; err != nil {
			return err
		}
	}
	if app.Selector == "" || selectorCount > 0 {
		app.Selector = "aps_" + randomID
	}
	if app.Alias == "" || (app.Slot != "primary" && app.Alias == "primary") {
		if app.Slot == "primary" {
			app.Alias = "primary"
		} else {
			app.Alias = "app-" + randomID
		}
	}
	return nil
}

type AppConfigVersion struct {
	ID                         uint64     `json:"id" gorm:"primaryKey"`
	CustomerAppID              uint64     `json:"customer_app_id" gorm:"uniqueIndex:idx_claw_app_config_version,priority:1;index;not null"`
	ConfigVersion              int64      `json:"config_version" gorm:"uniqueIndex:idx_claw_app_config_version,priority:2;not null"`
	Status                     string     `json:"status" gorm:"type:varchar(24);index;not null"`
	Region                     string     `json:"region" gorm:"type:varchar(80);not null"`
	SpaceID                    string     `json:"space_id" gorm:"type:varchar(191);not null"`
	TemplateAgentID            string     `json:"template_agent_id" gorm:"type:varchar(191);not null"`
	CredentialProfileID        *uint64    `json:"credential_profile_id,omitempty" gorm:"index"`
	AppKeySecretRef            string     `json:"app_key_secret_ref" gorm:"type:varchar(255);not null"`
	AppKeyFingerprint          string     `json:"app_key_fingerprint" gorm:"type:varchar(128);not null"`
	AppKeyFingerprintVersion   int        `json:"app_key_fingerprint_version" gorm:"not null"`
	CredentialChangeApprovalID *uint64    `json:"credential_change_approval_id,omitempty" gorm:"index"`
	RowVersion                 int64      `json:"row_version" gorm:"not null"`
	LimitsJSON                 string     `json:"limits_json" gorm:"type:text;not null"`
	CapabilitiesJSON           string     `json:"capabilities_json" gorm:"type:text;not null"`
	VerifiedAt                 *time.Time `json:"verified_at,omitempty"`
	RetiredAt                  *time.Time `json:"retired_at,omitempty"`
	CreatedBy                  string     `json:"created_by" gorm:"type:varchar(128);not null"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
}

func (AppConfigVersion) TableName() string { return "claw_app_config_versions" }

type AppVerification struct {
	ID                     uint64    `json:"id" gorm:"primaryKey"`
	PublicID               string    `json:"verification_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	CustomerAppID          uint64    `json:"customer_app_id" gorm:"index;not null"`
	AppConfigVersionID     uint64    `json:"app_config_version_id" gorm:"index;not null"`
	Result                 string    `json:"result" gorm:"type:varchar(24);index;not null"`
	AppMode                int       `json:"app_mode" gorm:"not null"`
	ReleaseStatus          string    `json:"release_status" gorm:"type:varchar(48);not null"`
	TemplateAgentStatus    string    `json:"template_agent_status" gorm:"type:varchar(48);not null"`
	DynamicAgentConfig     bool      `json:"dynamic_agent_config"`
	ProviderRequestIDsJSON string    `json:"provider_request_ids_json" gorm:"type:text;not null"`
	SanitizedResponseHash  string    `json:"sanitized_response_hash" gorm:"type:varchar(128);not null"`
	ErrorCode              string    `json:"error_code,omitempty" gorm:"type:varchar(80)"`
	ErrorMessage           string    `json:"error_message,omitempty" gorm:"type:text"`
	VerifiedBy             string    `json:"verified_by" gorm:"type:varchar(128);not null"`
	VerifiedAt             time.Time `json:"verified_at"`
	CreatedAt              time.Time `json:"created_at"`
}

func (AppVerification) TableName() string { return "claw_app_verifications" }

// AppMigrationJob is the immutable control-plane snapshot that gates an AppId
// cutover. Provider credentials are deliberately absent: workers receive them
// only from the signed claim response and keep them in memory.
type AppMigrationJob struct {
	ID                        uint64     `json:"id" gorm:"primaryKey"`
	PublicID                  string     `json:"migration_job_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	CustomerID                uint64     `json:"customer_id" gorm:"index;not null"`
	SourceCustomerAppID       uint64     `json:"source_app_profile_id" gorm:"index;not null"`
	TargetCustomerAppID       uint64     `json:"target_app_profile_id" gorm:"uniqueIndex:idx_claw_migration_target_generation,priority:1;index;not null"`
	Generation                int64      `json:"generation" gorm:"uniqueIndex:idx_claw_migration_target_generation,priority:2;not null"`
	TargetAppConfigVersionID  uint64     `json:"target_app_config_version_id" gorm:"index;not null"`
	TargetConfigVersion       int64      `json:"target_config_version" gorm:"not null"`
	TargetCredentialProfileID uint64     `json:"target_credential_profile_id" gorm:"index;not null"`
	TargetConfigFingerprint   string     `json:"target_config_fingerprint" gorm:"type:varchar(80);not null"`
	TargetProviderAppMode     int        `json:"target_provider_app_mode"`
	TargetRuntimeProfile      string     `json:"target_runtime_profile" gorm:"type:varchar(48)"`
	TargetExecutionEnabled    bool       `json:"target_execution_enabled"`
	MemberSetFingerprint      string     `json:"member_set_fingerprint" gorm:"type:varchar(80);not null"`
	ExpectedMembers           int        `json:"expected_members" gorm:"not null"`
	SucceededMembers          int        `json:"succeeded_members" gorm:"not null"`
	FailedMembers             int        `json:"failed_members" gorm:"not null"`
	Status                    string     `json:"status" gorm:"type:varchar(24);index;not null"`
	RowVersion                int64      `json:"row_version" gorm:"not null"`
	ReadyAt                   *time.Time `json:"ready_at,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}

func (AppMigrationJob) TableName() string { return "claw_app_migration_jobs" }

// AppMigrationMember records one active identity binding's target-App Agent
// readiness. Lease tokens are stored only as SHA-256 digests.
type AppMigrationMember struct {
	ID                      uint64     `json:"id" gorm:"primaryKey"`
	PublicID                string     `json:"migration_member_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	JobID                   uint64     `json:"migration_job_database_id" gorm:"uniqueIndex:idx_claw_migration_job_binding,priority:1;index;not null"`
	IdentityBindingID       uint64     `json:"identity_binding_database_id" gorm:"uniqueIndex:idx_claw_migration_job_binding,priority:2;index;not null"`
	BindingPublicID         string     `json:"binding_id" gorm:"type:varchar(64);index;not null"`
	ADPAccountID            string     `json:"adp_account_id" gorm:"type:varchar(128);not null"`
	TargetAgentID           string     `json:"target_agent_id,omitempty" gorm:"type:varchar(128)"`
	TargetReadbackHash      string     `json:"target_readback_hash,omitempty" gorm:"type:varchar(80)"`
	TargetConfigFingerprint string     `json:"target_config_fingerprint" gorm:"type:varchar(80);not null"`
	Status                  string     `json:"status" gorm:"type:varchar(24);index;not null"`
	AttemptID               string     `json:"attempt_id,omitempty" gorm:"type:varchar(64);index"`
	ClaimedBy               string     `json:"claimed_by,omitempty" gorm:"type:varchar(64);index"`
	RecoveryMode            string     `json:"recovery_mode,omitempty" gorm:"type:varchar(24);not null"`
	LeaseTokenHash          string     `json:"-" gorm:"type:char(64)"`
	LeaseUntil              *time.Time `json:"lease_until,omitempty" gorm:"index"`
	AttemptCount            int        `json:"attempt_count" gorm:"not null"`
	RowVersion              int64      `json:"row_version" gorm:"not null"`
	ReportHash              string     `json:"-" gorm:"type:varchar(80)"`
	ErrorCode               string     `json:"error_code,omitempty" gorm:"type:varchar(80)"`
	CompletedAt             *time.Time `json:"completed_at,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

func (AppMigrationMember) TableName() string { return "claw_app_migration_members" }

// AppMigrationLineage is the immutable source-to-target tuple activated by a
// completed governance cutover. A prepared or verified migration never creates
// this row, so archived provider resources cannot become history-readable early.
type AppMigrationLineage struct {
	ID                         uint64    `json:"id" gorm:"primaryKey"`
	PublicID                   string    `json:"lineage_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	EventKey                   string    `json:"event_key" gorm:"type:varchar(160);uniqueIndex;not null"`
	CustomerID                 uint64    `json:"customer_id" gorm:"index;not null"`
	MigrationJobID             uint64    `json:"migration_job_database_id" gorm:"uniqueIndex;not null"`
	SourceCustomerAppID        uint64    `json:"source_app_profile_id" gorm:"uniqueIndex:idx_claw_lineage_source_target,priority:1;index;not null"`
	SourceAppConfigVersionID   uint64    `json:"source_app_config_version_id" gorm:"index;not null"`
	SourceApplicationID        string    `json:"source_application_id" gorm:"type:varchar(128);not null"`
	SourceProviderAppID        string    `json:"source_provider_app_id" gorm:"type:varchar(128);not null"`
	SourceConfigVersion        int64     `json:"source_config_version" gorm:"not null"`
	SourceProviderAppMode      int       `json:"source_provider_app_mode"`
	SourceRuntimeProfile       string    `json:"source_runtime_profile" gorm:"type:varchar(48)"`
	SourceExecutionEnabled     bool      `json:"source_execution_enabled"`
	TargetCustomerAppID        uint64    `json:"target_app_profile_id" gorm:"uniqueIndex:idx_claw_lineage_source_target,priority:2;index;not null"`
	TargetAppConfigVersionID   uint64    `json:"target_app_config_version_id" gorm:"index;not null"`
	TargetApplicationID        string    `json:"target_application_id" gorm:"type:varchar(128);not null"`
	TargetProviderAppID        string    `json:"target_provider_app_id" gorm:"type:varchar(128);not null"`
	TargetConfigVersion        int64     `json:"target_config_version" gorm:"not null"`
	MigrationConfigFingerprint string    `json:"migration_config_fingerprint" gorm:"type:varchar(80);not null"`
	ActivatedAt                time.Time `json:"activated_at" gorm:"index;not null"`
	CreatedAt                  time.Time `json:"created_at"`
}

func (AppMigrationLineage) TableName() string { return "claw_app_migration_lineages" }

type Plan struct {
	ID          uint64    `json:"id" gorm:"primaryKey"`
	PlanCode    string    `json:"plan_code" gorm:"type:varchar(80);uniqueIndex;not null"`
	DisplayName string    `json:"display_name" gorm:"type:varchar(160);not null"`
	Status      string    `json:"status" gorm:"type:varchar(24);index;not null"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (Plan) TableName() string { return "claw_plans" }

type PlanVersion struct {
	ID               uint64     `json:"id" gorm:"primaryKey"`
	PlanID           uint64     `json:"plan_id" gorm:"uniqueIndex:idx_claw_plan_version,priority:1;index;not null"`
	Version          int64      `json:"version" gorm:"uniqueIndex:idx_claw_plan_version,priority:2;not null"`
	Name             string     `json:"name" gorm:"type:varchar(160);not null"`
	MonthlyPriceCNY  string     `json:"monthly_price_cny" gorm:"type:text;not null"`
	Currency         string     `json:"currency" gorm:"type:varchar(8);not null"`
	CapabilitiesJSON string     `json:"capabilities_json" gorm:"type:text;not null"`
	LimitsJSON       string     `json:"limits_json" gorm:"type:text;not null"`
	Status           string     `json:"status" gorm:"type:varchar(24);index;not null"`
	ValidFrom        time.Time  `json:"valid_from" gorm:"index"`
	ValidTo          *time.Time `json:"valid_to,omitempty" gorm:"index"`
	PublishedBy      string     `json:"published_by" gorm:"type:varchar(128);not null"`
	PublishedAt      time.Time  `json:"published_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

func (PlanVersion) TableName() string { return "claw_plan_versions" }

type PlanPeriod struct {
	ID                 uint64     `json:"id" gorm:"primaryKey"`
	CustomerID         uint64     `json:"customer_id" gorm:"uniqueIndex:idx_claw_period_window,priority:1;index;not null"`
	PlanVersionID      uint64     `json:"plan_version_id" gorm:"index;not null"`
	StartAt            time.Time  `json:"start_at" gorm:"uniqueIndex:idx_claw_period_window,priority:2;index;not null"`
	EndAt              time.Time  `json:"end_at" gorm:"uniqueIndex:idx_claw_period_window,priority:3;index;not null"`
	AmountCNY          string     `json:"amount_cny" gorm:"type:text;not null"`
	PaymentMode        string     `json:"payment_mode" gorm:"type:varchar(32);not null"`
	PaymentStatus      string     `json:"payment_status" gorm:"type:varchar(24);index;not null"`
	Status             string     `json:"status" gorm:"type:varchar(32);index;not null"`
	SnapshotJSON       string     `json:"snapshot_json" gorm:"type:text;not null"`
	PaymentEvidenceRef string     `json:"payment_evidence_ref,omitempty" gorm:"type:varchar(255)"`
	RowVersion         int64      `json:"row_version" gorm:"not null"`
	ActivatedAt        *time.Time `json:"activated_at,omitempty"`
	ExpiredAt          *time.Time `json:"expired_at,omitempty"`
	CanceledAt         *time.Time `json:"canceled_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (PlanPeriod) TableName() string { return "claw_plan_periods" }

type CustomerInvoice struct {
	ID                  uint64     `json:"id" gorm:"primaryKey"`
	InvoiceNumber       string     `json:"invoice_number" gorm:"type:varchar(80);uniqueIndex;not null"`
	PeriodID            uint64     `json:"period_id" gorm:"uniqueIndex;not null"`
	CustomerID          uint64     `json:"customer_id" gorm:"index;not null"`
	Description         string     `json:"description" gorm:"type:varchar(255);not null"`
	FixedAmountCNY      string     `json:"fixed_amount_cny" gorm:"type:text;not null"`
	ManualAdjustmentCNY string     `json:"manual_adjustment_cny" gorm:"type:text;not null"`
	AmountCNY           string     `json:"amount_cny" gorm:"type:text;not null"`
	Status              string     `json:"status" gorm:"type:varchar(24);index;not null"`
	EvidenceRef         string     `json:"evidence_ref,omitempty" gorm:"type:varchar(255)"`
	IssuedAt            time.Time  `json:"issued_at"`
	PaidAt              *time.Time `json:"paid_at,omitempty"`
	VoidAt              *time.Time `json:"void_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func (CustomerInvoice) TableName() string { return "claw_customer_invoices" }

type UsageAudit struct {
	ID                   uint64     `json:"id" gorm:"primaryKey"`
	PublicID             string     `json:"audit_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	CustomerID           *uint64    `json:"customer_id,omitempty" gorm:"index"`
	CustomerAppID        *uint64    `json:"customer_app_id,omitempty" gorm:"index"`
	PlanPeriodID         *uint64    `json:"plan_period_id,omitempty" gorm:"index"`
	PeriodStart          time.Time  `json:"period_start" gorm:"index;not null"`
	PeriodEnd            time.Time  `json:"period_end" gorm:"index;not null"`
	Source               string     `json:"source" gorm:"type:varchar(80);not null"`
	AllocationConfidence string     `json:"allocation_confidence" gorm:"type:varchar(40);index;not null"`
	ResourceIdentifier   string     `json:"resource_identifier,omitempty" gorm:"type:varchar(191)"`
	AllocationMethod     string     `json:"allocation_method,omitempty" gorm:"type:text"`
	UpstreamCostCNY      string     `json:"upstream_cost_cny" gorm:"type:text;not null"`
	UsageJSON            string     `json:"usage_json" gorm:"type:text;not null"`
	EvidenceRef          string     `json:"evidence_ref,omitempty" gorm:"type:varchar(255)"`
	EvidenceHash         string     `json:"evidence_hash,omitempty" gorm:"type:varchar(128)"`
	ImportSourceKey      *string    `json:"-" gorm:"type:varchar(191);uniqueIndex"`
	Note                 string     `json:"note,omitempty" gorm:"type:text"`
	Status               string     `json:"status" gorm:"type:varchar(24);index;not null"`
	RowVersion           int64      `json:"row_version" gorm:"not null"`
	CreatedBy            string     `json:"created_by" gorm:"type:varchar(128);not null"`
	ReviewedBy           string     `json:"reviewed_by,omitempty" gorm:"type:varchar(128)"`
	LockedBy             string     `json:"locked_by,omitempty" gorm:"type:varchar(128)"`
	ReviewedAt           *time.Time `json:"reviewed_at,omitempty"`
	LockedAt             *time.Time `json:"locked_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func (UsageAudit) TableName() string { return "claw_usage_audits" }

type UsageAuditRevision struct {
	ID                 uint64    `json:"id" gorm:"primaryKey"`
	PublicID           string    `json:"revision_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	OriginalAuditID    uint64    `json:"original_audit_id" gorm:"uniqueIndex;not null"`
	ReplacementAuditID uint64    `json:"replacement_audit_id" gorm:"uniqueIndex;not null"`
	Reason             string    `json:"reason" gorm:"type:text;not null"`
	CreatedBy          string    `json:"created_by" gorm:"type:varchar(128);not null"`
	AppliedAt          time.Time `json:"applied_at" gorm:"not null"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (UsageAuditRevision) TableName() string { return "claw_usage_audit_revisions" }

// TencentBillingImportRun is a durable, account-scoped read-only import job.
// Provider credentials and the payer UIN are deliberately absent. ScopeHash
// is derived from the payer UIN, month, and BusinessCode and makes draft
// creation idempotent across retries and replicas.
type TencentBillingImportRun struct {
	ID                        uint64     `json:"id" gorm:"primaryKey"`
	PublicID                  string     `json:"import_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	ScopeHash                 string     `json:"-" gorm:"type:char(64);uniqueIndex;not null"`
	Month                     string     `json:"month" gorm:"type:char(7);index;not null"`
	BusinessCode              string     `json:"business_code" gorm:"type:varchar(80);index;not null"`
	PayerUINHash              string     `json:"-" gorm:"type:char(64);not null"`
	Status                    string     `json:"status" gorm:"type:varchar(32);index;not null"`
	AttemptCount              int        `json:"attempt_count" gorm:"not null"`
	MaxAttempts               int        `json:"max_attempts" gorm:"not null"`
	RowVersion                int64      `json:"row_version" gorm:"not null"`
	LeaseToken                string     `json:"-" gorm:"type:varchar(64)"`
	LeaseUntil                *time.Time `json:"-" gorm:"index"`
	NextAttemptAt             time.Time  `json:"next_attempt_at" gorm:"index;not null"`
	DetailPageCount           int        `json:"detail_page_count" gorm:"not null"`
	DetailRecordCount         int        `json:"detail_record_count" gorm:"not null"`
	UpstreamCostCNY           string     `json:"upstream_cost_cny" gorm:"type:text;not null"`
	ManualReviewRequired      bool       `json:"manual_review_required" gorm:"not null"`
	ReviewReasonsJSON         string     `json:"review_reasons_json" gorm:"type:text;not null"`
	BillQueryRequestIDsJSON   string     `json:"bill_query_request_ids_json" gorm:"type:text;not null"`
	AdjustQueryRequestIDsJSON string     `json:"adjust_query_request_ids_json" gorm:"type:text;not null"`
	EvidenceRef               string     `json:"evidence_ref,omitempty" gorm:"type:varchar(64);index"`
	EvidenceHash              string     `json:"evidence_hash,omitempty" gorm:"type:varchar(128)"`
	UsageAuditID              *uint64    `json:"usage_audit_id,omitempty" gorm:"uniqueIndex"`
	UsageAuditPublicID        string     `json:"usage_audit_public_id,omitempty" gorm:"type:varchar(64);index"`
	ErrorCode                 string     `json:"error_code,omitempty" gorm:"type:varchar(80)"`
	RequestedBy               string     `json:"requested_by" gorm:"type:varchar(128);not null"`
	RequestID                 string     `json:"request_id,omitempty" gorm:"type:varchar(96);index"`
	StartedAt                 *time.Time `json:"started_at,omitempty"`
	CompletedAt               *time.Time `json:"completed_at,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}

func (TencentBillingImportRun) TableName() string { return "claw_tencent_billing_import_runs" }

// TencentBillingImportCoordinator serializes provider calls across Blue/Green
// replicas so the account-wide DescribeBillDetail limit is not multiplied by
// the number of control instances.
type TencentBillingImportCoordinator struct {
	Key        string     `json:"-" gorm:"type:varchar(64);primaryKey"`
	LeaseToken string     `json:"-" gorm:"type:varchar(64)"`
	LeaseUntil *time.Time `json:"-" gorm:"index"`
	RowVersion int64      `json:"-" gorm:"not null"`
	UpdatedAt  time.Time  `json:"-"`
}

func (TencentBillingImportCoordinator) TableName() string {
	return "claw_tencent_billing_import_coordinators"
}

// EvidenceObject stores only the encrypted object's locator and safe metadata.
// StorageKey is an opaque server-side value and must never be projected to API
// clients. The independent evidence master key is supplied at runtime and is
// deliberately not represented in the database.
type EvidenceObject struct {
	ID               uint64    `json:"id" gorm:"primaryKey"`
	PublicID         string    `json:"evidence_ref" gorm:"type:varchar(64);uniqueIndex;not null"`
	CustomerID       *uint64   `json:"customer_id,omitempty" gorm:"index"`
	OriginalFilename string    `json:"filename" gorm:"type:varchar(255);not null"`
	MIMEType         string    `json:"mime_type" gorm:"type:varchar(96);not null"`
	SizeBytes        int64     `json:"size_bytes" gorm:"not null"`
	ContentSHA256    string    `json:"content_sha256" gorm:"type:char(71);index;not null"`
	StorageKey       string    `json:"-" gorm:"type:varchar(96);uniqueIndex;not null"`
	FormatVersion    int       `json:"format_version" gorm:"not null"`
	Status           string    `json:"status" gorm:"type:varchar(24);index;not null"`
	CreatedBy        string    `json:"created_by" gorm:"type:varchar(128);not null"`
	CreatedAt        time.Time `json:"created_at"`
}

func (EvidenceObject) TableName() string { return "claw_evidence_objects" }

type AdminAudit struct {
	ID           uint64    `json:"id" gorm:"primaryKey"`
	CustomerID   *uint64   `json:"customer_id,omitempty" gorm:"index"`
	Actor        string    `json:"actor" gorm:"type:varchar(128);index;not null"`
	Action       string    `json:"action" gorm:"type:varchar(96);index;not null"`
	ResourceType string    `json:"resource_type" gorm:"type:varchar(80);index;not null"`
	ResourceID   string    `json:"resource_id" gorm:"type:varchar(128);index;not null"`
	BeforeHash   string    `json:"before_hash,omitempty" gorm:"type:varchar(128)"`
	AfterHash    string    `json:"after_hash,omitempty" gorm:"type:varchar(128)"`
	Result       string    `json:"result" gorm:"type:varchar(24);not null"`
	Reason       string    `json:"reason,omitempty" gorm:"type:text"`
	RequestID    string    `json:"request_id,omitempty" gorm:"type:varchar(96);index"`
	CreatedAt    time.Time `json:"created_at" gorm:"index"`
}

func (AdminAudit) TableName() string { return "claw_admin_audits" }

type GovernanceNotification struct {
	ID           uint64     `json:"id" gorm:"primaryKey"`
	PublicID     string     `json:"notification_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	EventKey     string     `json:"-" gorm:"type:varchar(160);uniqueIndex;not null"`
	CustomerID   uint64     `json:"customer_id" gorm:"index;not null"`
	Type         string     `json:"type" gorm:"type:varchar(48);index;not null"`
	Title        string     `json:"title" gorm:"type:varchar(160);not null"`
	Message      string     `json:"message" gorm:"type:text;not null"`
	ResourceType string     `json:"resource_type" gorm:"type:varchar(80);not null"`
	ResourceID   string     `json:"resource_id" gorm:"type:varchar(128);not null"`
	Status       string     `json:"status" gorm:"type:varchar(24);index;not null"`
	OccurredAt   time.Time  `json:"occurred_at" gorm:"index;not null"`
	ReadAt       *time.Time `json:"read_at,omitempty"`
	ReadBy       string     `json:"read_by,omitempty" gorm:"type:varchar(128)"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (GovernanceNotification) TableName() string { return "claw_governance_notifications" }

type CustomerRetentionPolicy struct {
	ID            uint64    `json:"id" gorm:"primaryKey"`
	CustomerID    uint64    `json:"customer_id" gorm:"uniqueIndex;not null"`
	RetentionDays int       `json:"retention_days" gorm:"not null"`
	LegalHold     bool      `json:"legal_hold" gorm:"not null"`
	Status        string    `json:"status" gorm:"type:varchar(24);index;not null"`
	RowVersion    int64     `json:"row_version" gorm:"not null"`
	UpdatedBy     string    `json:"updated_by" gorm:"type:varchar(128);not null"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (CustomerRetentionPolicy) TableName() string { return "claw_customer_retention_policies" }

type CustomerRetentionRun struct {
	ID            uint64     `json:"id" gorm:"primaryKey"`
	PublicID      string     `json:"retention_run_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	CustomerID    uint64     `json:"customer_id" gorm:"index;not null"`
	PolicyID      uint64     `json:"policy_id" gorm:"index;not null"`
	PolicyVersion int64      `json:"policy_version" gorm:"not null"`
	CutoffAt      time.Time  `json:"cutoff_at" gorm:"index;not null"`
	CountsJSON    string     `json:"counts_json" gorm:"type:text;not null"`
	Status        string     `json:"status" gorm:"type:varchar(24);index;not null"`
	RowVersion    int64      `json:"row_version" gorm:"not null"`
	RequestedBy   string     `json:"requested_by" gorm:"type:varchar(128);not null"`
	ExecutedBy    string     `json:"executed_by,omitempty" gorm:"type:varchar(128)"`
	ExecutedAt    *time.Time `json:"executed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (CustomerRetentionRun) TableName() string { return "claw_customer_retention_runs" }

// CustomerRetentionDelivery is the durable cross-component deletion intent.
// Its payload contains only immutable tenant scope and policy metadata; provider
// credentials and object locators remain exclusively in ADP.
type CustomerRetentionDelivery struct {
	ID            uint64     `json:"id" gorm:"primaryKey"`
	IntentID      string     `json:"intent_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	RunID         uint64     `json:"run_id" gorm:"uniqueIndex;not null"`
	CustomerID    uint64     `json:"customer_id" gorm:"index;not null"`
	PolicyID      uint64     `json:"policy_id" gorm:"index;not null"`
	PolicyVersion int64      `json:"policy_version" gorm:"not null"`
	CutoffAt      time.Time  `json:"cutoff_at" gorm:"not null"`
	Status        string     `json:"status" gorm:"type:varchar(24);index;not null"`
	Attempts      int        `json:"attempts" gorm:"not null"`
	NextRetryAt   time.Time  `json:"next_retry_at" gorm:"index;not null"`
	ReceiptID     string     `json:"receipt_id,omitempty" gorm:"type:varchar(64);index"`
	ReceiptJSON   string     `json:"receipt_json,omitempty" gorm:"type:text"`
	LastError     string     `json:"last_error,omitempty" gorm:"type:text"`
	DeliveredAt   *time.Time `json:"delivered_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (CustomerRetentionDelivery) TableName() string { return "claw_customer_retention_deliveries" }

type GovernanceApproval struct {
	ID             uint64     `json:"id" gorm:"primaryKey"`
	PublicID       string     `json:"approval_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	RequestKey     string     `json:"request_key" gorm:"type:varchar(128);uniqueIndex;not null"`
	CustomerID     *uint64    `json:"customer_id,omitempty" gorm:"index"`
	ActionType     string     `json:"action_type" gorm:"type:varchar(48);index;not null"`
	PayloadJSON    string     `json:"-" gorm:"type:text;not null"`
	PayloadHash    string     `json:"payload_hash" gorm:"type:char(71);not null"`
	Status         string     `json:"status" gorm:"type:varchar(24);index;not null"`
	RequestedBy    string     `json:"requested_by" gorm:"type:varchar(128);index;not null"`
	ApprovedBy     string     `json:"approved_by,omitempty" gorm:"type:varchar(128)"`
	RejectedBy     string     `json:"rejected_by,omitempty" gorm:"type:varchar(128)"`
	ExecutedBy     string     `json:"executed_by,omitempty" gorm:"type:varchar(128)"`
	Reason         string     `json:"reason" gorm:"type:text;not null"`
	DecisionReason string     `json:"decision_reason,omitempty" gorm:"type:text"`
	ExpiresAt      time.Time  `json:"expires_at" gorm:"index;not null"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
	RejectedAt     *time.Time `json:"rejected_at,omitempty"`
	ExecutedAt     *time.Time `json:"executed_at,omitempty"`
	RowVersion     int64      `json:"row_version" gorm:"not null"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (GovernanceApproval) TableName() string { return "claw_governance_approvals" }

type ControlOutbox struct {
	ID          uint64     `json:"id" gorm:"primaryKey"`
	EventKey    string     `json:"event_key" gorm:"type:varchar(160);uniqueIndex;not null"`
	CustomerID  *uint64    `json:"customer_id,omitempty" gorm:"index"`
	EventType   string     `json:"event_type" gorm:"type:varchar(64);index;not null"`
	PayloadJSON string     `json:"payload_json" gorm:"type:text;not null"`
	Status      string     `json:"status" gorm:"type:varchar(24);index;not null"`
	Attempts    int        `json:"attempts" gorm:"not null"`
	NextRetryAt time.Time  `json:"next_retry_at" gorm:"index"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	LastError   string     `json:"last_error,omitempty" gorm:"type:text"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (ControlOutbox) TableName() string { return "claw_control_outbox" }

type SSOTicket struct {
	ID                 uint64     `json:"id" gorm:"primaryKey"`
	TokenHash          string     `json:"-" gorm:"type:varchar(128);uniqueIndex;not null"`
	BrowserBindingHash string     `json:"-" gorm:"type:varchar(128)"`
	IdentityBindingID  uint64     `json:"identity_binding_id" gorm:"index;not null"`
	ControlSessionID   uint64     `json:"control_session_id" gorm:"index"`
	CustomerID         uint64     `json:"customer_id" gorm:"index;not null"`
	NewAPIUserID       int64      `json:"new_api_user_id" gorm:"index;not null"`
	CustomerAppID      uint64     `json:"customer_app_id" gorm:"index;not null"`
	AppConfigVersionID uint64     `json:"app_config_version_id" gorm:"index;not null"`
	AccessMode         string     `json:"access_mode" gorm:"type:varchar(24);not null"`
	AuthEpoch          int64      `json:"auth_epoch" gorm:"not null"`
	IdentityVersion    string     `json:"identity_version" gorm:"type:varchar(128);not null"`
	ExpiresAt          time.Time  `json:"expires_at" gorm:"index;not null"`
	ConsumedAt         *time.Time `json:"consumed_at,omitempty" gorm:"index"`
	ConsumedByService  string     `json:"consumed_by_service,omitempty" gorm:"type:varchar(80)"`
	CallbackOrigin     string     `json:"callback_origin,omitempty" gorm:"type:varchar(255)"`
	CreatedAt          time.Time  `json:"created_at"`
}

func (SSOTicket) TableName() string { return "claw_sso_tickets" }

type EntryTicket struct {
	ID              uint64     `json:"id" gorm:"primaryKey"`
	TokenHash       string     `json:"-" gorm:"type:varchar(128);uniqueIndex;not null"`
	NewAPIUserID    int64      `json:"new_api_user_id" gorm:"index;not null"`
	IdentityVersion string     `json:"identity_version" gorm:"type:varchar(128);not null"`
	Surface         string     `json:"surface" gorm:"type:varchar(24);index;not null"`
	IsSuperAdmin    bool       `json:"is_super_admin" gorm:"not null"`
	AuthenticatedAt *time.Time `json:"authenticated_at,omitempty" gorm:"index"`
	AuthMethods     string     `json:"auth_methods,omitempty" gorm:"type:varchar(128)"`
	ReauthNonceHash *string    `json:"-" gorm:"type:char(64);uniqueIndex:idx_claw_entry_reauth_nonce"`
	ExpiresAt       time.Time  `json:"expires_at" gorm:"index;not null"`
	ConsumedAt      *time.Time `json:"consumed_at,omitempty" gorm:"index"`
	CreatedAt       time.Time  `json:"created_at"`
}

func (EntryTicket) TableName() string { return "claw_entry_tickets" }

type ControlSession struct {
	ID                 uint64     `json:"id" gorm:"primaryKey"`
	TokenHash          string     `json:"-" gorm:"type:varchar(128);uniqueIndex;not null"`
	CSRFTokenHash      string     `json:"-" gorm:"type:varchar(128)"`
	SelectionState     string     `json:"selection_state" gorm:"type:varchar(32);index"`
	IdentityBindingID  uint64     `json:"identity_binding_id" gorm:"index;not null"`
	CustomerID         uint64     `json:"customer_id" gorm:"index;not null"`
	NewAPIUserID       int64      `json:"new_api_user_id" gorm:"index;not null"`
	CustomerAppID      uint64     `json:"customer_app_id" gorm:"index;not null"`
	AppConfigVersionID uint64     `json:"app_config_version_id" gorm:"index;not null"`
	AccessMode         string     `json:"access_mode" gorm:"type:varchar(24);not null"`
	AuthEpoch          int64      `json:"auth_epoch" gorm:"not null"`
	IdentityVersion    string     `json:"identity_version" gorm:"type:varchar(128);not null"`
	ExpiresAt          time.Time  `json:"expires_at" gorm:"index;not null"`
	LastSeenAt         time.Time  `json:"last_seen_at" gorm:"index;not null"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty" gorm:"index"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (ControlSession) TableName() string { return "claw_control_sessions" }

func (session *ControlSession) BeforeCreate(_ *gorm.DB) error {
	if session.SelectionState == "" {
		session.SelectionState = ControlSessionStateSelected
	}
	return nil
}

// AgentCatalogItem is the stable product identity. Provider configuration and
// credentials live on the customer-specific CustomerApp, never on this row.
type AgentCatalogItem struct {
	ID               string     `json:"item_id" gorm:"type:varchar(64);primaryKey"`
	Slug             string     `json:"slug" gorm:"type:varchar(96);uniqueIndex;not null"`
	Status           string     `json:"status" gorm:"type:varchar(24);index;not null"`
	CurrentVersionID *string    `json:"current_version_id,omitempty" gorm:"type:varchar(64);index"`
	DraftVersionID   *string    `json:"draft_version_id,omitempty" gorm:"type:varchar(64);index"`
	SortOrder        int        `json:"sort_order" gorm:"index;not null"`
	Featured         bool       `json:"featured" gorm:"index;not null"`
	RowVersion       int64      `json:"row_version" gorm:"not null"`
	CreatedBy        string     `json:"created_by" gorm:"type:varchar(128);not null"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ArchivedAt       *time.Time `json:"archived_at,omitempty" gorm:"index"`
}

func (AgentCatalogItem) TableName() string { return "claw_agent_catalog_items" }

// AgentCatalogVersion is immutable after creation. Publishing only changes
// the parent item's CurrentVersionID pointer.
type AgentCatalogVersion struct {
	ID             string    `json:"version_id" gorm:"type:varchar(64);primaryKey"`
	ItemID         string    `json:"item_id" gorm:"type:varchar(64);uniqueIndex:idx_claw_catalog_generation,priority:1;index;not null"`
	Generation     int64     `json:"generation" gorm:"uniqueIndex:idx_claw_catalog_generation,priority:2;not null"`
	DisplayName    string    `json:"display_name" gorm:"type:varchar(160);not null"`
	Summary        string    `json:"summary" gorm:"type:varchar(500);not null"`
	Description    string    `json:"description" gorm:"type:text;not null"`
	AvatarURL      string    `json:"avatar_url,omitempty" gorm:"type:varchar(1024)"`
	Category       string    `json:"category" gorm:"type:varchar(96);index;not null"`
	TagsJSON       string    `json:"-" gorm:"type:text;not null"`
	MetadataSHA256 string    `json:"metadata_sha256" gorm:"type:varchar(71);not null"`
	CreatedBy      string    `json:"created_by" gorm:"type:varchar(128);not null"`
	CreatedAt      time.Time `json:"created_at"`
}

func (AgentCatalogVersion) TableName() string { return "claw_agent_catalog_versions" }

type CustomerAgentDeployment struct {
	ID                      string     `json:"deployment_id" gorm:"type:varchar(64);primaryKey"`
	ItemID                  string     `json:"item_id" gorm:"type:varchar(64);uniqueIndex:idx_claw_agent_deployment_customer,priority:1;index;not null"`
	CustomerID              uint64     `json:"customer_id" gorm:"uniqueIndex:idx_claw_agent_deployment_customer,priority:2;index;not null"`
	CustomerAppID           uint64     `json:"customer_app_id" gorm:"uniqueIndex:idx_claw_agent_deployment_app;index;not null"`
	VerifiedConfigVersionID *uint64    `json:"verified_config_version_id,omitempty" gorm:"index"`
	VerifiedConfigVersion   int64      `json:"verified_config_version" gorm:"not null"`
	VerifiedAppAuthEpoch    int64      `json:"verified_app_auth_epoch" gorm:"not null"`
	VerifiedCredentialHash  string     `json:"-" gorm:"type:varchar(128)"`
	ProviderAppMode         int        `json:"provider_app_mode" gorm:"index;not null"`
	RuntimeProfile          string     `json:"runtime_profile" gorm:"type:varchar(48);index;not null"`
	DynamicAgentConfig      bool       `json:"dynamic_agent_config" gorm:"not null"`
	ExecutionEnabled        bool       `json:"execution_enabled" gorm:"index;not null"`
	CapabilitiesJSON        string     `json:"-" gorm:"type:text;not null"`
	ProviderRequestIDsJSON  string     `json:"-" gorm:"type:text;not null"`
	SanitizedResponseHash   string     `json:"sanitized_response_hash,omitempty" gorm:"type:varchar(71)"`
	ProviderDisplayName     string     `json:"provider_display_name,omitempty" gorm:"type:varchar(160)"`
	ProviderDescription     string     `json:"provider_description,omitempty" gorm:"type:text"`
	ProviderAvatarURL       string     `json:"provider_avatar_url,omitempty" gorm:"type:varchar(1024)"`
	Status                  string     `json:"status" gorm:"type:varchar(24);index;not null"`
	RowVersion              int64      `json:"row_version" gorm:"not null"`
	VerifiedAt              *time.Time `json:"verified_at,omitempty" gorm:"index"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

func (CustomerAgentDeployment) TableName() string { return "claw_customer_agent_deployments" }

type AgentCatalogEntitlement struct {
	ID           string     `json:"entitlement_id" gorm:"type:varchar(64);primaryKey"`
	DeploymentID string     `json:"deployment_id" gorm:"type:varchar(64);uniqueIndex:idx_claw_agent_entitlement_subject,priority:1;index;not null"`
	SubjectType  string     `json:"subject_type" gorm:"type:varchar(24);uniqueIndex:idx_claw_agent_entitlement_subject,priority:2;index;not null"`
	SubjectRef   string     `json:"subject_ref" gorm:"type:varchar(191);uniqueIndex:idx_claw_agent_entitlement_subject,priority:3;index;not null"`
	Status       string     `json:"status" gorm:"type:varchar(24);index;not null"`
	ValidFrom    time.Time  `json:"valid_from" gorm:"index;not null"`
	ValidUntil   *time.Time `json:"valid_until,omitempty" gorm:"index"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (AgentCatalogEntitlement) TableName() string { return "claw_agent_catalog_entitlements" }

type AgentLaunchAudit struct {
	ID           string    `json:"launch_audit_id" gorm:"type:varchar(64);primaryKey"`
	DeploymentID string    `json:"deployment_id" gorm:"type:varchar(64);index;not null"`
	CustomerID   uint64    `json:"customer_id" gorm:"index;not null"`
	NewAPIUserID int64     `json:"new_api_user_id" gorm:"index;not null"`
	Outcome      string    `json:"outcome" gorm:"type:varchar(24);index;not null"`
	ReasonCode   string    `json:"reason_code,omitempty" gorm:"type:varchar(80);index"`
	RequestID    string    `json:"request_id,omitempty" gorm:"type:varchar(128);index"`
	CreatedAt    time.Time `json:"created_at" gorm:"index"`
}

func (AgentLaunchAudit) TableName() string { return "claw_agent_launch_audits" }

type AgentCatalogCursor struct {
	ID               uint64    `json:"-" gorm:"primaryKey"`
	TokenHash        string    `json:"-" gorm:"type:char(64);uniqueIndex;not null"`
	ControlSessionID uint64    `json:"-" gorm:"index;not null"`
	CustomerID       uint64    `json:"-" gorm:"index;not null"`
	NewAPIUserID     int64     `json:"-" gorm:"index;not null"`
	QueryHash        string    `json:"-" gorm:"type:char(64);not null"`
	LastSortOrder    int       `json:"-" gorm:"not null"`
	LastItemID       string    `json:"-" gorm:"type:varchar(64);not null"`
	ExpiresAt        time.Time `json:"-" gorm:"index;not null"`
	CreatedAt        time.Time `json:"-"`
}

func (AgentCatalogCursor) TableName() string { return "claw_agent_catalog_cursors" }

type ContextSelectionNonce struct {
	ID                 uint64     `json:"id" gorm:"primaryKey"`
	TokenHash          string     `json:"-" gorm:"type:varchar(128);uniqueIndex;not null"`
	ControlSessionID   uint64     `json:"control_session_id" gorm:"index;not null"`
	NewAPIUserID       int64      `json:"new_api_user_id" gorm:"index;not null"`
	IdentityBindingID  uint64     `json:"identity_binding_id" gorm:"index;not null"`
	CustomerMemberID   uint64     `json:"customer_member_id" gorm:"index;not null"`
	CustomerID         uint64     `json:"customer_id" gorm:"index;not null"`
	CustomerAppID      uint64     `json:"customer_app_id" gorm:"index;not null"`
	AppConfigVersionID uint64     `json:"app_config_version_id" gorm:"index;not null"`
	IdentityVersion    string     `json:"identity_version" gorm:"type:varchar(128);not null"`
	IdentityAuthEpoch  int64      `json:"identity_auth_epoch" gorm:"not null"`
	MemberAuthEpoch    int64      `json:"member_auth_epoch" gorm:"not null"`
	AppAuthEpoch       int64      `json:"app_auth_epoch" gorm:"not null"`
	Purpose            string     `json:"-" gorm:"type:varchar(32);index"`
	AgentCatalogItemID string     `json:"-" gorm:"type:varchar(64);index"`
	AgentDeploymentID  string     `json:"-" gorm:"type:varchar(64);index"`
	CatalogVersionID   string     `json:"-" gorm:"type:varchar(64);index"`
	CatalogRowVersion  int64      `json:"-"`
	DeploymentVersion  int64      `json:"-"`
	ExpiresAt          time.Time  `json:"expires_at" gorm:"index;not null"`
	ConsumedAt         *time.Time `json:"consumed_at,omitempty" gorm:"index"`
	CreatedAt          time.Time  `json:"created_at"`
}

func (ContextSelectionNonce) TableName() string { return "claw_context_selection_nonces" }

type AdminSession struct {
	ID              uint64     `json:"id" gorm:"primaryKey"`
	TokenHash       string     `json:"-" gorm:"type:varchar(128);uniqueIndex;not null"`
	CSRFTokenHash   string     `json:"-" gorm:"type:varchar(128);not null"`
	NewAPIUserID    int64      `json:"new_api_user_id" gorm:"index;not null"`
	IdentityVersion string     `json:"identity_version" gorm:"type:varchar(128);not null"`
	AuthenticatedAt *time.Time `json:"authenticated_at,omitempty" gorm:"index"`
	AuthMethods     string     `json:"auth_methods,omitempty" gorm:"type:varchar(128)"`
	ReauthNonceHash *string    `json:"-" gorm:"type:char(64);index"`
	ExpiresAt       time.Time  `json:"expires_at" gorm:"index;not null"`
	LastSeenAt      time.Time  `json:"last_seen_at" gorm:"index;not null"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty" gorm:"index"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (AdminSession) TableName() string { return "claw_admin_sessions" }

type ServiceNonce struct {
	ID          uint64    `json:"id" gorm:"primaryKey"`
	ServiceName string    `json:"service_name" gorm:"type:varchar(48);uniqueIndex:idx_claw_service_nonce,priority:1;not null"`
	Nonce       string    `json:"nonce" gorm:"type:varchar(96);uniqueIndex:idx_claw_service_nonce,priority:2;not null"`
	ExpiresAt   time.Time `json:"expires_at" gorm:"index;not null"`
	CreatedAt   time.Time `json:"created_at"`
}

func (ServiceNonce) TableName() string { return "claw_service_nonces" }
