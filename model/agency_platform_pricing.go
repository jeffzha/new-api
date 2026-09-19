package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

// LoadAgencyPlatformPolicy reads the single committed platform price version.
// A missing state is a valid legacy installation and returns an empty policy.
func LoadAgencyPlatformPolicy(db *gorm.DB) (agencycontract.PlatformPolicy, error) {
	if db == nil {
		return agencycontract.PlatformPolicy{}, errors.New("database unavailable")
	}
	var state AgencyPlatformPriceState
	if err := db.First(&state, 1).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return agencycontract.PlatformPolicy{}, nil
		}
		return agencycontract.PlatformPolicy{}, err
	}
	var version AgencyPlatformPriceVersion
	if err := db.Where("id = ? AND revision = ?", state.CurrentVersionID, state.Revision).First(&version).Error; err != nil {
		return agencycontract.PlatformPolicy{}, err
	}
	var policy agencycontract.PlatformPolicy
	if err := common.Unmarshal([]byte(version.PolicyJSON), &policy); err != nil {
		return agencycontract.PlatformPolicy{}, err
	}
	policy.Revision = version.Revision
	if err := agencycontract.ValidatePlatformPolicy(policy); err != nil {
		return agencycontract.PlatformPolicy{}, err
	}
	return policy, nil
}
