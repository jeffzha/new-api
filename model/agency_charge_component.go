package model

// AgencyChargeComponent retains the original financial result and independent
// cumulative refund watermarks. ComponentKey hashes the byte-exact component ID
// so database collation cannot merge distinct identifiers.
type AgencyChargeComponent struct {
	ID                       int64  `gorm:"primaryKey"`
	ChargeID                 string `gorm:"size:128;not null;uniqueIndex:uidx_agency_charge_component,priority:1"`
	SegmentNo                int    `gorm:"not null;uniqueIndex:uidx_agency_charge_component,priority:2"`
	ComponentKey             string `gorm:"size:64;not null;uniqueIndex:uidx_agency_charge_component,priority:3"`
	ComponentID              string `gorm:"size:128;not null"`
	UserID                   int64  `gorm:"not null;index:idx_agency_component_user"`
	OriginalResult           string `gorm:"type:text;not null"`
	RefundedQuota            int64  `gorm:"not null"`
	RestoredPaidQuota        int64  `gorm:"not null"`
	RestoredNonpaidQuota     int64  `gorm:"not null"`
	RestoredDebtQuota        int64  `gorm:"not null"`
	ReversedCommissionQuota  int64  `gorm:"not null"`
	ReversedCommissionMicros int64  `gorm:"not null"`
	Version                  int64  `gorm:"not null"`
}

func (AgencyChargeComponent) TableName() string { return AgencyTablePrefix + "charge_components" }

// AgencyComponentFunding records the original lot/allocation matrix without
// changing allocation IDs referenced by payment chargebacks and debt records.
type AgencyComponentFunding struct {
	ID                   int64 `gorm:"primaryKey"`
	ChargeComponentID    int64 `gorm:"not null;uniqueIndex:uidx_agency_component_allocation,priority:1"`
	AllocationID         int64 `gorm:"not null;uniqueIndex:uidx_agency_component_allocation,priority:2;index:idx_agency_component_allocation"`
	LotID                int64 `gorm:"not null"`
	PaidQuota            int64 `gorm:"not null"`
	NonpaidQuota         int64 `gorm:"not null"`
	DebtQuota            int64 `gorm:"not null"`
	RestoredPaidQuota    int64 `gorm:"not null"`
	RestoredNonpaidQuota int64 `gorm:"not null"`
	RestoredDebtQuota    int64 `gorm:"not null"`
	Version              int64 `gorm:"not null"`
}

func (AgencyComponentFunding) TableName() string { return AgencyTablePrefix + "component_funding" }
