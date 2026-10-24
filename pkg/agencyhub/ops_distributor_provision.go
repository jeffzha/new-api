package agencyhub

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This command is host-local only. Neither its credential hash nor a generated
// password belongs in a receipt, audit event, or HTTP response.
type OpsDistributorProvision struct {
	Name               string `json:"name"`
	Username           string `json:"username"`
	PasswordHash       string `json:"passwordHash"`
	MustChangePassword bool   `json:"mustChangePassword"`
	ParentID           *int64 `json:"parentId"`
	RootID             int64  `json:"rootUserId"`
	CommandID          string `json:"commandId"`
}

type OpsDistributorReceipt struct {
	Directory  OpsDirectorySnapshot `json:"directory"`
	OperatorID int64                `json:"operatorId"`
}

var portableOpsCredential = regexp.MustCompile(`^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)

// ApplyOpsDistributorProvision reuses the normal creation transaction, including
// parent locking, child cost inheritance, immutable prices and username uniqueness.
func ApplyOpsDistributorProvision(db *gorm.DB, input OpsDistributorProvision, config Config) (OpsDistributorReceipt, error) {
	input.Name, input.Username = strings.TrimSpace(input.Name), strings.TrimSpace(input.Username)
	if input.RootID <= 0 || input.Name == "" || len([]rune(input.Name)) > 191 ||
		input.Username == "" || len([]rune(input.Username)) > 191 ||
		len(input.CommandID) < 8 || len(input.CommandID) > 160 ||
		!portableOpsCredential.MatchString(input.PasswordHash) ||
		(input.ParentID != nil && *input.ParentID <= 0) {
		return OpsDistributorReceipt{}, errors.New("invalid source distributor provisioning")
	}
	parts := strings.Split(input.PasswordHash, "$")
	if _, err := base64.RawStdEncoding.Strict().DecodeString(parts[4]); err != nil {
		return OpsDistributorReceipt{}, errors.New("invalid source distributor credential")
	}
	if _, err := base64.RawStdEncoding.Strict().DecodeString(parts[5]); err != nil {
		return OpsDistributorReceipt{}, errors.New("invalid source distributor credential")
	}
	body, err := common.Marshal(input)
	if err != nil {
		return OpsDistributorReceipt{}, err
	}
	bodyHash := fmt.Sprintf("%x", sha256.Sum256(body))
	scopeHash := fmt.Sprintf("%x", sha256.Sum256([]byte("tokenops.distributor.provision\n"+input.CommandID)))
	var result OpsDistributorReceipt
	err = db.Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		var root model.User
		if err := tx.Select("id,role,status").First(&root, input.RootID).Error; err != nil {
			return err
		}
		if root.Role != common.RoleRootUser || root.Status != common.UserStatusEnabled {
			return errors.New("source distributor root is inactive")
		}
		var receipt model.AgencyIdempotencyRecord
		if err := tx.Where("scope_hash = ?", scopeHash).First(&receipt).Error; err == nil {
			if receipt.BodyHash != bodyHash {
				return errors.New("source distributor command identity conflict")
			}
			return common.UnmarshalJsonStr(receipt.ResponseJSON, &result)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		app := New(tx, tx, config)
		policy := agencycontract.Policy{DefaultSalesBPS: 10000, MinSpreadBPS: config.MinSpreadBPS, SalesCapBPS: config.SalesCapBPS}
		if policy.SalesCapBPS == 0 {
			policy.SalesCapBPS = 30000
		}
		if input.ParentID != nil {
			var err error
			policy, err = inheritChildCostPolicy(tx, *input.ParentID, policy)
			if err != nil {
				return err
			}
		}
		agency, _, _, err := app.createAgency(input.RootID, input.Name, input.Username, policy, "", deliveryBinding{}, input.ParentID, ActorTypeRoot)
		if err != nil {
			return err
		}
		var operator model.AgencyOperatorAccount
		if err := tx.Where("agency_id = ?", agency.ID).First(&operator).Error; err != nil {
			return err
		}
		if err := tx.Model(&operator).Updates(map[string]any{"password_hash": input.PasswordHash, "must_change_password": input.MustChangePassword}).Error; err != nil {
			return err
		}
		items, err := ReadOpsDirectorySnapshots(tx, []int64{agency.ID})
		if err != nil {
			return err
		}
		result = OpsDistributorReceipt{Directory: items[0], OperatorID: operator.ID}
		encoded, err := common.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Create(&model.AgencyIdempotencyRecord{ScopeHash: scopeHash, ActorType: "tokenops",
			Action: "distributor.provision", BodyHash: bodyHash, ResourceID: strconv.FormatInt(agency.ID, 10),
			ResultCode: 201, ResponseJSON: string(encoded), CreatedAt: time.Now().Unix(), ExpiresAt: 253402300799}).Error
	})
	return result, err
}
