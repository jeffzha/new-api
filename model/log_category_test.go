package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuotaLogCategoriesAcrossDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("external test database is not configured")
			}
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			previous, previousType := LOG_DB, common.LogDatabaseType()
			LOG_DB = db
			common.SetLogDatabaseType(common.DatabaseType(dialect))
			t.Cleanup(func() { LOG_DB = previous; common.SetLogDatabaseType(previousType) })
			require.NoError(t, db.Migrator().DropTable(&Log{}))
			require.NoError(t, db.AutoMigrate(&Log{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&Log{})) })
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("%s: %s", dialect, version)
			rows := []Log{
				{Id: 1, Other: `{"quota_source":"online_topup"}`},
				{Id: 2, Other: `{"quota_source":"admin_topup"}`},
				{Id: 3, Content: "使用在线充值成功，充值金额: ¥10，支付金额：10", Other: `{"admin_info":{"payment_method":"wxpay"}}`},
				{Id: 4, Content: "管理员补单成功，充值金额: ¥10，支付金额：10"},
				{Id: 5, Other: `{"quota_source":"admin_adjustment"}`},
				{Id: 6, Other: `{"quota_source":"redemption"}`},
				{Id: 7, Other: `{ "op": { "action": "user.quota_add" } }`},
				{Id: 8, Other: `{"op":{"action":"user.quota_subtract"}}`},
				{Id: 9, Other: `{"op":{"action":"user.quota_override"}}`},
				{Id: 10, Content: "通过兑换码充值 ¥10 额度，兑换码ID 1", Other: "{}"},
				{Id: 11, Content: "Overrode user quota from 1000 to 0"},
				{Id: 12, Content: "使用在线充值成功，充值金额: ¥10", Other: `{"quota_source":"redemption"}`},
				{Id: 13, Content: "通过兑换码充值 ¥10", Other: `{"quota_source":"online_topup"}`},
				{Id: 14, Other: `{"quota_source":"subscription_balance_purchase"}`},
				{Id: 15, Content: "unknown historical source", Other: "null"},
				{Id: 16, Other: `{"quota_source":"subscription_purchase"}`},
			}
			for i := range rows {
				rows[i].Type, rows[i].UserId, rows[i].CreatedAt, rows[i].Quota = LogTypeTopup, 42, int64(rows[i].Id), 100
			}
			rows = append(rows, Log{Id: 17, Type: LogTypeConsume, UserId: 42, CreatedAt: 17, Other: `{"quota_source":"redemption"}`},
				Log{Id: 18, Type: LogTypeTopup, UserId: 43, CreatedAt: 18, Other: `{"quota_source":"admin_adjustment"}`},
				Log{Id: 19, Type: LogTypeManage, UserId: 42, CreatedAt: 19, Other: `{"op":{"action":"user.quota_add"}}`},
				Log{Id: 20, Type: LogTypeManage, UserId: 42, CreatedAt: 20, Other: `{"op":{"action":"user.update"}}`},
				Log{Id: 21, Type: LogTypeTopup, UserId: 42, CreatedAt: 21, Other: `{"quota_source":null,"op":{"action":"user.quota_override"}}`})
			require.NoError(t, db.Create(&rows).Error)
			for _, scenario := range []struct {
				filter int
				ids    []int
			}{
				{1, []int{13, 4, 3, 2, 1}},
				{8, []int{21, 19, 18, 12, 11, 10, 9, 8, 7, 6, 5}},
			} {
				logs, total, err := GetAllLogs(scenario.filter, 0, 0, "", "", "", 0, 100, 0, "", "", "")
				require.NoError(t, err)
				assert.EqualValues(t, len(scenario.ids), total)
				ids := make([]int, len(logs))
				for i, log := range logs {
					ids[i] = log.Id
					assert.Equal(t, rows[log.Id-1].Type, log.Type, "stored type contract must remain unchanged")
					require.NotNil(t, log.DisplayType)
					assert.Equal(t, scenario.filter, *log.DisplayType)
				}
				assert.Equal(t, scenario.ids, ids)
				page, count, err := GetAllLogs(scenario.filter, 0, 0, "", "", "", 1, 2, 0, "", "", "")
				require.NoError(t, err)
				assert.Equal(t, total, count)
				require.Len(t, page, 2)
				assert.Equal(t, scenario.ids[1], page[0].Id)
				userLogs, userTotal, err := GetUserLogs(42, scenario.filter, 0, 0, "", "", 0, 100, "", "", "")
				require.NoError(t, err)
				wantTotal := len(scenario.ids)
				if scenario.filter == 8 {
					wantTotal--
				}
				assert.EqualValues(t, wantTotal, userTotal)
				for _, log := range userLogs {
					assert.Equal(t, 42, log.UserId)
					require.NotNil(t, log.DisplayType)
					assert.Equal(t, scenario.filter, *log.DisplayType)
				}
			}
			all, total, err := GetAllLogs(0, 0, 0, "", "", "", 0, 100, 0, "", "", "")
			require.NoError(t, err)
			assert.EqualValues(t, len(rows), total)
			assert.Len(t, all, len(rows))
			managed, managedTotal, err := GetAllLogs(3, 0, 0, "", "", "", 0, 100, 0, "", "", "")
			require.NoError(t, err)
			require.Len(t, managed, 1)
			assert.EqualValues(t, 1, managedTotal)
			assert.Equal(t, 20, managed[0].Id)
			filtered, filteredTotal, err := GetAllLogs(8, 7, 9, "", "", "", 0, 100, 0, "", "", "")
			require.NoError(t, err)
			assert.EqualValues(t, 3, filteredTotal)
			assert.Len(t, filtered, 3)
			var unchanged []Log
			require.NoError(t, db.Order("id asc").Find(&unchanged).Error)
			assert.Equal(t, rows, unchanged, "classification must never rewrite historical data")
		})
	}
}
