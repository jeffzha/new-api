package agencyhub

import (
	"context"
	"errors"
	"fmt"
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
		items = append(items, a.reconciliationIssueView(issue))
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
	items, enrichErr := a.auditViews(rows)
	if enrichErr != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取审计对象名称失败", nil)
		return
	}
	respondOK(c, gin.H{"items": items, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
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
	items, enrichErr := a.auditViews(rows)
	if enrichErr != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取审计对象名称失败", nil)
		return
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

type auditView struct {
	EventID     string `json:"event_id"`
	Action      string `json:"action"`
	ObjectType  string `json:"object_type"`
	ObjectID    string `json:"object_id"`
	ObjectName  string `json:"object_name"`
	RequestID   string `json:"request_id"`
	Reason      string `json:"reason,omitempty"`
	CreatedAtMS int64  `json:"created_at_ms"`
}

func (a *App) auditViews(rows []model.AgencyAuditLog) ([]auditView, error) {
	userIDs := make([]int64, 0)
	agencyIDs := make([]int64, 0)
	accountIDs := make([]int64, 0)
	withdrawalIDs := make([]int64, 0)
	operatorIDs := make([]int64, 0)
	bindingIDs := make([]int64, 0)
	provisioningIDs := make([]int64, 0)
	seenUsers := map[int64]struct{}{}
	seenAgencies := map[int64]struct{}{}
	seenAccounts := map[int64]struct{}{}
	seenWithdrawals := map[int64]struct{}{}
	seenOperators := map[int64]struct{}{}
	seenBindings := map[int64]struct{}{}
	seenProvisioning := map[int64]struct{}{}
	if a == nil || a.db == nil {
		items := make([]auditView, 0, len(rows))
		for _, row := range rows {
			items = append(items, auditView{
				EventID: row.EventID, Action: row.Action, ObjectType: row.ObjectType,
				ObjectID: row.ObjectID, ObjectName: row.ObjectID, RequestID: row.RequestID,
				Reason: row.Reason, CreatedAtMS: row.CreatedAtMS,
			})
		}
		return items, nil
	}
	for _, row := range rows {
		prefix, rawID := row.ObjectType, row.ObjectID
		if explicitType, explicitID, ok := strings.Cut(row.ObjectID, ":"); ok {
			if _, err := strconv.ParseInt(explicitID, 10, 64); err == nil {
				prefix, rawID = explicitType, explicitID
			}
		}
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		switch prefix {
		case "user", "customer":
			if _, ok := seenUsers[id]; !ok {
				seenUsers[id] = struct{}{}
				userIDs = append(userIDs, id)
			}
		case "agency":
			if _, ok := seenAgencies[id]; !ok {
				seenAgencies[id] = struct{}{}
				agencyIDs = append(agencyIDs, id)
			}
		case "withdrawal_account":
			if _, ok := seenAccounts[id]; !ok {
				seenAccounts[id] = struct{}{}
				accountIDs = append(accountIDs, id)
			}
		case "withdrawal":
			if _, ok := seenWithdrawals[id]; !ok {
				seenWithdrawals[id] = struct{}{}
				withdrawalIDs = append(withdrawalIDs, id)
			}
		case "operator_account":
			if _, ok := seenOperators[id]; !ok {
				seenOperators[id] = struct{}{}
				operatorIDs = append(operatorIDs, id)
			}
		case "user_binding":
			if _, ok := seenBindings[id]; !ok {
				seenBindings[id] = struct{}{}
				bindingIDs = append(bindingIDs, id)
			}
		case "provisioning":
			if _, ok := seenProvisioning[id]; !ok {
				seenProvisioning[id] = struct{}{}
				provisioningIDs = append(provisioningIDs, id)
			}
		}
	}
	bindingUsers := map[int64]int64{}
	if len(bindingIDs) > 0 {
		var values []model.AgencyUserBinding
		if err := a.db.Select("id, user_id").Where("id IN ?", bindingIDs).Find(&values).Error; err == nil {
			for _, value := range values {
				bindingUsers[value.ID] = value.UserID
				if _, ok := seenUsers[value.UserID]; !ok {
					seenUsers[value.UserID] = struct{}{}
					userIDs = append(userIDs, value.UserID)
				}
			}
		}
	}
	provisioningUsers := map[int64]int64{}
	if len(provisioningIDs) > 0 {
		var values []model.AgencyProvisioningJob
		if err := a.db.Select("id, user_id").Where("id IN ?", provisioningIDs).Find(&values).Error; err == nil {
			for _, value := range values {
				provisioningUsers[value.ID] = value.UserID
				if _, ok := seenUsers[value.UserID]; !ok {
					seenUsers[value.UserID] = struct{}{}
					userIDs = append(userIDs, value.UserID)
				}
			}
		}
	}
	users := map[int64]string{}
	if len(userIDs) > 0 {
		var values []model.User
		if err := a.db.Select("id, username, display_name").Where("id IN ?", userIDs).Find(&values).Error; err == nil {
			for _, value := range values {
				users[int64(value.Id)] = userAccountName(value)
			}
		}
	}
	agencies := map[int64]string{}
	if len(agencyIDs) > 0 {
		var values []model.Agency
		if err := a.db.Select("id, display_name").Where("id IN ?", agencyIDs).Find(&values).Error; err == nil {
			for _, value := range values {
				agencies[value.ID] = value.DisplayName
			}
		}
	}
	accounts := map[int64]string{}
	if len(accountIDs) > 0 {
		var values []model.AgencyWithdrawalAccount
		if err := a.db.Select("id, last4").Where("id IN ?", accountIDs).Find(&values).Error; err == nil {
			for _, value := range values {
				accounts[value.ID] = fmt.Sprintf("收款账户 · 尾号 %s", value.Last4)
			}
		}
	}
	withdrawals := map[int64]string{}
	if len(withdrawalIDs) > 0 {
		var values []model.AgencyWithdrawal
		if err := a.db.Select("id, request_no").Where("id IN ?", withdrawalIDs).Find(&values).Error; err == nil {
			for _, value := range values {
				withdrawals[value.ID] = value.RequestNo
			}
		}
	}
	operators := map[int64]string{}
	if len(operatorIDs) > 0 {
		var values []model.AgencyOperatorAccount
		if err := a.db.Select("id, username").Where("id IN ?", operatorIDs).Find(&values).Error; err == nil {
			for _, value := range values {
				operators[value.ID] = value.Username
			}
		}
	}
	items := make([]auditView, 0, len(rows))
	for _, row := range rows {
		name := ""
		prefix, rawID := row.ObjectType, row.ObjectID
		if explicitType, explicitID, ok := strings.Cut(row.ObjectID, ":"); ok {
			if _, err := strconv.ParseInt(explicitID, 10, 64); err == nil {
				prefix, rawID = explicitType, explicitID
			}
		}
		if id, err := strconv.ParseInt(rawID, 10, 64); err == nil {
			switch prefix {
			case "agency":
				name = agencies[id]
			case "withdrawal_account":
				name = accounts[id]
			case "withdrawal":
				name = withdrawals[id]
			case "operator_account":
				name = operators[id]
			case "user_binding":
				if userID := bindingUsers[id]; userID > 0 {
					name = users[userID]
				} else {
					name = users[id]
				}
			case "provisioning":
				name = users[provisioningUsers[id]]
			default:
				name = users[id]
			}
		}
		if strings.TrimSpace(name) == "" {
			name = row.ObjectID
		}
		items = append(items, auditView{EventID: row.EventID, Action: row.Action, ObjectType: row.ObjectType, ObjectID: row.ObjectID, ObjectName: name, RequestID: row.RequestID, Reason: row.Reason, CreatedAtMS: row.CreatedAtMS})
	}
	return items, nil
}
