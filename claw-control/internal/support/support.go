package support

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func PublicID(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func Hash(value any) string {
	if value == nil {
		return ""
	}
	data, err := jsonx.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func Audit(tx *gorm.DB, customerID *uint64, actor, action, resourceType, resourceID string, before, after any, reason, requestID string) error {
	return tx.Create(&model.AdminAudit{
		CustomerID:   customerID,
		Actor:        actor,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		BeforeHash:   Hash(before),
		AfterHash:    Hash(after),
		Result:       "success",
		Reason:       reason,
		RequestID:    requestID,
		CreatedAt:    time.Now().UTC(),
	}).Error
}

func Enqueue(tx *gorm.DB, customerID *uint64, eventType, eventKey string, payload any) error {
	data, err := jsonx.Marshal(payload)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ControlOutbox{
		EventKey:    eventKey,
		CustomerID:  customerID,
		EventType:   eventType,
		PayloadJSON: string(data),
		Status:      model.OutboxStatusPending,
		NextRetryAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}).Error
}

func ResourceID(id uint64) string {
	return fmt.Sprintf("%d", id)
}
