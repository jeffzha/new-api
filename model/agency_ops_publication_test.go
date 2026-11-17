package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAgencyOpsPublicationFindsLateSourceIDs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-ops-publication?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgencyBillingOutbox{}, &AgencyOpsPublisher{}, &AgencyOpsPublication{}, &AgencyFundingAccount{}, &AgencyFundingLedger{}))
	require.NoError(t, db.Create(&AgencyOpsPublisher{ID: 1}).Error)
	newer := AgencyBillingOutbox{ID: 20, EventID: "newer", OperationID: "newer-operation", EventKind: "agency.billing_finalized", PayloadHash: "hash-newer", Payload: "{}", CreatedAtMS: 20}
	require.NoError(t, db.Create(&newer).Error)
	require.NoError(t, PublishAgencyOpsEvents(t.Context(), db))
	late := AgencyBillingOutbox{ID: 10, EventID: "late", OperationID: "late-operation", EventKind: "agency.billing_finalized", PayloadHash: "hash-late", Payload: "{}", CreatedAtMS: 10}
	require.NoError(t, db.Create(&late).Error)
	require.NoError(t, PublishAgencyOpsEvents(t.Context(), db))
	var rows []AgencyOpsPublication
	require.NoError(t, db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, int64(20), rows[0].SourceID)
	require.Equal(t, int64(10), rows[1].SourceID)
	require.Less(t, rows[0].ID, rows[1].ID)
	require.NoError(t, PublishAgencyOpsEvents(t.Context(), db))
	var count int64
	require.NoError(t, db.Model(&AgencyOpsPublication{}).Count(&count).Error)
	require.Equal(t, int64(2), count)
}
