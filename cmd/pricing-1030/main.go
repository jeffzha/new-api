// pricing-1030 emits a version-checked API patch from an exported pricing
// snapshot. It never connects to a server or opens a database.
package main

import (
	"flag"
	"fmt"
	"io"
	"maps"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
)

func main() {
	snapshotPath := flag.String("snapshot", "", "JSON from GET /api/option/model_pricing for the six yellow models")
	flag.Parse()
	if err := prepare(*snapshotPath, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepare(path string, output io.Writer) error {
	if path == "" {
		return fmt.Errorf("-snapshot is required; this command emits a patch but never applies it")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	type entry struct {
		ModelName  string         `json:"model_name"`
		Version    string         `json:"version"`
		Configured map[string]any `json:"configured"`
		Effective  map[string]any `json:"effective"`
	}
	var snapshot struct {
		Entries []entry `json:"entries"`
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    *struct {
			Entries []entry `json:"entries"`
		} `json:"data"`
	}
	if err := common.Unmarshal(content, &envelope); err != nil {
		return err
	}
	if envelope.Data != nil {
		if !envelope.Success {
			return fmt.Errorf("snapshot API reported failure")
		}
		snapshot.Entries = envelope.Data.Entries
	} else if err := common.Unmarshal(content, &snapshot); err != nil {
		return err
	}
	entries := make(map[string]entry)
	for _, row := range snapshot.Entries {
		if _, exists := entries[row.ModelName]; exists {
			return fmt.Errorf("duplicate model %s", row.ModelName)
		}
		entries[row.ModelName] = row
	}
	type change struct {
		ModelName       string         `json:"model_name"`
		ExpectedVersion string         `json:"expected_version"`
		Pricing         map[string]any `json:"pricing"`
	}
	changes := make([]change, 0, 6)
	for _, name := range []string{"doubao-seed-2-0-mini-260428", "deepseek-v4.1-flash", "deepseek-v4-pro", "deepseek-v4-1-flash-1017", "doubao-seed-2.1-lite", "HappyHorse1.1"} {
		row, exists := entries[name]
		if !exists || row.Version == "" {
			return fmt.Errorf("missing versioned snapshot for %s", name)
		}
		if variants, ok := row.Configured[billing_setting.PluginBillingExprOption].(map[string]any); ok && len(variants) > 0 {
			return fmt.Errorf("%s has provider-specific overrides; review before preparing this patch", name)
		}
		expression, ok := billing_setting.GetBuiltinBillingExpr(name)
		if !ok {
			return fmt.Errorf("missing approved expression for %s", name)
		}
		if row.Effective["billing_setting.billing_mode"] == billing_setting.BillingModeTieredExpr && row.Effective["billing_setting.billing_expr"] == expression {
			continue
		}
		draft := make(map[string]any)
		maps.Copy(draft, row.Configured)
		draft["billing_setting.billing_mode"] = billing_setting.BillingModeTieredExpr
		draft["billing_setting.billing_expr"] = expression
		changes = append(changes, change{name, row.Version, draft})
	}
	payload, err := common.Marshal(map[string]any{"changes": changes})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, string(payload))
	return err
}
