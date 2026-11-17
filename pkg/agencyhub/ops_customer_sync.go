package agencyhub

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

type OpsCustomerBinding struct {
	UserID        int64 `json:"userId"`
	AgencyID      int64 `json:"agencyId"`
	BindingID     int64 `json:"bindingId"`
	Revision      int64 `json:"revision"`
	EffectiveAtMS int64 `json:"effectiveAtMs"`
}

type OpsCustomerProvision struct {
	JobID       int64  `json:"jobId"`
	UserID      int64  `json:"userId"`
	AgencyID    int64  `json:"agencyId"`
	Status      string `json:"status"`
	BlockReason string `json:"blockReason"`
}

// ApplyOpsCustomerProvision is a host-local transport into the existing durable
// provisioning workflow. It does not bind directly or bypass the task barrier.
func ApplyOpsCustomerProvision(db *gorm.DB, userID, agencyID, rootID int64, commandID string) (OpsCustomerProvision, error) {
	if userID <= 0 || agencyID <= 0 || rootID <= 0 || len(commandID) < 8 || len(commandID) > 160 {
		return OpsCustomerProvision{}, errors.New("invalid source customer provisioning")
	}
	var root model.User
	if err := db.Select("id, role, status").First(&root, rootID).Error; err != nil {
		return OpsCustomerProvision{}, err
	}
	if root.Role != common.RoleRootUser || root.Status != common.UserStatusEnabled {
		return OpsCustomerProvision{}, errors.New("source customer root is inactive")
	}
	bodyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d", userID, agencyID, rootID))))
	scopeHash := fmt.Sprintf("%x", sha256.Sum256([]byte("tokenops.provision\n"+commandID)))
	var job model.AgencyProvisioningJob
	err := db.Transaction(func(tx *gorm.DB) error {
		var receipt model.AgencyIdempotencyRecord
		if err := tx.Where("scope_hash = ?", scopeHash).First(&receipt).Error; err == nil {
			if receipt.BodyHash != bodyHash {
				return errors.New("source customer command identity conflict")
			}
			id, err := strconv.ParseInt(receipt.ResourceID, 10, 64)
			if err != nil {
				return err
			}
			return tx.First(&job, id).Error
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var agency model.Agency
		if err := tx.First(&agency, agencyID).Error; err != nil {
			return err
		}
		var err error
		job, _, err = New(tx, tx, Config{}).enqueueProvisioningJob(userID, agency.InviteCode, rootID, "TokenOps customer provisioning "+commandID)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		return tx.Create(&model.AgencyIdempotencyRecord{ScopeHash: scopeHash, ActorType: "tokenops",
			Action: "customer.provision", BodyHash: bodyHash, ResourceID: strconv.FormatInt(job.ID, 10),
			ResultCode: 202, ResponseJSON: "{}", CreatedAt: now, ExpiresAt: 253402300799}).Error
	})
	if err != nil {
		return OpsCustomerProvision{}, err
	}
	app := New(db, db, Config{})
	if job.Status == provisioningQueued || job.Status == provisioningBlocked {
		claimed, token, err := app.claimProvisioningJob(job.ID)
		if err != nil {
			return OpsCustomerProvision{}, err
		}
		if claimed {
			if err := app.runProvisioningJob(job.ID, token); err != nil {
				var blocked *provisioningBlockedError
				if errors.As(err, &blocked) {
					if err := app.markProvisioningBlocked(job.ID, token, blocked.Tasks); err != nil {
						return OpsCustomerProvision{}, err
					}
				} else if err := app.markProvisioningFailed(job.ID, token, err); err != nil {
					return OpsCustomerProvision{}, err
				}
			}
		}
	}
	if err := db.First(&job, job.ID).Error; err != nil {
		return OpsCustomerProvision{}, err
	}
	return OpsCustomerProvision{JobID: job.ID, UserID: userID, AgencyID: agencyID, Status: job.Status, BlockReason: job.BlockReason}, nil
}

func ReadOpsCustomerBindings(db *gorm.DB, after int64, limit int) ([]OpsCustomerBinding, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid bounded customer request")
	}
	var rows []OpsCustomerBinding
	err := db.Table("agency_hub_active_user_bindings AS a").
		Select("a.user_id,a.agency_id,a.binding_id,a.revision,b.effective_at_ms").
		Joins("JOIN agency_hub_user_bindings b ON b.id=a.binding_id AND b.ended_at_ms IS NULL").
		Where("a.user_id > ?", after).Order("a.user_id").Limit(limit).Scan(&rows).Error
	return rows, err
}

// ApplyOpsCustomerTransfer reuses the Hub transfer transaction, including its
// agency locks, funding-account lock, immutable old ownership and price checks.
func ApplyOpsCustomerTransfer(db *gorm.DB, userID, agencyID, expectedRevision int64, commandID string) (model.AgencyActiveUserBinding, error) {
	if userID <= 0 || agencyID <= 0 || expectedRevision <= 0 || len(commandID) < 8 || len(commandID) > 160 {
		return model.AgencyActiveUserBinding{}, errors.New("invalid source customer transfer")
	}
	bodyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d", userID, agencyID, expectedRevision))))
	scopeHash := fmt.Sprintf("%x", sha256.Sum256([]byte("tokenops.customer\n"+commandID)))
	var result model.AgencyActiveUserBinding
	err := db.Transaction(func(tx *gorm.DB) error {
		var receipt model.AgencyIdempotencyRecord
		if err := tx.Where("scope_hash = ?", scopeHash).First(&receipt).Error; err == nil {
			if receipt.BodyHash != bodyHash {
				return errors.New("source customer command identity conflict")
			}
			return common.UnmarshalJsonStr(receipt.ResponseJSON, &result)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var err error
		result, err = transferCustomer(tx, &Identity{ActorType: ActorTypeRoot}, userID, agencyID,
			expectedRevision, "TokenOps customer transfer "+commandID, nil)
		if err != nil {
			return err
		}
		encoded, err := common.Marshal(result)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		return tx.Create(&model.AgencyIdempotencyRecord{ScopeHash: scopeHash, ActorType: "tokenops",
			Action: "customer.transfer", BodyHash: bodyHash, ResourceID: strconv.FormatInt(userID, 10),
			ResultCode: 200, ResponseJSON: string(encoded), CreatedAt: now, ExpiresAt: 253402300799}).Error
	})
	return result, err
}
