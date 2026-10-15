package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCNWalletValueAcrossDialects(t *testing.T) {
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_WALLET_ENABLED", "true")
	t.Setenv("OPS_CN_DURABLE_TASK_ENABLED", "true")
	t.Setenv("OPS_CN_WALLET_VALUE_ENABLED", "true")
	oldBatch, oldRedis, oldUnit := common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit
	common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit = false, false, 10
	t.Cleanup(func() {
		common.BatchUpdateEnabled, common.RedisEnabled, common.QuotaPerUnit = oldBatch, oldRedis, oldUnit
	})
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("disposable external database not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			if dialect == "sqlite" {
				require.NoError(t, pool.Close())
				db, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wallet-value.db")), &gorm.Config{})
				require.NoError(t, err)
				pool, err = db.DB()
				require.NoError(t, err)
				pool.SetMaxOpenConns(1)
			}
			t.Cleanup(func() { _ = pool.Close() })
			oldDB, oldType := DB, common.MainDatabaseType()
			DB = db
			switch dialect {
			case "mysql":
				common.SetMainDatabaseType(common.DatabaseTypeMySQL)
			case "postgres":
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
			default:
				common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			}
			t.Cleanup(func() { DB = oldDB; common.SetMainDatabaseType(oldType) })
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			cnReset(t, db)
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &Task{}, &TaskBillingReconciliation{}, &TopUp{}, &Option{}, &Log{}))
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, "test-cn"))
			}
			t.Run("upgrade_preserves_history_and_open_reservations", func(t *testing.T) {
				require.NoError(t, db.Migrator().DropTable(&CNWalletValueSlice{}, &CNWalletValuePart{}, &CNWalletValueCharge{}, &CNWalletValueLot{}, &CNWalletValueAccount{}))
				legacy := User{Id: 99, Username: "value-legacy-upgrade", AffCode: "value-legacy-upgrade", Quota: 95}
				require.NoError(t, db.Create(&legacy).Error)
				reserved := CNWalletLifecycle{RequestID: "value-pre-upgrade", UserID: 99, Reserved: 5, State: "reserved", Version: 1, CreatedAt: time.Now().UTC().Format(CNTimeLayout)}
				require.NoError(t, db.Create(&reserved).Error)
				oldPayment, err := common.Marshal(TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "USD", PaymentReference: "value-legacy-receipt"})
				require.NoError(t, err)
				old := TopUp{UserId: 99, TradeNo: "value-pre-upgrade-payment", Status: common.TopUpStatusSuccess, CreditedQuota: 100, PaymentSnapshot: string(oldPayment), CompleteTime: time.Now().Unix()}
				require.NoError(t, db.Create(&old).Error)
				for range 2 {
					require.NoError(t, MigrateCNFinance(db, "test-cn"))
				}
				var preserved User
				require.NoError(t, db.First(&preserved, 99).Error)
				assert.Equal(t, legacy.Quota, preserved.Quota)
				var count int64
				require.NoError(t, db.Model(&CNWalletValueLot{}).Count(&count).Error)
				assert.Zero(t, count)
				order := TopUp{UserId: 99, TradeNo: "value-upgrade-new", Amount: 6, PaymentProvider: PaymentProviderCreem, Status: common.TopUpStatusPending}
				require.NoError(t, db.Create(&order).Error)
				require.NoError(t, RechargeCreem(order.TradeNo, "", "", "127.0.0.1", &TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "USD", PaymentReference: "new-upgrade-receipt"}))
				require.NoError(t, FinalizeCNWallet(reserved.RequestID, 99, 5, false))
				var account CNWalletValueAccount
				require.NoError(t, db.First(&account, 99).Error)
				assert.EqualValues(t, 100, account.OpeningQuota)
				assert.EqualValues(t, 95, account.OpeningBalance)
				assert.EqualValues(t, 95, account.UnknownQuota)
				var preservedTopup TopUp
				require.NoError(t, db.First(&preservedTopup, old.Id).Error)
				assert.Equal(t, old, preservedTopup)
			})
			nextID := 100
			var transitionEvidence []any
			newUser := func(quota int) int {
				nextID++
				id := nextID
				name := fmt.Sprintf("value-%d", id)
				require.NoError(t, db.Create(&User{Id: id, Username: name, AffCode: name, Quota: quota, Status: common.UserStatusEnabled}).Error)
				require.NoError(t, db.Create(&Token{Id: id, UserId: id, Key: name, RemainQuota: 10000, Status: common.TokenStatusEnabled, ExpiredTime: -1}).Error)
				require.NoError(t, db.Create(&Channel{Id: id, Name: name}).Error)
				return id
			}
			credit := func(user, quota int, amount, currency, trade string) error {
				order := TopUp{UserId: user, Amount: int64(quota), TradeNo: trade, PaymentProvider: PaymentProviderCreem, Status: common.TopUpStatusPending}
				if err := db.Create(&order).Error; err != nil {
					return err
				}
				return RechargeCreem(trade, "", "", "127.0.0.1", &TopupPaymentSnapshot{ActualMoney: amount, CurrencyCode: currency, PaymentReference: "receipt-" + trade})
			}
			wallet := func(user, reserve, actual int, request string) {
				_, err := PreConsumeCNBoundWallet(request, user, reserve, user, false, false)
				require.NoError(t, err)
				require.NoError(t, ReserveCNBoundWallet(request, user, reserve, true))
				require.NoError(t, FinalizeCNBoundWallet(request, user, actual))
			}
			t.Run("unknown_first_currency_residual_and_task_reversal", func(t *testing.T) {
				user := newUser(3)
				require.NoError(t, credit(user, 6, "1", "USD", "value-usd"))
				wallet(user, 2, 2, "unknown-first")
				var unknown CNWalletValueCharge
				require.NoError(t, db.Where("economic_id = ?", "request:unknown-first").First(&unknown).Error)
				require.EqualValues(t, 2, unknown.UnknownQuota)
				wallet(user, 2, 4, "unknown-then-paid")
				require.NoError(t, credit(user, 6, "2", "CNY", "value-cny"))
				seed := &Task{TaskID: "value-task", UserId: user, ChannelId: user, Platform: constant.TaskPlatform("alibaba"), Status: TaskStatusSubmitted, CreatedAt: time.Now().Unix()}
				row, err := PrepareCNTask(seed, "value-task-request", TaskBillingSourceWallet, 5, user, false, false)
				require.NoError(t, err)
				seed.CNBillingKey, seed.Quota = row.Key, 5
				seed.PrivateData.TokenId, seed.PrivateData.BillingSource = user, TaskBillingSourceWallet
				seed.PrivateData.Execution = &TaskExecutionSnapshot{RequestID: row.RequestID}
				require.NoError(t, BeginCNTaskDispatch(row.Key, strings.Repeat("a", 64), user, "alibaba"))
				seed.PrivateData.UpstreamTaskID = "value-provider-task"
				require.NoError(t, RecordCNTaskUpstream(row.Key, seed.PrivateData.UpstreamTaskID))
				require.NoError(t, seed.Insert())
				for _, target := range []int{2, 2, 5, 0} {
					require.NoError(t, RequestCNTaskAdjustment(seed, target, false))
					require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
					require.NoError(t, db.First(seed, seed.ID).Error)
				}
				var lots []CNWalletValueLot
				require.NoError(t, db.Where("user_id = ?", user).Order("id").Find(&lots).Error)
				require.Len(t, lots, 2)
				for i, lot := range lots {
					var pool CNFundingValueLot
					require.NoError(t, db.Where("lot_id = ?", lot.LotID).First(&pool).Error)
					require.NoError(t, cnValidateFundingValueTx(db, &pool))
					if i == 0 {
						assert.EqualValues(t, 3, pool.RemainingQuota)
						assert.Equal(t, "0.5", pool.RemainingValue)
					} else {
						assert.EqualValues(t, 6, pool.RemainingQuota)
						assert.Equal(t, "2", pool.RemainingValue)
					}
				}
				var taskFacts []CNFinanceOutbox
				require.NoError(t, db.Where("chain_key = ?", CNIdentity("charge", "request:value-task-request", "0")).Order("revision").Find(&taskFacts).Error)
				require.Len(t, taskFacts, 4)
				var account CNWalletValueAccount
				require.NoError(t, db.First(&account, user).Error)
				assert.False(t, account.Suspended)
				assert.Zero(t, account.UnknownQuota)
				assert.EqualValues(t, 9, account.ExpectedQuota)
			})
			t.Run("atomic_outbox_failure_and_cancel_release", func(t *testing.T) {
				user := newUser(0)
				require.NoError(t, credit(user, 6, "1", "USD", "value-rollback"))
				_, err := PreConsumeCNBoundWallet("value-rollback-request", user, 3, user, false, false)
				require.NoError(t, err)
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("value-fail-outbox", func(tx *gorm.DB) {
					if tx.Statement.Table == "cn_finance_outboxes" {
						tx.AddError(errors.New("injected outbox failure"))
					}
				}))
				require.Error(t, FinalizeCNBoundWallet("value-rollback-request", user, 4))
				require.NoError(t, db.Callback().Create().Remove("value-fail-outbox"))
				var native User
				require.NoError(t, db.First(&native, user).Error)
				assert.Equal(t, 3, native.Quota)
				var account CNWalletValueAccount
				require.NoError(t, db.First(&account, user).Error)
				assert.EqualValues(t, 3, account.ExpectedQuota)
				var count int64
				require.NoError(t, db.Model(&CNWalletValuePart{}).Where("charge_key = ?", CNIdentity("request:value-rollback-request", "0")).Count(&count).Error)
				assert.Zero(t, count)
				require.NoError(t, RequestCNBoundWalletCancellation(db, "value-rollback-request", user))
				require.NoError(t, RecoverCNWalletCancellations(context.Background(), db, 100))
				require.NoError(t, db.First(&native, user).Error)
				assert.Equal(t, 6, native.Quota)
			})
			t.Run("committed_payment_time_and_preexisting_facts", func(t *testing.T) {
				for _, early := range []bool{false, true} {
					user := newUser(0)
					trade := fmt.Sprintf("value-fixed-month-%t", early)
					payment := &TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "USD", PaymentReference: "receipt-" + trade}
					conversion := &TopupQuotaConversion{SchemaVersion: 1, Calculation: "identity", CreditedQuota: "6"}
					p, err := common.Marshal(payment)
					require.NoError(t, err)
					c, err := common.Marshal(conversion)
					require.NoError(t, err)
					fixed := time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC)
					order := TopUp{UserId: user, TradeNo: trade, CompleteTime: fixed.Unix(), Status: common.TopUpStatusSuccess, CreditedQuota: 6, PaymentSnapshot: string(p), QuotaConversionSnapshot: string(c)}
					require.NoError(t, db.Create(&order).Error)
					var before []CNFinanceOutbox
					if early {
						require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return cnCaptureTopup(tx, order) }))
						require.NoError(t, db.Where("fact_id IN ?", []string{CNIdentity("payment", "topup", trade), CNIdentity("funding_lot", "topup", trade)}).Order("id").Find(&before).Error)
					}
					require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
						return creditTopUpQuotaWithFunding(tx, user, 6, nil, "payment", trade, &TopupFundingSnapshot{Payment: payment, Conversion: conversion})
					}))
					var rows []CNFinanceOutbox
					require.NoError(t, db.Where("fact_id IN ?", []string{CNIdentity("payment", "topup", trade), CNIdentity("funding_lot", "topup", trade)}).Order("id").Find(&rows).Error)
					require.Len(t, rows, 2)
					for _, row := range rows {
						var fact CNFact
						require.NoError(t, common.UnmarshalJsonStr(row.Payload, &fact))
						assert.Equal(t, fixed.Format(CNTimeLayout), fact.EffectiveAt)
						assert.Equal(t, "2026-09", fact.SourcePeriod)
					}
					var account CNWalletValueAccount
					require.NoError(t, db.First(&account, user).Error)
					if early {
						assert.Equal(t, before, rows)
						assert.EqualValues(t, 6, account.UnknownQuota)
					} else {
						assert.Zero(t, account.UnknownQuota)
					}
				}
				for _, provider := range []string{PaymentProviderEpay, PaymentProviderStripe, PaymentProviderCreem, PaymentProviderWaffo, PaymentProviderWaffoPancake} {
					user := newUser(0)
					trade := "value-provider-" + provider
					order := TopUp{UserId: user, TradeNo: trade, Amount: 2, Money: 3, PaymentProvider: provider, Status: common.TopUpStatusPending}
					require.NoError(t, db.Create(&order).Error)
					require.NoError(t, completePaymentSnapshotOrder(provider, trade, &TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "USD", PaymentReference: "receipt-" + trade}))
					require.NoError(t, db.First(&order, order.Id).Error)
					var row CNFinanceOutbox
					require.NoError(t, db.Where("fact_id = ?", CNIdentity("payment", "topup", trade)).First(&row).Error)
					var fact CNFact
					require.NoError(t, common.UnmarshalJsonStr(row.Payload, &fact))
					assert.Equal(t, time.Unix(order.CompleteTime, 0).UTC().Format(CNTimeLayout), fact.EffectiveAt)
				}
			})
			t.Run("increment_slices_restore_late_unknown_before_paid", func(t *testing.T) {
				user := newUser(0)
				require.NoError(t, credit(user, 6, "1", "USD", "value-slices"))
				task := &Task{TaskID: "value-slice-task", UserId: user, ChannelId: user, Platform: constant.TaskPlatform("alibaba"), Status: TaskStatusSubmitted, CreatedAt: time.Now().Unix()}
				row, err := PrepareCNTask(task, "value-slice-request", TaskBillingSourceWallet, 1, user, false, false)
				require.NoError(t, err)
				task.CNBillingKey, task.Quota = row.Key, 1
				task.PrivateData.TokenId, task.PrivateData.BillingSource = user, TaskBillingSourceWallet
				task.PrivateData.Execution = &TaskExecutionSnapshot{RequestID: row.RequestID}
				require.NoError(t, BeginCNTaskDispatch(row.Key, strings.Repeat("b", 64), user, "alibaba"))
				task.PrivateData.UpstreamTaskID = "slice-provider"
				require.NoError(t, RecordCNTaskUpstream(row.Key, task.PrivateData.UpstreamTaskID))
				require.NoError(t, task.Insert())
				for _, target := range []int{6, 1} {
					require.NoError(t, RequestCNTaskAdjustment(task, target, false))
					require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
					require.NoError(t, db.First(task, task.ID).Error)
				}
				key := CNIdentity("request:value-slice-request", "0")
				var part CNWalletValuePart
				require.NoError(t, db.Where("charge_key = ?", key).First(&part).Error)
				var allocation CNFundingValueAllocation
				require.NoError(t, db.Where("allocation_id = ?", part.AllocationID).First(&allocation).Error)
				assert.Equal(t, "0.166666666666666667", allocation.Value)
				require.NoError(t, credit(user, 2, "", "", "value-slice-unknown"))
				for _, target := range []int{3, 1} {
					require.NoError(t, RequestCNTaskAdjustment(task, target, false))
					require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
					require.NoError(t, db.First(task, task.ID).Error)
				}
				require.NoError(t, db.Where("allocation_id = ?", part.AllocationID).First(&allocation).Error)
				assert.EqualValues(t, 1, allocation.Quota)
				assert.Equal(t, "0.166666666666666667", allocation.Value)
				var charge CNWalletValueCharge
				require.NoError(t, db.Where("charge_key = ?", key).First(&charge).Error)
				assert.Zero(t, charge.UnknownQuota)
				require.NoError(t, decreaseUserQuota(user, 1))
				for _, target := range []int{2, 1, 0} {
					require.NoError(t, RequestCNTaskAdjustment(task, target, false))
					require.NoError(t, CompleteCNTaskIntent(context.Background(), db, row.Key))
					require.NoError(t, db.First(task, task.ID).Error)
				}
				require.NoError(t, db.Where("allocation_id = ?", part.AllocationID).First(&allocation).Error)
				assert.EqualValues(t, 0, allocation.Quota)
				assert.Equal(t, "0", allocation.Value)
				var pool CNFundingValueLot
				require.NoError(t, db.Where("lot_id = ?", part.LotID).First(&pool).Error)
				assert.Equal(t, "1", pool.RemainingValue)
				require.NoError(t, cnValidateFundingValueTx(db, &pool))
			})
			t.Run("fifo_uses_frozen_purchase_time_and_stable_lot_id", func(t *testing.T) {
				for _, sameSecond := range []bool{false, true} {
					user := newUser(0)
					trades := []string{fmt.Sprintf("value-fifo-%t-a", sameSecond), fmt.Sprintf("value-fifo-%t-b", sameSecond)}
					if sameSecond && CNIdentity("ordinary-wallet-lot", trades[0]) < CNIdentity("ordinary-wallet-lot", trades[1]) {
						trades[0], trades[1] = trades[1], trades[0]
					}
					fixed := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
					for i, trade := range trades {
						payment := &TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "USD", PaymentReference: "receipt-" + trade}
						conversion := &TopupQuotaConversion{SchemaVersion: 1, Calculation: "identity", CreditedQuota: "3"}
						p, err := common.Marshal(payment)
						require.NoError(t, err)
						c, err := common.Marshal(conversion)
						require.NoError(t, err)
						at := fixed
						if !sameSecond && i == 0 {
							at = at.Add(time.Second)
						}
						require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
							order := TopUp{UserId: user, TradeNo: trade, CompleteTime: at.Unix(), Status: common.TopUpStatusSuccess, CreditedQuota: 3, PaymentSnapshot: string(p), QuotaConversionSnapshot: string(c)}
							if err := tx.Create(&order).Error; err != nil {
								return err
							}
							return creditTopUpQuotaWithFunding(tx, user, 3, nil, "payment", trade, &TopupFundingSnapshot{Payment: payment, Conversion: conversion})
						}))
					}
					request := fmt.Sprintf("value-fifo-charge-%t", sameSecond)
					wallet(user, 3, 3, request)
					var parts []CNWalletValuePart
					require.NoError(t, db.Where("charge_key = ?", CNIdentity("request:"+request, "0")).Find(&parts).Error)
					require.Len(t, parts, 1)
					assert.Equal(t, CNIdentity("ordinary-wallet-lot", trades[1]), parts[0].LotID)
					var pool CNFundingValueLot
					require.NoError(t, db.Where("lot_id = ?", parts[0].LotID).First(&pool).Error)
					assert.Zero(t, pool.RemainingQuota)
					assert.Equal(t, "0", pool.RemainingValue)
				}
			})
			t.Run("billing_mode_writer_shapes_suspend_before_switch_back", func(t *testing.T) {
				writers := []struct {
					name  string
					write func(int, string) error
				}{
					{"map_column", func(id int, mode string) error {
						return db.Model(&User{}).Where("id = ?", id).Updates(map[string]any{"billing_mode": mode}).Error
					}},
					{"map_field", func(id int, mode string) error {
						return db.Model(&User{}).Where("id = ?", id).Updates(map[string]any{"BillingMode": mode}).Error
					}},
					{"struct", func(id int, mode string) error {
						return db.Model(&User{}).Where("id = ?", id).Updates(User{BillingMode: mode}).Error
					}},
					{"slice_model", func(id int, mode string) error {
						return db.Model(&[]User{{Id: id}}).Updates(map[string]any{"billing_mode": mode}).Error
					}},
					{"pointer_select", func(id int, mode string) error {
						return db.Model(&User{Id: id}).Select("billing_mode").Updates(&User{BillingMode: mode}).Error
					}},
					{"field_select", func(id int, mode string) error {
						return db.Model(&User{}).Where("id = ?", id).Select("BillingMode").Updates(User{BillingMode: mode}).Error
					}},
					{"star_omit_quota", func(id int, mode string) error {
						return db.Model(&User{Id: id}).Select("*").Omit("quota", "id", "username", "aff_code").Updates(User{BillingMode: mode}).Error
					}},
					{"projection", func(id int, mode string) error {
						return db.Model(&User{}).Where("id = ?", id).Updates(struct{ BillingMode string }{mode}).Error
					}},
					{"nil_primary", func(id int, mode string) error {
						return db.Table("users").Model(&struct{ BillingMode string }{}).Where("id = ?", id).Updates(struct{ BillingMode string }{mode}).Error
					}},
					{"update_column", func(id int, mode string) error {
						return db.Model(&User{Id: id}).UpdateColumn("billing_mode", mode).Error
					}},
					{"update_columns", func(id int, mode string) error {
						return db.Model(&User{Id: id}).UpdateColumns(User{BillingMode: mode}).Error
					}},
					{"skip_transaction", func(id int, mode string) error {
						return db.Session(&gorm.Session{SkipDefaultTransaction: true}).Model(&User{Id: id}).Updates(User{BillingMode: mode}).Error
					}},
					{"save", func(id int, mode string) error {
						var native User
						if err := db.First(&native, id).Error; err != nil {
							return err
						}
						native.BillingMode = mode
						return db.Save(&native).Error
					}},
				}
				unrelated := newUser(0)
				require.NoError(t, credit(unrelated, 6, "1", "USD", "value-mode-unrelated"))
				for _, writer := range writers {
					t.Run(writer.name, func(t *testing.T) {
						user := newUser(0)
						require.NoError(t, credit(user, 6, "1", "USD", "value-mode-"+writer.name))
						t.Setenv("OPS_CN_WALLET_VALUE_ENABLED", "false")
						require.NoError(t, writer.write(user, AgencyDurableBillingMode))
						var native User
						require.NoError(t, db.First(&native, user).Error)
						assert.Equal(t, AgencyDurableBillingMode, native.BillingMode)
						assert.Equal(t, 6, native.Quota)
						var account CNWalletValueAccount
						require.NoError(t, db.First(&account, user).Error)
						require.True(t, account.Suspended)
						require.NoError(t, writer.write(user, "legacy"))
						t.Setenv("OPS_CN_WALLET_VALUE_ENABLED", "true")
						require.NoError(t, credit(user, 3, "1", "USD", "value-mode-back-"+writer.name))
						wallet(user, 1, 1, "value-mode-charge-"+writer.name)
						require.NoError(t, db.First(&account, user).Error)
						assert.True(t, account.Suspended)
						var lots []CNWalletValueLot
						require.NoError(t, db.Where("user_id = ?", user).Find(&lots).Error)
						require.Len(t, lots, 1)
						assert.True(t, lots[0].Isolated)
					})
				}
				var account CNWalletValueAccount
				require.NoError(t, db.First(&account, unrelated).Error)
				assert.False(t, account.Suspended)
			})
			t.Run("omitted_mode_preserves_owner_selected_zero_invalidates", func(t *testing.T) {
				user := newUser(0)
				require.NoError(t, credit(user, 6, "1", "USD", "value-mode-selection"))
				require.NoError(t, db.Model(&User{Id: user}).Omit("BillingMode").Updates(map[string]any{"BillingMode": AgencyDurableBillingMode, "DisplayName": "still ordinary"}).Error)
				require.NoError(t, db.Model(&User{Id: user}).Select("DisplayName").Updates(User{BillingMode: AgencyDurableBillingMode, DisplayName: "still ordinary selected"}).Error)
				var account CNWalletValueAccount
				require.NoError(t, db.First(&account, user).Error)
				assert.False(t, account.Suspended)
				require.NoError(t, db.Model(&User{Id: user}).Select("billing_mode").Updates(User{BillingMode: ""}).Error)
				require.NoError(t, db.First(&account, user).Error)
				assert.True(t, account.Suspended, "an explicit zero-value mode write still invalidates the source")
			})
			t.Run("old_snapshot_cannot_hide_new_owner_or_paid_lot", func(t *testing.T) {
				if dialect == "sqlite" {
					t.Skip("SQLite serializes the writer before this external snapshot window")
				}
				for _, existing := range []bool{false, true} {
					user := newUser(0)
					if existing {
						require.NoError(t, credit(user, 6, "1", "USD", "value-old-snapshot-existing"))
					}
					tx := db.Begin()
					require.NoError(t, tx.Error)
					defer tx.Rollback()
					var before int64
					require.NoError(t, tx.Model(&CNWalletValueAccount{}).Where("user_id = ?", user).Count(&before).Error)
					require.NoError(t, credit(user, 6, "1", "USD", fmt.Sprintf("value-old-snapshot-new-%t", existing)))
					require.NoError(t, tx.Model(&User{Id: user}).Select("billing_mode").Updates(User{BillingMode: AgencyDurableBillingMode}).Error)
					require.NoError(t, tx.Model(&User{Id: user}).Select("billing_mode").Updates(User{BillingMode: "legacy"}).Error)
					require.NoError(t, tx.Commit().Error)
					var account CNWalletValueAccount
					require.NoError(t, db.First(&account, user).Error)
					assert.True(t, account.Suspended)
					assert.EqualValues(t, 6*(before+1), account.UnknownQuota)
					var lots []CNWalletValueLot
					require.NoError(t, db.Where("user_id = ?", user).Find(&lots).Error)
					require.Len(t, lots, int(before+1))
					for _, lot := range lots {
						assert.True(t, lot.Isolated)
					}
				}
			})
			t.Run("covered_writers_use_current_owner_after_old_snapshot", func(t *testing.T) {
				if dialect == "sqlite" {
					t.Skip("SQLite cannot upgrade this old read snapshot to a concurrent writer")
				}
				for _, added := range []int{6, 9} {
					for _, operation := range []string{"reserve", "final", "topup"} {
						t.Run(fmt.Sprintf("%s_%d", operation, added), func(t *testing.T) {
							user := newUser(0)
							prefix := fmt.Sprintf("value-covered-snapshot-%s-%d", operation, added)
							require.NoError(t, credit(user, 6, "1", "USD", prefix+"-initial"))
							if operation == "final" {
								ok, err := ReserveCNWallet(prefix+"-owned", user, 2, true)
								require.NoError(t, err)
								require.True(t, ok)
							}
							tx := db.Begin()
							require.NoError(t, tx.Error)
							defer tx.Rollback()
							var before CNWalletValueAccount
							require.NoError(t, tx.First(&before, user).Error)
							require.NoError(t, credit(user, added, "3", "CNY", prefix+"-concurrent"))
							wallet(user, 6, 6, prefix+"-concurrent-charge")
							if operation == "topup" {
								require.NoError(t, creditTopUpQuotaWithFunding(tx, user, 3, nil, "", ""))
							} else {
								func() {
									DB = tx
									defer func() { DB = db }()
									if operation == "reserve" {
										ok, err := ReserveCNWallet(prefix+"-owned", user, 2, true)
										require.NoError(t, err)
										require.True(t, ok)
									}
									require.NoError(t, FinalizeCNWallet(prefix+"-owned", user, 2, false))
								}()
							}
							require.NoError(t, tx.Commit().Error)
							var account CNWalletValueAccount
							require.NoError(t, db.First(&account, user).Error)
							assert.False(t, account.Suspended, "covered native changes cannot permanently invalidate the owner")
							assert.Zero(t, account.GapCount)
							unknown := int64(0)
							if operation == "topup" {
								unknown = 3
								assert.EqualValues(t, added+3, account.ExpectedQuota)
							} else {
								var charge CNWalletValueCharge
								require.NoError(t, db.Where("charge_key = ?", CNIdentity("request:"+prefix+"-owned", "0")).First(&charge).Error)
								assert.Zero(t, charge.UnknownQuota, "the newly committed paid lot must remain visible to final allocation")
							}
							assert.Equal(t, unknown, account.UnknownQuota)
							var native User
							require.NoError(t, db.First(&native, user).Error)
							assert.EqualValues(t, native.Quota, account.ExpectedQuota)
						})
					}
				}
			})
			t.Run("old_snapshot_admission_counts_current_holds_and_agency_owner", func(t *testing.T) {
				if dialect == "sqlite" {
					t.Skip("SQLite cannot upgrade this old read snapshot to a concurrent writer")
				}
				require.NoError(t, MigrateAgency(db))
				for _, switched := range []bool{false, true} {
					user := newUser(10)
					tx := db.Begin()
					require.NoError(t, tx.Error)
					defer tx.Rollback()
					var before int64
					require.NoError(t, tx.Model(&CNWalletValueAccount{}).Where("user_id = ?", user).Count(&before).Error)
					var openingTask *CNTaskBilling
					if switched {
						require.NoError(t, db.Transaction(func(other *gorm.DB) error {
							if err := other.Model(&User{Id: user}).Update("billing_mode", AgencyDurableBillingMode).Error; err != nil {
								return err
							}
							if err := EnsureAgencyFundingAccount(other, int64(user)); err != nil {
								return err
							}
							return other.Model(&User{Id: user}).Update("billing_mode", "legacy").Error
						}))
					} else {
						t.Setenv("OPS_CN_WALLET_VALUE_ENABLED", "false")
						_, err := PreConsumeCNBoundWallet("value-current-opening-wallet", user, 3, user, false, false)
						require.NoError(t, err)
						seed := &Task{TaskID: "value-current-opening-task", UserId: user, ChannelId: user, Platform: constant.TaskPlatform("alibaba"), Status: TaskStatusSubmitted}
						openingTask, err = PrepareCNTask(seed, "value-current-opening-task-request", TaskBillingSourceWallet, 2, user, false, false)
						require.NoError(t, err)
						t.Setenv("OPS_CN_WALLET_VALUE_ENABLED", "true")
					}
					account, err := cnWalletValueAccountTx(tx, user, true)
					require.NoError(t, err)
					if switched {
						assert.Nil(t, account, "a committed agency owner cannot be adopted through an old snapshot")
					} else {
						require.NotNil(t, account)
						assert.EqualValues(t, 5, account.OpeningBalance)
						assert.EqualValues(t, 10, account.OpeningQuota)
						assert.EqualValues(t, 10, account.UnknownQuota)
						assert.False(t, account.Suspended)
					}
					require.NoError(t, tx.Commit().Error)
					if openingTask != nil {
						require.NoError(t, FinalizeCNBoundWallet("value-current-opening-wallet", user, 0))
						require.NoError(t, RequestCNTaskCancellation(openingTask.Key))
						require.NoError(t, CompleteCNTaskIntent(context.Background(), db, openingTask.Key))
					}
				}
			})
			t.Run("uncovered_writers_commit_and_permanently_suspend", func(t *testing.T) {
				user := newUser(0)
				require.NoError(t, credit(user, 6, "1", "USD", "value-invalidated"))
				var original TopUp
				require.NoError(t, db.Where("trade_no = ?", "value-invalidated").First(&original).Error)
				rollback := errors.New("rollback invalidation")
				require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
					require.NoError(t, tx.Model(&User{}).Where("id = ?", user).Update("quota", gorm.Expr("quota - 1")).Error)
					return rollback
				}), rollback)
				var account CNWalletValueAccount
				require.NoError(t, db.First(&account, user).Error)
				assert.False(t, account.Suspended)
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&User{}).Where("id = ?", user).Update("quota", gorm.Expr("quota - 1")).Error; err != nil {
						return err
					}
					return tx.Model(&User{}).Where("id = ?", user).Update("quota", gorm.Expr("quota + 1")).Error
				}))
				require.NoError(t, db.First(&account, user).Error)
				assert.True(t, account.Suspended)
				assert.NotEmpty(t, account.SuspendedAt)
				wallet(user, 1, 1, "value-after-net-zero")
				require.NoError(t, credit(user, 6, "2", "CNY", "value-after-gap"))
				var lots []CNWalletValueLot
				require.NoError(t, db.Where("user_id = ?", user).Find(&lots).Error)
				require.Len(t, lots, 1)
				assert.True(t, lots[0].Isolated)
				var pool CNFundingValueLot
				require.NoError(t, db.Where("lot_id = ?", lots[0].LotID).First(&pool).Error)
				assert.Equal(t, "1", pool.RemainingValue)
				var preserved TopUp
				require.NoError(t, db.First(&preserved, original.Id).Error)
				assert.Equal(t, original, preserved)
			})
			t.Run("flags_off_and_source_switch", func(t *testing.T) {
				user := newUser(5)
				t.Setenv("OPS_CN_WALLET_VALUE_ENABLED", "false")
				require.NoError(t, credit(user, 6, "1", "USD", "value-disabled"))
				wallet(user, 2, 2, "value-disabled-request")
				var count int64
				require.NoError(t, db.Model(&CNWalletValueAccount{}).Where("user_id = ?", user).Count(&count).Error)
				assert.Zero(t, count)
				t.Setenv("OPS_CN_WALLET_VALUE_ENABLED", "true")
				owned := newUser(0)
				require.NoError(t, credit(owned, 6, "1", "USD", "value-switch"))
				require.NoError(t, MigrateAgency(db))
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&User{}).Where("id = ?", owned).Update("billing_mode", AgencyDurableBillingMode).Error; err != nil {
						return err
					}
					return EnsureAgencyFundingAccount(tx, int64(owned))
				}))
				var account CNWalletValueAccount
				require.NoError(t, db.First(&account, owned).Error)
				assert.True(t, account.Suspended)
				var opening AgencyFundingLot
				require.NoError(t, db.Where("user_id = ? AND source_kind = ?", owned, "legacy_migration").First(&opening).Error)
				assert.EqualValues(t, 6, opening.PaidInitial)
				transitionEvidence = append(transitionEvidence, map[string]any{"ordinarySuspension": account, "agencyOpening": opening, "policy": "ordinary value isolated before agency bootstrap; opening has no verified payment value"})
				require.NoError(t, db.Model(&User{}).Where("id = ?", owned).Update("billing_mode", "legacy").Error)
				require.NoError(t, credit(owned, 3, "1", "USD", "value-switch-back"))
				wallet(owned, 1, 1, "value-switch-back-request")
				require.NoError(t, db.First(&account, owned).Error)
				assert.True(t, account.Suspended)
			})
			t.Run("zero_bonus_unknown_payment_and_flags_off_existing_owner", func(t *testing.T) {
				user := newUser(0)
				require.NoError(t, credit(user, 6, "0", "CNY", "value-zero-bonus"))
				wallet(user, 3, 3, "value-zero-charge")
				require.NoError(t, credit(user, 6, "", "", "value-unknown-payment"))
				t.Setenv("OPS_CN_WALLET_VALUE_ENABLED", "false")
				require.NoError(t, credit(user, 6, "2", "CNY", "value-owner-disabled"))
				wallet(user, 3, 3, "value-disabled-owned-charge")
				var account CNWalletValueAccount
				require.NoError(t, db.First(&account, user).Error)
				assert.False(t, account.Suspended)
				assert.EqualValues(t, 9, account.UnknownQuota)
				var lotCount int64
				require.NoError(t, db.Model(&CNWalletValueLot{}).Where("user_id = ?", user).Count(&lotCount).Error)
				assert.EqualValues(t, 1, lotCount)
			})
			t.Run("flags_off_original_business_writers_keep_results", func(t *testing.T) {
				writers := []struct {
					name     string
					write    func(int) error
					expected int
				}{
					{"admin", func(id int) error { _, err := AdjustUserQuota(id, common.RoleRootUser, "add", 1); return err }, 7},
					{"grant", func(id int) error { return IncreaseUserQuota(id, 1, true) }, 7},
					{"debit", func(id int) error { return DecreaseUserQuota(id, 1, true) }, 5},
					{"legacy_reserve", func(id int) error {
						ok, err := reserveUserQuotaDB(id, 1)
						if err == nil && !ok {
							return errors.New("reserve lost")
						}
						return err
					}, 5},
					{"without_default_transaction", func(id int) error {
						return db.Session(&gorm.Session{SkipDefaultTransaction: true}).Model(&User{}).Where("id = ?", id).UpdateColumn("quota", 5).Error
					}, 5},
					{"save_zero", func(id int) error {
						var user User
						if err := db.First(&user, id).Error; err != nil {
							return err
						}
						user.Quota = 0
						return db.Save(&user).Error
					}, 0},
				}
				for _, writer := range writers {
					t.Run(writer.name, func(t *testing.T) {
						user := newUser(0)
						require.NoError(t, credit(user, 6, "1", "USD", "value-writer-"+writer.name))
						t.Setenv("OPS_CN_WALLET_VALUE_ENABLED", "false")
						require.NoError(t, writer.write(user))
						var native User
						require.NoError(t, db.First(&native, user).Error)
						assert.Equal(t, writer.expected, native.Quota)
						var account CNWalletValueAccount
						require.NoError(t, db.First(&account, user).Error)
						assert.True(t, account.Suspended)
					})
				}
			})
			t.Run("concurrent_topup_reserve_finalize", func(t *testing.T) {
				user := newUser(0)
				require.NoError(t, credit(user, 6, "1", "USD", "value-concurrent-first"))
				var wg sync.WaitGroup
				results := make(chan error, 3)
				start := make(chan struct{})
				wg.Add(3)
				for i := range 3 {
					go func() {
						defer wg.Done()
						<-start
						if i == 2 {
							results <- credit(user, 6, "1", "USD", "value-concurrent-second")
							return
						}
						request := fmt.Sprintf("value-concurrent-%d", i)
						_, err := PreConsumeCNBoundWallet(request, user, 3, user, false, false)
						if err == nil {
							err = FinalizeCNBoundWallet(request, user, 3)
						}
						results <- err
					}()
				}
				close(start)
				wg.Wait()
				close(results)
				for err := range results {
					require.NoError(t, err)
				}
				var account CNWalletValueAccount
				require.NoError(t, db.First(&account, user).Error)
				assert.False(t, account.Suspended)
				assert.EqualValues(t, 6, account.ExpectedQuota)
				var lots []CNWalletValueLot
				require.NoError(t, db.Where("user_id = ?", user).Find(&lots).Error)
				for _, lot := range lots {
					var pool CNFundingValueLot
					require.NoError(t, db.Where("lot_id = ?", lot.LotID).First(&pool).Error)
					require.NoError(t, cnValidateFundingValueTx(db, &pool))
				}
			})
			t.Run("independent_first_owners_do_not_gap_lock", func(t *testing.T) {
				users := []int{newUser(1), newUser(1), newUser(1)}
				arrived, release := make(chan struct{}, len(users)), make(chan struct{})
				if dialect != "sqlite" {
					require.NoError(t, db.Callback().Create().Before("gorm:create").Register("value-independent-owner", func(tx *gorm.DB) {
						if tx.Statement.Table == "cn_wallet_value_accounts" {
							arrived <- struct{}{}
							<-release
						}
					}))
				}
				results := make(chan error, len(users))
				for _, user := range users {
					go func() {
						request := fmt.Sprintf("value-independent-%d", user)
						_, err := PreConsumeCNBoundWallet(request, user, 1, user, false, false)
						if err == nil {
							err = FinalizeCNBoundWallet(request, user, 1)
						}
						results <- err
					}()
				}
				if dialect != "sqlite" {
					deadline := time.NewTimer(10 * time.Second)
					defer deadline.Stop()
					allArrived := true
					for range users {
						select {
						case <-arrived:
						case <-deadline.C:
							allArrived = false
						}
						if !allArrived {
							break
						}
					}
					close(release)
					assert.True(t, allArrived, "independent owners must reach insert before any commits")
				}
				for range users {
					require.NoError(t, <-results)
				}
				if dialect != "sqlite" {
					require.NoError(t, db.Callback().Create().Remove("value-independent-owner"))
				}
			})
			t.Run("cold_flags_off_recovery_and_invalidation", func(t *testing.T) {
				user := newUser(0)
				require.NoError(t, credit(user, 6, "1", "USD", "value-cold"))
				_, err := PreConsumeCNBoundWallet("value-cold-request", user, 3, user, false, false)
				require.NoError(t, err)
				require.NoError(t, RequestCNBoundWalletCancellation(db, "value-cold-request", user))
				cold, err := gorm.Open(db.Dialector, &gorm.Config{})
				require.NoError(t, err)
				cold.Logger = db.Logger
				coldPool, err := cold.DB()
				require.NoError(t, err)
				defer coldPool.Close()
				t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "false")
				t.Setenv("OPS_CN_DURABLE_WALLET_ENABLED", "false")
				t.Setenv("OPS_CN_WALLET_VALUE_ENABLED", "false")
				require.NoError(t, cnInstallWalletValueGuard(cold))
				require.NoError(t, RecoverCNWalletCancellations(context.Background(), cold, 100))
				var native User
				require.NoError(t, cold.First(&native, user).Error)
				assert.Equal(t, 6, native.Quota)
				require.NoError(t, cold.Model(&User{}).Where("id = ?", user).Update("quota", 5).Error)
				var account CNWalletValueAccount
				require.NoError(t, cold.First(&account, user).Error)
				assert.True(t, account.Suspended)
			})
			// Representative upgrade: pre-lot records and published revisions are
			// preserved, and neither migration run manufactures historical value.
			var before []CNFinanceOutbox
			require.NoError(t, db.Order("id").Find(&before).Error)
			for range 2 {
				require.NoError(t, MigrateCNFinance(db, "test-cn"))
			}
			var after []CNFinanceOutbox
			require.NoError(t, db.Order("id").Find(&after).Error)
			assert.Equal(t, before, after)
			page, err := ReadCNFacts(context.Background(), db, "", 1000)
			require.NoError(t, err)
			facts := page["items"].([]CNFact)
			require.NotEmpty(t, facts)
			assert.Zero(t, page["publicationPendingCount"])
			var checkpoint CNFinancePublication
			require.NoError(t, db.Where("fact_id IS NULL").Order("sequence DESC").First(&checkpoint).Error)
			var proof struct {
				Dependencies []CNDependency `json:"dependencies"`
			}
			require.NoError(t, common.UnmarshalJsonStr(checkpoint.Payload, &proof))
			foundFundingProof := false
			for _, dep := range proof.Dependencies {
				if dep.SourceID == "ordinary_wallet_native_funding" {
					foundFundingProof = true
					assert.Equal(t, "gap", dep.Status)
					assert.Positive(t, dep.UnknownCount)
					assert.Zero(t, dep.PendingCount)
					assert.NotNil(t, dep.ProofID)
				}
			}
			require.True(t, foundFundingProof)
			var wire []CNFact
			for _, f := range facts {
				if f.CustomerKey != nil && *f.CustomerKey == "101" {
					wire = append(wire, f)
				}
			}
			require.NotEmpty(t, wire)
			var accounts []CNWalletValueAccount
			require.NoError(t, db.Order("user_id").Find(&accounts).Error)
			page["synthetic"], page["items"], page["nativeAccounts"], page["sourceTransitions"], page["sourceProofs"] = true, wire, accounts, transitionEvidence, proof.Dependencies
			encoded, err := common.Marshal(page)
			require.NoError(t, err)
			if os.Getenv("CN_WALLET_VALUE_WRITE_EVIDENCE") == "1" {
				require.NoError(t, os.WriteFile(filepath.Join("testdata", "cn-task", "s4-wallet-value-"+dialect+".jsonl"), append(encoded, '\n'), 0600))
			}
			t.Run("clean_source_complete_then_unknown_keeps_gap", func(t *testing.T) {
				cnReset(t, db)
				require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &TopUp{}, &Channel{}, &Task{}, &TaskBillingReconciliation{}, &Option{}))
				// This synthetic source was empty before the fixture. Advance its
				// observation window without making tests wait for the wall clock.
				require.NoError(t, db.Model(&CNFinanceStream{}).Where("id = ?", 1).Update("started_at", time.Now().UTC().Add(-10*time.Minute).Format(CNTimeLayout)).Error)
				var stream CNFinanceStream
				require.NoError(t, db.First(&stream, 1).Error)
				empty, err := cnWalletValueProof(db, &stream, nil, map[string]bool{})
				require.NoError(t, err)
				assert.True(t, empty.coverageGap)
				user := newUser(0)
				require.NoError(t, credit(user, 6, "1", "USD", "value-clean-source"))
				wallet(user, 3, 3, "value-clean-request")
				previousClock := cnSourceProofNow
				proofTime := time.Now().UTC().Truncate(5 * time.Minute).Add(5 * time.Minute)
				cnSourceProofNow = func() time.Time { return proofTime }
				defer func() { cnSourceProofNow = previousClock }()
				clean, err := ReadCNFacts(context.Background(), db, "", 1000)
				require.NoError(t, err)
				checkpoint = CNFinancePublication{}
				require.NoError(t, db.Where("fact_id IS NULL").Order("sequence DESC").First(&checkpoint).Error)
				require.NoError(t, common.UnmarshalJsonStr(checkpoint.Payload, &proof))
				for _, dep := range proof.Dependencies {
					if dep.SourceID == "ordinary_wallet_native_funding" {
						assert.Equal(t, "complete", dep.Status)
						assert.Zero(t, dep.UnknownCount)
						assert.Zero(t, dep.PendingCount)
						assert.NotNil(t, dep.CapturedThrough)
						cutoff, err := time.Parse(CNTimeLayout, *dep.CoverageTo)
						require.NoError(t, err)
						for _, fact := range clean["items"].([]CNFact) {
							occurred, err := time.Parse(CNTimeLayout, fact.OccurredAt)
							require.NoError(t, err)
							assert.True(t, cutoff.After(occurred), "proof cutoff must cover actual fact %s", fact.FactID)
						}
					}
				}
				clean["sourceProofs"], clean["synthetic"] = proof.Dependencies, true
				clean["testProofClock"] = proofTime.Format(CNTimeLayout)
				require.NoError(t, increaseUserQuota(user, 1))
				proofTime = proofTime.Add(5 * time.Minute)
				_, err = ReadCNFacts(context.Background(), db, clean["nextCursor"].(string), 1000)
				require.NoError(t, err)
				checkpoint = CNFinancePublication{}
				require.NoError(t, db.Where("fact_id IS NULL").Order("sequence DESC").First(&checkpoint).Error)
				var afterProof struct {
					Dependencies []CNDependency `json:"dependencies"`
				}
				require.NoError(t, common.UnmarshalJsonStr(checkpoint.Payload, &afterProof))
				for _, dep := range afterProof.Dependencies {
					if dep.SourceID == "ordinary_wallet_native_funding" {
						assert.Equal(t, "gap", dep.Status)
						assert.Positive(t, dep.UnknownCount)
					}
				}
				clean["afterUncoveredWriterProofs"] = afterProof.Dependencies
				if os.Getenv("CN_WALLET_VALUE_WRITE_EVIDENCE") == "1" {
					encoded, err := common.Marshal(clean)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join("testdata", "cn-task", "s4-wallet-value-clean-"+dialect+".jsonl"), append(encoded, '\n'), 0600))
				}
			})
		})
	}
}
