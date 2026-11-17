package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareYellowPricePatch(t *testing.T) {
	names := []string{"doubao-seed-2-0-mini-260428", "deepseek-v4.1-flash", "deepseek-v4-pro", "deepseek-v4-1-flash-1017", "doubao-seed-2.1-lite", "HappyHorse1.1"}
	for _, tc := range []struct {
		name                         string
		missing, duplicate, provider bool
		want                         string
		matched                      bool
	}{
		{name: "preserves legacy fields and excludes unrelated models"},
		{name: "skips an already matching effective tariff", matched: true},
		{name: "missing snapshot", missing: true, want: "missing versioned snapshot"},
		{name: "duplicate snapshot", duplicate: true, want: "duplicate model"},
		{name: "provider override needs review", provider: true, want: "provider-specific overrides"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := []map[string]any{}
			for _, name := range append(names[:len(names):len(names)], "unrelated-model") {
				entries = append(entries, map[string]any{"model_name": name, "version": "existing-version", "configured": map[string]any{"ModelRatio": 3.0}})
			}
			if tc.missing {
				entries = entries[1:]
			}
			if tc.duplicate {
				entries = append(entries, entries[0])
			}
			if tc.provider {
				entries[0]["configured"] = map[string]any{"billing_setting.plugin_billing_expr": map[string]any{"provider": "custom"}}
			}
			if tc.matched {
				expression, ok := billing_setting.GetBuiltinBillingExpr("HappyHorse1.1")
				require.True(t, ok)
				entries[5]["effective"] = map[string]any{"billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": expression}
			}
			encoded, err := common.Marshal(map[string]any{"success": true, "data": map[string]any{"entries": entries}})
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "snapshot.json")
			require.NoError(t, os.WriteFile(path, encoded, 0600))
			var output bytes.Buffer
			err = prepare(path, &output)
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				assert.Empty(t, output.String())
				return
			}
			require.NoError(t, err)
			var payload struct {
				Changes []struct {
					ModelName       string         `json:"model_name"`
					ExpectedVersion string         `json:"expected_version"`
					Pricing         map[string]any `json:"pricing"`
				} `json:"changes"`
			}
			require.NoError(t, common.Unmarshal(output.Bytes(), &payload))
			count := 6
			if tc.matched {
				count = 5
			}
			require.Len(t, payload.Changes, count)
			for i, change := range payload.Changes {
				assert.Equal(t, names[i], change.ModelName)
				assert.Equal(t, "existing-version", change.ExpectedVersion)
				assert.Equal(t, 3.0, change.Pricing["ModelRatio"])
				assert.Equal(t, "tiered_expr", change.Pricing["billing_setting.billing_mode"])
				assert.NotEmpty(t, change.Pricing["billing_setting.billing_expr"])
			}
		})
	}
}
