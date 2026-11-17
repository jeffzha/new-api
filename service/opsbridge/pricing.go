package opsbridge

import (
	"context"
	"crypto/rand"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

type LocalPrice struct {
	Version    string         `json:"version"`
	Configured map[string]any `json:"configured"`
}

type PricingStore interface {
	Read(names []string) (map[string]LocalPrice, error)
	Apply(before map[string]LocalPrice, after map[string]map[string]any) error
}

type Component struct {
	Key      string `json:"priceKey"`
	Vendor   string `json:"vendorName"`
	Model    string `json:"modelName"`
	CallType string `json:"callType"`
	Metric   string `json:"metricCode"`
	Price    string `json:"pricePerUnit"`
}

type Baseline struct {
	Version    string      `json:"version"`
	Components []Component `json:"components"`
}

type ImportPlan struct {
	ID             string                    `json:"id"`
	CreatedAt      int64                     `json:"created_at"`
	CatalogVersion string                    `json:"catalog_version"`
	RemoteHash     string                    `json:"remote_hash"`
	Before         map[string]LocalPrice     `json:"before"`
	After          map[string]map[string]any `json:"after"`
	Components     map[string][]Component    `json:"components"`
	Protected      []string                  `json:"protected_models"`
	Unsupported    map[string]string         `json:"unsupported_models"`
	RemoteInput    map[string]any            `json:"remote_input"`
	Status         string                    `json:"status"`
	Selected       []string                  `json:"selected_models,omitempty"`
	Overwrite      []string                  `json:"overwrite_models,omitempty"`
	Prepare        map[string]any            `json:"prepare,omitempty"`
}

func (b *Bridge) PreviewImport(ctx context.Context, store PricingStore) (*ImportPlan, error) {
	b.importMu.Lock()
	defer b.importMu.Unlock()
	var catalog struct {
		Schema     string      `json:"schemaVersion"`
		Version    string      `json:"catalogVersion"`
		Currency   string      `json:"currency"`
		Components []Component `json:"components"`
	}
	if err := b.request(ctx, "GET", "official-catalog?currency=USD", "", nil, &catalog); err != nil {
		return nil, err
	}
	if catalog.Schema != "1" || catalog.Currency != "USD" || catalog.Version == "" || len(catalog.Components) == 0 {
		return nil, errors.New("unsupported Ops official catalog")
	}
	groups := map[string][]Component{}
	for _, item := range catalog.Components {
		groups[item.Model] = append(groups[item.Model], item)
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	local, err := store.Read(names)
	if err != nil {
		return nil, err
	}
	plan := &ImportPlan{ID: rand.Text(), CreatedAt: time.Now().Unix(), CatalogVersion: catalog.Version, Before: local,
		After: map[string]map[string]any{}, Components: groups, Unsupported: map[string]string{}, Protected: []string{}, Status: "preview"}
	b.mu.Lock()
	baselines := maps.Clone(b.state.Baselines)
	b.mu.Unlock()
	gatewayPrices := []map[string]any{}
	for _, name := range names {
		before, ok := local[name]
		if !ok {
			return nil, errors.New("pricing store omitted model version")
		}
		baseline, imported := baselines[name]
		modified := len(before.Configured) > 0 && (!imported || before.Version != baseline.Version)
		if modified {
			plan.Protected = append(plan.Protected, name)
		}
		if imported {
			for _, item := range baseline.Components {
				gatewayPrices = append(gatewayPrices, map[string]any{"priceKey": item.Key, "pricePerUnit": item.Price, "localModified": modified})
			}
		}
		expression, err := catalogExpression(groups[name])
		if err != nil {
			plan.Unsupported[name] = err.Error()
			continue
		}
		after := maps.Clone(before.Configured)
		if after == nil {
			after = map[string]any{}
		}
		after["billing_setting.billing_mode"] = "tiered_expr"
		after["billing_setting.billing_expr"] = expression
		plan.After[name] = after
	}
	plan.RemoteInput = map[string]any{"currency": "USD", "gatewayPricing": gatewayPrices}
	var remote struct {
		PreviewHash    string `json:"previewHash"`
		CatalogVersion string `json:"catalogVersion"`
	}
	if err := b.request(ctx, "POST", "official-catalog-imports/preview", "", plan.RemoteInput, &remote); err != nil {
		return nil, err
	}
	if remote.CatalogVersion != catalog.Version || remote.PreviewHash == "" {
		return nil, errors.New("official catalog changed during preview")
	}
	plan.RemoteHash = remote.PreviewHash
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.state.Imports) >= 1000 {
		return nil, errors.New("Ops import evidence capacity exceeded")
	}
	b.state.Imports[plan.ID] = plan
	if err := b.saveLocked(); err != nil {
		delete(b.state.Imports, plan.ID)
		return nil, err
	}
	// Return a copy; API consumers must never mutate a persisted plan.
	data, _ := common.Marshal(plan)
	var result ImportPlan
	err = common.Unmarshal(data, &result)
	return &result, err
}

func catalogExpression(items []Component) (string, error) {
	vars := map[string]string{"input_token": "p", "output_token": "c", "cache_read_input_token": "cr",
		"cache_write_input_token": "cc", "cache_write_5m_input_token": "cc", "cache_write_1h_input_token": "cc1h",
		"image_input_token": "img", "image_output_token": "img_o", "image_cache_read_input_token": "img_cr",
		"audio_input_token": "ai", "audio_output_token": "ao"}
	terms := []string{}
	seen := map[string]bool{}
	if len(items) == 0 {
		return "", errors.New("empty component group")
	}
	for _, item := range items {
		if item.Model == "" || item.Key == "" || item.Vendor != items[0].Vendor || item.CallType != items[0].CallType {
			return "", errors.New("ambiguous vendor or call type; explicit model mapping required")
		}
		if item.CallType != "text_generation" && item.CallType != "embedding" {
			return "", errors.New("call type requires a task-specific pricing adapter")
		}
		variable, ok := vars[item.Metric]
		if !ok || seen[variable] {
			return "", errors.New("unsupported or overlapping price components")
		}
		seen[variable] = true
		price, err := decimal.NewFromString(item.Price)
		if err != nil || price.IsNegative() || price.GreaterThan(decimal.NewFromInt(1000000000)) {
			return "", errors.New("invalid official unit price")
		}
		terms = append(terms, variable+" * "+price.Mul(decimal.NewFromInt(1000000)).String())
	}
	if !seen["p"] || (items[0].CallType == "text_generation" && !seen["c"]) {
		return "", errors.New("incomplete token component group")
	}
	sort.Strings(terms)
	return "tier(\"ops\", " + strings.Join(terms, " + ") + ")", nil
}

func (b *Bridge) ApplyImport(ctx context.Context, store PricingStore, id string, selected, overwrite []string) error {
	b.importMu.Lock()
	defer b.importMu.Unlock()
	b.mu.Lock()
	saved := b.state.Imports[id]
	if saved == nil {
		data, err := os.ReadFile(filepath.Join(b.cfg.StateDir, "archive", "import-"+hash([]byte(id))+".json"))
		if err == nil {
			var archived ImportPlan
			if common.Unmarshal(data, &archived) != nil || archived.ID != id {
				b.mu.Unlock()
				return errors.New("invalid archived import")
			}
			saved = &archived
		} else if !os.IsNotExist(err) {
			b.mu.Unlock()
			return errors.New("cannot read archived import")
		}
	}
	if saved == nil {
		b.mu.Unlock()
		return errors.New("unknown import preview")
	}
	data, _ := common.Marshal(saved)
	b.mu.Unlock()
	var plan ImportPlan
	if err := common.Unmarshal(data, &plan); err != nil {
		return err
	}
	sort.Strings(selected)
	sort.Strings(overwrite)
	if len(selected) == 0 {
		return errors.New("select at least one supported model")
	}
	selection := map[string]bool{}
	replacements := map[string]bool{}
	for _, name := range selected {
		if selection[name] || plan.After[name] == nil {
			return errors.New("invalid or unsupported selected model")
		}
		selection[name] = true
	}
	for _, name := range overwrite {
		if replacements[name] || !selection[name] {
			return errors.New("invalid overwrite selection")
		}
		replacements[name] = true
	}
	for _, name := range plan.Protected {
		if selection[name] && !replacements[name] {
			return errors.New("local administrator price requires explicit overwrite selection")
		}
	}
	if plan.Status != "preview" {
		previous, _ := common.Marshal([]any{plan.Selected, plan.Overwrite})
		current, _ := common.Marshal([]any{selected, overwrite})
		if string(previous) != string(current) {
			return errors.New("import replay selection conflict")
		}
		if plan.Status == "applied" {
			return nil
		}
	} else {
		if time.Now().Unix()-plan.CreatedAt > 3600 {
			return errors.New("import preview expired")
		}
		plan.Selected = selected
		plan.Overwrite = overwrite
		plan.Status = "prepared"
		keys := []string{}
		overwriteKeys := []string{}
		for _, name := range selected {
			for _, item := range plan.Components[name] {
				keys = append(keys, item.Key)
				if replacements[name] {
					overwriteKeys = append(overwriteKeys, item.Key)
				}
			}
		}
		prepare := maps.Clone(plan.RemoteInput)
		prepare["importId"] = id
		prepare["previewHash"] = plan.RemoteHash
		prepare["priceKeys"] = keys
		prepare["overwritePriceKeys"] = overwriteKeys
		plan.Prepare = prepare
		b.mu.Lock()
		b.state.Imports[id] = &plan
		err := b.saveLocked()
		b.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if err := b.request(ctx, "POST", "official-catalog-imports", id, plan.Prepare, nil); err != nil {
		return err
	}
	current, err := store.Read(selected)
	if err != nil {
		return err
	}
	before := map[string]LocalPrice{}
	after := map[string]map[string]any{}
	alreadyApplied := true
	for _, name := range selected {
		desired, _ := common.Marshal(plan.After[name])
		actual, _ := common.Marshal(current[name].Configured)
		if string(desired) != string(actual) {
			alreadyApplied = false
		}
		before[name] = plan.Before[name]
		after[name] = plan.After[name]
	}
	// If the process stopped after the database commit, recover by comparing the
	// whole configuration. Otherwise the existing optimistic API rejects drift.
	if !alreadyApplied {
		if err := store.Apply(before, after); err != nil {
			return err
		}
	}
	current, err = store.Read(selected)
	if err != nil {
		return err
	}
	versions := map[string]string{}
	keys := []string{}
	for _, name := range selected {
		actual, _ := common.Marshal(current[name].Configured)
		desired, _ := common.Marshal(plan.After[name])
		if string(actual) != string(desired) {
			return errors.New("administrator changed price after apply; import needs review")
		}
		versions[name] = current[name].Version
		for _, item := range plan.Components[name] {
			keys = append(keys, item.Key)
		}
	}
	versionJSON, _ := common.Marshal(versions)
	confirmation := map[string]any{"importId": id, "appliedPriceKeys": keys, "gatewayVersion": hash(versionJSON)}
	// Persist the confirmation in the same state write as the baseline. A crash
	// cannot acknowledge an import whose local result has not been recorded.
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, name := range selected {
		b.state.Baselines[name] = Baseline{Version: current[name].Version, Components: plan.Components[name]}
	}
	plan.Status = "applied"
	b.state.Imports[id] = &plan
	deliveryID := hash([]byte("official-catalog-imports/confirm\n" + id))
	b.state.Deliveries[deliveryID] = &Delivery{ID: deliveryID, Path: "official-catalog-imports/confirm", Key: id, Body: confirmation, Status: "pending"}
	return b.saveLocked()
}
