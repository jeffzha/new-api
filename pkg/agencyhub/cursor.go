package agencyhub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

type agencyCursor struct {
	Version    int    `json:"v"`
	Kind       string `json:"kind"`
	Scope      string `json:"scope"`
	ActorType  string `json:"actor_type"`
	ActorID    int64  `json:"actor_id"`
	AgencyID   int64  `json:"agency_id,omitempty"`
	PositionMS int64  `json:"position_ms,omitempty"`
	PositionID int64  `json:"position_id,omitempty"`
	PositionU  int64  `json:"position_user_id,omitempty"`
}

func cursorScope(c *gin.Context, kind string, actor *Identity) string {
	if c == nil || actor == nil {
		return ""
	}
	query := c.Request.URL.Query()
	query.Del("cursor")
	query.Del("page")
	query.Del("page_size")
	query.Del("limit")
	agencyID := ""
	if actor.AgencyID != nil {
		agencyID = strconv.FormatInt(*actor.AgencyID, 10)
	}
	return kind + "|" + actor.ActorType + "|" + strconv.FormatInt(actor.ActorID, 10) + "|" + agencyID + "|" + query.Encode()
}

func (a *App) encodeCursor(cursor agencyCursor) (string, error) {
	if len(a.cursorSecret) < 32 {
		return "", errors.New("cursor signing key unavailable")
	}
	cursor.Version = 1
	payload, err := common.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, a.cursorSecret)
	_, _ = mac.Write(payload)
	token := append(payload, '.')
	token = append(token, mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(token), nil
}

func (a *App) decodeCursor(raw string, c *gin.Context, kind string, actor *Identity) (agencyCursor, error) {
	if actor == nil || len(a.cursorSecret) < 32 {
		return agencyCursor{}, errors.New("cursor signing key unavailable")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return agencyCursor{}, errors.New("invalid cursor")
	}
	separator := len(decoded) - sha256.Size - 1
	if separator <= 0 || len(decoded)-separator-1 != sha256.Size {
		return agencyCursor{}, errors.New("invalid cursor")
	}
	if decoded[separator] != '.' {
		return agencyCursor{}, errors.New("invalid cursor")
	}
	payload, suppliedMAC := decoded[:separator], decoded[separator+1:]
	mac := hmac.New(sha256.New, a.cursorSecret)
	_, _ = mac.Write(payload)
	if !hmac.Equal(suppliedMAC, mac.Sum(nil)) {
		return agencyCursor{}, errors.New("invalid cursor")
	}
	var cursor agencyCursor
	if err := common.Unmarshal(payload, &cursor); err != nil ||
		cursor.Version != 1 ||
		cursor.Kind != kind ||
		cursor.ActorType != actor.ActorType ||
		cursor.ActorID != actor.ActorID ||
		cursor.Scope != cursorScope(c, kind, actor) {
		return agencyCursor{}, errors.New("invalid cursor")
	}
	if actor.AgencyID == nil {
		if cursor.AgencyID != 0 {
			return agencyCursor{}, errors.New("invalid cursor")
		}
	} else if cursor.AgencyID != *actor.AgencyID {
		return agencyCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func cursorPageSize(c *gin.Context) (int, error) {
	raw := strings.TrimSpace(c.Query("page_size"))
	if raw == "" {
		raw = strings.TrimSpace(c.Query("limit"))
	}
	if raw == "" {
		return 50, nil
	}
	size, err := strconv.Atoi(raw)
	if err != nil || size < 1 || size > 200 {
		return 0, errors.New("page_size must be between 1 and 200")
	}
	return size, nil
}

func hasCursorPagingConflict(c *gin.Context) bool {
	return strings.TrimSpace(c.Query("cursor")) != "" &&
		strings.TrimSpace(c.Query("page")) != ""
}
