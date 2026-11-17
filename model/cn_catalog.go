package model

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrCNCatalogTooLarge = errors.New("catalog_snapshot_too_large")

// ReadCNCatalog reads only allowlisted columns in one repeatable snapshot. It
// includes metadata rules, disabled routes and price-only models, never keys.
func ReadCNCatalog(ctx context.Context, db *gorm.DB, gateway string) ([]byte, error) {
	var encoded []byte
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var metadata []Model
		if err := tx.Order("id").Limit(10001).Find(&metadata).Error; err != nil {
			return err
		}
		var vendors []Vendor
		if err := tx.Order("id").Limit(10001).Find(&vendors).Error; err != nil {
			return err
		}
		if len(metadata) > 10000 || len(vendors) > 10000 {
			return ErrCNCatalogTooLarge
		}
		var channels []Channel
		if err := tx.Select("id", "models").Find(&channels).Error; err != nil {
			return err
		}
		var connections []ModelConnection
		if err := tx.Table("abilities").Select("abilities.*, channels.type as channel_type").Joins("JOIN channels ON abilities.channel_id = channels.id").Where("abilities.enabled = ? AND channels.status = ?", true, common.ChannelStatusEnabled).Scan(&connections).Error; err != nil {
			return err
		}
		configured := map[string]bool{}
		names := map[string]bool{}
		rows := []map[string]any{}
		for _, m := range metadata {
			names[m.ModelName] = true
		}
		for _, channel := range channels {
			for _, name := range normalizeLookupValues(channel.GetModels()) {
				configured[name] = true
				names[name] = true
			}
		}
		pricing, err := modelPricingSnapshot(tx, nil)
		if err != nil {
			return err
		}
		for _, entry := range pricing.Entries {
			names[entry.ModelName] = true
		}
		if len(names) > 10000 {
			return ErrCNCatalogTooLarge
		}
		ordered := slices.Sorted(maps.Keys(names))
		resolved := resolveModelMetadata(metadata, ordered)
		exact := map[string]Model{}
		for _, m := range metadata {
			exact[m.ModelName] = m
		}
		for _, name := range ordered {
			m, exists := exact[name]
			if !exists {
				m = Model{ModelName: name, NameRule: NameRuleExact}
			}
			groups := map[string]bool{}
			endpoints := map[string]bool{}
			endpointMetadata := m.Endpoints
			if !exists && resolved[name] != nil {
				endpointMetadata = resolved[name].Endpoints
			}
			if endpointMetadata != "" {
				if err := ValidateModelEndpoints(endpointMetadata); err != nil {
					return err
				}
				var declared any
				if err := common.UnmarshalJsonStr(endpointMetadata, &declared); err != nil {
					return err
				}
				switch value := declared.(type) {
				case []any:
					for _, endpoint := range value {
						endpoints[endpoint.(string)] = true
					}
				case map[string]any:
					for endpoint := range value {
						endpoints[endpoint] = true
					}
				}
			}
			matched := map[string]bool{}
			visible, hidden, total := 0, 0, 0
			for _, con := range connections {
				if !m.MatchesName(con.Model) {
					continue
				}
				groups[con.Group] = true
				for _, endpoint := range GetModelSupportEndpointTypes(con.Model) {
					endpoints[string(endpoint)] = true
				}
			}
			for concrete := range configured {
				if !m.MatchesName(concrete) {
					continue
				}
				total++
				matched[concrete] = true
				policy := resolved[concrete]
				if policy != nil && policy.Status != 1 {
					hidden++
					continue
				}
				for _, con := range connections {
					if con.Model == concrete {
						visible++
						break
					}
				}
			}
			state := ModelSquareUnavailable
			if m.NameRule == NameRuleExact {
				if policy := resolved[name]; policy != nil && policy.Status != 1 {
					state = ModelSquareHidden
				} else if visible > 0 {
					state = ModelSquareVisible
				}
			} else {
				switch {
				case total == 0 && m.Status != 1:
					state = ModelSquareHidden
				case total > 0 && hidden == total:
					state = ModelSquareHidden
				case total > 0 && visible == total:
					state = ModelSquareVisible
				case visible > 0:
					state = ModelSquarePartial
				}
			}
			rows = append(rows, map[string]any{"id": m.Id, "model_name": m.ModelName, "description": m.Description, "icon": m.Icon, "tags": normalizeLookupValues(strings.Split(m.Tags, ",")), "vendor_id": m.VendorID, "status": m.Status, "name_rule": m.NameRule, "has_metadata": exists, "square_state": state, "enable_groups": append([]string{}, slices.Sorted(maps.Keys(groups))...), "supported_endpoints": append([]string{}, slices.Sorted(maps.Keys(endpoints))...), "matched_models": append([]string{}, slices.Sorted(maps.Keys(matched))...)})
		}
		// Pricing and catalog identities are independent. No online option is written.
		pricing, err = modelPricingSnapshot(tx, ordered)
		if err != nil {
			return err
		}
		publicVendors := []map[string]any{}
		for _, v := range vendors {
			publicVendors = append(publicVendors, map[string]any{"id": v.Id, "name": v.Name, "description": v.Description, "icon": v.Icon, "status": v.Status})
		}
		catalogBody, err := common.Marshal(map[string]any{"models": rows, "vendors": publicVendors})
		if err != nil {
			return err
		}
		payload := map[string]any{"gatewayId": gateway, "snapshotVersion": CNIdentity("catalog", string(catalogBody)), "models": rows, "vendors": publicVendors, "pricing": pricing}
		encoded, err = common.Marshal(payload)
		if err != nil {
			return err
		}
		if len(encoded) > 8<<20 {
			return ErrCNCatalogTooLarge
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return encoded, err
}
