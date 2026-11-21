package opspricing

import (
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// DisplayRow is a display-only rule in OPS matching order and snapshot currency,
// with default components inherited. It is never a billing setting.
type DisplayRow struct {
	When   map[string]any    `json:"when"`
	Prices map[string]string `json:"prices"`
}

var displayComponents = map[string]bool{
	"cache_write_1h_per_million": true,
	"input_per_million":          true, "output_per_million": true, "cache_read_per_million": true, "cache_write_per_million": true,
	"audio_input_per_million": true, "audio_output_per_million": true, "per_image": true, "per_second": true,
	"per_reference_image": true, "per_reference_video_second": true, "per_million_tokens": true,
	"per_output_credit": true, "per_material_credit": true, "per_request": true,
}

func displayRows(components map[string]any) ([]DisplayRow, error) {
	defaults := make(map[string]string)
	for key, value := range components {
		if key == "matrix" {
			continue
		}
		price, err := boundedPrice(value)
		if !displayComponents[key] || err != nil {
			return nil, errors.New("unsupported list price component")
		}
		defaults[key] = price.String()
	}
	if len(defaults) == 0 || (defaults["per_request"] != "" && len(defaults) > 1) {
		return nil, errors.New("invalid default list price")
	}
	rows := make([]DisplayRow, 0)
	if raw, exists := components["matrix"]; exists {
		matrix, ok := raw.([]any)
		if !ok || len(matrix) > 200 {
			return nil, errors.New("invalid list price matrix")
		}
		seen := make(map[string]bool)
		for _, rawRow := range matrix {
			row, ok := rawRow.(map[string]any)
			if !ok {
				return nil, errors.New("invalid list price row")
			}
			when, ok := row["when"].(map[string]any)
			if !ok || len(when) == 0 || len(row) < 2 {
				return nil, errors.New("empty list price condition")
			}
			conditions := make(map[string]any, len(when))
			for key, value := range when {
				switch key {
				case "inputMax":
					n, err := boundedPrice(value)
					if err != nil || !n.IsPositive() || !n.Equal(n.Truncate(0)) {
						return nil, errors.New("invalid context limit")
					}
					conditions[key] = n.IntPart()
				case "referenceVideo":
					flag, ok := value.(bool)
					if !ok {
						return nil, errors.New("invalid reference video condition")
					}
					conditions[key] = flag
				case "resolution", "size", "quality", "operation", "period", "taskPlugin":
					text, ok := value.(string)
					if !ok || strings.TrimSpace(text) == "" || len(text) > 80 {
						return nil, errors.New("invalid list price condition")
					}
					conditions[key] = strings.ToLower(text)
				default:
					return nil, errors.New("unsupported list price condition")
				}
			}
			// Stable key ordering is required by both supported JSON backends.
			keys := make([]string, 0, len(conditions))
			for key := range conditions {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			identity := make([]any, 0, len(keys)*2)
			for _, key := range keys {
				identity = append(identity, key, conditions[key])
			}
			encoded, err := common.Marshal(identity)
			if err != nil || seen[string(encoded)] {
				return nil, errors.New("duplicate list price condition")
			}
			seen[string(encoded)] = true
			prices := maps.Clone(defaults)
			for key, value := range row {
				if key == "when" {
					continue
				}
				price, err := boundedPrice(value)
				if !displayComponents[key] || err != nil {
					return nil, errors.New("unsupported list price component")
				}
				prices[key] = price.String()
			}
			if prices["per_request"] != "" && len(prices) > 1 {
				return nil, errors.New("mixed request price")
			}
			rows = append(rows, DisplayRow{When: conditions, Prices: prices})
		}
	}
	// OPS picks the most specific matching row, then the smallest inputMax;
	// ties retain the response order. The final row is the default fallback.
	slices.SortStableFunc(rows, func(a, b DisplayRow) int {
		if len(a.When) != len(b.When) {
			return len(b.When) - len(a.When)
		}
		am, _ := a.When["inputMax"].(int64)
		bm, _ := b.When["inputMax"].(int64)
		if am < bm {
			return -1
		}
		if am > bm {
			return 1
		}
		return 0
	})
	return append(rows, DisplayRow{When: map[string]any{}, Prices: defaults}), nil
}
