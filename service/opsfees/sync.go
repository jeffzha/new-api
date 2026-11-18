// Package opsfees reads OPS's fee results independently of relay and settlement.
package opsfees

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type Fee struct {
	RequestID     string  `json:"requestId"`
	UserID        string  `json:"userId"`
	Model         string  `json:"model"`
	Amount        *string `json:"opsAmount"`
	StationAmount *string `json:"stationAmount"`
	RefundAmount  *string `json:"refundAmount"`
	Difference    *string `json:"difference"`
	ChargedAt     string  `json:"chargedAt"`
}
type page struct {
	Data *struct {
		Items      []Fee   `json:"items"`
		NextCursor *string `json:"nextCursor"`
		HasMore    *bool   `json:"hasMore"`
	} `json:"data"`
}

type Worker struct {
	Config      Config
	DB, LogDB   *gorm.DB
	Client      *opsclient.Client
	Owner       string
	Now         func() time.Time
	nextRequest time.Time
}

// Optional bindings attach in-process receipt readers without coupling the fee
// feed to project billing. Product deployments need no binding.
func StartConfigured(ctx context.Context, bindings ...func(Config, *model.OpsFeeRuntime)) error {
	if os.Getenv("OPS_FEES_ENABLED") != "true" {
		return nil
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return errors.New("OPS fees need SQL usage logs")
	}
	c, e := LoadConfig()
	if e != nil {
		return e
	}
	if e = model.MigrateOpsFees(model.DB); e != nil {
		return e
	}
	w := New(model.DB, model.LOG_DB, c)
	sharePush.Store(nil)
	runtime := &model.OpsFeeRuntime{Connection: c.Connection, Since: c.Since, SharedLogConnection: c.SharedLogConnection, SharedLogScope: c.SharedLogScope}
	runtime.ShareResults = shareResults(ctx, c)
	for _, bind := range bindings {
		bind(c, runtime)
	}
	model.ConfigureOpsFees(runtime)
	receiptConfig.Store(&c)
	go w.Run(ctx)
	common.SysLog("OPS fee comparison enabled")
	return nil
}

func (c Config) AcceptsReceipts(client *opsclient.Client) bool {
	if client == nil || client.BaseURL != c.BaseURL {
		return false
	}
	return client.KeyID == c.KeyID && client.Secret == c.Secret || c.ReceiptKeyID != "" && client.KeyID == c.ReceiptKeyID
}

func New(db, logDB *gorm.DB, c Config) *Worker {
	owner := make([]byte, 16)
	if _, e := rand.Read(owner); e != nil {
		panic(e)
	}
	return &Worker{Config: c, DB: db, LogDB: logDB, Owner: hex.EncodeToString(owner), Now: time.Now, Client: opsclient.New(c.BaseURL, c.KeyID, c.Secret, c.Timeout)}
}

func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		wait, err := w.Step(ctx)
		if err != nil {
			common.SysError("OPS fee reader: " + err.Error())
		}
		// Product calls arrive through another process sharing the log database.
		// Recheck activity without waiting for its process-local wake signal.
		timer := time.NewTimer(max(min(wait, w.Config.ActiveInterval), time.Second))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-model.OpsFeeWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (w *Worker) Step(ctx context.Context) (time.Duration, error) {
	ctx, stepCancel := context.WithTimeout(ctx, w.Config.RoundBudget+5*time.Second)
	defer stepCancel()
	at := w.Now()
	active := model.OpsFeesRecentlyActive(ctx, w.LogDB, at)
	if active {
		// An idle connection may be brought forward when new calls arrive, but an
		// outage backoff and the active interval are never bypassed.
		if err := w.DB.WithContext(ctx).Model(&model.OpsFeeSync{}).Where("connection = ? AND next_run_at > ? AND last_success_at <= ? AND last_error = '' AND owner = ''", w.Config.Connection, at.Unix(), at.Add(-w.Config.ActiveInterval).Unix()).Update("next_run_at", at.Unix()).Error; err != nil {
			return w.Config.IdleInterval, errors.New("activity schedule unavailable")
		}
	}
	state, err := model.ClaimOpsFeeSync(ctx, w.DB, w.Config.Connection, w.Owner, at.Unix())
	if err != nil {
		return w.Config.IdleInterval, errors.New("database unavailable")
	}
	if state == nil {
		return min(w.Config.ActiveInterval, 30*time.Second), nil
	}
	roundCtx, cancel := context.WithTimeout(ctx, w.Config.RoundBudget)
	defer cancel()
	if state.Cursor == "" && w.Config.Since > 0 {
		state.Cursor = time.Unix(w.Config.Since, 0).UTC().Format(time.RFC3339) + "~0"
	}
	wait := w.Config.IdleInterval
	code := ""
	failures := state.Failures
	var stepErr error
	more := false
	for range w.Config.MaxPages {
		if persisted := time.UnixMilli(state.NextRequestAt); persisted.After(w.nextRequest) {
			w.nextRequest = persisted
		}
		if until := w.nextRequest.Sub(w.Now()); until > 0 {
			timer := time.NewTimer(until)
			select {
			case <-roundCtx.Done():
				timer.Stop()
				goto done
			case <-timer.C:
			}
		}
		target := "/api/v1/catalog/v1/fees"
		if state.Cursor != "" {
			target += "?since=" + url.QueryEscape(state.Cursor)
		}
		reserved, reserveErr := model.ReserveOpsFeeRequest(roundCtx, w.DB, w.Config.Connection, w.Owner, w.Now().UnixMilli(), w.Config.MinRequestGap.Milliseconds())
		if reserveErr != nil || !reserved {
			wait = w.Config.MinRequestGap
			code = "request_deferred"
			stepErr = errors.New(code)
			break
		}
		state.NextRequestAt = w.Now().Add(w.Config.MinRequestGap).UnixMilli()
		requestCtx, stop := context.WithTimeout(roundCtx, w.Config.Timeout)
		raw, readErr := w.Client.Get(requestCtx, target, w.Now())
		stop()
		w.nextRequest = w.Now().Add(w.Config.MinRequestGap)
		if readErr != nil {
			failures++
			wait = min(time.Minute*time.Duration(1<<min(failures-1, 3)), 5*time.Minute)
			code = "read_unavailable"
			var httpErr *opsclient.ReadError
			if errors.As(readErr, &httpErr) {
				code = "http_" + strconv.Itoa(httpErr.Status)
				wait = max(wait, httpErr.RetryAfter)
				if httpErr.Status == 401 || httpErr.Status == 403 {
					wait = max(wait, 5*time.Minute)
				}
			}
			stepErr = errors.New(code)
			break
		}
		rows, cursor, hasMore, parseErr := decodePage(raw, w.Config.Connection, w.Now().Unix())
		if cursor == "" && len(rows) == 0 && !hasMore {
			cursor = state.Cursor
		}
		if parseErr != nil || (hasMore && (cursor == "" || cursor == state.Cursor)) {
			failures++
			wait = 5 * time.Minute
			code = "invalid_response"
			stepErr = errors.New(code)
			break
		}
		if err = model.SaveOpsFeePage(roundCtx, w.DB, w.Config.Connection, w.Owner, cursor, rows, w.Now().Unix()); err != nil {
			wait = time.Minute
			code = "save_failed"
			stepErr = errors.New(code)
			failures++
			break
		}
		state.Cursor = cursor
		failures = 0
		more = hasMore
		if !hasMore {
			break
		}
	}
done:
	if code == "" {
		if more {
			wait = w.Config.MinRequestGap
		} else if active {
			wait = w.Config.ActiveInterval
		}
	}
	// An independent short DB context releases the lease even when a round times out.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer finishCancel()
	if err = model.FinishOpsFeeSync(finishCtx, w.DB, w.Config.Connection, w.Owner, w.Now().Add(wait).Unix(), code, failures); err != nil {
		return wait, errors.New("progress unavailable")
	}
	return wait, stepErr
}

func decodePage(raw []byte, connection string, at int64) ([]model.OpsFee, string, bool, error) {
	var p page
	if e := common.Unmarshal(raw, &p); e != nil || p.Data == nil || p.Data.HasMore == nil || len(p.Data.Items) > 500 {
		return nil, "", false, errors.New("invalid page")
	}
	cursor := ""
	if p.Data.NextCursor != nil {
		cursor = *p.Data.NextCursor
	}
	if len(cursor) > 256 || len(p.Data.Items) > 0 && cursor == "" {
		return nil, "", false, errors.New("invalid cursor")
	}
	rows := make([]model.OpsFee, 0, len(p.Data.Items))
	for _, f := range p.Data.Items {
		if f.RequestID == "" || len(f.RequestID) > 160 || f.UserID == "" || len(f.UserID) > 160 || f.Model == "" || len(f.Model) > 200 {
			return nil, "", false, errors.New("invalid identity")
		}
		if _, e := time.Parse(time.RFC3339Nano, f.ChargedAt); e != nil {
			return nil, "", false, errors.New("invalid time")
		}
		for i, value := range []*string{f.Amount, f.StationAmount, f.RefundAmount, f.Difference} {
			if value == nil {
				continue
			}
			if len(*value) == 0 || len(*value) > 80 || strings.ContainsAny(*value, "eE+ \t\r\n") {
				return nil, "", false, errors.New("invalid amount")
			}
			d, e := decimal.NewFromString(*value)
			if e != nil || d.Exponent() < -18 || i < 3 && d.IsNegative() {
				return nil, "", false, errors.New("invalid amount")
			}
		}
		status := "available"
		if f.Amount == nil {
			status = "unconfirmed"
		}
		rows = append(rows, model.OpsFee{Connection: connection, RequestID: f.RequestID, UserID: f.UserID, Model: f.Model, Amount: f.Amount, StationAmount: f.StationAmount, RefundAmount: f.RefundAmount, Difference: f.Difference, Currency: "CNY", ChargedAt: f.ChargedAt, FetchedAt: at, Status: status})
	}
	// Corrections in one page may repeat a request; retain its final result.
	positions := make(map[string]int, len(rows))
	unique := make([]model.OpsFee, 0, len(rows))
	for _, row := range rows {
		if i, ok := positions[row.RequestID]; ok {
			unique[i] = row
		} else {
			positions[row.RequestID] = len(unique)
			unique = append(unique, row)
		}
	}
	return unique, cursor, *p.Data.HasMore, nil
}
