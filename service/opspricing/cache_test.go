package opspricing

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestUSDPricesStayNativeAndRejectOtherCurrencies(t *testing.T) {
	currency := "USD"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		raw, err := common.Marshal(map[string]any{"data": map[string]any{"version": "usd-v1", "currency": currency, "station": map[string]any{"quotaPerUnit": 1000000, "usdExchangeRate": "1"}, "items": []any{map[string]any{"model": "text", "callType": "text", "officialCny": map[string]any{"input_per_million": "2", "output_per_million": "4"}}}}})
		require.NoError(t, err)
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	r := New(Config{BaseURL: server.URL, Currency: "USD", Timeout: time.Second, Interval: time.Minute})
	require.NoError(t, r.Refresh(context.Background()))
	local := model.Pricing{ModelName: "text", ModelRatio: 1, CompletionRatio: 2}
	row := r.Display([]model.Pricing{local}, 7.3, 500000)[0]
	require.Equal(t, "ops", row.ListPrice.Source)
	require.Equal(t, "USD", row.ListPrice.Currency)
	require.Equal(t, "match", row.ListPrice.Comparison)
	require.Contains(t, row.BillingExpr, "p * 2")
	require.Contains(t, row.BillingExpr, "c * 4")
	require.Equal(t, local, row.ListPrice.Local)
	for _, invalid := range []string{"CNY", ""} {
		currency = invalid
		require.ErrorContains(t, r.Refresh(context.Background()), "currency mismatch")
		require.Equal(t, "USD", r.Display([]model.Pricing{local}, 7.3, 500000)[0].ListPrice.Currency)
	}
}

func TestUSDOfficialRowsRetainVideoTiersAndContextInheritance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		components string
	}{
		{"video", `{"per_million_tokens":"7","matrix":[{"when":{"resolution":"720p","referenceVideo":true},"per_million_tokens":"4.3"},{"when":{"resolution":"1080p","referenceVideo":false},"per_million_tokens":"7.7"}]}`},
		{"text", `{"input_per_million":"2","output_per_million":"4","cache_read_per_million":"0","matrix":[{"when":{"inputMax":128000},"input_per_million":"1"},{"when":{"inputMax":32000},"input_per_million":"0.5"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var components map[string]any
			require.NoError(t, common.UnmarshalJsonStr(tc.components, &components))
			r := New(Config{Currency: "USD"})
			s := &snapshot{byModel: map[string][]priceEntry{tc.name: {{Official: components}}}}
			s.list.Currency = "USD"
			s.list.Station.USDExchangeRate, s.list.Station.QuotaPerUnit = "1", 1000000
			r.current.Store(s)
			local := model.Pricing{ModelName: tc.name, BillingExpr: `tier("local", p * 9)`, EnableGroup: []string{"zenlayer"}}
			row := r.Display([]model.Pricing{local}, 7.3, 500000)[0]
			require.Equal(t, "ops", row.ListPrice.Source)
			require.Equal(t, "USD", row.ListPrice.Currency)
			require.Len(t, row.ListPrice.Rows, 3)
			require.Equal(t, local, row.ListPrice.Local)
			if tc.name == "video" {
				require.Equal(t, "4.3", row.ListPrice.Rows[0].Prices["per_million_tokens"])
				require.Equal(t, true, row.ListPrice.Rows[0].When["referenceVideo"])
			} else {
				require.EqualValues(t, 32000, row.ListPrice.Rows[0].When["inputMax"])
				require.Equal(t, "0.5", row.ListPrice.Rows[0].Prices["input_per_million"])
				require.Equal(t, "4", row.ListPrice.Rows[0].Prices["output_per_million"])
				require.Equal(t, "0", row.ListPrice.Rows[0].Prices["cache_read_per_million"])
			}
		})
	}
	for _, raw := range []string{
		`{"per_second":"1","matrix":[{"when":{"resolution":"2K"},"per_second":"2"},{"when":{"resolution":"2k"},"per_second":"3"}]}`,
		`{"per_second":"1","matrix":[{"when":{"referenceVideo":"true"},"per_second":"2"}]}`,
		`{"input_per_million":"1","matrix":[{"when":{"inputMax":0},"input_per_million":"2"}]}`,
	} {
		var components map[string]any
		require.NoError(t, common.UnmarshalJsonStr(raw, &components))
		_, err := displayRows(components)
		require.Error(t, err, raw)
	}
}

func TestScheduledUSDPricesSwitchAtBoundaryWithoutRefresh(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	p := priceEntry{Official: map[string]any{"per_second": "0.08"}, Upcoming: []scheduledPrice{{EffectiveAt: at.Format(time.RFC3339), Official: map[string]any{"per_second": "0.13"}}}}
	require.NoError(t, validateSchedule(p.Upcoming))
	require.Equal(t, "0.08", p.officialAt(at.Add(-time.Nanosecond))["per_second"])
	require.Equal(t, "0.13", p.officialAt(at)["per_second"])
	require.Equal(t, "0.13", p.officialAt(at.Add(time.Hour))["per_second"])
	p.Upcoming = append(p.Upcoming, p.Upcoming[0])
	require.Error(t, validateSchedule(p.Upcoming))
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
