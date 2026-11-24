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

func GetOpsCosts(ctx context.Context, source string, page int) (OpsCosts, error) {
	client, state := probeCostReader()
	if client == nil {
		return OpsCosts{State: state, Amounts: map[int64]int64{}}, nil
	}
	runs, err := ListRuns(ctx, source, page)
	if err != nil {
		return OpsCosts{}, err
	}
	return readOpsCosts(ctx, client, runs[:min(25, len(runs))], time.Now()), nil
}

// Only OPS-confirmed probe receipts are displayed. Provider response amounts,
// safety reservations and customer fee records are intentionally not consulted.
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
