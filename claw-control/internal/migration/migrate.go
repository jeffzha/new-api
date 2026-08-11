package migration

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const InitialSchemaVersion = "0001_initial_control_plane"

const InternalIdentityVersion = "0002_internal_identity_contracts"
const CredentialIndexVersion = "0003_credential_profile_versioning"
const IdentityVersion = "0004_new_api_identity_version"
const BrowserSessionVersion = "0005_entry_tickets_and_control_sessions"
const AdminSessionVersion = "0006_admin_surface_sessions"
const ADPAccountBindingVersion = "0007_unique_adp_account_bindings"
const SSOBrowserBindingVersion = "0008_sso_browser_binding"
const ResourceBindingVersion = "0009_resource_bindings"
const EvidenceStoreVersion = "0010_encrypted_evidence_store"
const GovernanceVersion = "0011_p1_governance"
const MultiContextVersion = "0012_multi_context_selection"
const TencentBillingImportVersion = "0013_tencent_billing_import"
const CredentialOwnerScopeVersion = "0014_credential_owner_scope"
const ProviderFingerprintVersion = "0015_provider_secret_fingerprints"
const MembershipScopeVersion = "0016_customer_membership_scope"
const UsageAuditRevisionVersion = "0017_usage_audit_revisions"
const AppMigrationReadinessVersion = "0018_app_migration_readiness"
const CrossComponentRetentionVersion = "0019_cross_component_retention"
const AppMigrationLineageVersion = "0020_app_migration_lineage"
const AgentStoreVersion = "0021_agent_store"
const AgentStoreRuntimeContractVersion = "0022_agent_store_runtime_contract"
const AppMigrationSourceRuntimeVersion = "0023_app_migration_source_runtime"
const AdminRecentAuthVersion = "0024_admin_recent_auth"

type SchemaMigration struct {
	Version   string    `gorm:"type:varchar(96);primaryKey"`
	AppliedAt time.Time `gorm:"not null"`
}

func (SchemaMigration) TableName() string { return "claw_schema_migrations" }

func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&SchemaMigration{}); err != nil {
		return err
	}
	migrations := []struct {
		version string
		models  []any
	}{
		{version: InitialSchemaVersion, models: []any{
			&model.Customer{}, &model.CustomerMember{}, &model.IdentityBinding{},
			&model.CredentialProfile{}, &model.CustomerApp{}, &model.AppConfigVersion{},
			&model.AppVerification{}, &model.Plan{}, &model.PlanVersion{}, &model.PlanPeriod{},
			&model.CustomerInvoice{}, &model.UsageAudit{}, &model.AdminAudit{}, &model.ControlOutbox{},
		}},
		{version: InternalIdentityVersion, models: []any{&model.SSOTicket{}, &model.ServiceNonce{}}},
		{version: CredentialIndexVersion, models: []any{&model.CredentialProfile{}}},
		{version: IdentityVersion, models: []any{&model.IdentityBinding{}, &model.SSOTicket{}}},
		{version: BrowserSessionVersion, models: []any{&model.EntryTicket{}, &model.ControlSession{}}},
		{version: AdminSessionVersion, models: []any{&model.EntryTicket{}, &model.AdminSession{}}},
		{version: ADPAccountBindingVersion, models: []any{&model.ADPAccountBinding{}}},
		{version: SSOBrowserBindingVersion, models: []any{&model.SSOTicket{}}},
		{version: ResourceBindingVersion, models: []any{&model.ResourceBinding{}}},
		{version: EvidenceStoreVersion, models: []any{&model.EvidenceObject{}}},
		{version: GovernanceVersion, models: []any{
			&model.CredentialProfile{}, &model.GovernanceNotification{},
			&model.CustomerRetentionPolicy{}, &model.CustomerRetentionRun{},
			&model.GovernanceApproval{},
		}},
		{version: MultiContextVersion, models: []any{
			&model.ControlSession{}, &model.SSOTicket{}, &model.ContextSelectionNonce{},
		}},
		{version: TencentBillingImportVersion, models: []any{
			&model.UsageAudit{}, &model.TencentBillingImportRun{}, &model.TencentBillingImportCoordinator{},
		}},
		{version: CredentialOwnerScopeVersion},
		{version: ProviderFingerprintVersion},
		{version: MembershipScopeVersion},
		{version: UsageAuditRevisionVersion, models: []any{&model.UsageAuditRevision{}}},
		{version: AppMigrationReadinessVersion, models: []any{&model.AppMigrationJob{}, &model.AppMigrationMember{}}},
		{version: CrossComponentRetentionVersion, models: []any{&model.CustomerRetentionDelivery{}}},
		{version: AppMigrationLineageVersion, models: []any{&model.AppMigrationLineage{}}},
		{version: AgentStoreVersion, models: []any{
			&model.AgentCatalogItem{}, &model.AgentCatalogVersion{},
			&model.CustomerAgentDeployment{}, &model.AgentCatalogEntitlement{},
			&model.AgentLaunchAudit{}, &model.AgentCatalogCursor{}, &model.ControlSession{}, &model.AppVerification{},
		}},
		{version: AgentStoreRuntimeContractVersion, models: []any{
			&model.ContextSelectionNonce{}, &model.AppMigrationJob{},
		}},
		{version: AppMigrationSourceRuntimeVersion, models: []any{&model.AppMigrationLineage{}}},
		{version: AdminRecentAuthVersion},
	}
	for _, migration := range migrations {
		var count int64
		if err := db.Model(&SchemaMigration{}).Where("version = ?", migration.version).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		if err := db.AutoMigrate(migration.models...); err != nil {
			return err
		}
		if migration.version == GovernanceVersion {
			if err := db.Model(&model.CredentialProfile{}).Where("row_version = ?", 0).Updates(map[string]any{
				"row_version":  1,
				"activated_at": gorm.Expr("COALESCE(activated_at, created_at)"),
			}).Error; err != nil {
				return err
			}
		}
		if migration.version == MultiContextVersion {
			if err := migrateMultiContext(db); err != nil {
				return err
			}
		}
		if migration.version == CredentialOwnerScopeVersion {
			if err := migrateCredentialOwnerScopes(db); err != nil {
				return err
			}
		}
		if migration.version == ProviderFingerprintVersion {
			if err := migrateProviderFingerprints(db); err != nil {
				return err
			}
		}
		if migration.version == MembershipScopeVersion {
			if err := migrateMembershipScopes(db); err != nil {
				return err
			}
		}
		if migration.version == AdminRecentAuthVersion {
			if err := migrateAdminRecentAuth(db); err != nil {
				return err
			}
		}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&SchemaMigration{
			Version: migration.version, AppliedAt: time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

type entryTicketRecentAuthColumns struct {
	ID              uint64     `gorm:"primaryKey"`
	AuthenticatedAt *time.Time `gorm:"index"`
	AuthMethods     string     `gorm:"type:varchar(128)"`
	ReauthNonceHash *string    `gorm:"type:char(64)"`
}

func (entryTicketRecentAuthColumns) TableName() string { return model.EntryTicket{}.TableName() }

type adminSessionRecentAuthColumns struct {
	ID              uint64     `gorm:"primaryKey"`
	AuthenticatedAt *time.Time `gorm:"index"`
	AuthMethods     string     `gorm:"type:varchar(128)"`
	ReauthNonceHash *string    `gorm:"type:char(64);index"`
}

func (adminSessionRecentAuthColumns) TableName() string { return model.AdminSession{}.TableName() }

func migrateAdminRecentAuth(db *gorm.DB) error {
	// SQLite cannot add a UNIQUE column to an existing table. Add the nullable
	// proof columns first, then create the replay-prevention index separately;
	// the same sequence is valid for MySQL and PostgreSQL upgrades.
	if err := db.AutoMigrate(&entryTicketRecentAuthColumns{}, &adminSessionRecentAuthColumns{}); err != nil {
		return err
	}
	const nonceIndex = "idx_claw_entry_reauth_nonce"
	if db.Migrator().HasIndex(&model.EntryTicket{}, nonceIndex) {
		return nil
	}
	return db.Exec("CREATE UNIQUE INDEX " + nonceIndex + " ON claw_entry_tickets (reauth_nonce_hash)").Error
}

func migrateMembershipScopes(db *gorm.DB) error {
	var members []model.CustomerMember
	if err := db.Order("id asc").Find(&members).Error; err != nil {
		return err
	}
	for index := range members {
		slot := strings.TrimSpace(members[index].MembershipSlot)
		if slot != "" && slot != "primary" {
			continue
		}
		if members[index].Status == model.MemberStatusActive {
			slot = model.MembershipSlot(members[index].CustomerID)
		} else {
			slot = fmt.Sprintf("historical:%d", members[index].ID)
		}
		if err := db.Model(&members[index]).Update("membership_slot", slot).Error; err != nil {
			return err
		}
	}

	var duplicates int64
	duplicateQuery := db.Model(&model.CustomerMember{}).
		Select("new_api_user_id, membership_slot").
		Group("new_api_user_id, membership_slot").
		Having("COUNT(*) > 1")
	if err := db.Table("(?) AS duplicate_membership_scopes", duplicateQuery).Count(&duplicates).Error; err != nil {
		return err
	}
	if duplicates != 0 {
		return fmt.Errorf("membership-scope migration found %d duplicate user/slot assignments", duplicates)
	}
	if !db.Migrator().HasIndex(&model.CustomerMember{}, "idx_claw_member_user_slot") {
		if err := db.Migrator().CreateIndex(&model.CustomerMember{}, "idx_claw_member_user_slot"); err != nil {
			return err
		}
	}
	return nil
}

type credentialFingerprintColumns struct {
	FingerprintVersion *int
}

func (credentialFingerprintColumns) TableName() string { return "claw_credential_profiles" }

type appFingerprintColumns struct {
	AppKeyFingerprintVersion   *int
	CredentialChangeApprovalID *uint64
	RowVersion                 *int64
}

func (appFingerprintColumns) TableName() string { return "claw_app_config_versions" }

func migrateProviderFingerprints(db *gorm.DB) error {
	migrator := db.Migrator()
	for _, column := range []string{"FingerprintVersion"} {
		if !migrator.HasColumn(&model.CredentialProfile{}, column) {
			if err := migrator.AddColumn(&credentialFingerprintColumns{}, column); err != nil {
				return err
			}
		}
	}
	if migrator.HasTable(&model.AppConfigVersion{}) {
		for _, column := range []string{"AppKeyFingerprintVersion", "CredentialChangeApprovalID", "RowVersion"} {
			if !migrator.HasColumn(&model.AppConfigVersion{}, column) {
				if err := migrator.AddColumn(&appFingerprintColumns{}, column); err != nil {
					return err
				}
			}
		}
		if err := db.Model(&model.AppConfigVersion{}).Where("app_key_fingerprint_version IS NULL").Update("app_key_fingerprint_version", 0).Error; err != nil {
			return err
		}
		if err := db.Model(&model.AppConfigVersion{}).Where("row_version IS NULL OR row_version = ?", 0).Update("row_version", 1).Error; err != nil {
			return err
		}
		if err := db.AutoMigrate(&model.AppConfigVersion{}); err != nil {
			return err
		}
	}
	if err := db.Model(&model.CredentialProfile{}).Where("fingerprint_version IS NULL").Update("fingerprint_version", 0).Error; err != nil {
		return err
	}
	return db.AutoMigrate(&model.CredentialProfile{})
}

type credentialOwnerScopeColumns struct {
	OwnerScope *string `gorm:"type:varchar(191)"`
	CustomerID *uint64 `gorm:"index"`
}

func (credentialOwnerScopeColumns) TableName() string { return "claw_credential_profiles" }

func migrateCredentialOwnerScopes(db *gorm.DB) error {
	migrator := db.Migrator()
	if !migrator.HasColumn(&model.CredentialProfile{}, "OwnerScope") {
		if err := migrator.AddColumn(&credentialOwnerScopeColumns{}, "OwnerScope"); err != nil {
			return err
		}
	}
	if !migrator.HasColumn(&model.CredentialProfile{}, "CustomerID") {
		if err := migrator.AddColumn(&credentialOwnerScopeColumns{}, "CustomerID"); err != nil {
			return err
		}
	}
	if err := db.Model(&model.CredentialProfile{}).
		Where("owner_scope IS NULL OR owner_scope = ?", "").
		Updates(map[string]any{"owner_scope": "platform", "customer_id": nil}).Error; err != nil {
		return err
	}
	if migrator.HasIndex(&model.CredentialProfile{}, "idx_claw_credential_version") {
		if err := migrator.DropIndex(&model.CredentialProfile{}, "idx_claw_credential_version"); err != nil {
			return err
		}
	}
	if !migrator.HasIndex(&model.CredentialProfile{}, "idx_claw_credential_owner_version") {
		if err := migrator.CreateIndex(&model.CredentialProfile{}, "idx_claw_credential_owner_version"); err != nil {
			return err
		}
	}
	var invalid int64
	if err := db.Model(&model.CredentialProfile{}).Where("owner_scope IS NULL OR owner_scope = ?", "").Count(&invalid).Error; err != nil {
		return err
	}
	if invalid != 0 {
		return fmt.Errorf("credential owner-scope migration left %d invalid rows", invalid)
	}
	return nil
}

func migrateMultiContext(db *gorm.DB) error {
	if db.Migrator().HasIndex(&model.CustomerMember{}, "idx_claw_member_primary") {
		if err := db.Migrator().DropIndex(&model.CustomerMember{}, "idx_claw_member_primary"); err != nil {
			return err
		}
	}
	for _, field := range []string{"Selector", "Alias"} {
		if !db.Migrator().HasColumn(&model.CustomerApp{}, field) {
			if err := db.Migrator().AddColumn(&model.CustomerApp{}, field); err != nil {
				return err
			}
		}
	}
	var apps []model.CustomerApp
	if err := db.Order("id asc").Find(&apps).Error; err != nil {
		return err
	}
	for index := range apps {
		updates := map[string]any{}
		if apps[index].Selector == "" {
			updates["selector"] = support.PublicID("aps")
		}
		if apps[index].Alias == "" {
			switch {
			case apps[index].Slot == "primary":
				updates["alias"] = "primary"
			case strings.HasPrefix(apps[index].Slot, "migration:"):
				updates["alias"] = fmt.Sprintf("migration-%d", apps[index].ID)
			case strings.HasPrefix(apps[index].Slot, "archived:"):
				updates["alias"] = fmt.Sprintf("archived-%d", apps[index].ID)
			default:
				updates["alias"] = fmt.Sprintf("app-%d", apps[index].ID)
			}
		}
		if len(updates) > 0 {
			if err := db.Model(&apps[index]).Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	if !db.Migrator().HasIndex(&model.CustomerApp{}, "Selector") {
		if err := db.Migrator().CreateIndex(&model.CustomerApp{}, "Selector"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasIndex(&model.CustomerApp{}, "idx_claw_app_customer_alias") {
		if err := db.Migrator().CreateIndex(&model.CustomerApp{}, "idx_claw_app_customer_alias"); err != nil {
			return err
		}
	}
	var members []model.CustomerMember
	if err := db.Where("status = ? AND membership_slot = ?", model.MemberStatusActive, "primary").Find(&members).Error; err != nil {
		return err
	}
	for index := range members {
		if err := db.Model(&members[index]).Update("membership_slot", model.MembershipSlot(members[index].CustomerID)).Error; err != nil {
			return err
		}
	}
	return db.Model(&model.ControlSession{}).Where("selection_state = ? OR selection_state IS NULL", "").Update("selection_state", model.ControlSessionStateSelected).Error
}
