package model

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/pkg/opsnotify"
	"gorm.io/gorm"
)

func (Midjourney) TableName() string { return opsauthority.TaskTable("midjourneys") }

type Midjourney struct {
	Id          int    `json:"id"`
	Code        int    `json:"code"`
	UserId      int    `json:"user_id" gorm:"index"`
	Action      string `json:"action" gorm:"type:varchar(40);index"`
	MjId        string `json:"mj_id" gorm:"index"`
	Prompt      string `json:"prompt"`
	PromptEn    string `json:"prompt_en"`
	Description string `json:"description"`
	State       string `json:"state"`
	SubmitTime  int64  `json:"submit_time" gorm:"index"`
	StartTime   int64  `json:"start_time" gorm:"index"`
	FinishTime  int64  `json:"finish_time" gorm:"index"`
	ImageUrl    string `json:"image_url"`
	VideoUrl    string `json:"video_url"`
	VideoUrls   string `json:"video_urls"`
	Status      string `json:"status" gorm:"type:varchar(20);index"`
	Progress    string `json:"progress" gorm:"type:varchar(30);index"`
	FailReason  string `json:"fail_reason"`
	ChannelId   int    `json:"channel_id"`
	Quota       int    `json:"quota"`
	Buttons     string `json:"buttons"`
	Properties  string `json:"properties"`

	TokenId              int                  `json:"-" gorm:"default:0"`
	BillingSource        string               `json:"-" gorm:"size:32"`
	OPSWalletBinding     string               `json:"-" gorm:"type:text"`
	BillingRequestID     string               `json:"-" gorm:"size:64"`
	BillingChannelId     int                  `json:"-" gorm:"default:0"`
	AgencyChargeID       string               `json:"-" gorm:"size:128"`
	AgencyBillingEventID string               `json:"-" gorm:"size:128"`
	CNBillingKey         string               `json:"-" gorm:"size:64;index"`
	cnBilling            *CNMidjourneyBilling `gorm:"-"`
}

// RefundOPSMidjourney atomically records the remote refund and reverses local
// API-key/accounting projections. User.Quota is never changed. A stale poller
// reads the persisted marker, so it cannot restore a token budget twice.
func RefundOPSMidjourney(task *Midjourney, modelName string) (int, error) {
	if task == nil || task.Id <= 0 || task.UserId <= 0 || task.BillingSource != "ops" {
		return 0, errors.New("invalid OPS Midjourney refund")
	}
	var current Midjourney
	var token Token
	refunded := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockTaskBillingUserTx(tx, task.UserId); err != nil {
			return err
		}
		if err := lockForUpdate(tx).First(&current, task.Id).Error; err != nil {
			return err
		}
		if current.UserId != task.UserId || current.MjId != task.MjId || current.Action != task.Action || current.BillingSource != "ops" || current.CNBillingKey != "" {
			return errors.New("OPS Midjourney refund identity changed")
		}
		if current.Quota == 0 {
			return nil
		}
		var binding opsauthority.Binding
		if common.UnmarshalJsonStr(current.OPSWalletBinding, &binding) != nil {
			return errors.New("accepted OPS Midjourney wallet destination missing")
		}
		if err := QueueOPSWalletRefundTx(tx, int64(current.UserId), int64(current.Quota), &binding, strconv.Itoa(current.Id), current.BillingRequestID, current.MjId, modelName); err != nil {
			return err
		}
		if current.TokenId > 0 {
			err := lockForUpdate(tx).Where("id = ? AND user_id = ?", current.TokenId, current.UserId).First(&token).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			// A deleted API key is not recreated by a customer wallet refund.
			if err == nil {
				remain, used := int64(token.RemainQuota)+int64(current.Quota), int64(token.UsedQuota)-int64(current.Quota)
				if remain > int64(common.MaxQuota) || remain < -int64(common.MaxQuota)-1 || used > int64(common.MaxQuota) || used < -int64(common.MaxQuota)-1 {
					return errors.New("Midjourney token refund arithmetic overflow")
				}
				if err := tx.Model(&Token{}).Where("id = ? AND user_id = ?", token.Id, current.UserId).Updates(map[string]any{"remain_quota": remain, "used_quota": used, "accessed_time": common.GetTimestamp()}).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Model(&User{}).Where("id = ?", current.UserId).Update("used_quota", gorm.Expr("used_quota - ?", current.Quota)).Error; err != nil {
			return err
		}
		if err := tx.Model(&Channel{}).Where("id = ?", current.GetBillingChannelId()).Update("used_quota", gorm.Expr("used_quota - ?", current.Quota)).Error; err != nil {
			return err
		}
		result := tx.Model(&Midjourney{}).Where("id = ? AND quota = ?", current.Id, current.Quota).Update("quota", 0)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("Midjourney refund marker changed concurrently")
		}
		refunded = current.Quota
		return nil
	})
	if err != nil {
		return 0, err
	}
	*task = current
	task.Quota = 0
	if refunded > 0 {
		opsnotify.FundingCommitted()
	}
	if refunded > 0 && token.Id > 0 && common.RedisEnabled {
		// The transaction already restored the budget. A cache miss may have
		// loaded that committed value before this post-commit step; adding the
		// delta again would over-credit the API key. Refill from the database.
		if err := InvalidateTokenCache(token.Key); err != nil {
			common.SysLog("failed to invalidate OPS Midjourney token refund cache: " + err.Error())
		}
	}
	return refunded, nil
}

// TaskQueryParams 用于包含所有搜索条件的结构体，可以根据需求添加更多字段
type TaskQueryParams struct {
	ChannelID      string
	MjID           string
	StartTimestamp string
	EndTimestamp   string
}

func GetAllUserTask(userId int, startIdx int, num int, queryParams TaskQueryParams) []*Midjourney {
	var tasks []*Midjourney
	var err error

	// 初始化查询构建器
	query := DB.Where("user_id = ?", userId)

	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		// 假设您已将前端传来的时间戳转换为数据库所需的时间格式，并处理了时间戳的验证和解析
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetAllTasks(startIdx int, num int, queryParams TaskQueryParams) []*Midjourney {
	var tasks []*Midjourney
	var err error

	// 初始化查询构建器
	query := DB

	// 添加过滤条件
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetAllUnFinishTasks() []*Midjourney {
	var tasks []*Midjourney
	var err error
	// get all tasks progress is not 100%
	err = DB.Where("progress != ?", "100%").Find(&tasks).Error
	if err != nil {
		return nil
	}
	return tasks
}

// HasUnfinishedMidjourneyTasks reports whether at least one Midjourney task is
// still in progress. It is a cheap existence check (LIMIT 1) used to decide
// whether the midjourney_poll system task needs to run; when no task is pending
// the scheduler skips creating a row entirely.
func HasUnfinishedMidjourneyTasks() bool {
	var id int
	err := DB.Model(&Midjourney{}).
		Where("progress != ?", "100%").
		Limit(1).
		Pluck("id", &id).Error
	return err == nil && id != 0
}

func GetByOnlyMJId(mjId string) *Midjourney {
	var mj *Midjourney
	var err error
	err = DB.Where("mj_id = ?", mjId).First(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func GetByMJId(userId int, mjId string) *Midjourney {
	var mj *Midjourney
	var err error
	err = DB.Where("user_id = ? and mj_id = ?", userId, mjId).First(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func GetByMJIds(userId int, mjIds []string) []*Midjourney {
	var mj []*Midjourney
	var err error
	err = DB.Where("user_id = ? and mj_id in (?)", userId, mjIds).Find(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func GetMjByuId(id int) *Midjourney {
	var mj *Midjourney
	var err error
	err = DB.Where("id = ?", id).First(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func UpdateProgress(id int, progress string) error {
	return DB.Model(&Midjourney{}).Where("id = ?", id).Update("progress", progress).Error
}

func (midjourney *Midjourney) Insert() error {
	if midjourney.CNBillingKey != "" {
		return cnInsertMidjourney(midjourney)
	}
	var err error
	err = DB.Create(midjourney).Error
	return err
}

func (midjourney *Midjourney) Update() error {
	if midjourney.CNBillingKey != "" {
		_, err := cnUpdateMidjourney(midjourney, nil)
		return err
	}
	var err error
	err = DB.Save(midjourney).Error
	return err
}

func (midjourney *Midjourney) UpdateBillingState() error {
	if midjourney.CNBillingKey != "" {
		return ErrCNConflict
	}
	return DB.Model(midjourney).
		Select("quota", "token_id", "billing_source", "ops_wallet_binding", "billing_request_id", "billing_channel_id", "agency_charge_id", "agency_billing_event_id").
		Updates(midjourney).Error
}

func (midjourney *Midjourney) GetBillingChannelId() int {
	if midjourney.BillingChannelId > 0 {
		return midjourney.BillingChannelId
	}
	return midjourney.ChannelId
}

// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Returns (true, nil) if this caller won the update, (false, nil) if
// another process already moved the task out of fromStatus.
// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Uses Model().Select("*").Updates() to avoid GORM Save()'s INSERT fallback.
func (midjourney *Midjourney) UpdateWithStatus(fromStatus string) (bool, error) {
	if midjourney.CNBillingKey != "" {
		return cnUpdateMidjourney(midjourney, &fromStatus)
	}
	result := DB.Model(midjourney).Where("status = ?", fromStatus).Select("*").Updates(midjourney)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func MjBulkUpdate(mjIds []string, params map[string]any) error {
	return cnBulkMidjourney(DB.Model(&Midjourney{}).Where("mj_id in (?)", mjIds), params)
}

func MjBulkUpdateByTaskIds(taskIDs []int, params map[string]any) error {
	return cnBulkMidjourney(DB.Model(&Midjourney{}).Where("id in (?)", taskIDs), params)
}

// CountAllTasks returns total midjourney tasks for admin query
func CountAllTasks(queryParams TaskQueryParams) int64 {
	var total int64
	query := DB.Model(&Midjourney{})
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	_ = query.Count(&total).Error
	return total
}

// CountAllUserTask returns total midjourney tasks for user
func CountAllUserTask(userId int, queryParams TaskQueryParams) int64 {
	var total int64
	query := DB.Model(&Midjourney{}).Where("user_id = ?", userId)
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	_ = query.Count(&total).Error
	return total
}
