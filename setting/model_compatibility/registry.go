package model_compatibility

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	OptionKey             = "model_compatibility.registry"
	CurrentSchemaVersion  = 1
	maxRegistryVersions   = 20
	maxProfilesPerVersion = 256
	maxRulesPerProfile    = 256
)

type Registry struct {
	SchemaVersion int       `json:"schema_version"`
	Enabled       bool      `json:"enabled"`
	ActiveVersion string    `json:"active_version"`
	Versions      []Version `json:"versions"`
}

type Version struct {
	ID          string    `json:"id"`
	Description string    `json:"description,omitempty"`
	Source      string    `json:"source,omitempty"`
	Profiles    []Profile `json:"profiles"`
}

type Profile struct {
	ID           string   `json:"id"`
	Disabled     bool     `json:"disabled,omitempty"`
	ChannelTypes []int    `json:"channel_types"`
	ChannelIDs   []int    `json:"channel_ids,omitempty"`
	Groups       []string `json:"groups,omitempty"`
	ModelRegex   string   `json:"model_regex"`
	RelayFormats []string `json:"relay_formats"`
	Rules        []Rule   `json:"rules"`
}

type Rule struct {
	Path       string         `json:"path"`
	Action     string         `json:"action"`
	To         string         `json:"to,omitempty"`
	Value      any            `json:"value,omitempty"`
	Values     map[string]any `json:"values,omitempty"`
	Conditions []Condition    `json:"conditions,omitempty"`
	Message    string         `json:"message,omitempty"`
}

type Condition struct {
	Path     string `json:"path"`
	Operator string `json:"operator"`
	Value    any    `json:"value,omitempty"`
}

type RequestContext struct {
	ChannelType int
	ChannelID   int
	Group       string
	Model       string
	RelayFormat string
}

type Change struct {
	ProfileID string
	Action    string
	Path      string
	Detail    string
}

type RequestError struct {
	ProfileID string
	Path      string
	Message   string
}

func (e *RequestError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "request rejected by model compatibility policy"
	}
	return e.Message
}

type compiledProfile struct {
	profile Profile
	model   *regexp.Regexp
}

type compiledRegistry struct {
	registry Registry
	version  Version
	profiles []compiledProfile
}

type registeredSettings struct {
	Registry Registry `json:"registry"`
}

var defaultRegistry = Registry{
	SchemaVersion: CurrentSchemaVersion,
	Enabled:       true,
	ActiveVersion: "builtin-2026-08-21",
	Versions: []Version{
		{
			ID:          "builtin-2026-08-21",
			Description: "Safe compatibility mappings verified against provider documentation",
			Source:      "builtin",
			Profiles: []Profile{
				{
					ID:           "deepseek-v4-openai",
					ChannelTypes: []int{43},
					ModelRegex:   `^deepseek-v4-(flash|pro)$`,
					RelayFormats: []string{"openai", "openai_responses"},
					Rules: []Rule{
						{
							Path:   "reasoning_effort",
							Action: "map",
							Values: map[string]any{"low": "high", "medium": "high", "xhigh": "max"},
						},
						{
							Path:   "reasoning.effort",
							Action: "map",
							Values: map[string]any{"low": "high", "medium": "high", "xhigh": "max"},
						},
					},
				},
				{
					ID:           "deepseek-v4-anthropic",
					ChannelTypes: []int{43},
					ModelRegex:   `^deepseek-v4-(flash|pro)$`,
					RelayFormats: []string{"claude"},
					Rules: []Rule{
						{
							Path:   "output_config.effort",
							Action: "map",
							Values: map[string]any{"low": "high", "medium": "high", "xhigh": "max"},
						},
					},
				},
			},
		},
	},
}

var registered = registeredSettings{Registry: defaultRegistry}

var registryCache = struct {
	sync.RWMutex
	raw      string
	compiled *compiledRegistry
	err      error
}{}

func init() {
	config.GlobalConfig.Register("model_compatibility", &registered)
}

func DefaultRegistryJSON() string {
	data, err := common.Marshal(defaultRegistry)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func ValidateRegistryJSON(raw string) error {
	_, err := compileRegistryJSON(raw)
	return err
}

func Normalize(raw []byte, ctx RequestContext) ([]byte, string, []Change, error) {
	compiled, err := activeRegistry()
	if err != nil {
		return nil, "", nil, err
	}
	if compiled == nil || !compiled.registry.Enabled {
		return raw, "", nil, nil
	}

	working := raw
	changes := make([]Change, 0)
	for _, candidate := range compiled.profiles {
		profile := candidate.profile
		if profile.Disabled || !profileMatches(candidate, ctx) {
			continue
		}
		for _, rule := range profile.Rules {
			if !conditionsMatch(working, rule.Conditions) {
				continue
			}
			result := gjson.GetBytes(working, rule.Path)
			switch rule.Action {
			case "drop":
				if !result.Exists() {
					continue
				}
				next, deleteErr := sjson.DeleteBytes(working, rule.Path)
				if deleteErr != nil {
					return nil, compiled.version.ID, changes, deleteErr
				}
				working = next
				changes = append(changes, Change{ProfileID: profile.ID, Action: rule.Action, Path: rule.Path})
			case "inject_if_absent":
				if result.Exists() {
					continue
				}
				next, setErr := setJSONValue(working, rule.Path, rule.Value)
				if setErr != nil {
					return nil, compiled.version.ID, changes, setErr
				}
				working = next
				changes = append(changes, Change{ProfileID: profile.ID, Action: rule.Action, Path: rule.Path})
			case "map":
				if !result.Exists() {
					continue
				}
				mapped, ok := rule.Values[scalarKey(result)]
				if !ok {
					continue
				}
				next, setErr := setJSONValue(working, rule.Path, mapped)
				if setErr != nil {
					return nil, compiled.version.ID, changes, setErr
				}
				working = next
				changes = append(changes, Change{
					ProfileID: profile.ID,
					Action:    rule.Action,
					Path:      rule.Path,
					Detail:    scalarKey(result) + "->" + fmt.Sprint(mapped),
				})
			case "rename":
				if !result.Exists() || gjson.GetBytes(working, rule.To).Exists() {
					continue
				}
				next, setErr := sjson.SetRawBytes(working, rule.To, []byte(result.Raw))
				if setErr != nil {
					return nil, compiled.version.ID, changes, setErr
				}
				next, deleteErr := sjson.DeleteBytes(next, rule.Path)
				if deleteErr != nil {
					return nil, compiled.version.ID, changes, deleteErr
				}
				working = next
				changes = append(changes, Change{ProfileID: profile.ID, Action: rule.Action, Path: rule.Path, Detail: rule.To})
			case "reject":
				if !result.Exists() {
					continue
				}
				message := strings.TrimSpace(rule.Message)
				if message == "" {
					message = fmt.Sprintf("parameter %s is not supported by model %s", rule.Path, ctx.Model)
				}
				return nil, compiled.version.ID, changes, &RequestError{ProfileID: profile.ID, Path: rule.Path, Message: message}
			}
		}
	}

	return working, compiled.version.ID, changes, nil
}

func activeRegistry() (*compiledRegistry, error) {
	raw := currentRegistryJSON()
	registryCache.RLock()
	if registryCache.raw == raw {
		compiled, err := registryCache.compiled, registryCache.err
		registryCache.RUnlock()
		return compiled, err
	}
	registryCache.RUnlock()

	registryCache.Lock()
	defer registryCache.Unlock()
	if registryCache.raw == raw {
		return registryCache.compiled, registryCache.err
	}
	compiled, err := compileRegistryJSON(raw)
	registryCache.raw = raw
	registryCache.compiled = compiled
	registryCache.err = err
	return compiled, err
}

func currentRegistryJSON() string {
	common.OptionMapRWMutex.RLock()
	raw := strings.TrimSpace(common.OptionMap[OptionKey])
	common.OptionMapRWMutex.RUnlock()
	if raw == "" {
		return DefaultRegistryJSON()
	}
	return raw
}

func compileRegistryJSON(raw string) (*compiledRegistry, error) {
	var registry Registry
	if err := common.UnmarshalJsonStr(raw, &registry); err != nil {
		return nil, fmt.Errorf("model compatibility registry must be valid JSON: %w", err)
	}
	if err := validateRegistry(registry); err != nil {
		return nil, err
	}
	for _, version := range registry.Versions {
		if version.ID != registry.ActiveVersion {
			continue
		}
		compiled := &compiledRegistry{registry: registry, version: version}
		compiled.profiles = make([]compiledProfile, 0, len(version.Profiles))
		for _, profile := range version.Profiles {
			modelPattern, _ := regexp.Compile(profile.ModelRegex)
			compiled.profiles = append(compiled.profiles, compiledProfile{profile: profile, model: modelPattern})
		}
		return compiled, nil
	}
	return nil, fmt.Errorf("active model compatibility version %q does not exist", registry.ActiveVersion)
}

func validateRegistry(registry Registry) error {
	if registry.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported model compatibility schema_version %d", registry.SchemaVersion)
	}
	if len(registry.Versions) == 0 || len(registry.Versions) > maxRegistryVersions {
		return fmt.Errorf("model compatibility versions must contain between 1 and %d entries", maxRegistryVersions)
	}
	activeFound := false
	versionIDs := make(map[string]struct{}, len(registry.Versions))
	for versionIndex, version := range registry.Versions {
		if err := validateIdentifier(version.ID, "version id"); err != nil {
			return fmt.Errorf("versions[%d]: %w", versionIndex, err)
		}
		if _, exists := versionIDs[version.ID]; exists {
			return fmt.Errorf("duplicate model compatibility version id %q", version.ID)
		}
		versionIDs[version.ID] = struct{}{}
		activeFound = activeFound || version.ID == registry.ActiveVersion
		if len(version.Profiles) > maxProfilesPerVersion {
			return fmt.Errorf("versions[%d] has more than %d profiles", versionIndex, maxProfilesPerVersion)
		}
		profileIDs := make(map[string]struct{}, len(version.Profiles))
		for profileIndex, profile := range version.Profiles {
			if err := validateProfile(profile); err != nil {
				return fmt.Errorf("versions[%d].profiles[%d]: %w", versionIndex, profileIndex, err)
			}
			if _, exists := profileIDs[profile.ID]; exists {
				return fmt.Errorf("duplicate profile id %q in version %q", profile.ID, version.ID)
			}
			profileIDs[profile.ID] = struct{}{}
		}
	}
	if !activeFound {
		return fmt.Errorf("active model compatibility version %q does not exist", registry.ActiveVersion)
	}
	return nil
}

func validateProfile(profile Profile) error {
	if err := validateIdentifier(profile.ID, "profile id"); err != nil {
		return err
	}
	if len(profile.ChannelTypes) == 0 {
		return fmt.Errorf("channel_types must not be empty")
	}
	for _, channelType := range profile.ChannelTypes {
		if channelType <= 0 {
			return fmt.Errorf("channel_types contains invalid value %d", channelType)
		}
	}
	for _, channelID := range profile.ChannelIDs {
		if channelID <= 0 {
			return fmt.Errorf("channel_ids contains invalid value %d", channelID)
		}
	}
	for _, group := range profile.Groups {
		if strings.TrimSpace(group) == "" || len(group) > 64 {
			return fmt.Errorf("groups contains an invalid value")
		}
	}
	if strings.TrimSpace(profile.ModelRegex) == "" || len(profile.ModelRegex) > 512 {
		return fmt.Errorf("model_regex must contain between 1 and 512 characters")
	}
	if _, err := regexp.Compile(profile.ModelRegex); err != nil {
		return fmt.Errorf("invalid model_regex: %w", err)
	}
	if len(profile.RelayFormats) == 0 {
		return fmt.Errorf("relay_formats must not be empty")
	}
	for _, relayFormat := range profile.RelayFormats {
		switch relayFormat {
		case "openai", "openai_responses", "claude":
		default:
			return fmt.Errorf("unsupported relay format %q", relayFormat)
		}
	}
	if len(profile.Rules) > maxRulesPerProfile {
		return fmt.Errorf("profile has more than %d rules", maxRulesPerProfile)
	}
	for index, rule := range profile.Rules {
		if err := validateRule(rule); err != nil {
			return fmt.Errorf("rules[%d]: %w", index, err)
		}
	}
	return nil
}

func validateRule(rule Rule) error {
	if err := validateParameterPath(rule.Path); err != nil {
		return err
	}
	switch rule.Action {
	case "drop", "inject_if_absent", "map", "rename", "reject":
	default:
		return fmt.Errorf("unsupported action %q", rule.Action)
	}
	if rule.Action == "map" && len(rule.Values) == 0 {
		return fmt.Errorf("map action requires values")
	}
	if rule.Action == "rename" {
		if err := validateParameterPath(rule.To); err != nil {
			return fmt.Errorf("invalid rename destination: %w", err)
		}
		if rule.To == rule.Path {
			return fmt.Errorf("rename destination must differ from source")
		}
	}
	if len(rule.Conditions) > 8 {
		return fmt.Errorf("a rule may contain at most 8 conditions")
	}
	for _, condition := range rule.Conditions {
		if err := validateParameterPath(condition.Path); err != nil {
			return fmt.Errorf("invalid condition: %w", err)
		}
		switch condition.Operator {
		case "equals", "not_equals", "exists", "not_exists":
		default:
			return fmt.Errorf("unsupported condition operator %q", condition.Operator)
		}
	}
	return nil
}

func validateParameterPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || len(path) > 256 {
		return fmt.Errorf("parameter path must contain between 1 and 256 characters")
	}
	root := strings.SplitN(path, ".", 2)[0]
	switch root {
	case "model", "messages", "input", "instructions", "system", "tools", "prompt", "metadata", "user", "stream", "cache_control":
		return fmt.Errorf("parameter path %q is protected", path)
	}
	return nil
}

func validateIdentifier(value string, label string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 96 {
		return fmt.Errorf("%s must contain between 1 and 96 characters", label)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("%s contains unsupported character %q", label, r)
	}
	return nil
}

func profileMatches(candidate compiledProfile, ctx RequestContext) bool {
	channelMatched := false
	for _, channelType := range candidate.profile.ChannelTypes {
		if channelType == ctx.ChannelType {
			channelMatched = true
			break
		}
	}
	if !channelMatched || !intScopeMatches(candidate.profile.ChannelIDs, ctx.ChannelID) ||
		!stringScopeMatches(candidate.profile.Groups, ctx.Group) || candidate.model == nil ||
		!candidate.model.MatchString(ctx.Model) {
		return false
	}
	for _, relayFormat := range candidate.profile.RelayFormats {
		if relayFormat == ctx.RelayFormat {
			return true
		}
	}
	return false
}

func intScopeMatches(values []int, actual int) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if value == actual {
			return true
		}
	}
	return false
}

func stringScopeMatches(values []string, actual string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if value == actual {
			return true
		}
	}
	return false
}

func conditionsMatch(raw []byte, conditions []Condition) bool {
	for _, condition := range conditions {
		result := gjson.GetBytes(raw, condition.Path)
		switch condition.Operator {
		case "exists":
			if !result.Exists() {
				return false
			}
		case "not_exists":
			if result.Exists() {
				return false
			}
		case "equals":
			if !result.Exists() || scalarKey(result) != fmt.Sprint(condition.Value) {
				return false
			}
		case "not_equals":
			if result.Exists() && scalarKey(result) == fmt.Sprint(condition.Value) {
				return false
			}
		}
	}
	return true
}

func scalarKey(result gjson.Result) string {
	switch result.Type {
	case gjson.String:
		return result.String()
	case gjson.True:
		return "true"
	case gjson.False:
		return "false"
	case gjson.Number:
		return result.Raw
	default:
		return result.Raw
	}
}

func setJSONValue(raw []byte, path string, value any) ([]byte, error) {
	encoded, err := common.Marshal(value)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(raw, path, encoded)
}
