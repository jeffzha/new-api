package opswallet

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// HistoricalReview approves carrying exact, already held bills across custody.
// It never approves settling them or changes any source record or balance.
type HistoricalReview struct {
	Version    int                    `json:"version"`
	Scope      string                 `json:"scope"`
	KeyID      string                 `json:"keyId"`
	ApprovedBy string                 `json:"approvedBy"`
	ApprovedAt string                 `json:"approvedAt"`
	Evidence   string                 `json:"evidence"`
	Bills      []HistoricalReviewBill `json:"bills"`
}

type HistoricalReviewBill struct {
	ID     int64  `json:"id"`
	SHA256 string `json:"sha256"`
}

func historicalBillDigest(ctx context.Context, db *gorm.DB, id int64) (string, error) {
	var bill model.TaskBillingReconciliation
	if err := db.WithContext(ctx).First(&bill, id).Error; err != nil {
		return "", err
	}
	var task model.Task
	if err := db.WithContext(ctx).Select("id, task_id, user_id, channel_id, quota, status, private_data").First(&task, bill.TaskID).Error; err != nil {
		return "", err
	}
	if bill.Status != "manual_review" || strings.TrimSpace(bill.LastError) == "" || task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure || task.PrivateData.BillingSource == "ops" {
		return "", errors.New("historical review requires a held terminal local provider bill")
	}
	// Hash the complete bill and stable financial task context, excluding keys,
	// customer content and result URLs. Nonfinancial task polling cannot unpin it.
	raw, err := common.Marshal(struct {
		Bill                              model.TaskBillingReconciliation
		TaskID                            int64
		ExternalID                        string
		UserID, ChannelID, Quota, TokenID int
		Status                            model.TaskStatus
		Source, Scope                     string
		Billing                           *model.TaskBillingContext
	}{bill, task.ID, task.TaskID, task.UserId, task.ChannelId, task.Quota, task.PrivateData.TokenId, task.Status, task.PrivateData.BillingSource, task.PrivateData.BillingScope, task.PrivateData.BillingContext})
	return fmt.Sprintf("%x", sha256.Sum256(raw)), err
}

func ApproveHistoricalReview(ctx context.Context, db *gorm.DB, scope, keyID string, ids []int64, operator, evidence string) (*HistoricalReview, error) {
	if strings.TrimSpace(scope) == "" || keyID == "" || strings.TrimSpace(operator) == "" || len(operator) > 100 || strings.TrimSpace(evidence) == "" || len(evidence) > 200 || len(ids) == 0 || len(ids) > 100 {
		return nil, errors.New("explicit historical review identity and bill IDs required")
	}
	r := &HistoricalReview{Version: 1, Scope: scope, KeyID: keyID, ApprovedBy: operator, ApprovedAt: time.Now().UTC().Format(time.RFC3339Nano), Evidence: evidence}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, errors.New("invalid or duplicate historical bill")
		}
		seen[id] = true
		digest, err := historicalBillDigest(ctx, db, id)
		if err != nil {
			return nil, err
		}
		r.Bills = append(r.Bills, HistoricalReviewBill{ID: id, SHA256: digest})
	}
	return r, nil
}

func historicalReview(ctx context.Context, db *gorm.DB) (string, map[int64]bool, error) {
	path, expected := os.Getenv("OPS_WALLET_HISTORICAL_REVIEW_FILE"), os.Getenv("OPS_WALLET_HISTORICAL_REVIEW_SHA256")
	if path == "" && expected == "" {
		return "", nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil, errors.New("historical review unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil || len(raw) > 64*1024 || fmt.Sprintf("%x", sha256.Sum256(raw)) != expected {
		return "", nil, errors.New("historical review digest mismatch")
	}
	var r HistoricalReview
	if common.Unmarshal(raw, &r) != nil {
		return "", nil, errors.New("invalid historical review")
	}
	approved, err := time.Parse(time.RFC3339Nano, r.ApprovedAt)
	if err != nil || approved.After(time.Now().Add(time.Minute)) || r.Version != 1 || r.Scope != os.Getenv("OPS_PROJECT_SCOPE") || r.Scope == "" || r.KeyID != os.Getenv("OPS_PUSH_KEY_ID") || strings.TrimSpace(r.ApprovedBy) == "" || len(r.ApprovedBy) > 100 || strings.TrimSpace(r.Evidence) == "" || len(r.Evidence) > 200 || len(r.Bills) == 0 || len(r.Bills) > 100 {
		return "", nil, errors.New("historical review is not approved for this station")
	}
	ids := map[int64]bool{}
	for _, b := range r.Bills {
		if b.ID <= 0 || ids[b.ID] {
			return "", nil, errors.New("invalid historical review bill")
		}
		digest, err := historicalBillDigest(ctx, db, b.ID)
		if err != nil || digest != b.SHA256 {
			return "", nil, errors.New("historical held bill changed after approval")
		}
		ids[b.ID] = true
	}
	return expected, ids, nil
}
