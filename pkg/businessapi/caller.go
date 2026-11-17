// Package businessapi defines metadata shared by business API ingress and usage records.
package businessapi

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const (
	SourcesOption = "BusinessAPITokenSources"
	CallerKey     = "business_api_caller_context"
)

type Caller struct {
	SourceSystem string `json:"source_system,omitempty"`
	TenantID     string `json:"tenant_id,omitempty"`
	TenantName   string `json:"tenant_name,omitempty"`
	CallerID     string `json:"caller_id,omitempty"`
	CallerName   string `json:"caller_name,omitempty"`
}

// Token sources are indexed by authenticated token ID, never by the usable secret.
func ParseSources(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var sources map[string]string
	if err := common.UnmarshalJsonStr(raw, &sources); err != nil {
		return nil, errors.New("business API token sources must be a JSON object")
	}
	for id, source := range sources {
		n, err := strconv.Atoi(id)
		if err != nil || n <= 0 || strconv.Itoa(n) != id || !ValidText(source, 128) || strings.TrimSpace(source) == "" || source != strings.TrimSpace(source) {
			return nil, errors.New("business API token sources require positive token IDs and nonempty system codes")
		}
	}
	return sources, nil
}

func SourceForToken(id int) (string, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[SourcesOption]
	common.OptionMapRWMutex.RUnlock()
	sources, err := ParseSources(raw)
	return sources[strconv.Itoa(id)], err
}

func ValidText(value string, limit int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func FromContext(c *gin.Context) (Caller, bool) {
	value, exists := c.Get(CallerKey)
	caller, ok := value.(Caller)
	return caller, exists && ok && caller != (Caller{})
}
