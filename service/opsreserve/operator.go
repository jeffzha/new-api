package opsreserve

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// ResolveReviewed is an explicit operator action after examining the provider's
// terminal evidence. Age and transport errors are never evidence of no charge.
// It preserves the evidence reference and reviewer in the durable replay row.
func (c *Client) ResolveReviewed(ctx context.Context, request, decision, evidence, reviewer string, quota int) error {
	if strings.TrimSpace(evidence) == "" || len(evidence) > 500 || strings.TrimSpace(reviewer) == "" || len(reviewer) > 160 || quota < 0 || quota > common.MaxQuota || (decision != "confirmed_no_charge" && decision != "posted_charge") || (decision == "confirmed_no_charge" && quota != 0) {
		return ErrUnavailable
	}
	var row Intent
	if err := c.DB.WithContext(ctx).Where("gateway = ? AND request = ?", c.Transport.KeyID, request).First(&row).Error; err != nil {
		return err
	}
	if row.Resolution != "" && (row.Resolution != decision || row.ReviewEvidence != evidence || row.ReviewedBy != reviewer) {
		return ErrUnavailable
	}
	if row.Reconciled {
		if row.Resolution == "" || row.TokenQuota != quota {
			return ErrUnavailable
		}
		return nil
	}
	body, err := common.Marshal(map[string]any{"requestId": request, "userId": row.User, "model": row.Model, "operation": "status"})
	if err != nil {
		return err
	}
	raw, err := c.Transport.Post(ctx, Path, body, time.Now())
	if err != nil {
		return err
	}
	var status struct {
		Data struct {
			Request string `json:"requestId"`
			State   string `json:"state"`
		} `json:"data"`
	}
	if common.Unmarshal(raw, &status) != nil || status.Data.Request != request {
		return ErrUnavailable
	}
	if decision == "posted_charge" && status.Data.State != "settled" {
		return ErrUnavailable
	}
	if decision == "confirmed_no_charge" && status.Data.State != "reserved" && status.Data.State != "released" && status.Data.State != "not_found" {
		return ErrUnavailable
	}
	err = c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Intent
		if err := model.LockOPSHoldTx(tx).Where("gateway = ? AND request = ?", row.Gateway, request).First(&current).Error; err != nil {
			return err
		}
		if current.User != row.User || current.Model != row.Model {
			return ErrUnavailable
		}
		if current.Reconciled {
			if current.Resolution == decision && current.ReviewEvidence == evidence && current.ReviewedBy == reviewer && current.TokenQuota == quota {
				return nil
			}
			return ErrUnavailable
		}
		if current.Finalized && (decision != "posted_charge" || current.TokenQuota != quota) {
			return ErrUnavailable
		}
		if current.Resolution != "" && (current.Resolution != decision || current.ReviewEvidence != evidence || current.ReviewedBy != reviewer) {
			return ErrUnavailable
		}
		updates := map[string]any{"resolution": decision, "review_evidence": evidence, "reviewed_by": reviewer, "updated": time.Now().Unix()}
		if decision == "confirmed_no_charge" {
			updates["cancel_pending"] = true
		} else {
			user, err := strconv.Atoi(current.User)
			if err != nil {
				return err
			}
			if current.TokenID > 0 {
				if err := model.AdjustOPSHoldTokenTx(tx, user, current.TokenID, current.TokenQuota, quota, false); err != nil {
					return err
				}
			} else if quota != 0 {
				return ErrUnavailable
			}
			updates["token_quota"] = quota
			updates["finalized"] = true
			updates["cancel_pending"] = false
			updates["reconciled"] = true
			updates["resolved_at"] = time.Now().Unix()
			if current.TaskID != "" {
				var task model.Task
				if err := model.LockOPSHoldTx(tx).Where("task_id = ? AND user_id = ?", current.TaskID, user).First(&task).Error; err != nil {
					return err
				}
				if task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure {
					return ErrUnavailable
				}
				if task.PrivateData.OPSReservationGateway != current.Gateway || task.PrivateData.Execution == nil || task.PrivateData.Execution.RequestID != request || task.PrivateData.BillingSource != "ops" || task.PrivateData.TokenId != current.TokenID {
					return ErrUnavailable
				}
				if task.Status == model.TaskStatusFailure && quota != 0 {
					return ErrUnavailable
				}
				if err := tx.Model(&task).Update("quota", quota).Error; err != nil {
					return err
				}
			}
		}
		return tx.Model(&current).Updates(updates).Error
	})
	if err != nil {
		return err
	}
	if decision == "confirmed_no_charge" {
		if err := c.DB.WithContext(ctx).Where("gateway = ? AND request = ?", row.Gateway, request).First(&row).Error; err != nil {
			return err
		}
		return c.release(ctx, row)
	}
	return nil
}
