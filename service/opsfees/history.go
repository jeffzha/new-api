package opsfees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
)

type HistoryResult struct {
	Rows     int
	Complete bool
	Wait     time.Duration
}

// HistoryStep fetches at most one indexed page (500 fees), on a cursor that
// cannot rewind or delay live fees. No usage, pricing, or wallet POST is sent.
// Run only with an explicit reviewed since value; never from app startup.
func (w *Worker) HistoryStep(ctx context.Context, since time.Time, gap time.Duration) (HistoryResult, error) {
	result := HistoryResult{Wait: gap}
	if since.IsZero() || gap < 10*time.Second || gap > time.Minute {
		return result, errors.New("invalid history bounds")
	}
	initial := since.UTC().Format(time.RFC3339) + "~0"
	hash := sha256.Sum256([]byte("fee-history-v1\n" + w.Config.Connection + "\n" + initial))
	progress := hex.EncodeToString(hash[:])
	ctx, cancel := context.WithTimeout(ctx, w.Config.Timeout+5*time.Second)
	defer cancel()
	state, err := model.ClaimOpsFeeSync(ctx, w.DB, progress, w.Owner, w.Now().Unix())
	if err != nil {
		return result, errors.New("history progress unavailable")
	}
	if state == nil {
		var saved model.OpsFeeSync
		if err := w.DB.WithContext(ctx).Where("connection = ?", progress).First(&saved).Error; err != nil {
			return result, errors.New("history progress unavailable")
		}
		result.Complete = saved.NextRunAt == model.OpsFeeHistoryComplete
		if !result.Complete {
			result.Wait = max(gap, time.Duration(max(0, saved.NextRunAt-w.Now().Unix()))*time.Second)
		}
		return result, nil
	}
	code, failures := "history_failed", state.Failures+1
	defer func() {
		// Safe after success too: SaveOpsFeeHistoryPage already released ownership.
		finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		_ = model.FinishOpsFeeSync(finish, w.DB, progress, w.Owner, w.Now().Add(result.Wait).Unix(), code, failures)
	}()
	reserved, err := model.ReserveOpsFeeRequest(ctx, w.DB, progress, w.Owner, w.Now().UnixMilli(), gap.Milliseconds())
	if err != nil || !reserved {
		return result, errors.New("history request deferred")
	}
	cursor := state.Cursor
	if cursor == "" {
		cursor = initial
	}
	raw, err := w.Client.Get(ctx, "/api/v1/catalog/v1/fees?since="+url.QueryEscape(cursor), w.Now())
	if err != nil {
		result.Wait = time.Minute
		code = "read_unavailable"
		var httpErr *opsclient.ReadError
		if errors.As(err, &httpErr) {
			code = "http_" + strconv.Itoa(httpErr.Status)
			result.Wait = max(result.Wait, httpErr.RetryAfter)
			if httpErr.Status == 401 || httpErr.Status == 403 {
				result.Wait = max(result.Wait, 5*time.Minute)
			}
		}
		return result, errors.New(code)
	}
	rows, next, more, err := decodePage(raw, w.Config.Connection, w.Now().Unix(), w.Config.Currency)
	if err != nil || more && (next == "" || next == cursor) {
		code, result.Wait = "invalid_response", 5*time.Minute
		return result, errors.New(code)
	}
	if next == "" {
		next = cursor
	}
	if err := model.SaveOpsFeeHistoryPage(ctx, w.DB, progress, w.Config.Connection, w.Owner, next, rows, w.Now().Unix(), w.Now().Add(gap).Unix(), !more); err != nil {
		return result, errors.New("history cache save failed")
	}
	result.Rows, result.Complete = len(rows), !more
	return result, nil
}
