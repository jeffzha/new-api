//go:build overseas_upgrade

package model

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Run seed against the released overseas tree, then verify against the merge,
// using only an explicitly configured disposable database and snapshot path.
func TestOverseasReleasedSchemaUpgrade(t *testing.T) {
	phase, snapshotPath := os.Getenv("OVERSEAS_UPGRADE_PHASE"), os.Getenv("OVERSEAS_UPGRADE_SNAPSHOT")
	if (phase != "seed" && phase != "verify") || snapshotPath == "" {
		t.Skip("isolated release-upgrade runner required")
	}
	common.IsMasterNode = true
	common.SQLitePath = filepath.Join(filepath.Dir(snapshotPath), "gateway.db")
	require.NoError(t, InitDB())
	require.NoError(t, InitLogDB())
	require.NoError(t, MigrateAgency(DB))
	pool, err := DB.DB()
	require.NoError(t, err)
	defer pool.Close()
	if LOG_DB != DB {
		logPool, err := LOG_DB.DB()
		require.NoError(t, err)
		defer logPool.Close()
	}
	tables := []string{"users", "tokens", "channels", "options", "top_ups", "tasks", "agency_hub_agencies"}
	if phase == "seed" {
		user := User{Id: 17, Username: "upgrade-customer@example.test", Email: "upgrade-customer@example.test", Password: "synthetic-password-hash", AffCode: "upgrade-customer", Quota: 50000000, UsedQuota: 12345, Group: "zenlayer", Status: 1, Role: 1}
		require.NoError(t, DB.Create(&user).Error)
		token := Token{UserId: 17, Key: "synthetic-upgrade-token-not-usable", Status: 1, Name: "overseas-upgrade", Group: "zenlayer", RemainQuota: 49987655, UsedQuota: 12345}
		require.NoError(t, DB.Create(&token).Error)
		base := "https://foxtoken.linkomobile.com"
		channel := Channel{Id: 11, Type: 1, Key: "synthetic-upgrade-channel-not-usable", Status: 1, Name: "protected-overseas-fixture", BaseURL: &base, Models: "gpt-6.1-sol,doubao-seedance-2-0-260128", Group: "zenlayer", ModelMapping: func() *string { s := `{"gpt-6.1-sol":"upstream-sol"}`; return &s }()}
		require.NoError(t, DB.Create(&channel).Error)
		for _, option := range []Option{{Key: "DisplayInCurrency", Value: "USD"}, {Key: "StripePriceId", Value: "price_synthetic_upgrade"}, {Key: "GroupRatio", Value: `{"default":1,"zenlayer":1}`}} {
			require.NoError(t, DB.Create(&option).Error)
		}
		order := TopUp{UserId: 17, Amount: 100, Money: 100, TradeNo: "upgrade-stripe-order", PaymentProvider: "stripe", PaymentMethod: "stripe", Status: "success", PaymentSnapshot: `{"currency_code":"USD","actual_money":"100.00","payment_reference":"synthetic-checkout"}`}
		require.NoError(t, DB.Create(&order).Error)
		task := Task{TaskID: "upgrade-video-task", UserId: 17, ChannelId: 11, Group: "zenlayer", Status: TaskStatusSuccess, Quota: 12345, PrivateData: TaskPrivateData{UpstreamTaskID: "synthetic-upstream-task", BillingSource: TaskBillingSourceWallet, TokenId: token.Id}, Properties: Properties{OriginModelName: "doubao-seedance-2-0-260128"}}
		require.NoError(t, DB.Create(&task).Error)
		parent := Agency{ID: 101, Code: "upgrade-parent", DisplayName: "Overseas parent", Status: "active", InviteCode: "up-parent", Depth: 1, Version: 3}
		require.NoError(t, DB.Create(&parent).Error)
		child := Agency{ID: 102, Code: "upgrade-child", DisplayName: "Overseas child", Status: "active", InviteCode: "up-child", Depth: 2, ParentAgencyID: &parent.ID, Version: 4}
		require.NoError(t, DB.Create(&child).Error)
		require.NoError(t, LOG_DB.Create(&Log{UserId: 17, Type: 2, ModelName: "gpt-6.1-sol", Quota: 12345, Content: "synthetic upgrade usage"}).Error)
	}
	type snapshot map[string][]map[string]string
	collect := func() snapshot {
		result := snapshot{}
		for _, table := range append(tables, "logs") {
			db := DB
			if table == "logs" {
				db = LOG_DB
			}
			order := "id"
			if table == "options" {
				order = commonKeyCol
			}
			rows, err := db.Table(table).Order(order).Rows()
			require.NoError(t, err)
			columns, err := rows.Columns()
			require.NoError(t, err)
			for rows.Next() {
				values := make([]any, len(columns))
				targets := make([]any, len(columns))
				for i := range targets {
					targets[i] = &values[i]
				}
				require.NoError(t, rows.Scan(targets...))
				row := map[string]string{}
				for i, key := range columns {
					switch value := values[i].(type) {
					case nil:
						row[key] = "<null>"
					case []byte:
						row[key] = string(value)
					case time.Time:
						row[key] = value.Format(time.RFC3339Nano)
					default:
						row[key] = fmt.Sprint(value)
					}
				}
				result[table] = append(result[table], row)
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
		}
		return result
	}
	if phase == "seed" {
		data, err := common.Marshal(collect())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(snapshotPath, data, 0600))
		return
	}
	data, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	var before snapshot
	require.NoError(t, common.Unmarshal(data, &before))
	for range 2 {
		require.NoError(t, migrateDB())
		require.NoError(t, MigrateAuditLogs())
		require.NoError(t, MigrateAgencyWithTimeout(DB, time.Minute))
		after := collect()
		for table, rows := range before {
			require.Len(t, after[table], len(rows), table)
			for i, row := range rows {
				for column, value := range row {
					assert.Equal(t, value, after[table][i][column], "%s row %d column %s", table, i, column)
				}
			}
		}
		assert.True(t, DB.Migrator().HasIndex(&Token{}, "idx_tokens_key"))
		assert.True(t, DB.Migrator().HasIndex(&Agency{}, "uidx_agency_code"))
		assert.True(t, DB.Migrator().HasIndex(&TopUp{}, "idx_top_ups_trade_no"))
	}
	require.Error(t, DB.Create(&Agency{Code: "upgrade-parent", InviteCode: "different-invite"}).Error)
	require.Error(t, DB.Create(&TopUp{TradeNo: "upgrade-stripe-order"}).Error)
}
