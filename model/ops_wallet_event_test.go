package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOPSFundingBusinessOperationsAcrossDialects(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_PROFILE", "product")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	t.Setenv("OPS_PUSH_URL", "https://ops.example.invalid")
	t.Setenv("OPS_PUSH_KEY_ID", "business-funding-fixture")
	oldUnit, oldRate := common.QuotaPerUnit, operation_setting.USDExchangeRate
	oldRedis, oldBatch, oldCache := common.RedisEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled
	common.QuotaPerUnit, operation_setting.USDExchangeRate = 1000, 7.3
	common.RedisEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled = false, false, false
	t.Cleanup(func() {
		common.QuotaPerUnit, operation_setting.USDExchangeRate = oldUnit, oldRate
		common.RedisEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled = oldRedis, oldBatch, oldCache
	})
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("isolated external database not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			oldDB, oldLogs, mainType, logType := DB, LOG_DB, common.MainDatabaseType(), common.LogDatabaseType()
			DB, LOG_DB = db, db
			common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
			initCol()
			t.Cleanup(func() {
				DB, LOG_DB = oldDB, oldLogs
				common.SetDatabaseTypes(mainType, logType)
				initCol()
			})
			for range 2 {
				require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &Midjourney{}, &TopUp{}, &Redemption{}, &Checkin{}, &Log{}))
				require.NoError(t, PrepareOPSWalletFundingSchema(t.Context(), db))
			}
			suffix := common.GetUUID()[:16]
			user := User{Username: "funding-business-" + suffix, AffCode: suffix, Quota: 321}
			require.NoError(t, db.Create(&user).Error)
			order := TopUp{UserId: user.Id, TradeNo: "funding-business-" + suffix, Amount: 1, Money: 1, PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending}
			require.NoError(t, db.Create(&order).Error)
			payment := &TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "CNY", PaymentReference: "synthetic-receipt-" + suffix}
			done, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", payment)
			require.NoError(t, err)
			assert.False(t, done)
			done, err = RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", payment)
			require.NoError(t, err)
			assert.True(t, done, "a repeated provider callback does not create another credit")
			conflict := *payment
			conflict.ActualMoney = "2"
			_, err = RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", &conflict)
			require.ErrorIs(t, err, ErrAgencyTopupConflict)
			code := Redemption{Key: common.GetUUID(), Quota: 500, Status: common.RedemptionCodeStatusEnabled}
			require.NoError(t, db.Create(&code).Error)
			quota, err := Redeem(code.Key, user.Id)
			require.NoError(t, err)
			assert.Equal(t, 500, quota)
			_, err = Redeem(code.Key, user.Id)
			require.Error(t, err)
			checkin := &Checkin{UserId: user.Id, CheckinDate: "2026-11-25", QuotaAwarded: 200, CreatedAt: time.Now().Unix()}
			_, err = userCheckinWithTransaction(checkin, user.Id, 200)
			require.NoError(t, err)
			_, err = userCheckinWithTransaction(&Checkin{UserId: user.Id, CheckinDate: checkin.CheckinDate, QuotaAwarded: 200}, user.Id, 200)
			require.Error(t, err)
			// Rejecting a business credit must undo the code claim in the same
			// transaction; it cannot consume the code without a durable event.
			unclaimed := Redemption{Key: common.GetUUID(), Quota: 100, Status: common.RedemptionCodeStatusEnabled}
			require.NoError(t, db.Create(&unclaimed).Error)
			_, err = Redeem(unclaimed.Key, 2147483647)
			require.Error(t, err)
			var storedCode Redemption
			require.NoError(t, db.First(&storedCode, unclaimed.Id).Error)
			assert.Equal(t, common.RedemptionCodeStatusEnabled, storedCode.Status)
			assert.Zero(t, storedCode.UsedUserId)
			rows, err := ReadOPSWalletFunding(db, 100)
			require.NoError(t, err)
			var businessRows []OPSWalletEvent
			for _, row := range rows {
				var payload struct {
					Events []struct {
						UserID string `json:"userId"`
					} `json:"events"`
				}
				require.NoError(t, common.UnmarshalJsonStr(row.Payload, &payload))
				require.Len(t, payload.Events, 1)
				if payload.Events[0].UserID == fmt.Sprint(user.Id) {
					assert.NoError(t, ValidateOPSWalletFunding(row))
					assert.NotContains(t, row.Payload, code.Key, "redemption secrets never leave the business database")
					businessRows = append(businessRows, row)
				}
			}
			assert.Len(t, businessRows, 3, "payment, redemption and checkin each commit one durable event")
			var storedUser User
			require.NoError(t, db.First(&storedUser, user.Id).Error)
			assert.Equal(t, 321, storedUser.Quota, "business operations cannot also credit local money")
			assert.ErrorIs(t, IncreaseUserQuotaWithActor(user.Id, 100, 1), ErrOPSLocalWalletWrite)
			assert.ErrorIs(t, DecreaseUserQuotaWithActor(user.Id, 100, 1), ErrOPSLocalWalletWrite)
			assert.ErrorIs(t, ValidateTopUpQuotaCapacity(user.Id, 100), ErrOPSLocalWalletWrite, "payment admission cannot inspect the dormant local wallet")
			for _, mode := range []string{"add", "subtract", "override"} {
				adjustment, err := AdjustUserQuotaWithActor(user.Id, common.RoleRootUser, mode, 100, 1)
				assert.ErrorIs(t, err, ErrOPSLocalWalletWrite)
				assert.Nil(t, adjustment)
			}
			assert.ErrorIs(t, user.TransferAffQuotaToQuota(1000), ErrOPSLocalWalletWrite, "reward funds cannot bypass OPS by moving to the local wallet")
			assert.ErrorIs(t, PurchaseSubscriptionWithBalance(user.Id, 1), ErrOPSLocalWalletWrite, "an entitlement must wait for confirmed OPS payment")
			require.NoError(t, db.First(&storedUser, user.Id).Error)
			assert.Equal(t, 321, storedUser.Quota)
			// A refund must restore the API-key budget once, including after
			// rollback and observation by a stale poller. User money stays in OPS.
			token := Token{UserId: user.Id, Key: suffix, RemainQuota: 900, UsedQuota: 100}
			channel := Channel{UsedQuota: 100}
			require.NoError(t, db.Create(&token).Error)
			require.NoError(t, db.Create(&channel).Error)
			useUserCacheMiniRedis(t)
			cached, err := GetTokenByKey(token.Key, true)
			require.NoError(t, err)
			assert.Equal(t, 900, cached.RemainQuota)
			require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("used_quota", 100).Error)
			binding, err := opsauthority.NewBinding("https://ops.example.invalid", "business-funding-fixture", "")
			require.NoError(t, err)
			rawBinding, err := common.Marshal(binding)
			require.NoError(t, err)
			task := Midjourney{UserId: user.Id, MjId: "funding-refund-" + suffix, Quota: 100, TokenId: token.Id, ChannelId: channel.Id, BillingSource: "ops", OPSWalletBinding: string(rawBinding)}
			require.NoError(t, db.Create(&task).Error)
			stale := task
			refunded, err := RefundOPSMidjourney(&task, "mj_imagine")
			require.Error(t, err, "missing original request rolls back the complete transaction")
			assert.Zero(t, refunded)
			require.NoError(t, db.First(&token, token.Id).Error)
			assert.Equal(t, 900, token.RemainQuota)
			require.NoError(t, db.First(&task, task.Id).Error)
			assert.Equal(t, 100, task.Quota)
			require.NoError(t, db.Model(&Midjourney{}).Where("id = ?", task.Id).Update("billing_request_id", "refund-original-"+suffix).Error)
			t.Setenv("OPS_WALLET_AUTHORITY", "gateway")
			t.Setenv("OPS_FINANCIAL_OUTBOX_ENABLED", "true")
			refunded, err = RefundOPSMidjourney(&task, "mj_imagine")
			require.NoError(t, err)
			assert.Equal(t, 100, refunded)
			refunded, err = RefundOPSMidjourney(&stale, "mj_imagine")
			require.NoError(t, err)
			assert.Zero(t, refunded, "a second poller uses the persisted refund marker")
			require.NoError(t, db.First(&token, token.Id).Error)
			assert.Equal(t, 1000, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
			cached, err = GetTokenByKey(token.Key, false)
			require.NoError(t, err)
			assert.Equal(t, 1000, cached.RemainQuota, "authenticated API-key reads see the committed refund without a second credit")
			assert.Zero(t, cached.UsedQuota)
			require.NoError(t, db.First(&storedUser, user.Id).Error)
			assert.Equal(t, 321, storedUser.Quota)
			assert.Zero(t, storedUser.UsedQuota)
			require.NoError(t, db.First(&channel, channel.Id).Error)
			assert.Zero(t, channel.UsedQuota)
			rows, err = ReadOPSWalletFunding(db, 100)
			require.NoError(t, err)
			var refunds int
			for _, row := range rows {
				if strings.Contains(row.Payload, `"fundingSource":"midjourney_refund"`) && strings.Contains(row.Payload, task.MjId) {
					refunds++
					require.NoError(t, ValidateOPSWalletFunding(row))
					assert.Contains(t, row.Payload, "refund-original-"+suffix)
				}
			}
			assert.Equal(t, 1, refunds)
			t.Setenv("OPS_WALLET_AUTHORITY", "ops")
		})
	}
}

func TestMidjourneyWalletOwnershipSchemaAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("isolated external database not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			for _, upgrade := range []bool{false, true} {
				table := "mj_owner_" + common.GetUUID()[:16]
				view := db.Table(table)
				require.NoError(t, view.AutoMigrate(&Midjourney{}))
				legacy := Midjourney{MjId: "before-owner-column", UserId: 17, Quota: 71, TokenId: 19, BillingChannelId: 23}
				if upgrade {
					// The released layout does not contain immutable OPS ownership.
					// Preserve its outstanding refund markers during the upgrade.
					require.NoError(t, view.Migrator().DropColumn(&Midjourney{}, "BillingSource"))
					require.NoError(t, view.Migrator().DropColumn(&Midjourney{}, "OPSWalletBinding"))
					require.NoError(t, view.Migrator().DropColumn(&Midjourney{}, "BillingRequestID"))
					require.NoError(t, view.Omit("BillingSource", "OPSWalletBinding", "BillingRequestID").Create(&legacy).Error)
				}
				for range 2 {
					require.NoError(t, db.Table(table).AutoMigrate(&Midjourney{}))
				}
				if upgrade {
					var stored Midjourney
					require.NoError(t, db.Table(table).First(&stored, legacy.Id).Error)
					assert.Equal(t, legacy, stored, "legacy refunds retain their owner and amount")
				}
				binding, err := opsauthority.NewBinding("https://ops.example.invalid/test", "mj-schema-fixture", "accepted-scope")
				require.NoError(t, err)
				raw, err := common.Marshal(binding)
				require.NoError(t, err)
				owned := Midjourney{MjId: "ops-owned-task", UserId: 17, Quota: 29, BillingSource: "ops", OPSWalletBinding: string(raw), BillingRequestID: "original-request"}
				require.NoError(t, db.Table(table).Create(&owned).Error)
				var stored Midjourney
				require.NoError(t, db.Table(table).First(&stored, owned.Id).Error)
				assert.Equal(t, owned, stored)
				indexes, err := db.Table(table).Migrator().GetIndexes(&Midjourney{})
				require.NoError(t, err)
				var refundKeyIndexed bool
				for _, index := range indexes {
					if len(index.Columns()) == 1 && index.Columns()[0] == "cn_billing_key" {
						refundKeyIndexed = true
					}
				}
				assert.True(t, refundKeyIndexed, "existing durable refund lookup remains indexed")
			}
		})
	}
}

func TestOPSWalletFundingAcrossDialects(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	t.Setenv("OPS_WALLET_PROFILE", "project")
	t.Setenv("OPS_PROJECT_SCOPE", "funding-regression")
	t.Setenv("OPS_PUSH_URL", "https://ops.example.invalid/test")
	t.Setenv("OPS_PUSH_KEY_ID", "funding-regression")
	oldUnit, oldRate := common.QuotaPerUnit, operation_setting.USDExchangeRate
	t.Cleanup(func() { common.QuotaPerUnit, operation_setting.USDExchangeRate = oldUnit, oldRate })
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("isolated external database not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			// An existing user is preserved when the additive outbox is prepared.
			require.NoError(t, db.AutoMigrate(&User{}))
			suffix := common.GetUUID()[:16]
			user := User{Username: "funding-" + suffix, AffCode: suffix, Quota: 321}
			require.NoError(t, db.Create(&user).Error)
			alreadyPrepared := db.Migrator().HasTable(&OPSWalletEvent{})
			for attempt := range 2 {
				receipt, prepareErr := PrepareOPSWalletFundingSchemaReceipt(context.Background(), db)
				require.NoError(t, prepareErr)
				if attempt == 0 && !alreadyPrepared {
					assert.ElementsMatch(t, []string{"uidx_ops_wallet_event", "idx_ops_wallet_pending"}, receipt.Created)
					assert.Empty(t, receipt.Skipped)
				} else {
					assert.Empty(t, receipt.Created)
					assert.ElementsMatch(t, []string{"uidx_ops_wallet_event", "idx_ops_wallet_pending"}, receipt.Skipped)
				}
				require.NoError(t, VerifyOPSWalletFundingSchema(db))
			}
			common.QuotaPerUnit, operation_setting.USDExchangeRate = 500000, 7.3
			facts := &OPSFundingFacts{TradeNo: "order-" + suffix, PaymentMethod: "alipay", PaidMoney: "73", PaidCurrency: "CNY", MoneyCNY: "73"}
			queue := func(quota int64, recipient int) error {
				return db.Transaction(func(tx *gorm.DB) error {
					return QueueOPSWalletFundingTx(tx, int64(recipient), quota, "topup", "payment_self", facts.TradeNo, facts)
				})
			}
			require.NoError(t, queue(5000000, user.Id))
			var original OPSWalletEvent
			require.NoError(t, db.Where("intent_hash <> ?", "").Order("id DESC").First(&original).Error)
			require.NoError(t, ValidateOPSWalletFunding(original))
			for _, tc := range []struct {
				name, paid, currency, moneyCNY, reference string
			}{
				{"negative cash", "-1", "USD", "", "receipt"},
				{"exponent cash", "1e9", "USD", "", "receipt"},
				{"excess precision", "1.0000001", "USD", "", "receipt"},
				{"cash above contract limit", "1000000001", "USD", "", "receipt"},
				{"cash without currency", "1", "", "", "receipt"},
				{"invalid currency", "1", "123", "", "receipt"},
				{"unverified CNY conversion", "1", "USD", "7.3", "receipt"},
				{"different CNY cash", "1", "CNY", "2", "receipt"},
				{"malformed receipt", "1", "CNY", "1", " receipt "},
			} {
				t.Run(tc.name, func(t *testing.T) {
					badFacts := OPSFundingFacts{TradeNo: facts.TradeNo, PaidMoney: tc.paid, PaidCurrency: tc.currency, MoneyCNY: tc.moneyCNY, PaymentReference: tc.reference}
					require.Error(t, db.Transaction(func(tx *gorm.DB) error {
						return QueueOPSWalletFundingTx(tx, int64(user.Id), 5000000, "topup", "payment_self", facts.TradeNo, &badFacts)
					}))
				})
			}
			var unchanged OPSWalletEvent
			require.NoError(t, db.First(&unchanged, original.ID).Error)
			assert.Equal(t, original, unchanged, "bad payment evidence cannot replace committed money facts")
			wrongScope := original
			wrongScope.Scope = "another-ledger"
			require.Error(t, ValidateOPSWalletFunding(wrongScope))
			// Changing credentials cannot silently redirect a committed credit.
			t.Setenv("OPS_PUSH_KEY_ID", "other-ledger")
			require.ErrorContains(t, queue(5000000, user.Id), "identity conflict")
			t.Setenv("OPS_PUSH_KEY_ID", "funding-regression")
			// A callback replay cannot revalue an already committed credit.
			operation_setting.USDExchangeRate = 9
			require.NoError(t, queue(5000000, user.Id))
			var replay OPSWalletEvent
			require.NoError(t, db.First(&replay, original.ID).Error)
			assert.Equal(t, original, replay)
			require.Error(t, queue(5000001, user.Id))
			other := User{Username: "other-" + suffix, AffCode: "o" + suffix, Quota: 100}
			require.NoError(t, db.Create(&other).Error)
			require.Error(t, queue(5000000, other.Id))
			var stored User
			require.NoError(t, db.First(&stored, user.Id).Error)
			assert.Equal(t, 321, stored.Quota, "local balance must never be credited")
			var payload struct {
				SchemaVersion string `json:"schemaVersion"`
				Events        []struct {
					UserID string `json:"userId"`
					Quota  int64  `json:"gatewayQuota"`
					Extra  struct {
						Rate  string          `json:"usdExchangeRate"`
						Topup OPSFundingFacts `json:"topup"`
					} `json:"extra"`
				} `json:"events"`
			}
			require.NoError(t, common.UnmarshalJsonStr(original.Payload, &payload))
			require.Len(t, payload.Events, 1)
			assert.Equal(t, "1.1", payload.SchemaVersion)
			assert.Equal(t, fmt.Sprint(user.Id), payload.Events[0].UserID)
			assert.EqualValues(t, 5000000, payload.Events[0].Quota)
			assert.Equal(t, "7.3", payload.Events[0].Extra.Rate)
			assert.Equal(t, *facts, payload.Events[0].Extra.Topup)
			// A failed local business transaction must leave no remote event.
			err = db.Transaction(func(tx *gorm.DB) error {
				if err := QueueOPSWalletFundingTx(tx, int64(user.Id), 50, "gift", "checkin", suffix, nil); err != nil {
					return err
				}
				return errors.New("rollback business fact")
			})
			require.Error(t, err)
			var count int64
			rolledBackID := fmt.Sprintf("wf-%x", sha256.Sum256([]byte("funding-regression\x00checkin\x00"+suffix)))
			require.NoError(t, db.Model(&OPSWalletEvent{}).Where("event_id = ?", rolledBackID).Count(&count).Error)
			assert.Zero(t, count, "rolled-back business work must not be delivered")
			bad := original
			bad.Payload += " "
			require.Error(t, ValidateOPSWalletFunding(bad))
			bad = original
			bad.PayloadHash = "wrong"
			require.Error(t, AcknowledgeOPSWalletFunding(db, bad, original.CreatedAt+1))
			require.NoError(t, AcknowledgeOPSWalletFunding(db, original, original.CreatedAt+1))
			require.NoError(t, AcknowledgeOPSWalletFunding(db, original, original.CreatedAt+2))
			rows, err := ReadOPSWalletFunding(db, 100)
			require.NoError(t, err)
			for _, row := range rows {
				assert.NotEqual(t, original.EventID, row.EventID)
			}
			// Payment completion retains business metadata, but its only wallet
			// effect is a durable OPS event. Manual grants remain non-paid.
			paidSource := "callback-" + suffix
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return creditTopUpQuotaWithFunding(tx, user.Id, 500, map[string]any{"stripe_customer": "synthetic-customer"}, "payment_self", paidSource, &TopupFundingSnapshot{Payment: &TopupPaymentSnapshot{ActualMoney: "1", CurrencyCode: "CNY", PaymentReference: "synthetic-provider-receipt"}})
			}))
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return creditTopUpQuotaWithFunding(tx, user.Id, 100, nil, "admin", "manual-"+suffix)
			}))
			require.NoError(t, db.First(&stored, user.Id).Error)
			assert.Equal(t, 321, stored.Quota)
			assert.Equal(t, "synthetic-customer", stored.StripeCustomer)
			rows, err = ReadOPSWalletFunding(db, 100)
			require.NoError(t, err)
			require.Len(t, rows, 2)
			// A product sender on this shared database must never deliver the
			// project's pending credits to its own credential-owned ledger.
			t.Setenv("OPS_WALLET_PROFILE", "product")
			productRows, err := ReadOPSWalletFunding(db, 100)
			require.NoError(t, err)
			for _, productRow := range productRows {
				for _, projectRow := range rows {
					assert.NotEqual(t, projectRow.EventID, productRow.EventID, "product sender cannot read a project credit")
				}
			}
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return QueueOPSWalletFundingTx(tx, int64(user.Id), 100, "gift", "checkin", suffix, nil)
			}))
			productRows, err = ReadOPSWalletFunding(db, 100)
			require.NoError(t, err)
			productID := fmt.Sprintf("wf-%x", sha256.Sum256([]byte("\x00checkin\x00"+suffix)))
			var productEvent *OPSWalletEvent
			for i := range productRows {
				if productRows[i].EventID == productID {
					productEvent = &productRows[i]
				}
			}
			require.NotNil(t, productEvent)
			assert.Empty(t, productEvent.Scope)
			t.Setenv("OPS_WALLET_PROFILE", "project")
			projectRows, err := ReadOPSWalletFunding(db, 100)
			require.NoError(t, err)
			assert.Equal(t, rows, projectRows)
			kinds := map[string]int{}
			for _, row := range rows {
				var batch struct {
					Events []struct {
						Extra struct {
							Kind  string           `json:"recordKind"`
							Topup *OPSFundingFacts `json:"topup"`
						} `json:"extra"`
					} `json:"events"`
				}
				require.NoError(t, common.UnmarshalJsonStr(row.Payload, &batch))
				require.Len(t, batch.Events, 1)
				event := batch.Events[0]
				kinds[event.Extra.Kind]++
				if event.Extra.Kind == "gift" {
					assert.Nil(t, event.Extra.Topup, "manual completion is not a verified payment")
				} else {
					require.NotNil(t, event.Extra.Topup)
					assert.Equal(t, "1", event.Extra.Topup.MoneyCNY)
					assert.Equal(t, "CNY", event.Extra.Topup.PaidCurrency)
					assert.Equal(t, "synthetic-provider-receipt", event.Extra.Topup.PaymentReference)
				}
			}
			assert.Equal(t, map[string]int{"topup": 1, "gift": 1}, kinds)
			// An existing name with a different definition must block startup;
			// neither a successful earlier release nor an ORM name check is proof.
			require.NoError(t, db.Migrator().DropIndex(&OPSWalletEvent{}, "idx_ops_wallet_pending"))
			require.Error(t, VerifyOPSWalletFundingSchema(db))
			require.NoError(t, PrepareOPSWalletFundingSchema(context.Background(), db))
			require.NoError(t, db.First(&replay, original.ID).Error)
			assert.Equal(t, original.Payload, replay.Payload, "index preparation must preserve immutable money events")
			require.NoError(t, db.Migrator().DropIndex(&OPSWalletEvent{}, "idx_ops_wallet_pending"))
			require.NoError(t, db.Exec("CREATE INDEX idx_ops_wallet_pending ON ops_wallet_events (id, delivered_at)").Error)
			require.NoError(t, db.Migrator().DropIndex(&OPSWalletEvent{}, "uidx_ops_wallet_event"))
			require.ErrorContains(t, VerifyOPSWalletFundingSchema(db), "definition conflict")
			require.ErrorContains(t, PrepareOPSWalletFundingSchema(context.Background(), db), "definition conflict")
			assert.False(t, db.Migrator().HasIndex(&OPSWalletEvent{}, "uidx_ops_wallet_event"), "conflicting definitions must be found before any missing index is created")
			require.NoError(t, db.Migrator().DropIndex(&OPSWalletEvent{}, "idx_ops_wallet_pending"))
			require.NoError(t, PrepareOPSWalletFundingSchema(context.Background(), db))
			require.NoError(t, VerifyOPSWalletFundingSchema(db))
			// Names alone do not make a compatible outbox. Preparation must
			// reject an incompatible restored table without rewriting it.
			require.NoError(t, db.Migrator().RenameTable("ops_wallet_events", "ops_wallet_events_preserved"))
			require.NoError(t, db.Exec(`CREATE TABLE ops_wallet_events (
				id bigint, scope varchar(80), event_id varchar(80), payload text,
				payload_hash varchar(64), intent_hash varchar(64), created_at bigint, delivered_at bigint)`).Error)
			require.ErrorContains(t, VerifyOPSWalletFundingSchema(db), "primary key conflict")
			require.ErrorContains(t, PrepareOPSWalletFundingSchema(t.Context(), db), "primary key conflict")
			assert.False(t, db.Migrator().HasIndex("ops_wallet_events", "uidx_ops_wallet_event"))
			require.NoError(t, db.Migrator().DropTable("ops_wallet_events"))
			require.NoError(t, db.Migrator().RenameTable("ops_wallet_events_preserved", "ops_wallet_events"))
			require.NoError(t, VerifyOPSWalletFundingSchema(db))
			require.NoError(t, db.First(&replay, original.ID).Error)
			assert.Equal(t, original.Payload, replay.Payload)
		})
	}
}

func TestOPSRegistrationFundingAcrossDialects(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_WALLET_FUNDING_ENABLED", "true")
	t.Setenv("OPS_WALLET_PROFILE", "product")
	t.Setenv("OPS_PUSH_URL", "https://ops.example.invalid")
	t.Setenv("OPS_PUSH_KEY_ID", "registration-fixture")
	oldNew, oldInvitee := common.QuotaForNewUser, common.QuotaForInvitee
	oldPayment := *operation_setting.GetPaymentSetting()
	common.QuotaForNewUser, common.QuotaForInvitee = 200, 100
	operation_setting.GetPaymentSetting().ComplianceConfirmed = true
	operation_setting.GetPaymentSetting().ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() {
		common.QuotaForNewUser, common.QuotaForInvitee = oldNew, oldInvitee
		*operation_setting.GetPaymentSetting() = oldPayment
	})
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := agencyDialectDB(t, dialect)
			if db == nil {
				t.Skip("isolated external database not configured")
			}
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			oldDB, oldMain, oldLog := DB, common.MainDatabaseType(), common.LogDatabaseType()
			DB = db
			common.SetDatabaseTypes(common.DatabaseType(dialect), oldLog)
			t.Cleanup(func() { DB = oldDB; common.SetDatabaseTypes(oldMain, oldLog) })
			require.NoError(t, db.AutoMigrate(&User{}))
			require.NoError(t, PrepareOPSWalletFundingSchema(t.Context(), db))
			suffix := common.GetUUID()[:16]
			password := "synthetic-registration-password"
			user := User{Username: "registration-" + suffix, Email: suffix + "@example.invalid", Password: password}
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return user.InsertWithTx(tx, 81) }))
			var stored User
			require.NoError(t, db.First(&stored, user.Id).Error)
			assert.Zero(t, stored.Quota, "registration money is owned by OPS")
			assert.NotEqual(t, password, stored.Password)
			assert.True(t, common.ValidatePasswordAndHash(password, stored.Password))
			assert.False(t, common.ValidatePasswordAndHash("wrong-password", stored.Password))
			var rows []OPSWalletEvent
			require.NoError(t, db.Where("event_id IN ?", []string{
				fmt.Sprintf("wf-%x", sha256.Sum256([]byte("\x00registration\x00"+fmt.Sprint(user.Id)))),
				fmt.Sprintf("wf-%x", sha256.Sum256([]byte("\x00invitation\x00"+fmt.Sprint(user.Id)))),
			}).Order("id").Find(&rows).Error)
			require.Len(t, rows, 2)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return user.queueOPSRegistrationGiftsTx(tx, 81) }))
			for _, row := range rows {
				var replay OPSWalletEvent
				require.NoError(t, db.First(&replay, row.ID).Error)
				assert.Equal(t, row, replay, "replayed registration hooks cannot create a second credit")
			}
			duplicate := User{Username: "duplicate-" + suffix, Email: user.Email, Password: password}
			require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return duplicate.InsertWithTx(tx, 81) }), ErrEmailAlreadyTaken)
			rolledBack := User{Username: "rolledback-" + suffix, Password: password}
			rollback := errors.New("OAuth binding rejected")
			require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
				if err := rolledBack.InsertWithTx(tx, 81); err != nil {
					return err
				}
				return rollback
			}), rollback)
			var count int64
			require.NoError(t, db.Model(&User{}).Where("username = ?", rolledBack.Username).Count(&count).Error)
			assert.Zero(t, count)
			id := fmt.Sprintf("wf-%x", sha256.Sum256([]byte("\x00registration\x00"+fmt.Sprint(rolledBack.Id))))
			require.NoError(t, db.Model(&OPSWalletEvent{}).Where("event_id = ?", id).Count(&count).Error)
			assert.Zero(t, count, "a failed OAuth binding cannot leave a gift to deliver")
			for _, source := range []string{LogQuotaSourceRegister, LogQuotaSourceInvitee} {
				assert.Equal(t, "ops_funding", walletLogFields(source, false, nil)["wallet_event_owner"])
			}
		})
	}
}
