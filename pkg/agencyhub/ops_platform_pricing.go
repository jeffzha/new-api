package agencyhub

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

type OpsPlatformPriceSnapshot struct {
	Revision    int64                         `json:"revision"`
	Fingerprint string                        `json:"fingerprint"`
	Policy      agencycontract.PlatformPolicy `json:"policy"`
}

type OpsPlatformPriceCommand struct {
	RootID           int64                         `json:"rootUserId"`
	ExpectedSnapshot string                        `json:"expectedSnapshot"`
	CommandID        string                        `json:"commandId"`
	Policy           agencycontract.PlatformPolicy `json:"policy"`
}

func ReadOpsPlatformPriceSnapshot(db *gorm.DB, root int64) (OpsPlatformPriceSnapshot, error) {
	if err := validateOpsFinanceRoot(db, root); err != nil {
		return OpsPlatformPriceSnapshot{}, err
	}
	policy, err := model.LoadAgencyPlatformPolicy(db)
	if err != nil {
		return OpsPlatformPriceSnapshot{}, err
	}
	hash, err := agencycontract.CanonicalHash(policy)
	return OpsPlatformPriceSnapshot{Revision: policy.Revision, Fingerprint: hash, Policy: policy}, err
}

// The receipt and publication commit together. Check receipts before CAS so a
// lost acknowledgement remains replayable after another source publication.
func ApplyOpsPlatformPrice(db *gorm.DB, input OpsPlatformPriceCommand) (map[string]any, error) {
	if len(input.CommandID) < 8 || len(input.CommandID) > 160 || len(input.ExpectedSnapshot) != 64 {
		return nil, errors.New("source price command is invalid")
	}
	body, err := common.Marshal(input)
	if err != nil {
		return nil, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte("tokenops.platform-price\n"+input.CommandID)))
	var result map[string]any
	err = model.AgencyOpsTransaction(db, func(tx *gorm.DB) error {
		if err := validateOpsFinanceRoot(tx, input.RootID); err != nil {
			return err
		}
		var agencies []model.Agency
		if err := model.AgencyLockForUpdate(tx).Order("id ASC").Find(&agencies).Error; err != nil {
			return err
		}
		var receipt model.AgencyIdempotencyRecord
		if err := tx.Where("scope_hash = ?", scope).First(&receipt).Error; err == nil {
			if receipt.BodyHash != hash {
				return errors.New("source price command identity conflict")
			}
			return common.UnmarshalJsonStr(receipt.ResponseJSON, &result)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		snapshot, err := ReadOpsPlatformPriceSnapshot(tx, input.RootID)
		if err != nil {
			return err
		}
		if snapshot.Fingerprint != input.ExpectedSnapshot || snapshot.Revision != input.Policy.Revision {
			return agencycontract.ErrPolicyRevision
		}
		roots := make(map[int64]bool)
		for _, agency := range agencies {
			if agency.ParentAgencyID == nil && agency.Status == AgencyStatusActive {
				roots[agency.ID] = true
			}
		}
		for _, rule := range input.Policy.RootAgencyCosts {
			if !roots[rule.AgencyID] {
				return errors.New("source price target is not an active root agency")
			}
		}
		app := &App{db: tx}
		revision, err := app.publishPlatformPolicy(nil, &Identity{ActorType: ActorTypeRoot, ActorID: input.RootID},
			snapshot.Revision, input.Policy, "TokenOps "+input.CommandID)
		if err != nil {
			return err
		}
		result = map[string]any{"revision": revision}
		encoded, err := common.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Create(&model.AgencyIdempotencyRecord{ScopeHash: scope, ActorType: "tokenops", ActorID: input.RootID,
			Action: "pricing.platform.publish", BodyHash: hash, ResourceID: strconv.FormatInt(revision, 10),
			ResultCode: 200, ResponseJSON: string(encoded), CreatedAt: time.Now().Unix(), ExpiresAt: 253402300799}).Error
	})
	return result, err
}
