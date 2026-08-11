# Migrations

The executable migration registry lives in `internal/migration`. The current versions are:

- `0001_initial_control_plane`: customer, identity, App, fixed-plan, invoice, audit, and outbox tables;
- `0002_internal_identity_contracts`: single-use SSO tickets and HMAC replay nonces;
- `0003_credential_profile_versioning`: credential-profile version uniqueness.
- `0004_new_api_identity_version`: authoritative new-api identity fingerprint on bindings and SSO tickets.
- `0005_entry_tickets_and_control_sessions`: isolates browser entry tickets from browser control sessions.
- `0006_admin_surface_sessions`: adds entry-ticket surface assertions and independent admin/CSRF sessions.
- `0007_unique_adp_account_bindings`: enforces canonical ADP account ownership.
- `0008_sso_browser_binding`: binds an SSO ticket to the initiating browser.
- `0009_resource_bindings`: mirrors customer-scoped ADP resource ownership.
- `0010_encrypted_evidence_store`: adds encrypted compliance evidence metadata.
- `0011_p1_governance`: adds notifications, retention, approvals, and credential lifecycle state.
- `0012_multi_context_selection`: removes the global single-customer membership slot, adds stable App selectors/aliases, selected-session state, SSO-to-session binding, and hashed one-time context-selection nonces.
- `0013_tencent_billing_import`: adds read-only Fee Center import jobs, account coordination, and imported usage-draft idempotency.
- `0014_credential_owner_scope`: backfills existing credential profiles to `platform`, adds nullable customer ownership, and replaces the old version index with owner/provider/name/version uniqueness.
- `0015_provider_secret_fingerprints`: adds canonical fingerprint versions, App-config row versions, and credential-change approval binding. Existing fingerprints are marked version 0 and remain untrusted until the explicit audited re-enrollment operation derives version-1 values from the currently injected runtime secrets.
- `0016_customer_membership_scope`: backfills legacy active `primary`/empty membership slots to `customer:<customer_id>` (disabled rows to `historical:<id>`), rejects duplicate `(new_api_user_id,membership_slot)` assignments, and creates the portable composite unique index required for multi-customer membership isolation.
- `0017_usage_audit_revisions`: adds immutable links from a locked cost audit to its atomically applied replacement, preserving the original evidence while excluding the superseded row from effective margin totals.
- `0018_app_migration_readiness`: adds generation-versioned AppId migration jobs and per-active-binding Agent rebuild/readback readiness with hashed worker leases and optimistic recovery versions.
- `0021_agent_store`: adds immutable Agent/Application catalog versions, customer deployments, entitlements, launch audits, and control-session CSRF binding. All provider secrets remain referenced by existing customer App configuration.
- `0022_agent_store_runtime_contract`: upgrades already-migrated context-selection nonces with purpose-bound Agent Store item/deployment/version snapshots and App migration jobs with the verified provider mode/runtime/execution tuple.
- `0023_app_migration_source_runtime`: snapshots the verified source provider mode/runtime on immutable migration lineage rows so historical reads always carry an explicit read-only runtime contract; legacy zero-value lineages retain the original dynamic-Claw interpretation.
- `0024_admin_recent_auth`: adds trusted authentication time, AMR, and one-time reauthentication nonce hashes to administrator entry tickets and sessions. Existing sessions remain read-only until an explicit new-api password, 2FA, or Passkey step-up succeeds.

Versions use GORM migration primitives plus bounded data backfills so the same schema can be created on SQLite, MySQL 5.7.8+, and PostgreSQL 9.6+ without dialect-specific SQL.

Future schema changes must add a new immutable migration version. Do not edit an already-applied migration to change production tables.
