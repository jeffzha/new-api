package productpolicy

import (
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
)

const (
	CapabilityChat           = "chat"
	CapabilityFiles          = "files"
	CapabilityWebSearch      = "web_search"
	CapabilityTools          = "tools"
	CapabilityConnectors     = "connectors"
	CapabilityOAuth          = "oauth"
	CapabilityScheduledTasks = "scheduled_tasks"
	CapabilitySandbox        = "sandbox"
	CapabilityCatalogModels  = "catalog_models"
	CapabilityCatalogSkills  = "catalog_skills"
	CapabilityCatalogPlugins = "catalog_plugins"
)

var allowedCapabilities = map[string]struct{}{
	CapabilityChat:           {},
	CapabilityFiles:          {},
	CapabilityWebSearch:      {},
	CapabilityTools:          {},
	CapabilityConnectors:     {},
	CapabilityOAuth:          {},
	CapabilityScheduledTasks: {},
	CapabilitySandbox:        {},
	CapabilityCatalogModels:  {},
	CapabilityCatalogSkills:  {},
	CapabilityCatalogPlugins: {},
}

func IsCatalogReadCapability(value string) bool {
	switch value {
	case CapabilityCatalogModels, CapabilityCatalogSkills, CapabilityCatalogPlugins:
		return true
	default:
		return false
	}
}

func IsExecutionCapability(value string) bool {
	switch value {
	case CapabilityTools, CapabilityConnectors, CapabilityOAuth, CapabilityScheduledTasks, CapabilitySandbox:
		return true
	default:
		return false
	}
}

const MaxFileBytes int64 = 1 << 30

// Limits is the complete product-limit contract shared with ADP and the safe
// browser configuration. Keeping it typed prevents arbitrary database keys
// from becoming client-visible policy.
type Limits struct {
	CustomerConcurrency int64 `json:"customer_concurrency"`
	UserConcurrency     int64 `json:"user_concurrency"`
	MaxRuntimeSeconds   int64 `json:"max_runtime_seconds"`
	MaxReasoningRounds  int64 `json:"max_reasoning_rounds"`
	MaxOutputTokens     int64 `json:"max_output_tokens"`
	WebSearchPerTurn    int64 `json:"web_search_per_turn"`
	MaxFileBytes        int64 `json:"max_file_bytes"`
}

func NormalizeCapabilities(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, domain.Invalid("at least one and at most %d capabilities are required", len(allowedCapabilities))
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return nil, domain.Invalid("capability cannot be empty")
		}
		if _, allowed := allowedCapabilities[value]; !allowed {
			return nil, domain.Invalid("unsupported capability %q", value)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil, domain.Invalid("at least one capability is required")
	}
	sort.Strings(normalized)
	return normalized, nil
}

func ValidateLimits(limits Limits) error {
	if limits.CustomerConcurrency <= 0 || limits.CustomerConcurrency > 1000 ||
		limits.UserConcurrency <= 0 || limits.UserConcurrency > 100 ||
		limits.MaxRuntimeSeconds <= 0 || limits.MaxRuntimeSeconds > 86400 ||
		limits.MaxReasoningRounds <= 0 || limits.MaxReasoningRounds > 1000 ||
		limits.MaxOutputTokens <= 0 || limits.MaxOutputTokens > 1_000_000 ||
		limits.WebSearchPerTurn < 0 || limits.WebSearchPerTurn > 1000 ||
		limits.MaxFileBytes < 0 || limits.MaxFileBytes > MaxFileBytes {
		return domain.Invalid("one or more product limits are outside the supported range")
	}
	return nil
}

func IntersectCapabilities(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, capability := range right {
		rightSet[capability] = struct{}{}
	}
	intersection := make([]string, 0, len(left))
	for _, capability := range left {
		if _, ok := rightSet[capability]; ok {
			intersection = append(intersection, capability)
		}
	}
	sort.Strings(intersection)
	return intersection
}

func MinimumLimits(left, right Limits) Limits {
	return Limits{
		CustomerConcurrency: min(left.CustomerConcurrency, right.CustomerConcurrency),
		UserConcurrency:     min(left.UserConcurrency, right.UserConcurrency),
		MaxRuntimeSeconds:   min(left.MaxRuntimeSeconds, right.MaxRuntimeSeconds),
		MaxReasoningRounds:  min(left.MaxReasoningRounds, right.MaxReasoningRounds),
		MaxOutputTokens:     min(left.MaxOutputTokens, right.MaxOutputTokens),
		WebSearchPerTurn:    min(left.WebSearchPerTurn, right.WebSearchPerTurn),
		MaxFileBytes:        min(left.MaxFileBytes, right.MaxFileBytes),
	}
}
