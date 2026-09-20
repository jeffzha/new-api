package agencyhub

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// customerManagement exposes only the fields Root needs to review a binding
// change. Core user credentials and worker fencing tokens never leave the API.
func (a *App) customerManagement(c *gin.Context) {
	var user model.User
	username := strings.TrimSpace(c.Query("username"))
	var err error
	if username != "" {
		if len([]rune(username)) > 20 {
			respondError(c, http.StatusBadRequest, "invalid_username", "请输入有效的客户账号", nil)
			return
		}
		err = a.db.Select("id, username, billing_mode").Where("username = ?", username).First(&user).Error
	} else {
		id, parseErr := parseID(c.Param("user_id"))
		if parseErr != nil {
			respondError(c, http.StatusBadRequest, "invalid_id", "请输入有效的客户账号", nil)
			return
		}
		err = a.db.Select("id, username, billing_mode").First(&user, id).Error
	}
	if err != nil {
		respondError(c, http.StatusNotFound, "not_found", "用户不存在", nil)
		return
	}
	id := int64(user.Id)
	view := gin.H{"user_id": strconv.FormatInt(id, 10), "username": user.Username, "billing_mode": user.BillingMode}
	var binding model.AgencyActiveUserBinding
	if err := a.db.Where("user_id = ?", id).First(&binding).Error; err == nil {
		view["agency_id"] = strconv.FormatInt(binding.AgencyID, 10)
		view["binding_revision"] = strconv.FormatInt(binding.Revision, 10)
		var agency model.Agency
		if err := a.db.Select("id, display_name").First(&agency, binding.AgencyID).Error; err == nil {
			view["agency_name"] = agency.DisplayName
			var account model.AgencyOperatorAccount
			if err := a.db.Select("username").Where("agency_id = ?", agency.ID).First(&account).Error; err == nil {
				view["agency_operator_username"] = account.Username
			}
		}
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
