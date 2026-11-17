package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAgencyOpsWriterTransitionReceiptSurvivesLaterTransitions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-writer-receipt?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgencyOpsWriterState{}, &AgencyOpsWriterTransition{}))
	require.NoError(t, db.Create(&AgencyOpsWriterState{ID: 1, Mode: AgencyWriterSourcePrimary, FenceVersion: 1}).Error)

	first, err := TransitionAgencyOpsWriter(t.Context(), db, "command-1", AgencyWriterShadow, 1, "start shadow", map[string]any{"ticket": "OPS-1"}, AgencyOpsDeliveryHealth{})
	require.NoError(t, err)
	require.Equal(t, int64(2), first.FenceVersion)
	_, err = TransitionAgencyOpsWriter(t.Context(), db, "command-2", AgencyWriterDraining, 2, "start drain", map[string]any{"ticket": "OPS-2"}, AgencyOpsDeliveryHealth{})
	require.NoError(t, err)

	replayed, err := TransitionAgencyOpsWriter(t.Context(), db, "command-1", AgencyWriterShadow, 1, "start shadow", map[string]any{"ticket": "OPS-1"}, AgencyOpsDeliveryHealth{})
	require.NoError(t, err)
	require.Equal(t, AgencyWriterShadow, replayed.Mode)
	require.Equal(t, int64(2), replayed.FenceVersion)
	var current AgencyOpsWriterState
	require.NoError(t, db.First(&current, 1).Error)
	require.Equal(t, AgencyWriterDraining, current.Mode)
	require.Equal(t, int64(3), current.FenceVersion)

	_, err = TransitionAgencyOpsWriter(t.Context(), db, "command-1", AgencyWriterShadow, 1, "changed payload", map[string]any{"ticket": "OPS-1"}, AgencyOpsDeliveryHealth{})
	require.ErrorContains(t, err, "identity conflict")
	var receipts int64
	require.NoError(t, db.Model(&AgencyOpsWriterTransition{}).Count(&receipts).Error)
	require.Equal(t, int64(2), receipts)
}
