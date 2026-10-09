package model

import "gorm.io/gorm"

// ModelVendorDisplay is the presentation vendor assigned to a concrete model.
type ModelVendorDisplay struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
}

// ResolveModelVendors maps concrete model names to the vendor shown on the
// model square. It reuses the exact/prefix/suffix/contains metadata precedence
// and the built-in vendor rules, so another process (for example the agency
// hub) can render the same provider tabs without loading the pricing cache.
// Models without metadata vendor stay absent from the result.
func ResolveModelVendors(db *gorm.DB, names []string) (map[string]ModelVendorDisplay, error) {
	unique := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		unique = append(unique, name)
	}
	resolvedVendors := make(map[string]ModelVendorDisplay, len(unique))
	if len(unique) == 0 {
		return resolvedVendors, nil
	}

	var allMeta []Model
	if err := db.Find(&allMeta).Error; err != nil {
		return nil, err
	}
	metaMap := resolveModelMetadata(allMeta, unique)

	var vendors []Vendor
	if err := db.Find(&vendors).Error; err != nil {
		return nil, err
	}
	vendorMap := make(map[int]*Vendor, len(vendors))
	for i := range vendors {
		vendorMap[vendors[i].Id] = &vendors[i]
	}

	abilities := make([]AbilityWithChannel, 0, len(unique))
	for _, name := range unique {
		abilities = append(abilities, AbilityWithChannel{Ability: Ability{Model: name}})
	}
	initDefaultVendorMapping(metaMap, vendorMap, abilities)

	for _, name := range unique {
		meta, ok := metaMap[name]
		if !ok || meta.VendorID == 0 {
			continue
		}
		vendor, ok := vendorMap[meta.VendorID]
		if !ok || vendor == nil || vendor.Name == "" {
			continue
		}
		resolvedVendors[name] = ModelVendorDisplay{ID: vendor.Id, Name: vendor.Name, Icon: vendor.Icon}
	}
	return resolvedVendors, nil
}
