package agencyhub

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (a *App) listReconciliationIssues(c *gin.Context) {
	status := strings.TrimSpace(c.Query("status"))
	limit := 50
	if value, err := strconv.Atoi(c.Query("limit")); err == nil && value > 0 {
		limit = value
	}
	if limit > 200 {
		limit = 200
	}
	query := a.db.Model(&model.AgencyReconciliationIssue{}).Order("id DESC").Limit(limit)
	if status != "" {
		if status != "open" && status != "resolved" && status != "ignored" {
			respondError(c, http.StatusBadRequest, "invalid_status", "无效的异常状态", nil)
			return
		}
		query = query.Where("status = ?", status)
	}
	var issues []model.AgencyReconciliationIssue
	if err := query.Find(&issues).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取对账异常失败", nil)
		return
	}
	respondOK(c, gin.H{"items": issues, "total": len(issues)})
}

func (a *App) resolveReconciliationIssue(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的异常ID", nil)
		return
	}
	var request struct {
		Resolution string `json:"resolution"`
		Status     string `json:"status"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	request.Resolution = strings.TrimSpace(request.Resolution)
	if request.Resolution == "" || len(request.Resolution) > 2000 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_resolution", "必须提供处理说明", nil)
		return
	}
	status := strings.TrimSpace(request.Status)
	if status == "" {
		status = "resolved"
	}
	if status != "resolved" && status != "ignored" {
		respondError(c, http.StatusUnprocessableEntity, "invalid_status", "状态只能为 resolved 或 ignored", nil)
		return
	}
	identity := currentIdentity(c)
	now := time.Now().UnixMilli()
	var issue model.AgencyReconciliationIssue
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := model.AgencyLockForUpdate(tx).First(&issue, id).Error; err != nil {
			return err
		}
		if issue.Status != "open" {
			return errors.New("reconciliation issue is already closed")
		}
		if err := tx.Model(&issue).Updates(map[string]any{"status": status, "resolution": request.Resolution, "actor_id": identity.ActorID, "resolved_at_ms": now}).Error; err != nil {
			return err
		}
		return tx.Create(&model.AgencyAuditLog{EventID: "reconcile-" + strconv.FormatInt(issue.ID, 10) + "-" + strconv.FormatInt(now, 10), ActorType: identity.ActorType, ActorID: identity.ActorID, Action: "reconciliation.resolve", ObjectType: issue.ObjectType, ObjectID: issue.ObjectID, RequestID: requestID(c), Reason: request.Resolution, CreatedAtMS: now}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusNotFound, "not_found", "对账异常不存在", nil)
		} else {
			respondError(c, http.StatusConflict, "resolve_failed", err.Error(), nil)
		}
		return
	}
	respondOK(c, issue)
}

func (a *App) syncStatus(c *gin.Context) {
	schema, err := a.agencySchemaStatus()
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "schema_check_failed", "代理商schema检查失败", err.Error())
		return
	}
	backlog, err := a.agencyBacklogStatus(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "backlog_check_failed", "代理商积压检查失败", err.Error())
		return
	}
	respondOK(c, gin.H{"schema": schema, "capabilities": a.agencyCapabilities(), "backlog": backlog})
}

func (a *App) listAudit(c *gin.Context) {
	identity := currentIdentity(c)
	if hasCursorPagingConflict(c) {
		respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor不能与page或limit同时使用", nil)
		return
	}
	pageSize, err := cursorPageSize(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_page_size", err.Error(), nil)
		return
	}
	var cursor agencyCursor
	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		cursor, err = a.decodeCursor(raw, c, "root_audit", identity)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
			return
		}
	}
	var rows []model.AgencyAuditLog
	query := a.db.Model(&model.AgencyAuditLog{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取审计日志失败", nil)
		return
	}
	if strings.TrimSpace(c.Query("cursor")) != "" {
		query = query.Where("id < ?", cursor.PositionID)
	}
	if err := query.Order("id DESC").Limit(pageSize + 1).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取审计日志失败", nil)
		return
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		nextCursor, err = a.encodeCursor(agencyCursor{
			Kind: "root_audit", Scope: cursorScope(c, "root_audit", identity),
			ActorType: identity.ActorType, ActorID: identity.ActorID,
			PositionID: rows[len(rows)-1].ID,
		})
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
			return
		}
	}
	respondOK(c, gin.H{"items": rows, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
}

// listOwnAudit intentionally exposes a reduced operator view.  Root audit
// records may contain source IPs and before/after snapshots that are useful
// for incident response but are not part of the agency operator contract.
func (a *App) listOwnAudit(c *gin.Context) {
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	identity := currentIdentity(c)
	if hasCursorPagingConflict(c) {
		respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor不能与page或limit同时使用", nil)
		return
	}
	pageSize, err := cursorPageSize(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_page_size", err.Error(), nil)
		return
	}
	var cursor agencyCursor
	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		cursor, err = a.decodeCursor(raw, c, "own_audit", identity)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
			return
		}
	}
	var rows []model.AgencyAuditLog
	query := a.db.Where(
		"(acting_agency_id = ? OR (actor_type = ? AND actor_id = ?))",
		agency.ID, ActorTypeOperator, identity.ActorID,
	).Model(&model.AgencyAuditLog{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取审计日志失败", nil)
		return
	}
	if strings.TrimSpace(c.Query("cursor")) != "" {
		query = query.Where("id < ?", cursor.PositionID)
	}
	query = query.Order("id DESC").Limit(pageSize + 1)
	if err := query.Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取审计日志失败", nil)
		return
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	type auditView struct {
		EventID     string `json:"event_id"`
		Action      string `json:"action"`
		ObjectType  string `json:"object_type"`
		ObjectID    string `json:"object_id"`
		RequestID   string `json:"request_id"`
		Reason      string `json:"reason,omitempty"`
		CreatedAtMS int64  `json:"created_at_ms"`
	}
	items := make([]auditView, 0, len(rows))
	for _, row := range rows {
		items = append(items, auditView{
			EventID: row.EventID, Action: row.Action, ObjectType: row.ObjectType,
			ObjectID: row.ObjectID, RequestID: row.RequestID, Reason: row.Reason,
			CreatedAtMS: row.CreatedAtMS,
		})
	}
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		nextCursor, err = a.encodeCursor(agencyCursor{
			Kind: "own_audit", Scope: cursorScope(c, "own_audit", identity),
			ActorType: identity.ActorType, ActorID: identity.ActorID,
			AgencyID: agency.ID, PositionID: rows[len(rows)-1].ID,
		})
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
			return
		}
	}
	respondOK(c, gin.H{"items": items, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
}
