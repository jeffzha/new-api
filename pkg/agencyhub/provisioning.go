package agencyhub

// Existing-user provisioning is deliberately implemented as a durable job.
// The temporary user billing mode is the admission barrier; the job fencing
// token prevents an old worker from committing a binding after cancellation or
// a retry has taken ownership.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const (
	provisioningQueued     = "queued"
	provisioningProcessing = "processing"
	provisioningBlocked    = "blocked"
	provisioningCompleted  = "completed"
	provisioningFailed     = "failed"
	provisioningCancelled  = "cancelled"
)

type provisioningTask struct {
	ID       int64  `json:"id"`
	TaskID   string `json:"task_id,omitempty"`
	Status   string `json:"status"`
	Progress string `json:"progress,omitempty"`
}

type provisioningBlockedError struct {
	Tasks []provisioningTask
}

func (e *provisioningBlockedError) Error() string {
	if len(e.Tasks) == 0 {
		return "user provisioning is blocked"
	}
	return fmt.Sprintf("user has %d in-flight task(s); provisioning is blocked", len(e.Tasks))
}

func provisioningTaskList(tx *gorm.DB, userID int64) ([]provisioningTask, error) {
	var rows []model.Task
	if err := tx.Where("user_id = ? AND status NOT IN ?", userID, []model.TaskStatus{model.TaskStatusFailure, model.TaskStatusSuccess}).Order("id ASC").Limit(1000).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]provisioningTask, 0, len(rows))
	for _, row := range rows {
		result = append(result, provisioningTask{ID: row.ID, TaskID: row.TaskID, Status: string(row.Status), Progress: row.Progress})
	}
	return result, nil
}

func encodeProvisioningTasks(tasks []provisioningTask) (string, error) {
	if len(tasks) == 0 {
		return "[]", nil
	}
	data, err := common.Marshal(tasks)
	return string(data), err
}

func (a *App) enqueueProvisioningJob(userID int64, inviteCode string, rootID int64, reason string) (model.AgencyProvisioningJob, bool, error) {
	if userID <= 0 || rootID <= 0 || strings.TrimSpace(inviteCode) == "" {
		return model.AgencyProvisioningJob{}, false, errors.New("invalid provisioning request")
	}
	var job model.AgencyProvisioningJob
	created := false
	err := a.db.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := model.AgencyLockForUpdate(tx).Select("id, auth_version, billing_mode").First(&user, userID).Error; err != nil {
			return err
		}
		var existing model.AgencyProvisioningJob
		if err := tx.Where("user_id = ? AND status IN ?", userID, []string{provisioningQueued, provisioningProcessing, provisioningBlocked}).Order("id DESC").First(&existing).Error; err == nil {
			if existing.InviteCode != inviteCode {
				return errors.New("user already has a provisioning job for another agency")
			}
			job = existing
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if user.BillingMode == model.AgencyDurableBillingMode {
			return errors.New("user is already managed by an agency")
		}
		if user.BillingMode == model.AgencyProvisioningBillingMode {
			// Recover a crash between a job terminal update and restoration of the
			// temporary mode. No active job was found above, so reverting here is
			// safe and lets Root retry instead of leaving the user permanently
			// blocked.
			if err := tx.Model(&model.User{}).Where("id = ? AND billing_mode = ?", userID, model.AgencyProvisioningBillingMode).Update("billing_mode", "legacy").Error; err != nil {
				return err
			}
			user.BillingMode = "legacy"
		}
		var active model.AgencyActiveUserBinding
		if err := tx.Where("user_id = ?", userID).First(&active).Error; err == nil {
			return errors.New("user already has an agency binding")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		tasks, err := provisioningTaskList(tx, userID)
		if err != nil {
			return err
		}
		blockingJSON, err := encodeProvisioningTasks(tasks)
		if err != nil {
			return err
		}
		// Set the barrier before exposing the job. All quota mutation helpers
		// consult this persisted mode and fail closed until the worker commits.
		if err := tx.Model(&model.User{}).Where("id = ? AND billing_mode <> ?", userID, model.AgencyDurableBillingMode).Update("billing_mode", model.AgencyProvisioningBillingMode).Error; err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		job = model.AgencyProvisioningJob{UserID: userID, InviteCode: inviteCode, RootActorID: rootID, Reason: reason, ExpectedUserVersion: user.AuthVersion, Status: provisioningQueued, BlockingTasks: blockingJSON, BlockReason: "", FencingToken: 0, CreatedAtMS: now, UpdatedAtMS: now}
		if len(tasks) > 0 {
			job.BlockReason = "in_flight_tasks"
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	return job, created, err
}

// EnqueueProvisioningJob is the gateway-facing entry point used by the
// signed command worker. The HTTP handler keeps using the private method so
// request validation remains local to the hub, while the gateway worker can
// execute the already-authorized command against the shared database.
func (a *App) EnqueueProvisioningJob(userID int64, inviteCode string, rootID int64, reason string) (model.AgencyProvisioningJob, bool, error) {
	return a.enqueueProvisioningJob(userID, inviteCode, rootID, reason)
}

// ProcessProvisioningJobs advances queued barriers. It is safe to run from
// multiple hub instances: ownership is acquired with a status CAS and the
// fencing token is checked again in the final binding transaction.
func (a *App) ProcessProvisioningJobs(limit int) (int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	now := time.Now().UnixMilli()
	// A crashed worker must not leave a barrier stuck forever. A new worker
	// takes over only after a generous lease window, and gets a new token.
	_ = a.db.Model(&model.AgencyProvisioningJob{}).
		Where("status = ? AND updated_at_ms < ?", provisioningProcessing, now-5*60*1000).
		Updates(map[string]any{"status": provisioningQueued, "updated_at_ms": now})
	var jobs []model.AgencyProvisioningJob
	if err := a.db.Where("status IN ?", []string{provisioningQueued, provisioningBlocked}).Order("id ASC").Limit(limit).Find(&jobs).Error; err != nil {
		return 0, err
	}
	processed := 0
	for _, candidate := range jobs {
		claimed, token, err := a.claimProvisioningJob(candidate.ID)
		if err != nil {
			return processed, err
		}
		if !claimed {
			continue
		}
		processed++
		if err := a.runProvisioningJob(candidate.ID); err != nil {
			var blocked *provisioningBlockedError
			if errors.As(err, &blocked) {
				_ = a.markProvisioningBlocked(candidate.ID, token, blocked.Tasks)
				continue
			}
			_ = a.markProvisioningFailed(candidate.ID, err)
		}
	}
	return processed, nil
}

func (a *App) claimProvisioningJob(id int64) (bool, int64, error) {
	if id <= 0 {
		return false, 0, errors.New("invalid provisioning job")
	}
	now := time.Now().UnixMilli()
	token := time.Now().UnixNano()
	result := a.db.Model(&model.AgencyProvisioningJob{}).
		Where("id = ? AND status IN ?", id, []string{provisioningQueued, provisioningBlocked}).
		Updates(map[string]any{"status": provisioningProcessing, "fencing_token": token, "started_at_ms": now, "updated_at_ms": now})
	return result.RowsAffected == 1, token, result.Error
}

func (a *App) runProvisioningJob(id int64) error {
	var job model.AgencyProvisioningJob
	if err := a.db.First(&job, id).Error; err != nil {
		return err
	}
	if job.Status != provisioningProcessing || job.FencingToken == 0 {
		return errors.New("provisioning job is not owned")
	}
	// Read current blockers without holding locks while waiting. The final
	// transaction repeats this check under the user row lock.
	tasks, err := provisioningTaskList(a.db, job.UserID)
	if err != nil {
		return err
	}
	if len(tasks) > 0 {
		return &provisioningBlockedError{Tasks: tasks}
	}
	return a.commitProvisioningJob(job)
}

func (a *App) commitProvisioningJob(job model.AgencyProvisioningJob) error {
	return a.db.Transaction(func(tx *gorm.DB) error {
		var current model.AgencyProvisioningJob
		if err := model.AgencyLockForUpdate(tx).First(&current, job.ID).Error; err != nil {
			return err
		}
		if current.Status != provisioningProcessing || current.FencingToken != job.FencingToken {
			return errors.New("provisioning job fencing conflict")
		}
		// BindUserByInvite acquires agency then user locks, preserving the
		// global agency->user->funding lock order. It also repeats the task
		// barrier under the user lock.
		binding, err := BindUserByInvite(tx, current.UserID, current.InviteCode, "root_bind", current.RootActorID)
		if err != nil {
			if strings.Contains(err.Error(), "in-flight tasks") {
				tasks, listErr := provisioningTaskList(tx, current.UserID)
				if listErr != nil {
					return listErr
				}
				return &provisioningBlockedError{Tasks: tasks}
			}
			return err
		}
		var user model.User
		if err := model.AgencyLockForUpdate(tx).Select("id, auth_version, billing_mode").First(&user, current.UserID).Error; err != nil {
			return err
		}
		if current.ExpectedUserVersion > 0 && user.AuthVersion != current.ExpectedUserVersion {
			return errors.New("user version changed during provisioning")
		}
		now := time.Now().UnixMilli()
		result := tx.Model(&model.AgencyProvisioningJob{}).
			Where("id = ? AND status = ? AND fencing_token = ?", current.ID, provisioningProcessing, current.FencingToken).
			Updates(map[string]any{"status": provisioningCompleted, "blocking_tasks": "[]", "block_reason": "", "completed_at_ms": now, "updated_at_ms": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("provisioning job fencing conflict")
		}
		_ = binding
		return nil
	})
}

func (a *App) markProvisioningBlocked(id, token int64, tasks []provisioningTask) error {
	encoded, err := encodeProvisioningTasks(tasks)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	return a.db.Model(&model.AgencyProvisioningJob{}).
		Where("id = ? AND status = ? AND fencing_token = ?", id, provisioningProcessing, token).
		Updates(map[string]any{"status": provisioningBlocked, "blocking_tasks": encoded, "block_reason": "in_flight_tasks", "updated_at_ms": now}).Error
}

func (a *App) markProvisioningFailed(id int64, cause error) error {
	if cause == nil {
		cause = errors.New("provisioning failed")
	}
	now := time.Now().UnixMilli()
	return a.db.Transaction(func(tx *gorm.DB) error {
		var job model.AgencyProvisioningJob
		if err := model.AgencyLockForUpdate(tx).First(&job, id).Error; err != nil {
			return err
		}
		if job.Status != provisioningProcessing {
			return nil
		}
		result := tx.Model(&model.AgencyProvisioningJob{}).Where("id = ? AND status = ? AND fencing_token = ?", id, provisioningProcessing, job.FencingToken).
			Updates(map[string]any{"status": provisioningFailed, "cancel_reason": cause.Error(), "updated_at_ms": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("provisioning job fencing conflict")
		}
		// Restore legacy admission only if this job still owns the temporary
		// mode. A durable binding can never be downgraded by a stale worker.
		return tx.Model(&model.User{}).Where("id = ? AND billing_mode = ?", job.UserID, model.AgencyProvisioningBillingMode).Update("billing_mode", "legacy").Error
	})
}

func (a *App) cancelProvisioningJob(id int64, reason string) error {
	if id <= 0 {
		return gorm.ErrRecordNotFound
	}
	now := time.Now().UnixMilli()
	return a.db.Transaction(func(tx *gorm.DB) error {
		var job model.AgencyProvisioningJob
		if err := model.AgencyLockForUpdate(tx).First(&job, id).Error; err != nil {
			return err
		}
		if job.Status != provisioningQueued && job.Status != provisioningProcessing && job.Status != provisioningBlocked {
			return errors.New("provisioning job is not cancellable")
		}
		if err := tx.Model(&job).Updates(map[string]any{"status": provisioningCancelled, "cancel_reason": reason, "updated_at_ms": now}).Error; err != nil {
			return err
		}
		return tx.Model(&model.User{}).Where("id = ? AND billing_mode = ?", job.UserID, model.AgencyProvisioningBillingMode).Update("billing_mode", "legacy").Error
	})
}

// CancelProvisioningJob is the gateway-facing command execution primitive.
func (a *App) CancelProvisioningJob(id int64, reason string) error {
	return a.cancelProvisioningJob(id, reason)
}
