package service

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

// AgencyCustomerSales resolves an entire visible catalog against one immutable
// policy. There are at most three local DB reads, regardless of model count.
// A nil map means ordinary group pricing. The return value deliberately cannot
// expose settlement coefficients, internal costs, or commissions to customers.
func AgencyCustomerSales(userID int, modelNames []string) (map[string]int, error) {
	if userID <= 0 {
		return nil, nil
	}
	var user model.User
	if err := model.DB.Select("id", "billing_mode").First(&user, userID).Error; err != nil {
		return nil, err
	}
	if user.BillingMode == model.AgencyProvisioningBillingMode {
		return nil, errors.New("agency customer pricing is provisioning")
	}
	var active model.AgencyActiveUserBinding
	err := model.DB.Where("user_id = ?", userID).First(&active).Error
	if err != nil {
		if (errors.Is(err, gorm.ErrRecordNotFound) || agencySchemaUnavailable(err)) && user.BillingMode != model.AgencyDurableBillingMode {
			return nil, nil
		}
		return nil, err
	}
	// Joining the binding, current pointer and immutable policy reads a single
	// committed policy snapshot even if a policy publication happens concurrently.
	// Disabled agencies retain their sales policy, exactly as AgencyQuoteForUser.
	var row struct{ PolicyJSON string }
	err = model.DB.Table((model.AgencyUserBinding{}).TableName()+" AS binding").
		Select("policy.policy_json").
		Joins("JOIN "+(model.Agency{}).TableName()+" AS agency ON agency.id = binding.agency_id").
		Joins("JOIN "+(model.AgencyPricePolicyVersion{}).TableName()+" AS policy ON policy.id = agency.current_policy_version_id AND policy.agency_id = agency.id").
		Where("binding.id = ? AND binding.user_id = ? AND binding.agency_id = ? AND binding.ended_at_ms IS NULL", active.BindingID, userID, active.AgencyID).
		Take(&row).Error
	if err != nil {
		return nil, err
	}
	var agency model.Agency
	if err := model.DB.First(&agency, active.AgencyID).Error; err != nil {
		return nil, err
	}
	var policy agencycontract.Policy
	if err := common.Unmarshal([]byte(row.PolicyJSON), &policy); err != nil {
		return nil, err
	}
	if err := agencycontract.ValidatePolicy(policy); err != nil {
		return nil, err
	}
	platform, err := model.LoadAgencyPlatformPolicy(model.DB)
	if err != nil {
		return nil, err
	}
	if agency.ParentAgencyID == nil {
		policy, err = agencycontract.ApplyPlatformPolicy(policy, platform)
		if err != nil {
			return nil, err
		}
	}
	var customerOverrides []model.AgencyCustomerSalesOverride
	if err := model.DB.Where("agency_id = ? AND user_id = ?", agency.ID, userID).Find(&customerOverrides).Error; err != nil {
		return nil, err
	}
	customerSales := make(map[string]int, len(customerOverrides))
	for _, override := range customerOverrides {
		customerSales[override.ModelKey] = override.SalesBPS
	}
	// Exact Go string keys preserve case-sensitive public model identities on
	// MySQL installations using case-insensitive default collations, too.
	overrides := make(map[string]int, len(policy.ModelOverrides))
	for _, override := range policy.ModelOverrides {
		if override.SalesBPS != nil {
			overrides[override.OriginModelName] = *override.SalesBPS
		}
	}
	sales := make(map[string]int, len(modelNames))
	for _, name := range modelNames {
		if _, err := agencycontract.ModelKey(name); err != nil {
			return nil, err
		}
		coefficient, ok := overrides[name]
		if !ok {
			coefficient = policy.DefaultSalesBPS
		}
		modelKey, err := agencycontract.ModelKey(name)
		if err != nil {
			return nil, err
		}
		if customerCoefficient, exists := customerSales[modelKey]; exists {
			coefficient = customerCoefficient
		} else if globalCoefficient, exists := customerSales[""]; exists {
			coefficient = globalCoefficient
		}
		sales[name] = coefficient
	}
	return sales, nil
}
