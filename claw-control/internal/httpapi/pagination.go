package httpapi

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/agentstore"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/pagination"
)

type adminListCursor struct {
	Version        int    `json:"v"`
	Scope          string `json:"scope"`
	BeforeID       uint64 `json:"before_id,omitempty"`
	SortOrder      *int   `json:"sort_order,omitempty"`
	ItemID         string `json:"item_id,omitempty"`
	AdminBeforeID  uint64 `json:"admin_before_id,omitempty"`
	AdminDone      bool   `json:"admin_done,omitempty"`
	LaunchBefore   string `json:"launch_before,omitempty"`
	LaunchBeforeID string `json:"launch_before_id,omitempty"`
	LaunchDone     bool   `json:"launch_done,omitempty"`
}

func adminAgentStorePageRequest(w http.ResponseWriter, r *http.Request, scope string) (*int, string, int, bool) {
	limit := queryLimit(r)
	encoded := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if encoded == "" {
		return nil, "", limit, true
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw) > 1024 {
		writeError(w, r, domain.Invalid("cursor is invalid"))
		return nil, "", 0, false
	}
	var cursor adminListCursor
	if err := jsonx.Decode(bytes.NewReader(raw), &cursor); err != nil || cursor.Version != 1 || cursor.Scope != scope ||
		cursor.BeforeID != 0 || cursor.SortOrder == nil || strings.TrimSpace(cursor.ItemID) == "" || cursor.AdminBeforeID != 0 || cursor.AdminDone ||
		cursor.LaunchBefore != "" || cursor.LaunchBeforeID != "" || cursor.LaunchDone {
		writeError(w, r, domain.Invalid("cursor is invalid for this list"))
		return nil, "", 0, false
	}
	return cursor.SortOrder, cursor.ItemID, limit, true
}

func adminAgentStoreAuditPageRequest(w http.ResponseWriter, r *http.Request, scope string) (agentstore.AuditPosition, int, bool) {
	limit := queryLimit(r)
	encoded := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if encoded == "" {
		return agentstore.AuditPosition{}, limit, true
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw) > 2048 {
		writeError(w, r, domain.Invalid("cursor is invalid"))
		return agentstore.AuditPosition{}, 0, false
	}
	var cursor adminListCursor
	if err := jsonx.Decode(bytes.NewReader(raw), &cursor); err != nil || cursor.Version != 1 || cursor.Scope != scope || cursor.BeforeID != 0 ||
		cursor.SortOrder != nil || cursor.ItemID != "" || (!cursor.AdminDone && cursor.AdminBeforeID == 0) ||
		(!cursor.LaunchDone && (cursor.LaunchBefore == "" || cursor.LaunchBeforeID == "")) {
		writeError(w, r, domain.Invalid("cursor is invalid for this list"))
		return agentstore.AuditPosition{}, 0, false
	}
	position := agentstore.AuditPosition{
		AdminBeforeID: cursor.AdminBeforeID, AdminDone: cursor.AdminDone,
		LaunchBeforeID: cursor.LaunchBeforeID, LaunchDone: cursor.LaunchDone,
	}
	if !cursor.LaunchDone {
		parsed, err := time.Parse(time.RFC3339Nano, cursor.LaunchBefore)
		if err != nil {
			writeError(w, r, domain.Invalid("cursor is invalid for this list"))
			return agentstore.AuditPosition{}, 0, false
		}
		parsed = parsed.UTC()
		position.LaunchBefore = &parsed
	}
	return position, limit, true
}

func adminPageRequest(w http.ResponseWriter, r *http.Request, scope string) (uint64, int, bool) {
	limit := queryLimit(r)
	encoded := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if encoded == "" {
		beforeID, ok := queryUint64(w, r, "before_id", false)
		return beforeID, limit, ok
	}
	if strings.TrimSpace(r.URL.Query().Get("before_id")) != "" {
		writeError(w, r, domain.Invalid("cursor and before_id cannot be combined"))
		return 0, 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw) > 1024 {
		writeError(w, r, domain.Invalid("cursor is invalid"))
		return 0, 0, false
	}
	var cursor adminListCursor
	if err := jsonx.Decode(bytes.NewReader(raw), &cursor); err != nil || cursor.Version != 1 || cursor.Scope != scope || cursor.BeforeID == 0 || cursor.SortOrder != nil || cursor.ItemID != "" ||
		cursor.AdminBeforeID != 0 || cursor.AdminDone || cursor.LaunchBefore != "" || cursor.LaunchBeforeID != "" || cursor.LaunchDone {
		writeError(w, r, domain.Invalid("cursor is invalid for this list"))
		return 0, 0, false
	}
	return cursor.BeforeID, limit, true
}

func adminAgentStoreNextCursor(scope string, sortOrder int, itemID string) string {
	if strings.TrimSpace(itemID) == "" {
		return ""
	}
	raw, err := jsonx.Marshal(adminListCursor{Version: 1, Scope: scope, SortOrder: &sortOrder, ItemID: itemID})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func adminAgentStoreAuditNextCursor(scope string, position *agentstore.AuditPosition) string {
	if position == nil {
		return ""
	}
	cursor := adminListCursor{
		Version: 1, Scope: scope, AdminBeforeID: position.AdminBeforeID, AdminDone: position.AdminDone,
		LaunchBeforeID: position.LaunchBeforeID, LaunchDone: position.LaunchDone,
	}
	if position.LaunchBefore != nil {
		cursor.LaunchBefore = position.LaunchBefore.UTC().Format(time.RFC3339Nano)
	}
	raw, err := jsonx.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func adminNextCursor(scope string, beforeID uint64) string {
	if beforeID == 0 {
		return ""
	}
	raw, err := jsonx.Marshal(adminListCursor{Version: 1, Scope: scope, BeforeID: beforeID})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func writePageResult(w http.ResponseWriter, r *http.Request, data any, scope string, nextBeforeID uint64, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    data,
		"meta": map[string]any{
			"next_cursor": adminNextCursor(scope, nextBeforeID),
		},
	})
}

func queryLimit(r *http.Request) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	if err != nil {
		return pagination.DefaultLimit
	}
	return pagination.Limit(value)
}
