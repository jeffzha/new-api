package agencyhub

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

type OpsCustomerSalesOverride struct {
	UserID          int64
	OriginModelName string
	SalesBPS        int
}

type OpsChildPolicyOverride struct {
	AgencyID        int64
	OriginModelName string
	SettlementBPS   int
	SalesBPS        *int
}

func (a *App) applyOpsChildPolicyOverridesTx(tx *gorm.DB, identity *Identity, parent model.Agency, parentPolicy agencycontract.Policy, overrides []OpsChildPolicyOverride, commandID string, now int64) error {
	seen := map[string]bool{}
	for _, override := range overrides {
		modelName := strings.TrimSpace(override.OriginModelName)
		key := "*"
		if modelName != "*" {
			var err error
			key, err = agencycontract.ModelKey(modelName)
			if err != nil {
				return err
			}
		}
		identityKey := strconv.FormatInt(override.AgencyID, 10) + ":" + key
		if seen[identityKey] {
			return errors.New("duplicate child pricing rule")
		}
		seen[identityKey] = true
		var child model.Agency
		if err := model.AgencyLockForUpdate(tx).Where("id = ? AND parent_agency_id = ?", override.AgencyID, parent.ID).First(&child).Error; err != nil {
			return errors.New("child pricing target is not an active direct child")
		}
		if child.Status != AgencyStatusActive {
			return errors.New("child pricing target is not an active direct child")
		}
		var current model.AgencyPricePolicyVersion
		if err := tx.Where("id = ? AND agency_id = ?", child.CurrentPolicyVersionID, child.ID).First(&current).Error; err != nil {
			return err
		}
		var candidate agencycontract.Policy
		if err := common.Unmarshal([]byte(current.PolicyJSON), &candidate); err != nil {
			return errors.New("child pricing policy is invalid")
		}
		candidate.ModelOverrides = append([]agencycontract.ModelOverride(nil), candidate.ModelOverrides...)
		if modelName == "*" {
			if override.SettlementBPS < parentPolicy.DefaultSettlementBPS+parentPolicy.MinSpreadBPS {
				return errors.New("child-specific cost violates parent minimum spread")
			}
			candidate.DefaultSettlementBPS = override.SettlementBPS
			if override.SalesBPS != nil {
				candidate.DefaultSalesBPS = *override.SalesBPS
			}
		} else {
			parentResolved, err := agencycontract.Resolve(parentPolicy, modelName)
			if err != nil {
				return err
			}
			if override.SettlementBPS < parentResolved.SettlementBPS+parentPolicy.MinSpreadBPS {
				return errors.New("child-specific model cost violates parent minimum spread")
			}
			position := -1
			for i := range candidate.ModelOverrides {
				overrideKey, err := agencycontract.ModelKey(candidate.ModelOverrides[i].OriginModelName)
				if err == nil && overrideKey == key {
					position = i
					break
				}
			}
			settlement := override.SettlementBPS
			if position < 0 {
				candidate.ModelOverrides = append(candidate.ModelOverrides, agencycontract.ModelOverride{OriginModelName: modelName, SettlementBPS: &settlement, SalesBPS: override.SalesBPS})
			} else {
				candidate.ModelOverrides[position].SettlementBPS = &settlement
				if override.SalesBPS != nil {
					candidate.ModelOverrides[position].SalesBPS = override.SalesBPS
				}
			}
		}
		if err := agencycontract.ValidatePolicy(candidate); err != nil {
			return err
		}
		if err := validateCustomerSalesPolicyTx(tx, child.ID, candidate); err != nil {
			return err
		}
		candidate.Revision = child.PriceRevision + 1
		encoded, err := common.Marshal(candidate)
		if err != nil {
			return err
		}
		policyHash, err := agencycontract.CanonicalHash(candidate)
		if err != nil {
			return err
		}
		reason := "TokenOps child-specific pricing " + commandID
		row := model.AgencyPricePolicyVersion{AgencyID: child.ID, Revision: candidate.Revision, PolicyJSON: string(encoded), PolicyHash: policyHash, CreatedByType: "tokenops", CreatedByID: 0, Reason: reason, CreatedAtMS: now}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := a.writePolicyItems(tx, row.ID, candidate); err != nil {
			return err
		}
		result := tx.Model(&child).Where("id = ? AND price_revision = ?", child.ID, child.PriceRevision).Updates(map[string]any{"current_policy_version_id": row.ID, "price_revision": candidate.Revision, "version": child.Version + 1, "updated_at": now / 1000})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return agencycontract.ErrPolicyRevision
		}
		if err := recordAuditTx(tx, nil, identity, "pricing.tokenops.child_override", "agency", strconv.FormatInt(child.ID, 10), reason, map[string]any{"revision": child.PriceRevision}, map[string]any{"revision": candidate.Revision, "policy_version_id": row.ID, "source_agency_id": parent.ID}); err != nil {
			return err
		}
	}
	return nil
}

// ApplyOpsAgencyPolicy publishes a complete, already-authorized policy from
// TokenOps. Its command receipt commits atomically with the policy revision.
func ApplyOpsAgencyPolicy(db *gorm.DB, agencyID int64, policy agencycontract.Policy, customerSales []OpsCustomerSalesOverride, childPolicies []OpsChildPolicyOverride, commandID string) (int64, bool, error) {
	return applyOpsAgencyPolicy(db, agencyID, policy, customerSales, childPolicies, commandID, "")
}

// ApplyOpsAgencyPolicyAtSnapshot refuses to overwrite a concurrent source edit.
// The snapshot includes customer prices, which have their own revision counter.
func ApplyOpsAgencyPolicyAtSnapshot(db *gorm.DB, agencyID int64, policy agencycontract.Policy, customerSales []OpsCustomerSalesOverride, childPolicies []OpsChildPolicyOverride, commandID, expectedSnapshot string) (int64, bool, error) {
	if len(expectedSnapshot) != 64 {
		return 0, false, errors.New("source price snapshot is required")
	}
	return applyOpsAgencyPolicy(db, agencyID, policy, customerSales, childPolicies, commandID, expectedSnapshot)
}

func applyOpsAgencyPolicy(db *gorm.DB, agencyID int64, policy agencycontract.Policy, customerSales []OpsCustomerSalesOverride, childPolicies []OpsChildPolicyOverride, commandID, expectedSnapshot string) (int64, bool, error) {
	if db == nil || agencyID <= 0 || commandID == "" {
		return 0, false, errors.New("invalid Ops agency policy command")
	}
	if policy.SalesCapBPS == 0 {
		policy.SalesCapBPS = agencycontract.DefaultSalesCapBPS
	}
	if err := agencycontract.ValidatePolicy(policy); err != nil {
		return 0, false, err
	}
	app := &App{db: db, config: Config{SalesCapBPS: agencycontract.DefaultSalesCapBPS}}
	customerSales = append([]OpsCustomerSalesOverride(nil), customerSales...)
	childPolicies = append([]OpsChildPolicyOverride(nil), childPolicies...)
	var target model.Agency
	if err := db.First(&target, agencyID).Error; err != nil {
		return 0, false, err
	}
	bodyValues := map[string]any{"agency_id": agencyID, "policy": policy, "customers": customerSales, "children": childPolicies}
	if expectedSnapshot != "" {
		bodyValues["expected_snapshot"] = expectedSnapshot
	}
	body, err := common.Marshal(bodyValues)
	if err != nil {
		return 0, false, err
	}
	bodyHash := fmt.Sprintf("%x", sha256.Sum256(body))
	scopeHash := fmt.Sprintf("%x", sha256.Sum256([]byte("tokenops.policy\n"+commandID)))

	now := time.Now().UnixMilli()
	var revision int64
	created := false
	err = db.Transaction(func(tx *gorm.DB) error {
		if target.ParentAgencyID != nil {
			var parent model.Agency
			if err := model.AgencyLockForUpdate(tx).First(&parent, *target.ParentAgencyID).Error; err != nil {
				return err
			}
			if err := validateChildPolicyTx(tx, parent.ID, policy); err != nil {
				return err
			}
		}
		var agency model.Agency
		if err := model.AgencyLockForUpdate(tx).First(&agency, agencyID).Error; err != nil {
			return err
		}
		if agency.Status != AgencyStatusActive {
			return errors.New("agency is disabled")
		}
		var receipt model.AgencyIdempotencyRecord
		if err := tx.Where("scope_hash = ?", scopeHash).First(&receipt).Error; err == nil {
			if receipt.BodyHash != bodyHash {
				return errors.New("TokenOps policy command identity conflict")
			}
			revision, err = strconv.ParseInt(receipt.ResponseJSON, 10, 64)
			return err
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if expectedSnapshot != "" {
			snapshot, err := ReadOpsPriceSnapshot(tx, agency.ID)
			if err != nil {
				return err
			}
			if snapshot.Fingerprint != expectedSnapshot {
				return agencycontract.ErrPolicyRevision
			}
		}
		txApp := &App{db: tx, config: app.config}
		effective, err := txApp.effectiveAgencyPolicy(agency, policy)
		if err != nil {
			return err
		}
		if effective.DefaultSettlementBPS != policy.DefaultSettlementBPS {
			return errors.New("TokenOps cost conflicts with platform procurement policy")
		}
		for _, override := range effective.ModelOverrides {
			requested, reqErr := agencycontract.Resolve(policy, override.OriginModelName)
			applied, appErr := agencycontract.Resolve(effective, override.OriginModelName)
			if reqErr != nil || appErr != nil || requested.SettlementBPS != applied.SettlementBPS {
				return errors.New("TokenOps model cost conflicts with platform procurement policy")
			}
		}
		policy = effective
		seen := map[string]bool{}
		for i := range customerSales {
			override := &customerSales[i]
			if strings.TrimSpace(override.OriginModelName) == "*" {
				override.OriginModelName = ""
			}
			key, name, keyErr := customerSalesKey(override.OriginModelName)
			if keyErr != nil || override.UserID <= 0 {
				return errors.New("invalid customer pricing identity")
			}
			override.OriginModelName = name
			identity := fmt.Sprintf("%d:%s", override.UserID, key)
			if seen[identity] {
				return errors.New("duplicate customer pricing rule")
			}
			seen[identity] = true
			var binding model.AgencyActiveUserBinding
			if err := model.AgencyLockForUpdate(tx).Where("user_id = ? AND agency_id = ?", override.UserID, agency.ID).First(&binding).Error; err != nil {
				return errors.New("customer is outside the target agency")
			}
			if err := agencycontract.ValidateCoefficient(override.SalesBPS, policy.SalesCapBPS); err != nil {
				return err
			}
			if err := validateCustomerSalesPolicy(policy, override.OriginModelName, override.SalesBPS); err != nil {
				return err
			}
		}
		var current model.AgencyPricePolicyVersion
		if err := tx.Where("id = ? AND agency_id = ?", agency.CurrentPolicyVersionID, agency.ID).First(&current).Error; err != nil {
			return err
		}
		identity := &Identity{ActorType: "tokenops", ActorID: 0}
		if err := app.syncDirectChildPoliciesTx(tx, nil, identity, agency, policy, "TokenOps command "+commandID, "tokenops"); err != nil {
			return err
		}
		if err := app.applyOpsChildPolicyOverridesTx(tx, identity, agency, policy, childPolicies, commandID, now); err != nil {
			return err
		}
		policy.Revision = agency.PriceRevision + 1
		encoded, err := common.Marshal(policy)
		if err != nil {
			return err
		}
		policyHash, err := agencycontract.CanonicalHash(policy)
		if err != nil {
			return err
		}
		row := model.AgencyPricePolicyVersion{AgencyID: agency.ID, Revision: policy.Revision,
			PolicyJSON: string(encoded), PolicyHash: policyHash, CreatedByType: "tokenops", CreatedByID: 0,
			Reason: "TokenOps command " + commandID, CreatedAtMS: now}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := app.writePolicyItems(tx, row.ID, policy); err != nil {
			return err
		}
		result := tx.Model(&agency).Where("id = ? AND price_revision = ?", agency.ID, agency.PriceRevision).
			Updates(map[string]any{"current_policy_version_id": row.ID, "price_revision": policy.Revision, "version": agency.Version + 1, "updated_at": now / 1000})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return agencycontract.ErrPolicyRevision
		}
		deleteOverrides := tx.Where("agency_id = ?", agency.ID)
		if expectedSnapshot != "" {
			deleteOverrides = deleteOverrides.Where("user_id IN (?)", tx.Model(&model.AgencyActiveUserBinding{}).Select("user_id").Where("agency_id = ?", agency.ID))
		}
		if err := deleteOverrides.Delete(&model.AgencyCustomerSalesOverride{}).Error; err != nil {
			return err
		}
		for _, override := range customerSales {
			key, _, err := customerSalesKey(override.OriginModelName)
			if err != nil {
				return err
			}
			if err := tx.Create(&model.AgencyCustomerSalesOverride{AgencyID: agency.ID, UserID: override.UserID,
				ModelKey: key, OriginModelName: override.OriginModelName, SalesBPS: override.SalesBPS,
				Revision: policy.Revision, CreatedByType: "tokenops", CreatedByID: 0, CreatedAtMS: now, UpdatedAtMS: now}).Error; err != nil {
				return err
			}
		}
		if err := recordAuditTx(tx, nil, identity, "pricing.tokenops.publish", "agency", strconv.FormatInt(agency.ID, 10),
			"TokenOps command "+commandID, map[string]any{"revision": agency.PriceRevision},
			map[string]any{"revision": policy.Revision, "policy_version_id": row.ID, "customer_override_count": len(customerSales)}); err != nil {
			return err
		}
		revision = policy.Revision
		created = true
		return tx.Create(&model.AgencyIdempotencyRecord{ScopeHash: scopeHash, ActorType: "tokenops",
			Action: "tokenops.policy.publish", BodyHash: bodyHash, ResourceID: commandID, ResultCode: 200,
			ResponseJSON: strconv.FormatInt(revision, 10), ExpiresAt: 253402300799, CreatedAt: now / 1000}).Error
	})
	return revision, created, err
}
