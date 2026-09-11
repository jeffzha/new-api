package agencyhub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ProcessExportJobs claims and materializes queued export jobs. It is safe to
// call from more than one worker: the status CAS ensures one claimant per job.
// The generated file is private to the configured export directory and the
// job retains only a relative file key and content hash.
func (a *App) ProcessExportJobs(limit int) (int, error) {
	if a == nil || a.db == nil || limit <= 0 {
		return 0, nil
	}
	if a.config.ExportDir == "" {
		return 0, nil
	}
	processed := 0
	for processed < limit {
		var job model.AgencyExportJob
		claimed := false
		err := a.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("status = ?", "queued").Order("id ASC").First(&job).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			result := tx.Model(&model.AgencyExportJob{}).Where("id = ? AND status = ?", job.ID, "queued").Updates(map[string]any{"status": "processing"})
			if result.Error != nil {
				return result.Error
			}
			claimed = result.RowsAffected == 1
			return nil
		})
		if err != nil {
			return processed, err
		}
		if !claimed {
			break
		}
		if err := a.materializeExport(&job); err != nil {
			_ = a.db.Model(&model.AgencyExportJob{}).Where("id = ?", job.ID).Updates(map[string]any{"status": "failed", "file_key": "", "file_hash": ""}).Error
			return processed, err
		}
		processed++
	}
	return processed, nil
}

// CleanupExpiredExportJobs removes temporary export files only after the
// corresponding job has been atomically marked expired.  Processing jobs are
// intentionally left alone: a large export may legitimately take longer than
// one worker tick, and deleting its file while it is being materialized would
// create a race between the worker and cleanup.
func (a *App) CleanupExpiredExportJobs(limit int) (int, error) {
	if a == nil || a.db == nil || limit <= 0 || strings.TrimSpace(a.config.ExportDir) == "" {
		return 0, nil
	}
	now := time.Now().Unix()
	var jobs []model.AgencyExportJob
	if err := a.db.Where("status IN ? AND expires_at > 0 AND expires_at <= ?", []string{"queued", "ready", "failed"}, now).
		Order("id ASC").Limit(limit).Find(&jobs).Error; err != nil {
		return 0, err
	}
	cleaned := 0
	for _, job := range jobs {
		result := a.db.Model(&model.AgencyExportJob{}).
			Where("id = ? AND status IN ? AND expires_at > 0 AND expires_at <= ?", job.ID, []string{"queued", "ready", "failed"}, now).
			Updates(map[string]any{
				"status":                    "expired",
				"file_key":                  "",
				"file_hash":                 "",
				"download_token_hash":       "",
				"download_token_expires_at": 0,
				"download_token_session_id": 0,
			})
		if result.Error != nil {
			return cleaned, result.Error
		}
		if result.RowsAffected != 1 {
			continue
		}
		if job.FileKey != "" {
			if path, err := a.exportPath(job.FileKey); err == nil {
				_ = os.Remove(path)
			}
		}
		cleaned++
	}
	return cleaned, nil
}

func (a *App) materializeExport(job *model.AgencyExportJob) error {
	if job == nil || job.ID <= 0 || job.AgencyID == nil {
		return fmt.Errorf("invalid export job")
	}
	if err := os.MkdirAll(a.config.ExportDir, 0o700); err != nil {
		return err
	}
	fileKey := fmt.Sprintf("agency-export-%d.csv", job.ID)
	path, err := a.exportPath(fileKey)
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write([]byte{0xef, 0xbb, 0xbf}); err != nil {
		_ = file.Close()
		return err
	}
	writer := csv.NewWriter(file)
	writeErr := a.writeExportRows(writer, job)
	writer.Flush()
	if writeErr == nil {
		writeErr = writer.Error()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(tmpPath)
		return writeErr
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	hash, rowCount, err := hashExport(path)
	if err != nil {
		return err
	}
	return a.db.Model(&model.AgencyExportJob{}).Where("id = ? AND status = ?", job.ID, "processing").Updates(map[string]any{"status": "ready", "file_key": fileKey, "file_hash": hash, "row_count": rowCount}).Error
}

func (a *App) exportPath(fileKey string) (string, error) {
	root, err := filepath.Abs(a.config.ExportDir)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(root, fileKey))
	if err != nil || (path != root && !strings.HasPrefix(path, root+string(os.PathSeparator))) {
		return "", errors.New("invalid export file key")
	}
	return path, nil
}

func (a *App) writeExportRows(writer *csv.Writer, job *model.AgencyExportJob) error {
	filter, err := parseExportFilter(job.FilterJSON)
	if err != nil {
		return err
	}
	switch job.Kind {
	case "usage":
		if err := writer.Write([]string{"event_id", "user_id", "model", "business_status", "standard_quota", "charged_quota", "currency", "occurred_at"}); err != nil {
			return err
		}
		var rows []model.AgencyUsageFact
		query, err := applyExportFilter(a.db.Where("agency_id = ?", *job.AgencyID), "usage", filter)
		if err != nil {
			return err
		}
		if err := ensureExportRowLimit(query, model.AgencyUsageFact{}); err != nil {
			return err
		}
		if err := query.Order("occurred_at_ms, id").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := writeExportRow(writer, []string{row.EventID, strconv.FormatInt(row.UserID, 10), row.OriginModelName, row.BusinessStatus, strconv.FormatInt(row.StandardQuota, 10), strconv.FormatInt(row.ChargedQuota, 10), row.CurrencyCode, formatExportTime(row.OccurredAtMS)}); err != nil {
				return err
			}
		}
	case "topups":
		if err := writer.Write([]string{"source_operation_id", "user_id", "credited_quota", "paid_quota", "bonus_quota", "currency", "payment_status", "occurred_at"}); err != nil {
			return err
		}
		var rows []model.AgencyTopupFact
		query, err := applyExportFilter(a.db.Where("agency_id = ?", *job.AgencyID), "topups", filter)
		if err != nil {
			return err
		}
		if err := ensureExportRowLimit(query, model.AgencyTopupFact{}); err != nil {
			return err
		}
		if err := query.Order("occurred_at_ms, id").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := writeExportRow(writer, []string{row.SourceOperationID, strconv.FormatInt(row.UserID, 10), strconv.FormatInt(row.CreditedQuota, 10), strconv.FormatInt(row.PaidQuota, 10), strconv.FormatInt(row.BonusQuota, 10), row.CurrencyCode, row.PaymentStatus, formatExportTime(row.OccurredAtMS)}); err != nil {
				return err
			}
		}
	case "commissions":
		if err := writer.Write([]string{"event_id", "entry_type", "user_id", "model", "commission_quota", "amount_micros", "currency", "occurred_at"}); err != nil {
			return err
		}
		var rows []model.AgencyCommissionLedger
		query, err := applyExportFilter(a.db.Where("agency_id = ?", *job.AgencyID), "commissions", filter)
		if err != nil {
			return err
		}
		if err := ensureExportRowLimit(query, model.AgencyCommissionLedger{}); err != nil {
			return err
		}
		if err := query.Order("occurred_at_ms, id").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := writeExportRow(writer, []string{row.EventID, row.EntryType, strconv.FormatInt(row.UserID, 10), row.OriginModelName, strconv.FormatInt(row.CommissionQuota, 10), strconv.FormatInt(row.AmountMicros, 10), row.CurrencyCode, formatExportTime(row.OccurredAtMS)}); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported export kind %q", job.Kind)
	}
	return nil
}

const maxExportRows int64 = 1_000_000

func formatExportTime(ms int64) string {
	return time.UnixMilli(ms).In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format(time.RFC3339)
}

func ensureExportRowLimit(query *gorm.DB, modelValue any) error {
	var count int64
	if err := query.Model(modelValue).Count(&count).Error; err != nil {
		return err
	}
	if count > maxExportRows {
		return fmt.Errorf("export exceeds %d rows; narrow the time range", maxExportRows)
	}
	return nil
}

// parseExportFilter validates the small, database-independent export filter
// vocabulary. Values are intentionally kept as strings so JSON numbers cannot
// silently round large user IDs or timestamps.
func parseExportFilter(encoded string) (map[string]string, error) {
	result := map[string]string{}
	if strings.TrimSpace(encoded) == "" || strings.TrimSpace(encoded) == "null" {
		return result, nil
	}
	var raw map[string]any
	if err := common.Unmarshal([]byte(encoded), &raw); err != nil {
		return nil, fmt.Errorf("invalid filter")
	}
	for key, value := range raw {
		var text string
		switch v := value.(type) {
		case string:
			text = strings.TrimSpace(v)
		case float64:
			// IDs, timestamps and money filters must be decimal strings in the
			// JSON contract; accepting float64 would lose precision for int64.
			return nil, fmt.Errorf("filter %s must be a string", key)
		default:
			return nil, fmt.Errorf("invalid filter value for %s", key)
		}
		if text == "" {
			return nil, fmt.Errorf("empty filter value for %s", key)
		}
		result[key] = text
	}
	return result, nil
}

func applyExportFilter(query *gorm.DB, kind string, filter map[string]string) (*gorm.DB, error) {
	allowed := map[string]bool{"start_at": true, "end_at": true, "start_date": true, "end_date": true, "user_id": true, "currency": true}
	if kind == "usage" || kind == "commissions" {
		allowed["model"] = true
	}
	if kind == "usage" {
		allowed["status"] = true
	}
	if kind == "topups" {
		allowed["payment_status"] = true
	}
	for key := range filter {
		if !allowed[key] {
			return nil, fmt.Errorf("unsupported filter %q", key)
		}
	}
	if (filter["start_date"] != "" || filter["end_date"] != "") && (filter["start_at"] != "" || filter["end_at"] != "") {
		return nil, fmt.Errorf("date and timestamp filters are mutually exclusive")
	}
	var startMS, endMS *int64
	if _, ok := filter["start_date"]; ok {
		loc := time.FixedZone("Asia/Shanghai", 8*60*60)
		start, err := time.ParseInLocation("2006-01-02", filter["start_date"], loc)
		if err != nil {
			return nil, fmt.Errorf("invalid start_date")
		}
		value := start.UnixMilli()
		startMS = &value
	}
	if _, ok := filter["end_date"]; ok {
		loc := time.FixedZone("Asia/Shanghai", 8*60*60)
		end, err := time.ParseInLocation("2006-01-02", filter["end_date"], loc)
		if err != nil {
			return nil, fmt.Errorf("invalid end_date")
		}
		value := end.AddDate(0, 0, 1).UnixMilli()
		endMS = &value
	}
	if value, ok := filter["start_at"]; ok {
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return nil, fmt.Errorf("invalid start_at")
		}
		ms := t.UnixMilli()
		startMS = &ms
	}
	if value, ok := filter["end_at"]; ok {
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return nil, fmt.Errorf("invalid end_at")
		}
		ms := t.UnixMilli()
		endMS = &ms
	}
	if startMS != nil && endMS != nil && *startMS >= *endMS {
		return nil, fmt.Errorf("start must be before end")
	}
	if startMS != nil {
		query = query.Where("occurred_at_ms >= ?", *startMS)
	}
	if endMS != nil {
		query = query.Where("occurred_at_ms < ?", *endMS)
	}
	if s, ok := filter["user_id"]; ok {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid user_id")
		}
		query = query.Where("user_id = ?", id)
	}
	if s, ok := filter["currency"]; ok {
		query = query.Where("currency_code = ?", s)
	}
	if s, ok := filter["model"]; ok {
		query = query.Where("origin_model_name = ?", s)
	}
	if s, ok := filter["status"]; ok {
		query = query.Where("business_status = ?", s)
	}
	if s, ok := filter["payment_status"]; ok {
		query = query.Where("payment_status = ?", s)
	}
	return query, nil
}

func hashExport(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", 0, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	reader := csv.NewReader(file)
	var rows int64
	for {
		if _, err := reader.Read(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", 0, err
		}
		rows++
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), rows - 1, nil
}

func (a *App) listCustomers(c *gin.Context) {
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
		cursor, err = a.decodeCursor(raw, c, "customers", identity)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
			return
		}
	}
	baseQuery := a.db.Where("agency_id = ?", agency.ID)
	var total int64
	if err := baseQuery.Model(&model.AgencyActiveUserBinding{}).Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	query := baseQuery
	if cursor.PositionU > 0 {
		query = query.Where("user_id > ?", cursor.PositionU)
	}
	var bindings []model.AgencyActiveUserBinding
	if err := query.Order("user_id ASC").Limit(pageSize + 1).Find(&bindings).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	hasMore := len(bindings) > pageSize
	if hasMore {
		bindings = bindings[:pageSize]
	}
	ids := make([]int64, 0, len(bindings))
	for _, binding := range bindings {
		ids = append(ids, binding.UserID)
	}
	type customerView struct {
		UserID        int64  `json:"user_id"`
		Username      string `json:"username"`
		BindingID     int64  `json:"binding_id"`
		Revision      int64  `json:"revision"`
		EffectiveAtMS int64  `json:"effective_at_ms"`
	}
	views := make([]customerView, 0, len(bindings))
	if len(ids) > 0 {
		var users []model.User
		if err := a.db.Select("id, username").Where("id IN ?", ids).Find(&users).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
			return
		}
		byID := make(map[int64]model.User, len(users))
		for _, user := range users {
			byID[int64(user.Id)] = user
		}
		for _, binding := range bindings {
			user := byID[binding.UserID]
			views = append(views, customerView{UserID: binding.UserID, Username: user.Username, BindingID: binding.BindingID, Revision: binding.Revision})
		}
	}
	nextCursor := ""
	if hasMore && len(bindings) > 0 {
		last := bindings[len(bindings)-1]
		nextCursor, err = a.encodeCursor(agencyCursor{
			Kind:      "customers",
			Scope:     cursorScope(c, "customers", identity),
			ActorType: identity.ActorType,
			ActorID:   identity.ActorID,
			AgencyID:  agency.ID,
			PositionU: last.UserID,
		})
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
			return
		}
	}
	respondOK(c, gin.H{"items": views, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
}

func (a *App) authorizedCustomer(c *gin.Context, userID int64, activeOnly bool) (model.Agency, bool) {
	identity := currentIdentity(c)
	if identity == nil || identity.AgencyID == nil || userID <= 0 {
		if identity == nil || identity.AgencyID == nil {
			respondError(c, http.StatusForbidden, "agency_required", "当前会话没有代理商范围", nil)
		}
		return model.Agency{}, false
	}
	var agency model.Agency
	if err := a.db.First(&agency, *identity.AgencyID).Error; err != nil {
		respondError(c, http.StatusNotFound, "not_found", "代理商不存在", nil)
		return model.Agency{}, false
	}
	query := a.db.Model(&model.AgencyUserBinding{}).Where("user_id = ? AND agency_id = ?", userID, agency.ID)
	if activeOnly {
		query = query.Where("ended_at_ms IS NULL")
	}
	var count int64
	if err := query.Count(&count).Error; err != nil || count == 0 {
		respondError(c, http.StatusNotFound, "not_found", "客户不存在", nil)
		return model.Agency{}, false
	}
	return agency, true
}

func (a *App) getCustomer(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的用户ID", nil)
		return
	}
	// Customer profile is a current-ownership view.  A transferred customer
	// remains visible to the old agency only through historical fact routes,
	// never through the current username/status endpoint.
	if _, ok := a.authorizedCustomer(c, userID, true); !ok {
		return
	}
	var user model.User
	if err := a.db.Select("id, username, status, created_at").Where("id = ?", userID).First(&user).Error; err != nil {
		respondError(c, http.StatusNotFound, "not_found", "客户不存在", nil)
		return
	}
	respondOK(c, gin.H{"user_id": user.Id, "username": user.Username, "status": user.Status, "created_at": user.CreatedAt})
}

func (a *App) customerUsage(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的用户ID", nil)
		return
	}
	agency, ok := a.authorizedCustomer(c, userID, false)
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
	cursorKind := "customer_usage:" + strconv.FormatInt(userID, 10)
	var cursor agencyCursor
	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		cursor, err = a.decodeCursor(raw, c, cursorKind, identity)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
			return
		}
	}
	var rows []model.AgencyUsageFact
	query := a.db.Where("user_id = ? AND agency_id = ?", userID, agency.ID)
	var total int64
	if err := query.Model(&model.AgencyUsageFact{}).Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取用量失败", nil)
		return
	}
	if strings.TrimSpace(c.Query("cursor")) != "" {
		query = query.Where("(occurred_at_ms < ? OR (occurred_at_ms = ? AND id < ?))", cursor.PositionMS, cursor.PositionMS, cursor.PositionID)
	}
	if err := query.Order("occurred_at_ms DESC, id DESC").Limit(pageSize + 1).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取用量失败", nil)
		return
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{"event_id": row.EventID, "user_id": row.UserID, "model": row.OriginModelName, "endpoint": row.Endpoint, "business_status": row.BusinessStatus, "standard_quota": row.StandardQuota, "sales_bps": row.SalesBPS, "charged_quota": row.ChargedQuota, "currency_code": row.CurrencyCode, "skip_reason": row.SkipReason, "occurred_at_ms": row.OccurredAtMS})
	}
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		nextCursor, err = a.encodeCursor(agencyCursor{
			Kind: cursorKind, Scope: cursorScope(c, cursorKind, identity),
			ActorType: identity.ActorType, ActorID: identity.ActorID,
			AgencyID: agency.ID, PositionMS: last.OccurredAtMS, PositionID: last.ID,
		})
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
			return
		}
	}
	respondOK(c, gin.H{"items": items, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
}

func (a *App) customerTopups(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的用户ID", nil)
		return
	}
	agency, ok := a.authorizedCustomer(c, userID, false)
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
	cursorKind := "customer_topups:" + strconv.FormatInt(userID, 10)
	var cursor agencyCursor
	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		cursor, err = a.decodeCursor(raw, c, cursorKind, identity)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid_cursor", "cursor无效或已过期", nil)
			return
		}
	}
	var rows []model.AgencyTopupFact
	query := a.db.Where("user_id = ? AND agency_id = ?", userID, agency.ID)
	var total int64
	if err := query.Model(&model.AgencyTopupFact{}).Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取充值记录失败", nil)
		return
	}
	if strings.TrimSpace(c.Query("cursor")) != "" {
		query = query.Where("(occurred_at_ms < ? OR (occurred_at_ms = ? AND id < ?))", cursor.PositionMS, cursor.PositionMS, cursor.PositionID)
	}
	if err := query.Order("occurred_at_ms DESC, id DESC").Limit(pageSize + 1).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取充值记录失败", nil)
		return
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	type topupView struct {
		SourceOperationID string `json:"source_operation_id"`
		PaymentReference  string `json:"payment_reference,omitempty"`
		ActualMoney       string `json:"actual_money,omitempty"`
		CurrencyCode      string `json:"currency_code"`
		CreditedQuota     int64  `json:"credited_quota"`
		PaidQuota         int64  `json:"paid_quota"`
		BonusQuota        int64  `json:"bonus_quota"`
		CompletionSource  string `json:"completion_source"`
		PaymentStatus     string `json:"payment_status"`
		RefundedQuota     int64  `json:"refunded_quota"`
		OccurredAtMS      int64  `json:"occurred_at_ms"`
	}
	items := make([]topupView, 0, len(rows))
	for _, row := range rows {
		items = append(items, topupView{
			SourceOperationID: row.SourceOperationID,
			PaymentReference:  maskPaymentReference(row.PaymentReference),
			ActualMoney:       row.ActualMoney,
			CurrencyCode:      row.CurrencyCode,
			CreditedQuota:     row.CreditedQuota,
			PaidQuota:         row.PaidQuota,
			BonusQuota:        row.BonusQuota,
			CompletionSource:  row.CompletionSource,
			PaymentStatus:     row.PaymentStatus,
			RefundedQuota:     row.RefundedQuota,
			OccurredAtMS:      row.OccurredAtMS,
		})
	}
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		nextCursor, err = a.encodeCursor(agencyCursor{
			Kind: cursorKind, Scope: cursorScope(c, cursorKind, identity),
			ActorType: identity.ActorType, ActorID: identity.ActorID,
			AgencyID: agency.ID, PositionMS: last.OccurredAtMS, PositionID: last.ID,
		})
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "cursor_unavailable", "分页服务暂不可用", nil)
			return
		}
	}
	respondOK(c, gin.H{"items": items, "total": total, "meta": gin.H{"next_cursor": nextCursor}})
}

func maskPaymentReference(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 4 {
		return "****"
	}
	return "****" + string(runes[len(runes)-4:])
}

func (a *App) reportSummary(c *gin.Context) {
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	startMS, endMS, err := reportRange(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_range", err.Error(), nil)
		return
	}
	userID := int64(0)
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		userID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || userID <= 0 {
			respondError(c, http.StatusBadRequest, "invalid_user_id", "user_id无效", nil)
			return
		}
		// A user filter must still be authorized against event ownership.
		// Historical facts remain visible to the old agency, while a user
		// who was never associated with this agency is indistinguishable from
		// a missing user.
		var bindingCount int64
		if err := a.db.Model(&model.AgencyUserBinding{}).Where("user_id = ? AND agency_id = ?", userID, agency.ID).Count(&bindingCount).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", "读取客户归属失败", nil)
			return
		}
		if bindingCount == 0 {
			respondError(c, http.StatusNotFound, "not_found", "客户不存在", nil)
			return
		}
	}
	modelFilter := strings.TrimSpace(c.Query("model"))
	if len([]rune(modelFilter)) > 191 {
		respondError(c, http.StatusBadRequest, "invalid_model", "model筛选过长", nil)
		return
	}
	currencyFilter := strings.TrimSpace(c.Query("currency"))
	if len(currencyFilter) > 16 {
		respondError(c, http.StatusBadRequest, "invalid_currency", "currency筛选无效", nil)
		return
	}

	type reportItem struct {
		UserID           int64  `json:"user_id"`
		Model            string `json:"model"`
		CurrencyCode     string `json:"currency_code"`
		Calls            int64  `json:"calls"`
		ChargedQuota     int64  `json:"charged_quota"`
		CommissionMicros int64  `json:"commission_micros"`
		ReversalMicros   int64  `json:"reversal_micros"`
	}
	itemByKey := make(map[string]*reportItem)
	itemKey := func(userID int64, modelName, currency string) string {
		return strconv.FormatInt(userID, 10) + "\x00" + modelName + "\x00" + currency
	}
	usageQuery := a.db.Model(&model.AgencyUsageFact{}).
		Where("agency_id = ? AND occurred_at_ms >= ? AND occurred_at_ms < ?", agency.ID, startMS, endMS)
	if userID > 0 {
		usageQuery = usageQuery.Where("user_id = ?", userID)
	}
	if modelFilter != "" {
		usageQuery = usageQuery.Where("origin_model_name = ?", modelFilter)
	}
	if currencyFilter != "" {
		usageQuery = usageQuery.Where("currency_code = ?", currencyFilter)
	}
	var usageItems []reportItem
	if err := usageQuery.Select("user_id, origin_model_name AS model, currency_code, COUNT(*) AS calls, COALESCE(SUM(charged_quota), 0) AS charged_quota").
		Group("user_id, origin_model_name, currency_code").Find(&usageItems).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	for index := range usageItems {
		item := &usageItems[index]
		itemByKey[itemKey(item.UserID, item.Model, item.CurrencyCode)] = item
	}

	commissionQuery := a.db.Model(&model.AgencyCommissionLedger{}).
		Where("agency_id = ? AND occurred_at_ms >= ? AND occurred_at_ms < ?", agency.ID, startMS, endMS)
	if userID > 0 {
		commissionQuery = commissionQuery.Where("user_id = ?", userID)
	}
	if modelFilter != "" {
		commissionQuery = commissionQuery.Where("origin_model_name = ?", modelFilter)
	}
	if currencyFilter != "" {
		commissionQuery = commissionQuery.Where("currency_code = ?", currencyFilter)
	}
	type commissionItem struct {
		UserID       int64
		Model        string
		CurrencyCode string
		EntryType    string
		AmountMicros int64
	}
	var commissionItems []commissionItem
	if err := commissionQuery.Select("user_id, origin_model_name AS model, currency_code, entry_type, COALESCE(SUM(amount_micros), 0) AS amount_micros").
		Group("user_id, origin_model_name, currency_code, entry_type").Find(&commissionItems).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	for _, commission := range commissionItems {
		key := itemKey(commission.UserID, commission.Model, commission.CurrencyCode)
		item := itemByKey[key]
		if item == nil {
			item = &reportItem{UserID: commission.UserID, Model: commission.Model, CurrencyCode: commission.CurrencyCode}
			itemByKey[key] = item
		}
		if commission.EntryType == "reversal" {
			if commission.AmountMicros < 0 {
				item.ReversalMicros += -commission.AmountMicros
			} else {
				item.ReversalMicros += commission.AmountMicros
			}
		} else {
			item.CommissionMicros += commission.AmountMicros
		}
	}
	items := make([]reportItem, 0, len(itemByKey))
	var total reportItem
	for _, item := range itemByKey {
		items = append(items, *item)
		total.Calls += item.Calls
		total.ChargedQuota += item.ChargedQuota
		total.CommissionMicros += item.CommissionMicros
		total.ReversalMicros += item.ReversalMicros
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Model != items[j].Model {
			return items[i].Model < items[j].Model
		}
		if items[i].CurrencyCode != items[j].CurrencyCode {
			return items[i].CurrencyCode < items[j].CurrencyCode
		}
		return items[i].UserID < items[j].UserID
	})
	respondOK(c, gin.H{
		"start_at_ms":       startMS,
		"end_at_ms":         endMS,
		"calls":             total.Calls,
		"charged_quota":     total.ChargedQuota,
		"commission_micros": total.CommissionMicros,
		"reversal_micros":   total.ReversalMicros,
		"items":             items,
	})
}

func reportRange(c *gin.Context) (int64, int64, error) {
	start := strings.TrimSpace(c.Query("start_date"))
	end := strings.TrimSpace(c.Query("end_date"))
	startAt := strings.TrimSpace(c.Query("start_at"))
	endAt := strings.TrimSpace(c.Query("end_at"))
	if (start != "" || end != "") && (startAt != "" || endAt != "") {
		return 0, 0, fmt.Errorf("date and timestamp filters are mutually exclusive")
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	if startAt != "" || endAt != "" {
		if startAt == "" || endAt == "" {
			return 0, 0, fmt.Errorf("start_at and end_at are both required")
		}
		startTime, err := time.Parse(time.RFC3339, startAt)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid start_at")
		}
		endTime, err := time.Parse(time.RFC3339, endAt)
		if err != nil || !startTime.Before(endTime) {
			return 0, 0, fmt.Errorf("end_at must be after start_at")
		}
		if endTime.Sub(startTime) > 366*24*time.Hour {
			return 0, 0, fmt.Errorf("report range cannot exceed 366 days")
		}
		return startTime.UnixMilli(), endTime.UnixMilli(), nil
	}
	today := time.Now().In(location)
	if start == "" && end == "" {
		// The default is seven local calendar days, including today.
		start = today.AddDate(0, 0, -6).Format("2006-01-02")
		end = today.Format("2006-01-02")
	} else if start == "" || end == "" {
		return 0, 0, fmt.Errorf("start_date and end_date are both required")
	}
	startTime, err := time.ParseInLocation("2006-01-02", start, location)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start_date")
	}
	endTime, err := time.ParseInLocation("2006-01-02", end, location)
	if err != nil || endTime.Before(startTime) {
		return 0, 0, fmt.Errorf("end_date must be on or after start_date")
	}
	endExclusive := endTime.AddDate(0, 0, 1)
	if endExclusive.Sub(startTime) > 366*24*time.Hour {
		return 0, 0, fmt.Errorf("report range cannot exceed 366 days")
	}
	return startTime.UnixMilli(), endExclusive.UnixMilli(), nil
}

func (a *App) createExport(c *gin.Context) {
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	if strings.TrimSpace(a.config.ExportDir) == "" {
		respondError(c, http.StatusServiceUnavailable, "export_unavailable", "导出服务未配置", nil)
		return
	}
	var request struct {
		Kind   string         `json:"kind"`
		Filter map[string]any `json:"filter"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.Kind == "" {
		respondError(c, http.StatusBadRequest, "invalid_request", "导出参数无效", nil)
		return
	}
	if request.Kind != "usage" && request.Kind != "topups" && request.Kind != "commissions" {
		respondError(c, http.StatusUnprocessableEntity, "invalid_kind", "不支持的导出类型", nil)
		return
	}
	now := time.Now().UnixMilli()
	encoded, err := marshalJSON(request.Filter)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_filter", "筛选条件无效", nil)
		return
	}
	parsedFilter, err := parseExportFilter(encoded)
	if err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_filter", err.Error(), nil)
		return
	}
	if _, err := applyExportFilter(a.db, request.Kind, parsedFilter); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_filter", err.Error(), nil)
		return
	}
	identity := currentIdentity(c)
	job := model.AgencyExportJob{ActorType: identity.ActorType, ActorID: identity.ActorID, AgencyID: &agency.ID, Kind: request.Kind, FilterJSON: encoded, PermissionVersion: agency.StateRevision, Status: "queued", ExpiresAt: time.Now().Add(24 * time.Hour).Unix(), CreatedAtMS: now}
	if err = a.db.Create(&job).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
		return
	}
	respondAccepted(c, job)
}

// ExportCSV writes RFC4180 rows and protects spreadsheet consumers from formula
// injection. It is kept as a small pure helper for the asynchronous exporter.
func ExportCSV(w *csv.Writer, headers []string, rows [][]string) error {
	if err := w.Write(headers); err != nil {
		return err
	}
	for _, row := range rows {
		if err := writeExportRow(w, row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func writeExportRow(w *csv.Writer, row []string) error {
	safe := make([]string, len(row))
	for i, value := range row {
		safe[i] = csvSafe(value)
	}
	return w.Write(safe)
}

func csvSafe(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '\t', '\r', '\n':
		return "'" + value
	}
	// Numeric columns are emitted by the server and must retain a real
	// negative sign for accounting exports. Text beginning with '-' is still
	// protected below.
	if value[0] == '-' {
		if _, err := strconv.ParseFloat(value, 64); err == nil {
			return value
		}
	}
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	}
	return value
}
func marshalJSON(value any) (string, error) {
	encoded, err := common.Marshal(value)
	return string(encoded), err
}

func (a *App) getExport(c *gin.Context) {
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	if agency.Status != AgencyStatusActive {
		respondError(c, http.StatusForbidden, "agency_disabled", "代理商已停用", nil)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的导出任务ID", nil)
		return
	}
	identity := currentIdentity(c)
	var job model.AgencyExportJob
	query := a.db.Where("id = ? AND actor_id = ? AND actor_type = ?", id, identity.ActorID, identity.ActorType)
	if err = query.First(&job).Error; err != nil {
		respondError(c, http.StatusNotFound, "not_found", "导出任务不存在", nil)
		return
	}
	if job.AgencyID == nil || *job.AgencyID != agency.ID || job.PermissionVersion != agency.StateRevision {
		respondError(c, http.StatusForbidden, "export_scope_changed", "导出权限或范围已变化，请重新导出", nil)
		return
	}
	response := gin.H{"id": job.ID, "actor_type": job.ActorType, "actor_id": job.ActorID, "agency_id": job.AgencyID, "kind": job.Kind, "status": job.Status, "row_count": job.RowCount, "file_hash": job.FileHash, "expires_at": job.ExpiresAt, "created_at_ms": job.CreatedAtMS}
	if job.Status == "ready" {
		token, err := randomToken(32)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "token_error", "生成下载凭据失败", nil)
			return
		}
		tokenExpiry := time.Now().Add(10 * time.Minute).Unix()
		identity := currentIdentity(c)
		if err := a.db.Model(&model.AgencyExportJob{}).Where("id = ? AND actor_id = ? AND actor_type = ? AND status = ?", id, identity.ActorID, identity.ActorType, "ready").Updates(map[string]any{"download_token_hash": tokenHash(token), "download_token_expires_at": tokenExpiry, "download_token_session_id": identity.SessionID}).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "database_error", err.Error(), nil)
			return
		}
		response["download_token"] = token
		response["download_token_expires_at"] = tokenExpiry
	}
	respondOK(c, response)
}

func (a *App) downloadExport(c *gin.Context) {
	agency, _, ok := a.ownAgency(c)
	if !ok {
		return
	}
	if agency.Status != AgencyStatusActive {
		respondError(c, http.StatusForbidden, "agency_disabled", "代理商已停用", nil)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		respondError(c, http.StatusBadRequest, "invalid_id", "无效的导出任务ID", nil)
		return
	}
	identity := currentIdentity(c)
	var job model.AgencyExportJob
	if err := a.db.Where("id = ? AND actor_id = ? AND actor_type = ?", id, identity.ActorID, identity.ActorType).First(&job).Error; err != nil {
		respondError(c, http.StatusNotFound, "not_found", "导出任务不存在", nil)
		return
	}
	if job.AgencyID == nil || *job.AgencyID != agency.ID || job.PermissionVersion != agency.StateRevision {
		respondError(c, http.StatusForbidden, "export_scope_changed", "导出权限或范围已变化，请重新导出", nil)
		return
	}
	if job.Status != "ready" {
		respondError(c, http.StatusConflict, "export_not_ready", "导出任务尚未完成", gin.H{"status": job.Status})
		return
	}
	providedToken := strings.TrimSpace(c.GetHeader("X-Export-Token"))
	if providedToken == "" {
		providedToken = strings.TrimSpace(c.Query("token"))
	}
	if providedToken == "" || job.DownloadTokenHash == "" || job.DownloadTokenExpiresAt <= time.Now().Unix() || subtle.ConstantTimeCompare([]byte(tokenHash(providedToken)), []byte(job.DownloadTokenHash)) != 1 || job.DownloadTokenSessionID != identity.SessionID {
		respondError(c, http.StatusUnauthorized, "invalid_download_token", "下载凭据无效或已过期", nil)
		return
	}
	if job.ExpiresAt > 0 && job.ExpiresAt <= time.Now().Unix() {
		respondError(c, http.StatusGone, "export_expired", "导出文件已过期", nil)
		return
	}
	if a.config.ExportDir == "" || job.FileKey == "" {
		respondError(c, http.StatusGone, "export_unavailable", "导出文件不可用", nil)
		return
	}
	path, err := a.exportPath(job.FileKey)
	if err != nil {
		respondError(c, http.StatusNotFound, "not_found", "导出文件不存在", nil)
		return
	}
	if _, err := os.Stat(path); err != nil {
		respondError(c, http.StatusNotFound, "not_found", "导出文件不存在", nil)
		return
	}
	fileHash, _, err := hashExport(path)
	if err != nil || !strings.EqualFold(fileHash, job.FileHash) {
		respondError(c, http.StatusGone, "export_integrity_failed", "导出文件完整性校验失败", nil)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", `attachment; filename="agency-export.csv"`)
	c.File(path)
}
