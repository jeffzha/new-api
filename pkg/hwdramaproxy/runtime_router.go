package hwdramaproxy

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type RuntimeRouter struct {
	version string
	actions []compiledAction
	routes  map[runtimeRouteKey]compiledRoute
}

type compiledAction struct {
	key      string
	config   ActionConfig
	template pathTemplate
}

type compiledRoute struct {
	name               string
	channelID          int
	upstreamBaseURL    *url.URL
	upstreamAuthType   string
	upstreamAPIKey     string
	upstreamAccessKey  string
	upstreamSecretKey  string
	upstreamAuthHeader string
	upstreamAuthPrefix string
	assetNamespaceID   string
	assetScopeID       string
	actions            map[string]compiledRouteAction
}

type compiledRouteAction struct {
	method string
	path   string
}

type runtimeRouteKey struct {
	apiKeyID int
	model    string
	action   string
}

type ActionMatch struct {
	Key    string
	Config ActionConfig
	Params map[string]string
}

type RouteMatch struct {
	RouteName          string
	ChannelID          int
	UpstreamBaseURL    *url.URL
	UpstreamAuthType   string
	UpstreamAPIKey     string
	UpstreamAccessKey  string
	UpstreamSecretKey  string
	UpstreamMethod     string
	UpstreamPath       string
	AssetNamespaceID   string
	AssetScopeID       string
	UpstreamAuthHeader string
	UpstreamAuthPrefix string
}

func BuildRuntimeRouter(config *RoutesConfig, secretLookup func(string) string) (*RuntimeRouter, error) {
	if secretLookup == nil {
		secretLookup = func(string) string { return "" }
	}
	if err := config.Validate(secretLookup); err != nil {
		return nil, err
	}

	router := &RuntimeRouter{
		version: routesConfigVersion(config),
		actions: make([]compiledAction, 0, len(config.Actions)),
		routes:  make(map[runtimeRouteKey]compiledRoute),
	}

	actionKeys := make([]string, 0, len(config.Actions))
	for key := range config.Actions {
		actionKeys = append(actionKeys, key)
	}
	sortStrings(actionKeys)
	for _, actionKey := range actionKeys {
		action := config.Actions[actionKey]
		template, err := parsePathTemplate(action.DownstreamPath)
		if err != nil {
			return nil, fmt.Errorf("compile action %s downstream path: %w", actionKey, err)
		}
		router.actions = append(router.actions, compiledAction{
			key:      actionKey,
			config:   action,
			template: template,
		})
	}

	for _, route := range config.Routes {
		baseURL, err := parseBaseURL(route.UpstreamBaseURL)
		if err != nil {
			return nil, fmt.Errorf("route %s upstream_base_url: %w", route.Name, err)
		}
		var upstreamAPIKey string
		var upstreamAccessKey string
		var upstreamSecretKey string
		switch route.UpstreamAuthType {
		case UpstreamAuthTypeHeader:
			upstreamAPIKey = strings.TrimSpace(secretLookup(route.UpstreamAPIKeyEnv))
			if upstreamAPIKey == "" {
				return nil, fmt.Errorf("route %s upstream api key env %s is empty", route.Name, route.UpstreamAPIKeyEnv)
			}
		case UpstreamAuthTypeMobileCloudAKSK:
			upstreamAccessKey = strings.TrimSpace(secretLookup(route.UpstreamAccessKeyEnv))
			upstreamSecretKey = strings.TrimSpace(secretLookup(route.UpstreamSecretKeyEnv))
			if upstreamAccessKey == "" || upstreamSecretKey == "" {
				return nil, fmt.Errorf("route %s mobile cloud AK/SK credentials are empty", route.Name)
			}
		}
		routeActions := make(map[string]compiledRouteAction, len(route.EnabledActions))
		for _, actionKey := range route.EnabledActions {
			action := config.Actions[actionKey]
			method := action.DefaultUpstreamMethod
			path := action.DefaultUpstreamPath
			if override, ok := route.UpstreamActionOverrides[actionKey]; ok {
				if override.UpstreamMethod != "" {
					method = override.UpstreamMethod
				}
				if override.UpstreamPath != "" {
					path = override.UpstreamPath
				}
			}
			routeActions[actionKey] = compiledRouteAction{
				method: method,
				path:   path,
			}
		}
		compiled := compiledRoute{
			name:               route.Name,
			channelID:          route.ChannelID,
			upstreamBaseURL:    baseURL,
			upstreamAuthType:   route.UpstreamAuthType,
			upstreamAPIKey:     upstreamAPIKey,
			upstreamAccessKey:  upstreamAccessKey,
			upstreamSecretKey:  upstreamSecretKey,
			upstreamAuthHeader: route.UpstreamAuthHeader,
			upstreamAuthPrefix: route.UpstreamAuthPrefix,
			assetNamespaceID:   route.AssetNamespaceID,
			assetScopeID:       route.AssetScopeID,
			actions:            routeActions,
		}
		for _, apiKeyID := range routeAPIKeyIDs(route) {
			for _, model := range route.Models {
				for _, actionKey := range route.EnabledActions {
					key := runtimeRouteKey{apiKeyID: apiKeyID, model: model, action: actionKey}
					router.routes[key] = compiled
				}
			}
		}
	}

	return router, nil
}

func (router *RuntimeRouter) Version() string {
	if router == nil {
		return ""
	}
	return router.version
}

func (router *RuntimeRouter) LookupAction(method string, path string) (ActionMatch, RouteDecision) {
	if router == nil {
		return ActionMatch{}, RouteDecision{}
	}
	method = normalizeMethod(method)
	pathKnown := false
	for _, action := range router.actions {
		params, ok := action.template.Match(path)
		if !ok {
			continue
		}
		pathKnown = true
		if action.config.DownstreamMethod != method {
			continue
		}
		return ActionMatch{
				Key:    action.key,
				Config: action.config,
				Params: params,
			}, RouteDecision{
				PathKnown:     true,
				MethodAllowed: true,
			}
	}
	return ActionMatch{}, RouteDecision{
		PathKnown:     pathKnown,
		MethodAllowed: false,
	}
}

func (router *RuntimeRouter) Match(apiKeyID int, model string, action ActionMatch) (RouteMatch, bool, error) {
	if router == nil {
		return RouteMatch{}, false, errors.New("runtime router is not loaded")
	}
	model = strings.TrimSpace(model)
	keys := []runtimeRouteKey{
		{apiKeyID: apiKeyID, model: model, action: action.Key},
		{apiKeyID: apiKeyID, model: WildcardModel, action: action.Key},
		{apiKeyID: 0, model: model, action: action.Key},
		{apiKeyID: 0, model: WildcardModel, action: action.Key},
	}
	if model == "" {
		keys = []runtimeRouteKey{
			{apiKeyID: apiKeyID, model: WildcardModel, action: action.Key},
			{apiKeyID: 0, model: WildcardModel, action: action.Key},
		}
	}
	var matched *compiledRoute
	for _, key := range keys {
		if route, ok := router.routes[key]; ok {
			routeCopy := route
			matched = &routeCopy
			break
		}
	}
	if matched == nil {
		return RouteMatch{}, false, nil
	}
	routeAction, ok := matched.actions[action.Key]
	if !ok {
		return RouteMatch{}, false, fmt.Errorf("route %s does not enable action %s", matched.name, action.Key)
	}
	upstreamPath, err := fillPathTemplate(routeAction.path, action.Params)
	if err != nil {
		return RouteMatch{}, false, fmt.Errorf("route %s upstream path: %w", matched.name, err)
	}
	baseURL := *matched.upstreamBaseURL
	return RouteMatch{
		RouteName:          matched.name,
		ChannelID:          matched.channelID,
		UpstreamBaseURL:    &baseURL,
		UpstreamAuthType:   matched.upstreamAuthType,
		UpstreamAPIKey:     matched.upstreamAPIKey,
		UpstreamAccessKey:  matched.upstreamAccessKey,
		UpstreamSecretKey:  matched.upstreamSecretKey,
		UpstreamMethod:     routeAction.method,
		UpstreamPath:       upstreamPath,
		AssetNamespaceID:   matched.assetNamespaceID,
		AssetScopeID:       matched.assetScopeID,
		UpstreamAuthHeader: matched.upstreamAuthHeader,
		UpstreamAuthPrefix: matched.upstreamAuthPrefix,
	}, true, nil
}

type pathTemplate struct {
	raw      string
	segments []pathSegment
}

type pathSegment struct {
	literal string
	name    string
}

func parsePathTemplate(path string) (pathTemplate, error) {
	segments := splitPath(path)
	template := pathTemplate{
		raw:      path,
		segments: make([]pathSegment, 0, len(segments)),
	}
	seenVars := map[string]bool{}
	for _, segment := range segments {
		if isTemplateVar(segment) {
			name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
			if name == "" {
				return pathTemplate{}, errors.New("template variable name cannot be empty")
			}
			if strings.ContainsAny(name, "/{}") {
				return pathTemplate{}, fmt.Errorf("invalid template variable %q", segment)
			}
			if seenVars[name] {
				return pathTemplate{}, fmt.Errorf("template variable %q is duplicated", name)
			}
			seenVars[name] = true
			template.segments = append(template.segments, pathSegment{name: name})
			continue
		}
		if strings.Contains(segment, "{") || strings.Contains(segment, "}") {
			return pathTemplate{}, fmt.Errorf("invalid partial template segment %q", segment)
		}
		template.segments = append(template.segments, pathSegment{literal: segment})
	}
	return template, nil
}

func (template pathTemplate) Match(path string) (map[string]string, bool) {
	segments := splitPath(path)
	if len(segments) != len(template.segments) {
		return nil, false
	}
	params := map[string]string{}
	for i, segment := range template.segments {
		if segment.name != "" {
			if segments[i] == "" {
				return nil, false
			}
			params[segment.name] = segments[i]
			continue
		}
		if segment.literal != segments[i] {
			return nil, false
		}
	}
	return params, true
}

func (template pathTemplate) hasVariable(name string) bool {
	for _, segment := range template.segments {
		if segment.name == name {
			return true
		}
	}
	return false
}

func fillPathTemplate(path string, params map[string]string) (string, error) {
	template, err := parsePathTemplate(path)
	if err != nil {
		return "", err
	}
	segments := make([]string, 0, len(template.segments))
	for _, segment := range template.segments {
		if segment.name == "" {
			segments = append(segments, segment.literal)
			continue
		}
		value, ok := params[segment.name]
		if !ok {
			return "", fmt.Errorf("missing path parameter %s", segment.name)
		}
		segments = append(segments, url.PathEscape(value))
	}
	return "/" + strings.Join(segments, "/"), nil
}

func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return []string{}
	}
	return strings.Split(path, "/")
}

func isTemplateVar(segment string) bool {
	return strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") && len(segment) >= 3
}

func routesConfigVersion(config *RoutesConfig) string {
	parts := []string{fmt.Sprintf("v%d", config.Version)}
	actionKeys := make([]string, 0, len(config.Actions))
	for key := range config.Actions {
		actionKeys = append(actionKeys, key)
	}
	sortStrings(actionKeys)
	parts = append(parts, actionKeys...)
	for _, route := range config.Routes {
		parts = append(parts, route.Name)
	}
	return strings.Join(parts, ":")
}

func sortStrings(values []string) {
	if len(values) < 2 {
		return
	}
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
