package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func cnRetainIntegrityFindingsTx(tx *gorm.DB, parentID, batch string, collections map[string][]map[string]any) error {
	if parentID == "" {
		return nil
	}
	var previous CNIntegritySnapshotCollection
	if err := tx.Where("proof_id = ? AND collection = ?", parentID, "findings").First(&previous).Error; err != nil {
		return err
	}
	var prior []map[string]any
	if err := common.Unmarshal([]byte(previous.Payload), &prior); err != nil {
		return err
	}
	active := map[string]bool{}
	for _, finding := range collections["findings"] {
		active[finding["findingId"].(string)] = true
	}
	resolved := map[string]bool{}
	retained := map[string]bool{}
	for _, finding := range prior {
		retained[finding["findingId"].(string)] = true
		if id, ok := finding["resolvesFindingId"].(string); ok {
			resolved[id] = true
		}
	}
	current := collections["findings"]
	collections["findings"] = prior
	for _, finding := range current {
		if !retained[finding["findingId"].(string)] {
			collections["findings"] = append(collections["findings"], finding)
		}
	}
	for _, finding := range prior {
		id := finding["findingId"].(string)
		if finding["resolvesFindingId"] != nil || resolved[id] || active[id] {
			continue
		}
		resolution := map[string]any{}
		for key, value := range finding {
			resolution[key] = value
		}
		resolution["findingId"] = CNIdentity("integrity-finding-resolution-v2", id, batch)
		resolution["resolvesFindingId"] = id
		collections["findings"] = append(collections["findings"], resolution)
	}
	cnIntegritySortRows("findings", collections["findings"])
	return nil
}
