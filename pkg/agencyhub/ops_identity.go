package agencyhub

import (
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// This command is only available through the server-local bridge. A browser
// proves its source session with a signed, state-bound, single-use ticket.
type OpsIdentityCommand struct {
	RootID     int64  `json:"rootUserId"`
	OperatorID int64  `json:"operatorId"`
	Action     string `json:"action"`
	Ticket     string `json:"ticket"`
	StateHash  string `json:"stateHash"`
	RequestID  string `json:"requestId"`
}

func ApplyOpsIdentity(db *gorm.DB, key ed25519.PublicKey, input OpsIdentityCommand) (map[string]any, error) {
	if len(key) != ed25519.PublicKeySize {
		return nil, errors.New("identity verification is not configured")
	}
	var claims SSOTicketClaims
	var err error
	switch input.Action {
	case "login", "link":
		audience := "tokenops-distributor"
		if input.Action == "link" {
			audience = "agency-hub-account-link"
		}
		claims, err = VerifySSOTicket(key, input.Ticket, "new-api", audience)
		if err != nil || claims.SourceSID == "" || claims.UserAuthVersion <= 0 || claims.SessionVersion <= 0 {
			return nil, errors.New("identity ticket is invalid")
		}
		if input.Action == "login" {
			if claims.ActorType != ActorTypeOperator || claims.ActorID <= 0 || claims.AgencyID <= 0 ||
				len(input.StateHash) != 64 || claims.StateHash != input.StateHash {
				return nil, errors.New("identity ticket state is invalid")
			}
			input.OperatorID = claims.ActorID
		} else if claims.ActorType != ActorTypePlatformUser || input.RequestID == "" || claims.ObjectID != input.RequestID {
			return nil, errors.New("identity link proof is invalid")
		}
	case "status", "unlink":
	default:
		return nil, errors.New("identity command is invalid")
	}
	if input.OperatorID <= 0 {
		return nil, errors.New("identity operator is required")
	}
	var result map[string]any
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := validateOpsFinanceRoot(tx, input.RootID); err != nil {
			return err
		}
		var account model.AgencyOperatorAccount
		if err := model.AgencyLockForUpdate(tx).First(&account, input.OperatorID).Error; err != nil {
			return err
		}
		var agency model.Agency
		if err := tx.First(&agency, account.AgencyID).Error; err != nil {
			return err
		}
		if account.Status != OperatorStatusActive || agency.Status != AgencyStatusActive {
			return errors.New("identity account is unavailable")
		}
		now := time.Now().Unix()
		if input.Action == "login" || input.Action == "link" {
			var source model.UserSession
			if err := tx.Where("sid = ? AND user_id = ? AND version = ?", claims.SourceSID, claims.Subject, claims.SessionVersion).First(&source).Error; err != nil {
				return errPlatformLinkProofInvalid
			}
			var user model.User
			if err := tx.First(&user, claims.Subject).Error; err != nil {
				return errPlatformLinkProofInvalid
			}
			if source.Status != model.UserSessionStatusActive || source.RevokedAt != 0 || source.ExpiresAt <= now ||
				source.UserAuthVersion != claims.UserAuthVersion || user.AuthVersion != claims.UserAuthVersion ||
				user.Status != common.UserStatusEnabled || user.Role == common.RoleRootUser {
				return errPlatformLinkProofInvalid
			}
			if err := consumeSSOTicket(tx, claims, now); err != nil {
				return err
			}
			if input.Action == "link" {
				var bindings []model.AgencyOperatorIdentityBinding
				if err := model.AgencyLockForUpdate(tx).Where("operator_account_id = ? OR platform_user_id = ?",
					account.ID, claims.Subject).Find(&bindings).Error; err != nil {
					return err
				}
				for _, binding := range bindings {
					if binding.OperatorAccountID != account.ID || binding.PlatformUserID != claims.Subject || binding.AgencyID != agency.ID {
						return errPlatformLinkConflict
					}
				}
				if len(bindings) == 0 {
					if err := tx.Create(&model.AgencyOperatorIdentityBinding{OperatorAccountID: account.ID, AgencyID: agency.ID,
						PlatformUserID: claims.Subject, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
						return err
					}
				}
			} else {
				if claims.AgencyID != agency.ID {
					return errPlatformLinkProofInvalid
				}
				var binding model.AgencyOperatorIdentityBinding
				if err := tx.Where("operator_account_id = ? AND platform_user_id = ? AND agency_id = ?",
					account.ID, claims.Subject, agency.ID).First(&binding).Error; err != nil {
					return errPlatformLinkProofInvalid
				}
			}
			result = map[string]any{"operatorId": account.ID, "agencyId": agency.ID,
				"platformUserId": claims.Subject, "username": user.Username, "linked": true, "expiresAt": source.ExpiresAt}
		} else {
			var binding model.AgencyOperatorIdentityBinding
			err := tx.Where("operator_account_id = ?", account.ID).First(&binding).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				result = map[string]any{"linked": false}
				return nil
			}
			if err != nil {
				return err
			}
			var user model.User
			if err := tx.Select("username").First(&user, binding.PlatformUserID).Error; err != nil {
				return err
			}
			result = map[string]any{"linked": true, "username": user.Username}
			if input.Action == "unlink" {
				if err := tx.Delete(&binding).Error; err != nil {
					return err
				}
				if err := tx.Model(&model.AgencySession{}).Where("actor_type = ? AND actor_id = ? AND source_user_id = ? AND revoked_at IS NULL",
					ActorTypeOperator, account.ID, binding.PlatformUserID).Update("revoked_at", now).Error; err != nil {
					return err
				}
				result = map[string]any{"linked": false}
			}
		}
		if input.Action == "status" {
			return nil
		}
		return recordAuditTx(tx, nil, &Identity{ActorType: "tokenops", ActorID: input.RootID, AgencyID: &agency.ID},
			"auth.platform_account."+input.Action, "operator_account", stringID(account.ID),
			"OPS verified account action", nil, map[string]any{"operatorId": account.ID, "action": input.Action})
	})
	return result, err
}
