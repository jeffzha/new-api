package model

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"time"
)

// BusinessOpsFee reads an OPS receipt only for an already authorized invocation.
// It reuses the log-page identity checks, including user ID and model name.
func BusinessOpsFee(ctx context.Context, log *Log) (*OpsFee, *OpsFeeSync, string, error) {
	config := opsFeeRuntime.Load()
	if config == nil {
		return nil, nil, "ops_not_enabled", nil
	}
	if log == nil || log.UserId <= 0 || log.TokenId <= 0 || log.RequestId == "" || log.ModelName == "" {
		return nil, nil, "invocation_unavailable", nil
	}
	if log.Type != LogTypeConsume {
		return nil, nil, "call_failed", nil
	}
	if log.CreatedAt < config.Since {
		return nil, nil, "outside_ops_scope", nil
	}
	if DB == nil {
		return nil, nil, "", errors.New("fee database is not initialized")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// Avoid changing the caller's log or persisted settlement snapshot.
	copy := *log
	if err := AttachOpsFees(ctx, []*Log{&copy}); err != nil {
		return nil, nil, "", err
	}
	if copy.OpsFee != nil && copy.OpsFee.Status != "waiting" {
		return copy.OpsFee, nil, "", nil
	}
	if copy.OpsFee != nil && copy.OpsFee.Status == "waiting" {
		return copy.OpsFee, nil, "", nil
	}
	var state OpsFeeSync
	err := DB.WithContext(ctx).Where("connection = ?", config.Connection).Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return copy.OpsFee, nil, "", nil
	}
	return copy.OpsFee, &state, "", err
}
