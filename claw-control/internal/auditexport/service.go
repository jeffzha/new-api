package auditexport

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

const MaxExportWindow = 366 * 24 * time.Hour

type Service struct{ db *gorm.DB }

type Query struct {
	CustomerID *uint64
	StartAt    time.Time
	EndAt      time.Time
	AfterID    uint64
	ThroughID  uint64
	Limit      int
}

type Page struct {
	Rows              []model.AdminAudit `json:"rows"`
	NextAfterID       uint64             `json:"next_after_id,omitempty"`
	SnapshotThroughID uint64             `json:"snapshot_through_id"`
}

type Result struct {
	ExportID   string
	MediaType  string
	Filename   string
	Body       []byte
	NextCursor uint64
	ThroughID  uint64
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Export(query Query, format, actor, requestID string) (*Result, error) {
	query.StartAt = query.StartAt.UTC()
	query.EndAt = query.EndAt.UTC()
	format = strings.ToLower(strings.TrimSpace(format))
	if query.StartAt.IsZero() || query.EndAt.IsZero() || !query.EndAt.After(query.StartAt) || query.EndAt.Sub(query.StartAt) > MaxExportWindow {
		return nil, domain.Invalid("start and end must define a positive UTC range no longer than 366 days")
	}
	if query.CustomerID != nil && *query.CustomerID == 0 {
		return nil, domain.Invalid("customer_id must be positive")
	}
	if query.Limit <= 0 || query.Limit > 1000 {
		return nil, domain.Invalid("limit must be between 1 and 1000")
	}
	if format != "csv" && format != "json" {
		return nil, domain.Invalid("format must be csv or json")
	}
	if query.AfterID > 0 && query.ThroughID == 0 {
		return nil, domain.Invalid("through_id from the first page is required when after_id is set")
	}

	if query.ThroughID == 0 {
		maxQuery := s.db.Model(&model.AdminAudit{}).Where("created_at >= ? AND created_at < ?", query.StartAt, query.EndAt)
		if query.CustomerID != nil {
			maxQuery = maxQuery.Where("customer_id = ?", *query.CustomerID)
		}
		if err := maxQuery.Select("COALESCE(MAX(id), 0)").Scan(&query.ThroughID).Error; err != nil {
			return nil, err
		}
	}
	db := s.db.Where("created_at >= ? AND created_at < ? AND id > ? AND id <= ?", query.StartAt, query.EndAt, query.AfterID, query.ThroughID).
		Order("id asc").Limit(query.Limit + 1)
	if query.CustomerID != nil {
		db = db.Where("customer_id = ?", *query.CustomerID)
	}
	var rows []model.AdminAudit
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	nextCursor := uint64(0)
	if len(rows) > query.Limit {
		rows = rows[:query.Limit]
		nextCursor = rows[len(rows)-1].ID
	}

	result := &Result{ExportID: support.PublicID("aex"), NextCursor: nextCursor, ThroughID: query.ThroughID}
	if format == "json" {
		body, err := jsonx.Marshal(Page{Rows: rows, NextAfterID: nextCursor, SnapshotThroughID: query.ThroughID})
		if err != nil {
			return nil, err
		}
		result.MediaType = "application/json; charset=utf-8"
		result.Filename = "workbench-audits.json"
		result.Body = body
	} else {
		var body bytes.Buffer
		writer := csv.NewWriter(&body)
		if err := writer.Write([]string{"id", "customer_id", "actor", "action", "resource_type", "resource_id", "before_hash", "after_hash", "result", "reason", "request_id", "created_at"}); err != nil {
			return nil, err
		}
		for index := range rows {
			row := rows[index]
			customerID := ""
			if row.CustomerID != nil {
				customerID = strconv.FormatUint(*row.CustomerID, 10)
			}
			values := []string{
				strconv.FormatUint(row.ID, 10), customerID, row.Actor, row.Action,
				row.ResourceType, row.ResourceID, row.BeforeHash, row.AfterHash,
				row.Result, row.Reason, row.RequestID, row.CreatedAt.UTC().Format(time.RFC3339Nano),
			}
			for valueIndex := range values {
				values[valueIndex] = spreadsheetSafe(values[valueIndex])
			}
			if err := writer.Write(values); err != nil {
				return nil, err
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return nil, err
		}
		result.MediaType = "text/csv; charset=utf-8"
		result.Filename = "workbench-audits.csv"
		result.Body = body.Bytes()
	}

	scope := map[string]any{
		"export_id": result.ExportID, "format": format, "start_at": query.StartAt,
		"end_at": query.EndAt, "after_id": query.AfterID, "next_after_id": nextCursor,
		"through_id": query.ThroughID, "limit": query.Limit, "row_count": len(rows), "content_hash": support.Hash(result.Body),
	}
	if query.CustomerID != nil {
		scope["customer_id"] = *query.CustomerID
	}
	if err := support.Audit(s.db, query.CustomerID, actor, "admin.audit.export", "admin_audit_export", result.ExportID, nil, scope, "", requestID); err != nil {
		return nil, fmt.Errorf("audit export behavior: %w", err)
	}
	return result, nil
}

func spreadsheetSafe(value string) string {
	trimmed := strings.TrimLeftFunc(value, unicode.IsSpace)
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}
