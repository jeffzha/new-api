package agencyhub

import (
	"crypto/sha256"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

type OpsPriceSnapshot struct {
	AgencyID    int64                      `json:"agencyId"`
	Revision    int64                      `json:"revision"`
	Fingerprint string                     `json:"fingerprint"`
	RawPolicy   agencycontract.Policy      `json:"rawPolicy"`
	Policy      agencycontract.Policy      `json:"policy"`
	Customers   []OpsCustomerSalesOverride `json:"customers"`
}

// ReadOpsPriceSnapshot reads one agency only. Call inside a repeatable-read
// transaction when exporting; writers hold the same agency lock as the UI.
func ReadOpsPriceSnapshot(db *gorm.DB, agencyID int64) (OpsPriceSnapshot, error) {
	app := &App{db: db, config: Config{SalesCapBPS: agencycontract.DefaultSalesCapBPS}}
	agency, policy, err := app.loadAgencyPolicy(agencyID)
	if err != nil {
		return OpsPriceSnapshot{}, err
	}
	var rows []model.AgencyCustomerSalesOverride
	if err := db.Where("agency_id = ?", agencyID).Order("user_id, model_key").Find(&rows).Error; err != nil {
		return OpsPriceSnapshot{}, err
	}
	// Include even inactive/historical overrides in CAS; do not erase them on
	// the basis of an incomplete source fingerprint.
	effective, err := app.effectiveAgencyPolicy(agency, policy)
	if err != nil {
		return OpsPriceSnapshot{}, err
	}
	body, err := common.Marshal(map[string]any{"agency": agency, "policy": policy, "effective": effective, "customers": rows})
	if err != nil {
		return OpsPriceSnapshot{}, err
	}
	snapshot := OpsPriceSnapshot{AgencyID: agencyID, Revision: agency.PriceRevision,
		Fingerprint: fmt.Sprintf("%x", sha256.Sum256(body)), RawPolicy: policy,
		Customers: []OpsCustomerSalesOverride{}}
	snapshot.Policy = effective
	var bindings []model.AgencyActiveUserBinding
	if err := db.Where("agency_id = ?", agencyID).Find(&bindings).Error; err != nil {
		return OpsPriceSnapshot{}, err
	}
	active := make(map[int64]bool, len(bindings))
	for _, row := range bindings {
		active[row.UserID] = true
	}
	for _, row := range rows {
		if active[row.UserID] {
			snapshot.Customers = append(snapshot.Customers, OpsCustomerSalesOverride{
				UserID: row.UserID, OriginModelName: row.OriginModelName, SalesBPS: row.SalesBPS})
		}
	}
	return snapshot, nil
}
