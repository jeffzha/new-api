package agencyhub

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// customerManagement exposes only the fields Root needs to review a binding
// change. Core user credentials and worker fencing tokens never leave the API.
func (a *App) customerManagement(c *gin.Context) {
	id, err := parseID(c.Param("user_id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的用户ID", nil)
		return
	}
	var user model.User
	if err := a.db.Select("id, username, billing_mode").First(&user, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "not_found", "用户不存在", nil)
		return
	}
	view := gin.H{"user_id": strconv.FormatInt(id, 10), "username": user.Username, "billing_mode": user.BillingMode}
	var binding model.AgencyActiveUserBinding
	if err := a.db.Where("user_id = ?", id).First(&binding).Error; err == nil {
		view["agency_id"] = strconv.FormatInt(binding.AgencyID, 10)
		view["binding_revision"] = strconv.FormatInt(binding.Revision, 10)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusInternalServerError, "database_error", "读取归属失败", nil)
		return
	}
	var job model.AgencyProvisioningJob
	if err := a.db.Where("user_id = ?", id).Order("id DESC").First(&job).Error; err == nil {
		view["provisioning"] = provisioningView(job)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusInternalServerError, "database_error", "读取绑定任务失败", nil)
		return
	}
	c.Header("Cache-Control", "no-store")
	respondOK(c, view)
}

func provisioningView(job model.AgencyProvisioningJob) gin.H {
	tasks := []provisioningTask{}
	if job.BlockingTasks != "" {
		_ = common.Unmarshal([]byte(job.BlockingTasks), &tasks)
	}
	return gin.H{
		"id": strconv.FormatInt(job.ID, 10), "user_id": strconv.FormatInt(job.UserID, 10),
		"status": job.Status, "blocking_tasks": tasks, "block_reason": job.BlockReason,
		"reason": job.Reason, "cancel_reason": job.CancelReason,
		"created_at_ms": strconv.FormatInt(job.CreatedAtMS, 10), "updated_at_ms": strconv.FormatInt(job.UpdatedAtMS, 10),
	}
}
