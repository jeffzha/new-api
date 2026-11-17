package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAgencyPricingSnapshotReadersDoNotSerializeCustomers(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		for _, events := range []string{"false", "true"} {
			t.Run(dialect+"/events="+events, func(t *testing.T) {
				t.Setenv("OPS_DISTRIBUTOR_EVENTS_ENABLED", "false")
				db := agencyDialectDB(t, dialect)
				if db == nil {
					t.Skip("external test database not configured")
				}
				pool, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, pool.Close()) })
				require.NoError(t, db.AutoMigrate(&Agency{}, &AgencyPricePolicyVersion{}, &AgencyUserBinding{}, &AgencyActiveUserBinding{}, &AgencyOpsEvent{}, &AgencyOpsObjectRevision{}))
				t.Setenv("OPS_DISTRIBUTOR_EVENTS_ENABLED", events)
				agency := Agency{Code: "snapshot-reader-" + dialect, Status: "active", StateRevision: 3}
				require.NoError(t, db.Create(&agency).Error)
				t.Cleanup(func() {
					for _, table := range []any{&AgencyActiveUserBinding{}, &AgencyUserBinding{}, &AgencyPricePolicyVersion{}} {
						require.NoError(t, db.Where("agency_id = ?", agency.ID).Delete(table).Error)
					}
					require.NoError(t, db.Delete(&Agency{}, agency.ID).Error)
				})
				version := AgencyPricePolicyVersion{AgencyID: agency.ID, Revision: 7, PolicyJSON: "{}"}
				require.NoError(t, db.Create(&version).Error)
				require.NoError(t, db.Model(&agency).Update("current_policy_version_id", version.ID).Error)
				var snapshots []*agencycontract.PricingSnapshot
				for _, userID := range []int64{983701, 983702} {
					binding := AgencyUserBinding{UserID: userID, AgencyID: agency.ID, Revision: 4}
					require.NoError(t, db.Create(&binding).Error)
					require.NoError(t, db.Create(&AgencyActiveUserBinding{UserID: userID, BindingID: binding.ID, AgencyID: agency.ID, Revision: 4}).Error)
					snapshots = append(snapshots, &agencycontract.PricingSnapshot{AgencyID: agency.ID, BindingID: binding.ID, BindingRevision: 4,
						AgencyStateRevision: 3, PolicyVersionID: version.ID, PolicyRevision: 7, SalesBPS: 10000, CommissionEligible: true})
				}
				reader := db.Begin()
				require.NoError(t, reader.Error)
				defer reader.Rollback()
				require.NoError(t, ValidateAgencyPricingSnapshotTx(reader, 983701, snapshots[0]))
				if dialect == "sqlite" {
					require.NoError(t, ValidateAgencyPricingSnapshotTx(reader, 983702, snapshots[1]))
					return
				}
				// The first customer's transaction deliberately stays open. A second
				// customer of the same agency must not wait for its commit.
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				require.NoError(t, db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					return ValidateAgencyPricingSnapshotTx(tx, 983702, snapshots[1])
				}))
				// Publication must remain blocked by the accepted quote. Removing the
				// lock entirely would allow a stale quote to race a new price version.
				writerCtx, writerCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				defer writerCancel()
				err = db.WithContext(writerCtx).Transaction(func(tx *gorm.DB) error {
					return tx.Model(&Agency{}).Where("id = ?", agency.ID).Update("state_revision", 4).Error
				})
				require.Error(t, err)
				require.ErrorIs(t, writerCtx.Err(), context.DeadlineExceeded)
				require.NoError(t, reader.Commit().Error)
				require.NoError(t, db.Model(&Agency{}).Where("id = ?", agency.ID).Update("state_revision", 4).Error)
				require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error {
					return ValidateAgencyPricingSnapshotTx(tx, 983701, snapshots[0])
				}), "pricing snapshot is stale")
			})
		}
	}
}

func TestValidateAgencyPricingSnapshotAllowsPendingStandardSale(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-pricing-snapshot-pending?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Agency{}, &AgencyPricePolicyVersion{}, &AgencyUserBinding{}, &AgencyActiveUserBinding{}, &AgencyOpsEvent{}, &AgencyOpsObjectRevision{}))

	const userID int64 = 901
	require.NoError(t, db.Create(&Agency{ID: 91, Code: "pending", Status: "active", CurrentPolicyVersionID: 911, StateRevision: 3}).Error)
	require.NoError(t, db.Create(&AgencyPricePolicyVersion{ID: 911, AgencyID: 91, Revision: 7, PolicyJSON: "{}"}).Error)
	require.NoError(t, db.Create(&AgencyUserBinding{ID: 912, UserID: userID, AgencyID: 91, Revision: 4}).Error)
	require.NoError(t, db.Create(&AgencyActiveUserBinding{UserID: userID, BindingID: 912, AgencyID: 91, Revision: 4}).Error)

	snapshot := &agencycontract.PricingSnapshot{AgencyID: 91, BindingID: 912, BindingRevision: 4, AgencyStateRevision: 3, PolicyVersionID: 911, PolicyRevision: 7, SalesBPS: 10000, CommissionEligible: false, EligibilityReason: "platform_pricing_pending"}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ValidateAgencyPricingSnapshotTx(tx, userID, snapshot)
	}))

	snapshot.SalesBPS = 9000
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error {
		return ValidateAgencyPricingSnapshotTx(tx, userID, snapshot)
	}), "eligibility snapshot is stale")
}
