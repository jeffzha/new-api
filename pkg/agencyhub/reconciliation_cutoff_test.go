package agencyhub

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconciliationHistoricalCutoffCannotCertifyCurrentBalancesAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newReconciliationScanFixture(t, dialect)
			// This balance is broken now; reading it says nothing about the
			// requested past close. A rejected historical run must not scan it
			// and publish a current discrepancy as evidence for that cutoff.
			user := model.User{Username: common.GetUUID()[:16], AffCode: common.GetUUID()[:16], Quota: 100}
			f.create(t, &user)
			account := model.AgencyFundingAccount{UserID: int64(user.Id), PaidAvailable: 99, Version: 2, UpdatedAt: time.Now().Unix()}
			f.create(t, &account)
			f.observe("funding_account", stringID(account.UserID))
			cutoff := time.Date(2026, 9, 11, 15, 59, 59, 999000000, time.UTC).UnixMilli()
			for _, request := range []struct {
				trigger string
				cutoff  int64
			}{
				{trigger: "daily", cutoff: cutoff},
				{trigger: "manual", cutoff: cutoff},
				{trigger: "scheduled", cutoff: cutoff},
				{trigger: "daily", cutoff: 0},
			} {
				key := common.GetUUID()
				run, err := f.app.RunReconciliation(context.Background(), request.trigger, key, request.cutoff)
				require.ErrorIs(t, err, errHistoricalReconciliationUnavailable)
				require.NotZero(t, run.ID)
				f.rows = append(f.rows, &run)
				assert.Equal(t, "failed", run.Status)
				assert.Equal(t, request.cutoff, run.CutoffAtMS)
				assert.Empty(t, run.SummaryJSON)
				assert.Equal(t, errHistoricalReconciliationUnavailable.Error(), run.Error)
				assert.NotNil(t, run.FinishedAtMS)

				// Replaying the durable key cannot turn the failed historical
				// close into a successful current scan.
				_, err = f.app.RunReconciliation(context.Background(), "manual", key, 0)
				require.ErrorIs(t, err, errReconciliationRunExists)
				var stored model.AgencyReconciliationRun
				require.NoError(t, f.db.First(&stored, run.ID).Error)
				assert.Equal(t, run, stored)
			}
			var issues int64
			require.NoError(t, f.db.Model(&model.AgencyReconciliationIssue{}).
				Where("object_type = ? AND object_id = ?", "funding_account", stringID(account.UserID)).Count(&issues).Error)
			assert.Zero(t, issues)
		})
	}
}

func TestReconciliationCurrentRunRecordsItsActualConsistencyScope(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}))
	for _, trigger := range []string{"manual", "scheduled"} {
		run, err := app.RunReconciliation(context.Background(), trigger, common.GetUUID(), 0)
		require.NoError(t, err)
		assert.Equal(t, "completed", run.Status)
		assert.Zero(t, run.CutoffAtMS)
		var summary struct {
			ReconcileSummary
			Consistency string `json:"consistency"`
		}
		require.NoError(t, common.Unmarshal([]byte(run.SummaryJSON), &summary))
		assert.Equal(t, "current_state_per_page", summary.Consistency)
		assert.Empty(t, run.Error)
	}
}

func TestAgencyDailyReconciliationScheduleUsesPreviousShanghaiDay(t *testing.T) {
	for _, test := range []struct {
		name     string
		now      string
		lastDate string
		key      string
		cutoff   string
		due      bool
	}{
		{name: "before 0230", now: "2026-09-12T02:29:59+08:00"},
		{name: "at 0230", now: "2026-09-12T02:30:00+08:00", key: "daily-20260912", cutoff: "2026-09-11T23:59:59.999+08:00", due: true},
		{name: "restart after 0300", now: "2026-09-12T09:45:00+08:00", key: "daily-20260912", cutoff: "2026-09-11T23:59:59.999+08:00", due: true},
		{name: "already attempted", now: "2026-09-12T09:45:00+08:00", lastDate: "2026-09-12"},
		{name: "UTC caller", now: "2026-09-11T18:30:00Z", key: "daily-20260912", cutoff: "2026-09-11T23:59:59.999+08:00", due: true},
		{name: "year boundary", now: "2027-01-01T02:30:00+08:00", lastDate: "2026-12-31", key: "daily-20270101", cutoff: "2026-12-31T23:59:59.999+08:00", due: true},
		{name: "leap day", now: "2028-03-01T02:30:00+08:00", key: "daily-20280301", cutoff: "2028-02-29T23:59:59.999+08:00", due: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339Nano, test.now)
			require.NoError(t, err)
			key, cutoff, due := agencyDailyReconciliationSchedule(now, test.lastDate)
			assert.Equal(t, test.due, due)
			assert.Equal(t, test.key, key)
			if !test.due {
				assert.Zero(t, cutoff)
				return
			}
			expected, err := time.Parse(time.RFC3339Nano, test.cutoff)
			require.NoError(t, err)
			assert.Equal(t, expected.UnixMilli(), cutoff)
		})
	}
}
