package agencyhub

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// recordAuditTx stores only the whitelisted before/after values supplied by
// the caller. Passwords, tokens and account ciphertext must never be passed to
// this helper; security-sensitive operations use a small marker instead.
func recordAuditTx(tx *gorm.DB, c *gin.Context, identity *Identity, action, objectType, objectID, reason string, before, after any) error {
	if tx == nil || identity == nil {
		return errors.New("audit identity is required")
	}
	beforeJSON, err := marshalAuditValue(before)
	if err != nil {
		return err
	}
	afterJSON, err := marshalAuditValue(after)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	eventID := "audit-" + common.NewRequestId()
	requestIDValue := requestID(c)
	sourceIP := ""
	if c != nil {
		sourceIP = c.ClientIP()
	}
	var actingAgencyID *int64
	if identity.AgencyID != nil {
		value := *identity.AgencyID
		actingAgencyID = &value
	}
	return tx.Create(&model.AgencyAuditLog{
		EventID: eventID, ActorType: identity.ActorType, ActorID: identity.ActorID,
		ActingAgencyID: actingAgencyID, Action: action, ObjectType: objectType,
		ObjectID: objectID, RequestID: requestIDValue, Reason: reason,
		BeforeJSON: beforeJSON, AfterJSON: afterJSON, SourceIP: sourceIP,
		CreatedAtMS: now,
	}).Error
}

func marshalAuditValue(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	encoded, err := common.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
