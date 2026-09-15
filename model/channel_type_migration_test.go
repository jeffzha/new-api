package model

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMigratePrivateChannelTypeIDsKeepsUpstreamDoubaoAndMovesLegacySeedance(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Task{}))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	require.NoError(t, db.Create(&Channel{Id: 1, Type: 54, Name: "DoubaoVideo"}).Error)
	require.NoError(t, db.Create(&Channel{Id: 2, Type: 59, Name: "Seedance Domestic", Models: "doubao-seedance-2-0-260128"}).Error)
	require.NoError(t, db.Create(&Task{ChannelId: 2, Platform: "59"}).Error)
	require.NoError(t, migratePrivateChannelTypeIDs())

	var upstream, migrated Channel
	require.NoError(t, db.First(&upstream, 1).Error)
	require.NoError(t, db.First(&migrated, 2).Error)
	require.Equal(t, 54, upstream.Type)
	require.Equal(t, constant.ChannelTypeSeedanceDomestic, migrated.Type)
	var task Task
	require.NoError(t, db.First(&task).Error)
	require.Equal(t, "1002", string(task.Platform))
}
