package agencyhub

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// createReconciliationRun executes one auditable pass and stores its outcome.
// A deterministic run key makes retries safe when the client loses its
// response after the database commit.
func (a *App) createReconciliationRun(c *gin.Context) {
	now := time.Now()
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		key = "manual-" + strconv.FormatInt(now.UnixNano(), 10)
	}
	run, err := a.RunReconciliation(c.Request.Context(), "manual", key, 0)
	if err != nil {
		if errors.Is(err, errReconciliationRunExists) {
			var existing model.AgencyReconciliationRun
			if lookupErr := a.db.Where("run_key = ?", key).First(&existing).Error; lookupErr == nil {
				respondOK(c, existing)
				return
			}
		}
		respondError(c, http.StatusServiceUnavailable, "reconciliation_failed", "对账执行失败", err.Error())
		return
	}
	respondCreated(c, run)
}

var errReconciliationRunExists = errors.New("reconciliation run already exists")
var errHistoricalReconciliationUnavailable = errors.New("historical reconciliation requires immutable snapshots at the requested money sequence; current-state reconciliation cannot certify the requested cutoff")

// RunReconciliation is shared by the HTTP endpoint and the daily scheduler.
// It records a durable run before checking any projections, so a crash leaves
// an explicit running record for operators to investigate.
// A zero cutoff explicitly requests the existing current-state checks. A
// historical cutoff must never be labelled completed after those checks:
// mutable accounts, lots and balances cannot reconstruct a historical close.
func (a *App) RunReconciliation(parent context.Context, trigger, key string, cutoffAtMS int64) (model.AgencyReconciliationRun, error) {
	if a == nil || a.db == nil {
		return model.AgencyReconciliationRun{}, errors.New("agency database unavailable")
	}
	if parent == nil {
		parent = context.Background()
	}
	now := time.Now().UnixMilli()
	run := model.AgencyReconciliationRun{RunKey: key, Trigger: trigger, Status: "running", CutoffAtMS: cutoffAtMS, StartedAtMS: now}
	if err := a.db.Create(&run).Error; err != nil {
		var existing model.AgencyReconciliationRun
		if errors.Is(err, gorm.ErrDuplicatedKey) || a.db.WithContext(parent).Where("run_key = ?", key).First(&existing).Error == nil {
			return model.AgencyReconciliationRun{}, errReconciliationRunExists
		}
		return model.AgencyReconciliationRun{}, err
	}
	var summary ReconcileSummary
	var err error
	if cutoffAtMS != 0 || trigger == "daily" {
		err = errHistoricalReconciliationUnavailable
	} else {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		summary, err = a.Reconcile(ctx)
		cancel()
	}
	finished := time.Now().UnixMilli()
	updates := map[string]any{"finished_at_ms": finished}
	if err != nil {
		updates["status"] = "failed"
		updates["error"] = err.Error()
	} else {
		payload, marshalErr := common.Marshal(struct {
			ReconcileSummary
			Consistency string `json:"consistency"`
		}{ReconcileSummary: summary, Consistency: "current_state_per_page"})
		if marshalErr != nil {
			err = marshalErr
			updates["status"] = "failed"
			updates["error"] = marshalErr.Error()
		} else {
			updates["status"] = "completed"
			updates["summary_json"] = string(payload)
		}
	}
	if updateErr := a.db.Model(&run).Updates(updates).Error; updateErr != nil {
		return model.AgencyReconciliationRun{}, updateErr
	}
	if reloadErr := a.db.First(&run, run.ID).Error; reloadErr != nil {
		return run, reloadErr
	}
	if err != nil {
		return run, err
	}
	return run, nil
}

func (a *App) listReconciliationRuns(c *gin.Context) {
	identity := currentIdentity(c)
	if hasCursorPagingConflict(c) {
		respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor不能与page或limit同时使用", nil)
		return
	}
	limit, err := cursorPageSize(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_page_size", err.Error(), nil)
		return
	}
	var cursor agencyCursor
	rawCursor := strings.TrimSpace(c.Query("cursor"))
	if rawCursor != "" {
		cursor, err = a.decodeCursor(rawCursor, c, "root_reconciliation_runs", identity)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
			return
		}
	}
	var total int64
	query := a.db.Model(&model.AgencyReconciliationRun{})
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取对账运行记录失败", nil)
		return
	}
	if rawCursor != "" {
		query = query.Where("id < ?", cursor.PositionID)
	}
	var rows []model.AgencyReconciliationRun
	if err := query.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取对账运行记录失败", nil)
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		nextCursor, err = a.encodeCursor(agencyCursor{
			Kind: "root_reconciliation_runs", Scope: cursorScope(c, "root_reconciliation_runs", identity),
			ActorType: identity.ActorType, ActorID: identity.ActorID,
			PositionID: rows[len(rows)-1].ID,
		})
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
			return
		}
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		item := gin.H{
			"id": stringID(row.ID), "run_key": row.RunKey, "trigger": row.Trigger,
			"status": row.Status, "cutoff_at_ms": stringID(row.CutoffAtMS),
			"started_at_ms": stringID(row.StartedAtMS), "summary_json": row.SummaryJSON,
			"error": row.Error,
		}
		if row.FinishedAtMS != nil {
			item["finished_at_ms"] = stringID(*row.FinishedAtMS)
		}
		items = append(items, item)
	}
	respondOK(c, gin.H{"items": items, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
}

func (a *App) listReconciliationIssues(c *gin.Context) {
	identity := currentIdentity(c)
	if identity == nil || identity.ActorType != ActorTypeRoot {
		respondError(c, http.StatusForbidden, "root_required", "仅超级管理员可操作", nil)
		return
	}
	if hasCursorPagingConflict(c) {
		respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor不能与page同时使用", nil)
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && status != "open" && status != "resolved" && status != "ignored" {
		respondError(c, http.StatusBadRequest, "invalid_status", "无效的异常状态", nil)
		return
	}
	pageSize, err := cursorPageSize(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_page_size", err.Error(), nil)
		return
	}
	var cursor agencyCursor
	rawCursor := strings.TrimSpace(c.Query("cursor"))
	if rawCursor != "" {
		cursor, err = a.decodeCursor(rawCursor, c, "root_reconciliation_issues", identity)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
			return
		}
	}
	query := a.db.Model(&model.AgencyReconciliationIssue{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取对账异常失败", nil)
		return
	}
	if rawCursor != "" {
		query = query.Where("id < ?", cursor.PositionID)
	}
	var issues []model.AgencyReconciliationIssue
	if err := query.Order("id DESC").Limit(pageSize + 1).Find(&issues).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取对账异常失败", nil)
		return
	}
	hasMore := len(issues) > pageSize
	if hasMore {
		issues = issues[:pageSize]
	}
	nextCursor := ""
	if hasMore && len(issues) > 0 {
		nextCursor, err = a.encodeCursor(agencyCursor{
			Kind: "root_reconciliation_issues", Scope: cursorScope(c, "root_reconciliation_issues", identity),
			ActorType: identity.ActorType, ActorID: identity.ActorID,
			PositionID: issues[len(issues)-1].ID,
		})
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
			return
		}
	}
	items := make([]gin.H, 0, len(issues))
	for _, issue := range issues {
		items = append(items, reconciliationIssueView(issue))
	}
	respondOK(c, gin.H{"items": items, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
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
