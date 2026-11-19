package opspricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestScheduledOfficialPriceSwitchesOnlyAtEffectiveTime(t *testing.T) {
	at := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	p := priceEntry{Official: map[string]any{"per_image": "0.1"}, Upcoming: []scheduledPrice{{EffectiveAt: at.Add(time.Minute).Format(time.RFC3339), Official: map[string]any{"per_image": "0.2"}}}}
	require.NoError(t, validateSchedule(p.Upcoming))
	require.Equal(t, "0.1", p.officialAt(at.Add(59 * time.Second))["per_image"])
	require.Equal(t, "0.2", p.officialAt(at.Add(time.Minute))["per_image"])
	require.Equal(t, "0.2", p.officialAt(at.Add(time.Hour))["per_image"], "retained memory schedule works during an outage")
	p.Upcoming = append(p.Upcoming, p.Upcoming[0])
	require.Error(t, validateSchedule(p.Upcoming))
}

func TestReaderRetainsOfficialSnapshotAcrossUnchangedAndFailedReads(t *testing.T) {
	step := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/api/v1/catalog/v1/prices", req.URL.Path)
		expected := opsclient.SignWithNonce("station", "test-secret-for-prices", "GET", req.URL.RequestURI(), nil, req.Header.Get("X-Timestamp"), req.Header.Get("X-Nonce"))
		require.Equal(t, expected.Get("X-Signature"), req.Header.Get("X-Signature"))
		step++
		switch step {
		case 1:
			require.Empty(t, req.URL.Query().Get("since"))
			_, _ = w.Write([]byte(`{"data":{"version":"v1","station":{"quotaPerUnit":500000,"usdExchangeRate":"7.3"},"items":[{"model":"text","callType":"text","officialCny":{"input_per_million":"8","output_per_million":"28"},"defaultSale":{"pricesCny":{"input_per_million":"0.01"}},"userLevels":[{"pricesCny":{"input_per_million":"0"}}]}]}}`))
		case 2:
			require.Equal(t, "v1", req.URL.Query().Get("since"))
			w.WriteHeader(http.StatusNotModified)
		case 3:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte(`{"data":{"version":"bad","station":{"quotaPerUnit":0},"items":[]}}`))
		}
	}))
	defer server.Close()
	reader := New(Config{BaseURL: server.URL, KeyID: "station", Secret: "test-secret-for-prices", Interval: 5 * time.Minute, Timeout: time.Second})
	require.NoError(t, reader.Refresh(context.Background()))
	local := []model.Pricing{{ModelName: "text", ModelRatio: 4 / 7.3, CompletionRatio: 3.5}}
	original, err := common.Marshal(local)
	require.NoError(t, err)
	display := reader.Display(local, 7.3, 500000)
	require.Equal(t, "ops", display[0].ListPrice.Source)
	require.Equal(t, "match", display[0].ListPrice.Comparison)
	require.Equal(t, "8", display[0].ListPrice.Official["input_per_million"])
	require.Contains(t, display[0].BillingExpr, decimal.NewFromInt(8).DivRound(decimal.NewFromFloat(7.3), 24).String())
	require.NotContains(t, display[0].BillingExpr, "0.01")
	require.NoError(t, reader.Refresh(context.Background()))
	require.False(t, reader.Status().Stale)
	for range 2 {
		require.Error(t, reader.Refresh(context.Background()))
		require.True(t, reader.Status().Stale)
		require.Equal(t, "v1", reader.Status().Version)
		require.Equal(t, display, reader.Display(local, 7.3, 500000))
	}
	after, err := common.Marshal(local)
	require.NoError(t, err)
	require.Equal(t, string(original), string(after), "display must not change the local billing snapshot")
}

func TestDisplayFallbackAndDifferenceSemantics(t *testing.T) {
	local := model.Pricing{ModelName: "model", ModelRatio: 1, CompletionRatio: 2, EnableGroup: []string{"default"}}
	cases := []struct {
		name                       string
		official                   map[string]any
		wantSource, wantComparison string
	}{
		{"missing", nil, "local", "unavailable"},
		{"zero is a real free price", map[string]any{"input_per_million": "0", "output_per_million": "0"}, "ops", "differ"},
		{"negative", map[string]any{"input_per_million": "-1"}, "local", "unsupported"},
		{"unknown dimension", map[string]any{"audio_per_second": "1"}, "local", "unsupported"},
		{"malformed value", map[string]any{"input_per_million": nil}, "local", "unsupported"},
		{"unbounded exponent", map[string]any{"input_per_million": "1e-99999999"}, "local", "unsupported"},
		{"request and tokens mixed", map[string]any{"per_request": "1", "input_per_million": "2"}, "local", "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New(Config{})
			s := &snapshot{byModel: map[string][]priceEntry{"model": {{Model: "model", CallType: "text", Official: tc.official}}}}
			s.list.Station.USDExchangeRate = "7.3"
			s.list.Station.QuotaPerUnit = 500000
			r.current.Store(s)
			row := r.Display([]model.Pricing{local}, 7.3, 500000)[0]
			require.Equal(t, tc.wantSource, row.ListPrice.Source)
			require.Equal(t, tc.wantComparison, row.ListPrice.Comparison)
			require.Equal(t, local, row.ListPrice.Local)
			if tc.wantSource == "local" {
				require.Equal(t, local, row.Pricing)
			}
		})
	}
	r := New(Config{})
	require.Equal(t, "local", r.Display([]model.Pricing{local}, 7.3, 500000)[0].ListPrice.Source)
	s := &snapshot{byModel: map[string][]priceEntry{"model": {{Official: map[string]any{"per_request": "1"}}}}}
	s.list.Station.USDExchangeRate = "7.2"
	s.list.Station.QuotaPerUnit = 500000
	r.current.Store(s)
	require.Equal(t, "conversion_mismatch", r.Display([]model.Pricing{local}, 7.3, 500000)[0].ListPrice.Comparison)
	s.list.Station.USDExchangeRate = "7.3"
	s.byModel["model"] = append(s.byModel["model"], priceEntry{})
	require.Equal(t, "review", r.Display([]model.Pricing{local}, 7.3, 500000)[0].ListPrice.Comparison)
}

func TestPriceProjectionPreservesTaskUnitsAndLocalStructures(t *testing.T) {
	local := model.Pricing{ModelName: "video", BillingMode: "tiered_expr", BillingExpr: `tier("local", fixed(0.5))`, BillingUsageSchema: map[string]jsplugin.UsageFieldSchema{"seconds": {Type: "number", Unit: "second"}}, BillingPluginVariants: []model.PricingPluginVariant{{PluginKey: "local-provider"}}}
	before, err := common.Marshal(local)
	require.NoError(t, err)
	projected, err := projectPrice(local, map[string]any{"per_second": "0.5", "per_reference_image": "0.2", "matrix": []any{map[string]any{"when": map[string]any{"resolution": "2K"}, "per_second": "0.8"}}}, decimal.NewFromFloat(7.3))
	require.NoError(t, err)
	require.Contains(t, projected.BillingExpr, `u("resolution") == "2K"`)
	require.Contains(t, projected.BillingExpr, `u("seconds")`)
	require.Contains(t, projected.BillingExpr, `u("input_images")`)
	require.Len(t, projected.BillingUsageSchema, 3)
	require.Equal(t, []string{"2K"}, projected.BillingUsageSchema["resolution"].Enum)
	require.Nil(t, projected.BillingPluginVariants)
	_, err = billingexpr.CompileFromCache(projected.BillingExpr)
	require.NoError(t, err)
	after, err := common.Marshal(local)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	require.Equal(t, "review", comparePrice(local, projected, map[string]any{}, 7.3, 500000))
	tokens, err := projectPrice(model.Pricing{}, map[string]any{"per_million_tokens": "10"}, decimal.NewFromInt(1))
	require.NoError(t, err)
	require.Contains(t, tokens.BillingExpr, `u("billed_tokens") * 10 / 1000000`)
}

func TestUnchangedNeedsInitialSnapshotAndDoesNotChangePlainReads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) }))
	defer server.Close()
	r := New(Config{BaseURL: server.URL, Timeout: time.Second})
	require.ErrorContains(t, r.Refresh(context.Background()), "no initial snapshot")
	_, err := r.Client.Get(context.Background(), "/fees", time.Now())
	require.ErrorContains(t, err, "304")
}

func TestPriceConfigIsExplicitAndBounded(t *testing.T) {
	t.Setenv("OPS_PRICING_ENABLED", "")
	t.Setenv("OPS_PRICING_URL", "")
	require.NoError(t, StartConfigured(context.Background()))
	require.Nil(t, Default)
	t.Setenv("OPS_PRICING_URL", "https://ops.example/test")
	t.Setenv("OPS_PRICING_KEY_ID", "station")
	t.Setenv("OPS_PRICING_SECRET", strings.Repeat("s", 20))
	t.Setenv("OPS_PRICING_INTERVAL_SECONDS", "300")
	t.Setenv("OPS_PRICING_TIMEOUT_SECONDS", "5")
	c, err := LoadConfig()
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, c.Interval)
	t.Setenv("OPS_PRICING_INTERVAL_SECONDS", "1")
	_, err = LoadConfig()
	require.Error(t, err)
}

func TestCatalogReadDoesNotWaitForPendingOPSRequest(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	r := New(Config{BaseURL: server.URL, Timeout: 5 * time.Second, Interval: 5 * time.Minute})
	done := make(chan error, 1)
	go func() { done <- r.Refresh(context.Background()) }()
	<-started
	read := make(chan struct{})
	go func() {
		_ = r.Display([]model.Pricing{{ModelName: "local"}}, 7.3, 500000)
		_ = r.Status()
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("catalog waited for the pending OPS request")
	}
	close(release)
	require.Error(t, <-done)
}

func TestOfficialDisplayRowsPreserveConditionsAndUnits(t *testing.T) {
	var components map[string]any
	require.NoError(t, common.UnmarshalJsonStr(`{"input_per_million":"0.8","audio_input_per_million":"20.4","matrix":[{"when":{"inputMax":128000},"input_per_million":"0.4"},{"when":{"inputMax":32000},"input_per_million":"0.2"}]}`, &components))
	rows, err := displayRows(components)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.EqualValues(t, 32000, rows[0].When["inputMax"])
	require.Equal(t, "0.2", rows[0].Prices["input_per_million"])
	require.Equal(t, "20.4", rows[0].Prices["audio_input_per_million"], "each tier inherits the default audio price")
	require.EqualValues(t, 128000, rows[1].When["inputMax"])
	require.Empty(t, rows[2].When)
}

func TestOfficialDisplayRejectsPartialOrAmbiguousRules(t *testing.T) {
	for _, raw := range []string{
		`{"per_image":"1","matrix":[{"when":{"resolution":"2K","future":true},"per_image":"2"}]}`,
		`{"per_second":"1","matrix":[{"when":{"resolution":"2K"},"per_second":"2"},{"when":{"resolution":"2k"},"per_second":"3"}]}`,
		`{"input_per_million":"1","matrix":[{"when":{"inputMax":0},"input_per_million":"2"}]}`,
		`{"per_second":"1","matrix":[{"when":{"referenceVideo":"true"},"per_second":"2"}]}`,
		`{"per_second":"1","matrix":[{"when":{"resolution":"2K"}}]}`,
	} {
		var components map[string]any
		require.NoError(t, common.UnmarshalJsonStr(raw, &components))
		_, err := displayRows(components)
		require.Error(t, err, raw)
	}
}

func TestPublicCatalogOfficialRulesAreCompleteAndDoNotChangeLocalPricing(t *testing.T) {
	raw, err := os.ReadFile("testdata/official-prices.json")
	require.NoError(t, err)
	var envelope struct {
		Data priceList `json:"data"`
	}
	require.NoError(t, common.Unmarshal(raw, &envelope))
	require.Len(t, envelope.Data.Items, 31)
	r := New(Config{})
	s := &snapshot{list: envelope.Data, byModel: make(map[string][]priceEntry)}
	local := make([]model.Pricing, 0, len(s.list.Items))
	for _, entry := range s.list.Items {
		s.byModel[entry.Model] = []priceEntry{entry}
		local = append(local, model.Pricing{ModelName: entry.Model, ModelRatio: 9, CompletionRatio: 3, BillingExpr: `tier("local", p * 9)`, BillingUsageSchema: map[string]jsplugin.UsageFieldSchema{"local": {Type: "number", Unit: "credit"}}})
	}
	before, err := common.Marshal(local)
	require.NoError(t, err)
	r.current.Store(s)
	for _, row := range r.Display(local, 7.3, 500000) {
		require.Equal(t, "ops", row.ListPrice.Source, row.ModelName)
		require.NotEmpty(t, row.ListPrice.Rows, row.ModelName)
		expected := s.byModel[row.ModelName][0].Official
		require.Equal(t, expected, row.ListPrice.Official)
		matrix, _ := expected["matrix"].([]any)
		require.Len(t, row.ListPrice.Rows, len(matrix)+1, row.ModelName)
		// Every explicit base and override price survives in some display row;
		// neither task units nor zero prices can disappear during projection.
		all := []map[string]any{expected}
		for _, entry := range matrix {
			all = append(all, entry.(map[string]any))
		}
		for _, entry := range all {
			for key, value := range entry {
				if key == "when" || key == "matrix" {
					continue
				}
				amount, err := boundedPrice(value)
				require.NoError(t, err)
				require.True(t, slices.ContainsFunc(row.ListPrice.Rows, func(rule DisplayRow) bool { return rule.Prices[key] == amount.String() }), row.ModelName+":"+key)
			}
		}
	}
	after, err := common.Marshal(local)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}
