package model

// Agency Hub models are deliberately isolated from the legacy reseller hub.
// They use scalar references to core users/tokens so sidecar migrations never
// create foreign keys or alter core tables.

const AgencyTablePrefix = "agency_hub_"

type Agency struct {
	ID                     int64  `gorm:"primaryKey" json:"id"`
	Code                   string `gorm:"size:64;not null;uniqueIndex:uidx_agency_code" json:"code"`
	DisplayName            string `gorm:"size:191;not null" json:"display_name"`
	Status                 string `gorm:"size:32;not null;index:idx_agency_status" json:"status"`
	InviteCode             string `gorm:"size:16;not null;uniqueIndex:uidx_agency_invite_code" json:"invite_code"`
	CurrentPolicyVersionID int64  `gorm:"not null" json:"current_policy_version_id"`
	PriceRevision          int64  `gorm:"not null" json:"price_revision"`
	StateRevision          int64  `gorm:"not null" json:"state_revision"`
	Version                int64  `gorm:"not null" json:"version"`
	CreatedByType          string `gorm:"size:32;not null" json:"created_by_type"`
	CreatedByID            int64  `gorm:"not null" json:"created_by_id"`
	DisabledReason         string `gorm:"type:text" json:"disabled_reason,omitempty"`
	DisabledAt             *int64 `json:"disabled_at,omitempty"`
	CreatedAt              int64  `gorm:"not null;index:idx_agency_created" json:"created_at"`
	UpdatedAt              int64  `gorm:"not null" json:"updated_at"`
}

func (Agency) TableName() string { return AgencyTablePrefix + "agencies" }

type AgencyOperatorAccount struct {
	ID                 int64  `gorm:"primaryKey" json:"id"`
	AgencyID           int64  `gorm:"not null;uniqueIndex:uidx_agency_operator_agency" json:"agency_id"`
	NormalizedUsername string `gorm:"size:191;not null;uniqueIndex:uidx_agency_operator_username" json:"normalized_username"`
	Username           string `gorm:"size:191;not null" json:"username"`
	PasswordHash       string `gorm:"type:text;not null" json:"-"`
	Status             string `gorm:"size:32;not null;index:idx_agency_operator_status" json:"status"`
	MustChangePassword bool   `gorm:"not null" json:"must_change_password"`
	AuthVersion        int64  `gorm:"not null" json:"auth_version"`
	FailedCount        int    `gorm:"not null" json:"-"`
	LockedUntil        int64  `gorm:"not null" json:"-"`
	LastLoginAt        int64  `gorm:"not null" json:"last_login_at"`
	CreatedAt          int64  `gorm:"not null" json:"created_at"`
	UpdatedAt          int64  `gorm:"not null" json:"updated_at"`
}

func (AgencyOperatorAccount) TableName() string { return AgencyTablePrefix + "operator_accounts" }

type AgencySession struct {
	ID                   int64  `gorm:"primaryKey" json:"id"`
	TokenHash            string `gorm:"size:128;not null;uniqueIndex:uidx_agency_session_token" json:"-"`
	ActorType            string `gorm:"size:32;not null" json:"actor_type"`
	ActorID              int64  `gorm:"not null;index:idx_agency_session_actor" json:"actor_id"`
	AgencyID             *int64 `gorm:"index:idx_agency_session_agency" json:"agency_id,omitempty"`
	SourceSID            string `gorm:"size:191" json:"-"`
	SourceUserID         int64  `json:"-"`
	SourceSessionVersion int64  `json:"-"`
	AuthVersion          int64  `gorm:"not null" json:"-"`
	CSRFHash             string `gorm:"size:128;not null" json:"-"`
	LastSeenAt           int64  `gorm:"not null" json:"-"`
	CreatedAt            int64  `gorm:"not null" json:"-"`
	ExpiresAt            int64  `gorm:"not null;index:idx_agency_session_expiry" json:"-"`
	RevokedAt            *int64 `json:"-"`
}

func (AgencySession) TableName() string { return AgencyTablePrefix + "sessions" }

type AgencySSOTicketUse struct {
	ID         int64  `gorm:"primaryKey"`
	JTI        string `gorm:"size:128;not null;uniqueIndex:uidx_agency_sso_jti"`
	ActorID    int64  `gorm:"not null"`
	SourceSID  string `gorm:"size:191;not null"`
	ExpiresAt  int64  `gorm:"not null"`
	ConsumedAt int64  `gorm:"not null"`
	CreatedAt  int64  `gorm:"not null"`
}

func (AgencySSOTicketUse) TableName() string { return AgencyTablePrefix + "sso_ticket_uses" }

type AgencyVerificationUse struct {
	ID         int64  `gorm:"primaryKey"`
	JTI        string `gorm:"size:128;not null;uniqueIndex:uidx_agency_verify_jti"`
	ActorType  string `gorm:"size:32;not null"`
	ActorID    int64  `gorm:"not null"`
	Action     string `gorm:"size:128;not null"`
	ObjectID   string `gorm:"size:191;not null"`
	BodyHash   string `gorm:"size:128;not null"`
	ExpiresAt  int64  `gorm:"not null"`
	ConsumedAt *int64
	CreatedAt  int64 `gorm:"not null"`
}

func (AgencyVerificationUse) TableName() string { return AgencyTablePrefix + "verification_uses" }

type AgencyDeliverySecret struct {
	ID int64 `gorm:"primaryKey"`
	// OperationID is an opaque handle derived from the original Root request.
	// The delivery id alone is not a sufficient capability to recover it.
	OperationID       string `gorm:"size:128;not null;uniqueIndex:uidx_agency_delivery_operation"`
	CreatorRootID     int64  `gorm:"not null"`
	AgencyID          int64  `gorm:"index:idx_agency_delivery_agency"`
	OperatorAccountID int64  `gorm:"index:idx_agency_delivery_account"`
	SourceSID         string `gorm:"size:191;index:idx_agency_delivery_source"`
	Action            string `gorm:"size:128"`
	ObjectID          string `gorm:"size:191"`
	BodyHash          string `gorm:"size:128"`
	IdempotencyKey    string `gorm:"size:128"`
	Ciphertext        string `gorm:"type:text;not null"`
	KeyID             string `gorm:"size:64;not null"`
	AADSchema         string `gorm:"size:32"`
	ExpiresAt         int64  `gorm:"not null;index:idx_agency_delivery_expiry"`
	DeliveredAt       *int64
	ExpiredAt         *int64
	CreatedAt         int64 `gorm:"not null"`
}

func (AgencyDeliverySecret) TableName() string { return AgencyTablePrefix + "delivery_secrets" }

type AgencyUserBinding struct {
	ID             int64  `gorm:"primaryKey"`
	UserID         int64  `gorm:"not null;index:idx_agency_binding_user"`
	AgencyID       int64  `gorm:"not null;index:idx_agency_binding_agency"`
	Revision       int64  `gorm:"not null"`
	InviteSnapshot string `gorm:"size:16;not null"`
	CreatedSource  string `gorm:"size:32;not null"`
	EffectiveAtMS  int64  `gorm:"not null"`
	EndedAtMS      *int64
	RootActorID    int64
	Reason         string `gorm:"type:text"`
	CreatedAt      int64  `gorm:"not null"`
}

func (AgencyUserBinding) TableName() string { return AgencyTablePrefix + "user_bindings" }

type AgencyActiveUserBinding struct {
	UserID    int64 `gorm:"primaryKey"`
	BindingID int64 `gorm:"not null;uniqueIndex:uidx_agency_active_binding"`
	Revision  int64 `gorm:"not null"`
	AgencyID  int64 `gorm:"not null;index:idx_agency_active_agency"`
	UpdatedAt int64 `gorm:"not null"`
}

func (AgencyActiveUserBinding) TableName() string { return AgencyTablePrefix + "active_user_bindings" }

type AgencyPricePolicyVersion struct {
	ID            int64  `gorm:"primaryKey"`
	AgencyID      int64  `gorm:"not null;index:idx_agency_policy_agency;uniqueIndex:uidx_agency_policy_revision,priority:1"`
	Revision      int64  `gorm:"not null;uniqueIndex:uidx_agency_policy_revision,priority:2"`
	PolicyJSON    string `gorm:"type:text;not null"`
	PolicyHash    string `gorm:"size:128;not null"`
	CreatedByType string `gorm:"size:32;not null"`
	CreatedByID   int64  `gorm:"not null"`
	Reason        string `gorm:"type:text"`
	CreatedAtMS   int64  `gorm:"not null"`
}

func (AgencyPricePolicyVersion) TableName() string {
	return AgencyTablePrefix + "price_policy_versions"
}

type AgencyPricePolicyItem struct {
	ID                    int64  `gorm:"primaryKey"`
	PolicyVersionID       int64  `gorm:"not null;uniqueIndex:uidx_agency_policy_item,priority:1;index:idx_agency_policy_item_version"`
	Scope                 string `gorm:"size:32;not null;uniqueIndex:uidx_agency_policy_item,priority:2"`
	ModelKey              string `gorm:"size:64;not null;uniqueIndex:uidx_agency_policy_item,priority:3"`
	OriginModelName       string `gorm:"size:764;not null"`
	SettlementBPS         *int
	SalesBPS              *int
	ResolvedSettlementBPS int
	ResolvedSalesBPS      int
}

func (AgencyPricePolicyItem) TableName() string { return AgencyTablePrefix + "price_policy_items" }

type AgencyIdempotencyRecord struct {
	ID           int64  `gorm:"primaryKey"`
	ScopeHash    string `gorm:"size:128;not null;uniqueIndex:uidx_agency_idempotency_scope"`
	ActorType    string `gorm:"size:32;not null"`
	ActorID      int64  `gorm:"not null"`
	Action       string `gorm:"size:128;not null"`
	BodyHash     string `gorm:"size:128;not null"`
	ResourceID   string `gorm:"size:191"`
	ResultCode   int    `gorm:"not null"`
	ResponseJSON string `gorm:"type:text"`
	ExpiresAt    int64  `gorm:"not null"`
	CreatedAt    int64  `gorm:"not null"`
}

func (AgencyIdempotencyRecord) TableName() string { return AgencyTablePrefix + "idempotency_records" }

type AgencyFundingAccount struct {
	UserID           int64  `gorm:"primaryKey"`
	PaidAvailable    int64  `gorm:"not null"`
	NonpaidAvailable int64  `gorm:"not null"`
	DebtQuota        int64  `gorm:"not null"`
	MoneySeq         int64  `gorm:"not null"`
	Version          int64  `gorm:"not null"`
	OpeningSnapshot  string `gorm:"type:text"`
	ReconcileBlocked bool   `gorm:"not null"`
	ReconcileReason  string `gorm:"type:text"`
	UpdatedAt        int64  `gorm:"not null"`
}

func (AgencyFundingAccount) TableName() string { return AgencyTablePrefix + "funding_accounts" }

type AgencyFundingLot struct {
	ID               int64  `gorm:"primaryKey"`
	UserID           int64  `gorm:"not null;index:idx_agency_funding_lot_user_seq"`
	SourceKind       string `gorm:"size:32;not null"`
	SourceID         string `gorm:"size:191;not null"`
	CompletionSource string `gorm:"size:32;not null"`
	PaidInitial      int64  `gorm:"not null"`
	BonusInitial     int64  `gorm:"not null"`
	PaidAvailable    int64  `gorm:"not null"`
	PaidReserved     int64  `gorm:"not null"`
	PaidConsumed     int64  `gorm:"not null"`
	PaidRevoked      int64  `gorm:"not null"`
	// Keep bonus balances on the originating lot as well as in the account
	// aggregate so a payment refund remains attributable to its top-up.
	BonusAvailable int64 `gorm:"not null;default:0"`
	BonusReserved  int64 `gorm:"not null;default:0"`
	BonusConsumed  int64 `gorm:"not null;default:0"`
	BonusRevoked   int64 `gorm:"not null;default:0"`
	PaidDebtRepaid int64 `gorm:"not null;default:0"`
	MoneySeq       int64 `gorm:"not null;index:idx_agency_funding_lot_user_seq"`
	Version        int64 `gorm:"not null"`
	CreatedAt      int64 `gorm:"not null"`
}

func (AgencyFundingLot) TableName() string { return AgencyTablePrefix + "funding_lots" }

type AgencyFundingAllocation struct {
	ID              int64  `gorm:"primaryKey"`
	ChargeID        string `gorm:"size:128;not null;index:idx_agency_alloc_charge"`
	SegmentNo       int    `gorm:"not null"`
	ComponentID     string `gorm:"size:128;not null"`
	UserID          int64  `gorm:"not null;index:idx_agency_alloc_user"`
	LotID           int64  `gorm:"not null;index:idx_agency_alloc_lot"`
	Reserved        int64  `gorm:"not null"`
	Consumed        int64  `gorm:"not null"`
	NonpaidConsumed int64  `gorm:"not null"`
	DebtConsumed    int64  `gorm:"not null"`
	Released        int64  `gorm:"not null"`
	Reversed        int64  `gorm:"not null"`
	// RevokedReservedDebt records paid quota that was already allocated to a
	// charge when its source payment was reversed. It remains part of the
	// original charge for audit/reconciliation, but must not become available
	// again if the model charge is later cancelled.
	RevokedReservedDebt int64 `gorm:"not null;default:0"`
	// RevokedNonpaid is the bonus/non-paid counterpart. A later cancellation
	// must not restore bonus quota that a payment reversal already removed.
	RevokedNonpaid int64 `gorm:"not null;default:0"`
	Version        int64 `gorm:"not null"`
}

func (AgencyFundingAllocation) TableName() string { return AgencyTablePrefix + "funding_allocations" }

type AgencyFundingLedger struct {
	ID           int64  `gorm:"primaryKey"`
	OperationID  string `gorm:"size:128;not null;uniqueIndex:uidx_agency_funding_ledger_operation,priority:1"`
	EntryNo      int    `gorm:"not null;uniqueIndex:uidx_agency_funding_ledger_operation,priority:2"`
	UserID       int64  `gorm:"not null;index:idx_agency_funding_ledger_user_seq"`
	MoneySeq     int64  `gorm:"not null;index:idx_agency_funding_ledger_user_seq"`
	SourceKind   string `gorm:"size:32;not null"`
	LotID        *int64
	PaidDelta    int64 `gorm:"not null"`
	NonpaidDelta int64 `gorm:"not null"`
	DebtDelta    int64 `gorm:"not null"`
	PaidAfter    int64 `gorm:"not null"`
	NonpaidAfter int64 `gorm:"not null"`
	DebtAfter    int64 `gorm:"not null"`
	AgencyID     *int64
	BindingID    *int64
	CurrencyCode string `gorm:"size:16"`
	CreatedAtMS  int64  `gorm:"not null"`
}

func (AgencyFundingLedger) TableName() string { return AgencyTablePrefix + "funding_ledger" }

type AgencyBillingJournal struct {
	ID                         int64  `gorm:"primaryKey"`
	ChargeID                   string `gorm:"size:128;not null;uniqueIndex:uidx_agency_journal_charge,priority:1"`
	SegmentNo                  int    `gorm:"not null;uniqueIndex:uidx_agency_journal_charge,priority:2"`
	UserID                     int64  `gorm:"not null;index:idx_agency_journal_user"`
	TokenID                    *int64
	TaskID                     *int64
	Status                     string `gorm:"size:32;not null;index:idx_agency_journal_status"`
	BusinessStatus             string `gorm:"size:32;not null"`
	DeliveryStatus             string `gorm:"size:32;not null"`
	PricingSnapshot            string `gorm:"type:text;not null"`
	BillingBasis               string `gorm:"type:text;not null"`
	ReserveQuota               int64
	ChargedTotalQuota          int64
	CommissionableQuota        int64
	SettlementCostQuota        int64
	TheoreticalCommissionQuota int64
	PaidAllocatedQuota         int64
	CommissionQuota            int64
	CommissionAmountMicros     int64
	// UsageHash and LastCumulativeUsage make realtime segment processing
	// restart-safe. A duplicate frame with the same hash is a no-op; a
	// conflicting hash for the same charge/segment is an integrity error.
	UsageHash               string `gorm:"size:128;index:idx_agency_journal_usage_hash"`
	LastCumulativeUsage     string `gorm:"type:text"`
	ReversedQuota           int64
	ReversedCommissionQuota int64
	CurrencyCode            string `gorm:"size:16"`
	Revision                int64  `gorm:"not null"`
	Version                 int64  `gorm:"not null"`
	LastError               string `gorm:"type:text"`
	CreatedAtMS             int64  `gorm:"not null"`
	UpdatedAtMS             int64  `gorm:"not null;index:idx_agency_journal_updated"`
}

func (AgencyBillingJournal) TableName() string { return AgencyTablePrefix + "billing_journals" }

type AgencyBillingOperation struct {
	ID              int64  `gorm:"primaryKey"`
	ChargeID        string `gorm:"size:128;not null;index:idx_agency_operation_charge;uniqueIndex:uidx_agency_operation,priority:1"`
	SegmentNo       int    `gorm:"not null;uniqueIndex:uidx_agency_operation,priority:2"`
	Revision        int64  `gorm:"not null;uniqueIndex:uidx_agency_operation,priority:3"`
	Operation       string `gorm:"size:32;not null;uniqueIndex:uidx_agency_operation,priority:4"`
	InputHash       string `gorm:"size:128;not null"`
	CommittedResult string `gorm:"type:text"`
	MoneySeq        int64
	EventCount      int
	UsageHash       string `gorm:"size:128"`
	CreatedAtMS     int64  `gorm:"not null"`
}

func (AgencyBillingOperation) TableName() string { return AgencyTablePrefix + "billing_operations" }

type AgencyBillingOutbox struct {
	ID            int64  `gorm:"primaryKey"`
	EventID       string `gorm:"size:128;not null;uniqueIndex:uidx_agency_outbox_event"`
	OperationID   string `gorm:"size:128;not null;uniqueIndex:uidx_agency_outbox_operation,priority:1"`
	EventIndex    int    `gorm:"not null;uniqueIndex:uidx_agency_outbox_operation,priority:2"`
	EventCount    int    `gorm:"not null"`
	EventKind     string `gorm:"size:64;not null"`
	UserID        int64  `gorm:"not null;index:idx_agency_outbox_user_seq"`
	MoneySeq      int64  `gorm:"not null;index:idx_agency_outbox_user_seq"`
	Payload       string `gorm:"type:text;not null"`
	PayloadHash   string `gorm:"size:128;not null"`
	SchemaVersion string `gorm:"size:64;not null"`
	CreatedAtMS   int64  `gorm:"not null"`
}

func (AgencyBillingOutbox) TableName() string { return AgencyTablePrefix + "billing_outbox" }

type AgencyEventDelivery struct {
	ID          int64  `gorm:"primaryKey"`
	EventID     string `gorm:"size:128;not null;uniqueIndex:uidx_agency_delivery_event"`
	Status      string `gorm:"size:32;not null;index:idx_agency_delivery_status_retry"`
	LeaseOwner  string `gorm:"size:191"`
	LeaseToken  int64
	LeaseUntil  int64
	Attempts    int
	NextRetryAt int64  `gorm:"index:idx_agency_delivery_status_retry"`
	LastError   string `gorm:"type:text"`
	ProcessedAt *int64
	CreatedAt   int64 `gorm:"not null"`
}

func (AgencyEventDelivery) TableName() string { return AgencyTablePrefix + "event_deliveries" }

type AgencySourceEvent struct {
	ID                int64  `gorm:"primaryKey"`
	EventID           string `gorm:"size:128;not null;uniqueIndex:uidx_agency_source_event"`
	SourceOperationID string `gorm:"size:128;not null"`
	JournalRevision   int64
	SchemaVersion     string `gorm:"size:64;not null"`
	PayloadHash       string `gorm:"size:128;not null"`
	UserID            int64  `gorm:"not null;index:idx_agency_source_user"`
	MoneySeq          int64  `gorm:"not null"`
	ProcessingStatus  string `gorm:"size:32;not null;index:idx_agency_source_status"`
	SkipReason        string `gorm:"type:text"`
	Payload           string `gorm:"type:text;not null"`
	CreatedAtMS       int64  `gorm:"not null"`
}

func (AgencySourceEvent) TableName() string { return AgencyTablePrefix + "source_events" }

type AgencyUsageFact struct {
	ID               int64  `gorm:"primaryKey"`
	EventID          string `gorm:"size:128;not null;uniqueIndex:uidx_agency_usage_event_component,priority:1"`
	ComponentID      string `gorm:"size:128;not null;uniqueIndex:uidx_agency_usage_event_component,priority:2"`
	UsageHash        string `gorm:"size:128;index:idx_agency_usage_hash"`
	CumulativeUsage  string `gorm:"type:text"`
	UserID           int64  `gorm:"not null;index:idx_agency_usage_user_time"`
	AgencyID         *int64 `gorm:"index:idx_agency_usage_agency_time"`
	BindingID        *int64
	OriginModelName  string `gorm:"size:764;not null"`
	ModelKey         string `gorm:"size:64;not null;index:idx_agency_usage_model_time"`
	Endpoint         string `gorm:"size:191"`
	BusinessStatus   string `gorm:"size:32;not null"`
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	StandardQuota    int64
	SalesBPS         int
	ChargedQuota     int64
	CurrencyCode     string `gorm:"size:16"`
	OccurredAtMS     int64  `gorm:"not null;index:idx_agency_usage_agency_time"`
	SkipReason       string `gorm:"type:text"`
	LogID            *int64
}

func (AgencyUsageFact) TableName() string { return AgencyTablePrefix + "usage_facts" }

type AgencyTopupFact struct {
	ID                int64  `gorm:"primaryKey"`
	SourceOperationID string `gorm:"size:128;not null;uniqueIndex:uidx_agency_topup_source"`
	UserID            int64  `gorm:"not null;index:idx_agency_topup_user_time"`
	AgencyID          *int64 `gorm:"index:idx_agency_topup_agency_time"`
	BindingID         *int64
	PaymentReference  string `gorm:"size:191"`
	ActualMoney       string `gorm:"size:64"`
	CurrencyCode      string `gorm:"size:16"`
	CreditedQuota     int64
	PaidQuota         int64
	BonusQuota        int64
	CompletionSource  string `gorm:"size:32"`
	PaymentStatus     string `gorm:"size:32"`
	RefundedQuota     int64
	OccurredAtMS      int64 `gorm:"not null;index:idx_agency_topup_agency_time;index:idx_agency_topup_user_time"`
}

func (AgencyTopupFact) TableName() string { return AgencyTablePrefix + "topup_facts" }

// AgencyFundingReversal is the immutable idempotency/audit record for a
// payment refund or chargeback. The topup fact stores the cumulative amount;
// this table stores each external refund evidence item exactly once.
type AgencyFundingReversal struct {
	ID                int64  `gorm:"primaryKey"`
	RefundID          string `gorm:"size:128;not null;uniqueIndex:uidx_agency_funding_reversal_refund"`
	SourceOperationID string `gorm:"size:128;not null;index:idx_agency_funding_reversal_source"`
	UserID            int64  `gorm:"not null;index:idx_agency_funding_reversal_user"`
	Quota             int64  `gorm:"not null"`
	CurrencyCode      string `gorm:"size:16"`
	PaymentReference  string `gorm:"size:191"`
	EvidenceRef       string `gorm:"size:191"`
	Reason            string `gorm:"type:text"`
	CreatedAtMS       int64  `gorm:"not null"`
}

func (AgencyFundingReversal) TableName() string { return AgencyTablePrefix + "funding_reversals" }

type AgencyFundingReversalChargeRecord struct {
	ID       int64  `gorm:"primaryKey"`
	RefundID string `gorm:"size:128;not null;uniqueIndex:uidx_agency_funding_reversal_charge,priority:1"`
	ChargeID string `gorm:"size:128;not null;uniqueIndex:uidx_agency_funding_reversal_charge,priority:2"`
	Quota    int64  `gorm:"not null"`
}

func (AgencyFundingReversalChargeRecord) TableName() string {
	return AgencyTablePrefix + "funding_reversal_charges"
}

type AgencyCommissionLedger struct {
	ID                         int64  `gorm:"primaryKey"`
	EventID                    string `gorm:"size:128;not null;uniqueIndex:uidx_agency_commission_event,priority:1"`
	ComponentID                string `gorm:"size:128;not null;uniqueIndex:uidx_agency_commission_event,priority:2"`
	EntryType                  string `gorm:"size:32;not null;uniqueIndex:uidx_agency_commission_event,priority:3"`
	OriginalEntryID            *int64
	AgencyID                   int64 `gorm:"not null;index:idx_agency_commission_agency_time"`
	BindingID                  int64
	UserID                     int64
	OriginModelName            string `gorm:"size:764"`
	StandardQuota              int64
	SettlementCostQuota        int64
	TheoreticalCommissionQuota int64
	PaidAllocatedQuota         int64
	CommissionQuota            int64
	AmountMicros               int64
	CurrencyCode               string `gorm:"size:16;not null"`
	QuotaPerUnit               string `gorm:"size:64"`
	ExchangeRate               string `gorm:"size:64"`
	OccurredAtMS               int64  `gorm:"not null;index:idx_agency_commission_agency_time"`
}

func (AgencyCommissionLedger) TableName() string { return AgencyTablePrefix + "commission_ledger" }

type AgencyCommissionBalance struct {
	ID              int64  `gorm:"primaryKey"`
	AgencyID        int64  `gorm:"not null;uniqueIndex:uidx_agency_commission_balance_currency,priority:1"`
	CurrencyCode    string `gorm:"size:16;not null;uniqueIndex:uidx_agency_commission_balance_currency,priority:2"`
	EarnedMicros    int64
	ReversedMicros  int64
	AvailableMicros int64
	LockedMicros    int64
	PaidMicros      int64
	Version         int64 `gorm:"not null"`
	UpdatedAtMS     int64 `gorm:"not null"`
}

func (AgencyCommissionBalance) TableName() string { return AgencyTablePrefix + "commission_balances" }

type AgencyWithdrawalAccount struct {
	ID          int64  `gorm:"primaryKey"`
	AgencyID    int64  `gorm:"not null;index:idx_agency_withdraw_account_agency"`
	Version     int64  `gorm:"not null"`
	Ciphertext  string `gorm:"type:text;not null"`
	KeyID       string `gorm:"size:64;not null"`
	Last4       string `gorm:"size:8"`
	Status      string `gorm:"size:32;not null"`
	CreatedAtMS int64  `gorm:"not null"`
}

func (AgencyWithdrawalAccount) TableName() string { return AgencyTablePrefix + "withdrawal_accounts" }

type AgencyWithdrawal struct {
	ID                   int64  `gorm:"primaryKey"`
	RequestNo            string `gorm:"size:128;not null;uniqueIndex:uidx_agency_withdraw_no"`
	AgencyID             int64  `gorm:"not null;index:idx_agency_withdraw_agency_status"`
	CurrencyCode         string `gorm:"size:16;not null"`
	AmountMicros         int64  `gorm:"not null"`
	Status               string `gorm:"size:32;not null;index:idx_agency_withdraw_agency_status"`
	Version              int64  `gorm:"not null"`
	AccountID            int64  `gorm:"not null;index:idx_agency_withdraw_account"`
	AccountVersion       int64
	AccountSnapshotHash  string `gorm:"size:128"`
	AccountSnapshot      string `gorm:"type:text"`
	AccountSnapshotKeyID string `gorm:"size:64"`
	ReviewerID           *int64
	PaymentChannel       string `gorm:"size:64"`
	PaymentReference     string `gorm:"size:191"`
	PaymentLeaseOwner    string `gorm:"size:191"`
	PaymentLeaseToken    int64
	PaymentLeaseUntil    int64
	PreviousStatus       string `gorm:"size:32"`
	OnHoldReason         string `gorm:"type:text"`
	CreatedAtMS          int64  `gorm:"not null"`
	UpdatedAtMS          int64  `gorm:"not null"`
}

func (AgencyWithdrawal) TableName() string { return AgencyTablePrefix + "withdrawals" }

type AgencyWithdrawalPaymentReference struct {
	ID               int64  `gorm:"primaryKey"`
	PaymentChannel   string `gorm:"size:64;not null;uniqueIndex:uidx_agency_withdraw_payment_ref,priority:1"`
	PaymentReference string `gorm:"size:191;not null;uniqueIndex:uidx_agency_withdraw_payment_ref,priority:2"`
	WithdrawalID     int64  `gorm:"not null;uniqueIndex:uidx_agency_withdraw_payment_withdrawal"`
	CreatedAtMS      int64  `gorm:"not null"`
}

func (AgencyWithdrawalPaymentReference) TableName() string {
	return AgencyTablePrefix + "withdrawal_payment_references"
}

type AgencyWithdrawalTransition struct {
	ID                int64  `gorm:"primaryKey"`
	WithdrawalID      int64  `gorm:"not null;index:idx_agency_withdraw_transition"`
	OperationID       string `gorm:"size:128;not null;uniqueIndex:uidx_agency_withdraw_transition,priority:1"`
	BeforeStatus      string `gorm:"size:32;not null"`
	AfterStatus       string `gorm:"size:32;not null"`
	AmountDeltaMicros int64
	ActorType         string `gorm:"size:32;not null"`
	ActorID           int64
	Evidence          string `gorm:"type:text"`
	CreatedAtMS       int64  `gorm:"not null"`
}

func (AgencyWithdrawalTransition) TableName() string {
	return AgencyTablePrefix + "withdrawal_transitions"
}

type AgencyAuditLog struct {
	ID             int64  `gorm:"primaryKey"`
	EventID        string `gorm:"size:128;not null;uniqueIndex:uidx_agency_audit_event"`
	ActorType      string `gorm:"size:32;not null"`
	ActorID        int64  `gorm:"not null"`
	ActingAgencyID *int64
	Action         string `gorm:"size:128;not null;index:idx_agency_audit_action"`
	ObjectType     string `gorm:"size:64;not null"`
	ObjectID       string `gorm:"size:191;not null"`
	RequestID      string `gorm:"size:191;not null;index:idx_agency_audit_request"`
	Reason         string `gorm:"type:text"`
	BeforeJSON     string `gorm:"type:text"`
	AfterJSON      string `gorm:"type:text"`
	SourceIP       string `gorm:"size:64"`
	CreatedAtMS    int64  `gorm:"not null;index:idx_agency_audit_time"`
}

func (AgencyAuditLog) TableName() string { return AgencyTablePrefix + "audit_logs" }

type AgencyDailyStat struct {
	ID               int64  `gorm:"primaryKey"`
	StatDate         string `gorm:"size:10;not null;uniqueIndex:uidx_agency_daily_stat,priority:1"`
	AgencyID         int64  `gorm:"not null;uniqueIndex:uidx_agency_daily_stat,priority:2"`
	BindingID        *int64 `gorm:"uniqueIndex:uidx_agency_daily_stat,priority:3"`
	UserID           *int64 `gorm:"uniqueIndex:uidx_agency_daily_stat,priority:4"`
	ModelKey         string `gorm:"size:64;uniqueIndex:uidx_agency_daily_stat,priority:5"`
	CurrencyCode     string `gorm:"size:16;not null;uniqueIndex:uidx_agency_daily_stat,priority:6"`
	BillingSource    string `gorm:"size:32;not null;uniqueIndex:uidx_agency_daily_stat,priority:7"`
	Calls            int64
	UsageQuota       int64
	ChargedQuota     int64
	CommissionMicros int64
	ReversalMicros   int64
	Revision         int64
}

func (AgencyDailyStat) TableName() string { return AgencyTablePrefix + "daily_stats" }

type AgencyExportJob struct {
	ID                     int64  `gorm:"primaryKey"`
	ActorType              string `gorm:"size:32;not null"`
	ActorID                int64  `gorm:"not null"`
	AgencyID               *int64
	Kind                   string `gorm:"size:32;not null"`
	FilterJSON             string `gorm:"type:text;not null"`
	PermissionVersion      int64
	Status                 string `gorm:"size:32;not null;index:idx_agency_export_status"`
	FileKey                string `gorm:"size:512"`
	FileHash               string `gorm:"size:128"`
	RowCount               int64
	ExpiresAt              int64
	DownloadTokenHash      string `gorm:"size:128"`
	DownloadTokenExpiresAt int64
	DownloadTokenSessionID int64
	CreatedAtMS            int64 `gorm:"not null"`
}

func (AgencyExportJob) TableName() string { return AgencyTablePrefix + "export_jobs" }

type AgencyWorkerLease struct {
	Name         string `gorm:"size:128;primaryKey"`
	HolderID     string `gorm:"size:191;not null"`
	FencingToken int64  `gorm:"not null"`
	ExpiresAt    int64  `gorm:"not null;index:idx_agency_worker_lease_expiry"`
	UpdatedAt    int64  `gorm:"not null"`
}

func (AgencyWorkerLease) TableName() string { return AgencyTablePrefix + "worker_leases" }

type AgencyReconciliationIssue struct {
	ID           int64  `gorm:"primaryKey"`
	ObjectType   string `gorm:"size:64;not null"`
	ObjectID     string `gorm:"size:191;not null"`
	Difference   string `gorm:"type:text;not null"`
	EvidenceHash string `gorm:"size:128"`
	Status       string `gorm:"size:32;not null;index:idx_agency_reconcile_status"`
	Resolution   string `gorm:"type:text"`
	ActorID      *int64
	CreatedAtMS  int64 `gorm:"not null"`
	ResolvedAtMS *int64
}

func (AgencyReconciliationIssue) TableName() string {
	return AgencyTablePrefix + "reconciliation_issues"
}

type AgencyProvisioningJob struct {
	ID                  int64  `gorm:"primaryKey"`
	UserID              int64  `gorm:"not null;uniqueIndex:uidx_agency_provision_user_status,priority:1"`
	InviteCode          string `gorm:"size:32;not null"`
	RootActorID         int64  `gorm:"not null"`
	Reason              string `gorm:"type:text"`
	ExpectedUserVersion int64  `gorm:"not null"`
	Status              string `gorm:"size:32;not null;index:idx_agency_provision_status;uniqueIndex:uidx_agency_provision_user_status,priority:2"`
	BlockingTasks       string `gorm:"type:text"`
	BlockReason         string `gorm:"type:text"`
	FencingToken        int64  `gorm:"not null"`
	CancelReason        string `gorm:"type:text"`
	StartedAtMS         int64
	CompletedAtMS       int64
	CreatedAtMS         int64 `gorm:"not null"`
	UpdatedAtMS         int64 `gorm:"not null"`
}

func (AgencyProvisioningJob) TableName() string { return AgencyTablePrefix + "provisioning_jobs" }

// AgencyCommand is the durable envelope for privileged commands submitted by
// the agency hub to the gateway.  The command row is intentionally kept in
// the agency schema so retries can be answered without re-consuming the Root
// proof; the gateway remains the authority that performs the business change.
type AgencyCommand struct {
	ID              int64  `gorm:"primaryKey" json:"-"`
	CommandID       string `gorm:"size:128;not null;uniqueIndex:uidx_agency_command_id" json:"command_id"`
	Action          string `gorm:"size:64;not null;index:idx_agency_command_action" json:"action"`
	Actor           string `gorm:"size:191;not null" json:"actor"`
	SourceSID       string `gorm:"size:191;not null" json:"source_sid"`
	ObjectID        string `gorm:"size:191;not null;index:idx_agency_command_object" json:"object_id"`
	ExpectedVersion int64  `gorm:"not null" json:"expected_version"`
	Payload         string `gorm:"type:text;not null" json:"payload"`
	BodyHash        string `gorm:"size:128;not null" json:"body_hash"`
	IssuedAt        int64  `gorm:"not null" json:"issued_at"`
	ExpiresAt       int64  `gorm:"not null;index:idx_agency_command_expiry" json:"expires_at"`
	HubSignature    string `gorm:"type:text;not null" json:"-"`
	RootProof       string `gorm:"type:text;not null" json:"-"`
	RootProofJTI    string `gorm:"size:128;not null;uniqueIndex:uidx_agency_command_root_jti" json:"-"`
	Status          string `gorm:"size:32;not null;index:idx_agency_command_status" json:"status"`
	ResultCode      int    `gorm:"not null" json:"result_code,omitempty"`
	ResultJSON      string `gorm:"type:text" json:"result,omitempty"`
	LastError       string `gorm:"type:text" json:"error,omitempty"`
	CreatedAt       int64  `gorm:"not null" json:"created_at"`
	UpdatedAt       int64  `gorm:"not null" json:"updated_at"`
	CompletedAt     *int64 `json:"completed_at,omitempty"`
}

func (AgencyCommand) TableName() string { return AgencyTablePrefix + "commands" }

type AgencyTaskSubmissionAttempt struct {
	ID                     int64  `gorm:"primaryKey"`
	ChargeID               string `gorm:"size:128;not null;index:idx_agency_task_attempt_charge"`
	SubmitNo               int    `gorm:"not null"`
	PublicTaskID           string `gorm:"size:191;not null;uniqueIndex:uidx_agency_task_public"`
	ProviderIdempotencyKey string `gorm:"size:191"`
	ProviderTaskID         string `gorm:"size:191"`
	RequestHash            string `gorm:"size:128"`
	Status                 string `gorm:"size:32;not null"`
	EvidenceTrace          string `gorm:"type:text"`
	CreatedAtMS            int64  `gorm:"not null"`
}

func (AgencyTaskSubmissionAttempt) TableName() string {
	return AgencyTablePrefix + "task_submission_attempts"
}

type AgencyFundingDebt struct {
	ID                int64  `gorm:"primaryKey"`
	UserID            int64  `gorm:"not null;index:idx_agency_debt_user"`
	OriginOperationID string `gorm:"size:128;not null"`
	DebtKind          string `gorm:"size:32;not null"`
	OriginalQuota     int64
	OutstandingQuota  int64
	ReversedQuota     int64
	CreatedAtMS       int64 `gorm:"not null"`
}

func (AgencyFundingDebt) TableName() string { return AgencyTablePrefix + "funding_debts" }

type AgencyDebtRepayment struct {
	ID            int64  `gorm:"primaryKey"`
	DebtID        int64  `gorm:"not null;index:idx_agency_repayment_debt"`
	RepaymentID   string `gorm:"size:128;not null;uniqueIndex:uidx_agency_repayment_id"`
	FundingLotID  *int64
	Quota         int64
	ReversedQuota int64 `gorm:"not null;default:0"`
	RestoredTotal int64
	MoneySeq      int64
	CreatedAtMS   int64 `gorm:"not null"`
}

func (AgencyDebtRepayment) TableName() string { return AgencyTablePrefix + "debt_repayments" }

func AgencyModels() []any {
	return []any{
		&Agency{}, &AgencyOperatorAccount{}, &AgencySession{}, &AgencySSOTicketUse{}, &AgencyVerificationUse{}, &AgencyDeliverySecret{},
		&AgencyUserBinding{}, &AgencyActiveUserBinding{}, &AgencyPricePolicyVersion{}, &AgencyPricePolicyItem{}, &AgencyIdempotencyRecord{},
		&AgencyFundingAccount{}, &AgencyFundingLot{}, &AgencyFundingAllocation{}, &AgencyFundingLedger{}, &AgencyFundingDebt{}, &AgencyDebtRepayment{}, &AgencyFundingReversal{}, &AgencyFundingReversalChargeRecord{},
		&AgencyBillingJournal{}, &AgencyBillingOperation{}, &AgencyBillingOutbox{}, &AgencyEventDelivery{}, &AgencyTaskSubmissionAttempt{},
		&AgencySourceEvent{}, &AgencyUsageFact{}, &AgencyTopupFact{}, &AgencyCommissionLedger{}, &AgencyCommissionBalance{},
		&AgencyWithdrawalAccount{}, &AgencyWithdrawal{}, &AgencyWithdrawalPaymentReference{}, &AgencyWithdrawalTransition{}, &AgencyAuditLog{}, &AgencyDailyStat{},
		&AgencyExportJob{}, &AgencyWorkerLease{}, &AgencyReconciliationIssue{}, &AgencyProvisioningJob{},
		&AgencyCommand{},
	}
}
