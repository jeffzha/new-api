package opswallet

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/stretchr/testify/require"
)

func TestHistoricalReviewPinsHeldBillsWithoutSettling(t *testing.T) {
	db := migrationTestDB(t)
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-test")
	t.Setenv("OPS_PUSH_KEY_ID", "overseas-test-key")
	task := model.Task{UserId: 17, ChannelId: 11, Quota: 123, Status: model.TaskStatusSuccess, PrivateData: model.TaskPrivateData{BillingSource: "wallet", TokenId: 8}}
	require.NoError(t, db.Create(&task).Error)
	bill := model.TaskBillingReconciliation{TaskID: task.ID, Status: "manual_review", LastError: "User-approved hold", PreConsumedQuota: 123}
	require.NoError(t, db.Create(&bill).Error)
	require.ErrorContains(t, ValidateSourceReadiness(t.Context(), db), "local provider bill")
	r, err := ApproveHistoricalReview(t.Context(), db, "overseas-test", "overseas-test-key", []int64{bill.ID}, "human-operator", "retain-reviewed-history")
	require.NoError(t, err)
	raw, err := common.Marshal(r)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "held.json")
	require.NoError(t, SaveMigrationFile(path, r))
	t.Setenv("OPS_WALLET_HISTORICAL_REVIEW_FILE", path)
	t.Setenv("OPS_WALLET_HISTORICAL_REVIEW_SHA256", fmt.Sprintf("%x", sha256.Sum256(raw)))
	require.NoError(t, ValidateSourceReadiness(t.Context(), db))
	for range 2 {
		require.NoError(t, db.AutoMigrate(&model.TaskBillingReconciliation{}))
		require.NoError(t, ValidateSourceReadiness(t.Context(), db))
	}
	var after model.TaskBillingReconciliation
	require.NoError(t, db.First(&after, bill.ID).Error)
	require.Equal(t, bill, after)
	_, err = PrepareOpening(t.Context(), db, opsclient.New("https://ops.example", "overseas-test-key", "a-secret-at-least-16-chars", 0), "other-scope", "writer-drain", true)
	require.ErrorContains(t, err, "differs from opening station")
	otherTask := model.Task{Status: model.TaskStatusSuccess}
	require.NoError(t, db.Create(&otherTask).Error)
	otherBill := model.TaskBillingReconciliation{TaskID: otherTask.ID, Status: "manual_review", LastError: "not approved"}
	require.NoError(t, db.Create(&otherBill).Error)
	require.ErrorContains(t, ValidateSourceReadiness(t.Context(), db), "local provider bill")
	require.NoError(t, db.Delete(&otherBill).Error)
	require.NoError(t, db.Model(&bill).Update("supplier_amount_paid", "0.1").Error)
	require.ErrorContains(t, ValidateSourceReadiness(t.Context(), db), "changed after approval")
	require.NoError(t, db.Model(&bill).UpdateColumns(map[string]any{"supplier_amount_paid": after.SupplierAmountPaid, "updated_at": after.UpdatedAt}).Error)
	require.NoError(t, ValidateSourceReadiness(t.Context(), db))
	t.Setenv("OPS_PROJECT_SCOPE", "different")
	require.ErrorContains(t, ValidateSourceReadiness(t.Context(), db), "not approved")
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-test")
	t.Setenv("OPS_WALLET_HISTORICAL_REVIEW_SHA256", "wrong")
	require.ErrorContains(t, ValidateSourceReadiness(t.Context(), db), "digest mismatch")
}

func TestHistoricalReviewRejectsActiveAndUnapprovedBills(t *testing.T) {
	db := migrationTestDB(t)
	task := model.Task{Status: model.TaskStatusInProgress}
	require.NoError(t, db.Create(&task).Error)
	bill := model.TaskBillingReconciliation{TaskID: task.ID, Status: "manual_review", LastError: "held"}
	require.NoError(t, db.Create(&bill).Error)
	_, err := ApproveHistoricalReview(t.Context(), db, "scope", "key", []int64{bill.ID}, "operator", "approval")
	require.Error(t, err)
	require.NoError(t, db.Model(&task).Update("status", model.TaskStatusFailure).Error)
	require.NoError(t, db.Model(&bill).Update("status", model.TaskBillingReconciliationPending).Error)
	_, err = ApproveHistoricalReview(t.Context(), db, "scope", "key", []int64{bill.ID}, "operator", "approval")
	require.Error(t, err)
}
