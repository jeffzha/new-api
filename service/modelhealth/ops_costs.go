package modelhealth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/shopspring/decimal"
)

const opsCostsPath = "/api/v1/catalog/v1/model-health/costs"
const maxMoneyMicros = 1<<53 - 1 // Preserve exact integer amounts in the UI.

type OpsCosts struct {
	State      string          `json:"state"`
	Amounts    map[int64]int64 `json:"amounts"`
	WeekMicro  *int64          `json:"week_micro,omitempty"`
	TotalMicro *int64          `json:"total_micro,omitempty"`
}

var probeCostReader = sync.OnceValues(func() (*opsclient.Client, string) {
	base := os.Getenv("MODEL_HEALTH_OPS_URL")
	key := os.Getenv("MODEL_HEALTH_OPS_KEY_ID")
	secret := os.Getenv("MODEL_HEALTH_OPS_SECRET")
	if base == "" && key == "" && secret == "" {
		return nil, "unconfigured"
	}
	if opsclient.ValidateBase(base) != nil || strings.TrimSpace(key) == "" || len(secret) < 16 {
		return nil, "unavailable"
	}
	client := opsclient.New(base, key, secret, 3*time.Second)
	if client.EnableReadBudget() != nil {
		return nil, "unavailable"
	}
	return client, "unavailable"
})

func GetOpsCosts(ctx context.Context, source string, page int, limits ...int) (OpsCosts, error) {
	client, state := probeCostReader()
	if client == nil {
		if state == "unconfigured" && model.OpsFeesEnabled() {
			return readPlatformOpsCosts(ctx, source, page, time.Now(), limits...)
		}
		return OpsCosts{State: state, Amounts: map[int64]int64{}}, nil
	}
	runs, err := ListRuns(ctx, source, page, limits...)
	if err != nil {
		return OpsCosts{}, err
	}
	return readOpsCosts(ctx, client, runs[:min(runPageSize(limits), len(runs))], time.Now()), nil
}

// Reuse durable, identity-matched OPS results already fetched for usage logs.
// This does no network I/O, repricing, wallet adjustment or budget reconciliation.
func readPlatformOpsCosts(ctx context.Context, source string, page int, now time.Time, limits ...int) (OpsCosts, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	result := OpsCosts{State: "unavailable", Amounts: map[int64]int64{}}
	runs, err := ListRuns(ctx, source, page, limits...)
	if err != nil {
		return result, err
	}
	rows, err := model.ModelHealthOpsReceipts(ctx)
	if err != nil {
		return result, err
	}
	visible := make(map[int64]bool, len(runs))
	for _, run := range runs[:min(runPageSize(limits), len(runs))] {
		visible[run.ID] = true
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.RequestID]++
	}
	day := now.In(cst)
	monday := time.Unix(dayStart(now.Unix()), 0).In(cst).AddDate(0, 0, -(int(day.Weekday())+6)%7).Unix()
	week, total := decimal.Zero, decimal.Zero
	for _, row := range rows {
		if counts[row.RequestID] != 1 || !identifier.MatchString(row.RequestID) || len(row.Amount) == 0 || len(row.Amount) > 80 || strings.ContainsAny(row.Amount, "eE+ \t\r\n") {
			continue
		}
		amount, err := decimal.NewFromString(row.Amount)
		if err != nil || amount.IsNegative() || amount.Exponent() < -18 {
			continue
		}
		micro := amount.Mul(decimal.NewFromInt(1000000))
		if micro.GreaterThan(decimal.NewFromInt(maxMoneyMicros)) {
			continue
		}
		total = total.Add(micro)
		if row.CreatedAt >= monday {
			week = week.Add(micro)
		}
		if total.GreaterThan(decimal.NewFromInt(maxMoneyMicros)) {
			return OpsCosts{State: "unavailable", Amounts: map[int64]int64{}}, nil
		}
		if visible[row.ID] {
			result.Amounts[row.ID] = micro.Round(0).IntPart()
		}
	}
	w, all := week.Round(0).IntPart(), total.Round(0).IntPart()
	result.State, result.WeekMicro, result.TotalMicro = "confirmed", &w, &all
	return result, nil
}

// Only OPS-confirmed probe receipts are displayed. Provider response amounts,
// safety reservations and unrelated customer fees are never substituted.
func readOpsCosts(ctx context.Context, client *opsclient.Client, runs []model.ModelHealthRun, now time.Time) OpsCosts {
	result := OpsCosts{State: "unavailable", Amounts: map[int64]int64{}}
	day := now.In(cst)
	monday := time.Unix(dayStart(now.Unix()), 0).In(cst).AddDate(0, 0, -(int(day.Weekday())+6)%7)
	query := url.Values{"week_start": {strconv.FormatInt(monday.Unix(), 10)}}
	lookups := make(map[string][]int64)
	for _, run := range runs {
		request, task := run.RequestID, run.UpstreamTaskID
		if !identifier.MatchString(request) {
			request = ""
		}
		if !identifier.MatchString(task) {
			task = ""
		}
		if request == "" && task == "" || run.ChannelID <= 0 {
			continue
		}
		refHash := sha256.Sum256([]byte(strconv.Itoa(run.ChannelID) + "/" + request + "/" + task))
		ref := hex.EncodeToString(refHash[:])
		if _, exists := lookups[ref]; !exists {
			query.Add("probe", ref)
		}
		lookups[ref] = append(lookups[ref], run.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	body, err := client.Get(ctx, opsCostsPath+"?"+query.Encode(), now)
	if err != nil {
		return result
	}
	var response struct {
		Data struct {
			Source     string            `json:"source"`
			Currency   string            `json:"currency"`
			WeekStart  int64             `json:"week_start"`
			WeekMicro  *int64            `json:"week_micro"`
			TotalMicro *int64            `json:"total_micro"`
			Amounts    map[string]*int64 `json:"amounts"`
		} `json:"data"`
	}
	if common.Unmarshal(body, &response) != nil {
		return result
	}
	d := response.Data
	if d.Source != "model_health_receipts" || d.Currency != "CNY" || d.WeekStart != monday.Unix() ||
		d.WeekMicro == nil || d.TotalMicro == nil || *d.WeekMicro < 0 || *d.TotalMicro < *d.WeekMicro || *d.TotalMicro > maxMoneyMicros || d.Amounts == nil {
		return result
	}
	for ref, amount := range d.Amounts {
		if len(lookups[ref]) == 1 && amount != nil && *amount >= 0 && *amount <= *d.TotalMicro {
			result.Amounts[lookups[ref][0]] = *amount
		}
	}
	result.State, result.WeekMicro, result.TotalMicro = "confirmed", d.WeekMicro, d.TotalMicro
	return result
}
