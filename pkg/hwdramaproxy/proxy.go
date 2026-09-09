package hwdramaproxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/tidwall/gjson"
)

type TokenLookup func(key string) (bool, error)
type TokenResolver func(key string) (*TokenIdentity, error)

type TokenIdentity struct {
	ID int
}

type Config struct {
	UpstreamBaseURL  string
	UpstreamAPIKey   string
	RoutesConfigPath string
	SecretsFilePath  string
	Timeout          time.Duration
	TokenLookup      TokenLookup
	TokenResolver    TokenResolver
	Client           *http.Client
}

type Proxy struct {
	upstream         *url.URL
	upstreamAPIKey   string
	client           *http.Client
	tokenResolver    TokenResolver
	routesConfigPath string
	secretsFilePath  string
	runtimeRouter    atomic.Value
}

func New(config Config) (*Proxy, error) {
	routesConfigPath := strings.TrimSpace(config.RoutesConfigPath)
	var upstream *url.URL
	var upstreamAPIKey string
	if routesConfigPath == "" {
		upstreamBaseURL := strings.TrimSpace(config.UpstreamBaseURL)
		if upstreamBaseURL == "" {
			upstreamBaseURL = "https://ai.hwdrama.com"
		}
		parsedUpstream, err := url.Parse(upstreamBaseURL)
		if err != nil {
			return nil, fmt.Errorf("parse upstream base url: %w", err)
		}
		if parsedUpstream.Scheme == "" || parsedUpstream.Host == "" {
			return nil, errors.New("upstream base url must include scheme and host")
		}
		upstream = parsedUpstream
		upstreamAPIKey = strings.TrimSpace(config.UpstreamAPIKey)
		if upstreamAPIKey == "" {
			return nil, errors.New("upstream api key is required")
		}
	}
	tokenResolver := config.TokenResolver
	if tokenResolver == nil {
		tokenResolver = ResolveTokenInDatabase
	}
	if config.TokenLookup != nil {
		tokenLookup := config.TokenLookup
		tokenResolver = func(key string) (*TokenIdentity, error) {
			exists, err := tokenLookup(key)
			if err != nil || !exists {
				return nil, err
			}
			return &TokenIdentity{}, nil
		}
	}
	client := config.Client
	if client == nil {
		timeout := config.Timeout
		if timeout <= 0 {
			timeout = 600 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}

	proxy := &Proxy{
		upstream:         upstream,
		upstreamAPIKey:   upstreamAPIKey,
		client:           client,
		tokenResolver:    tokenResolver,
		routesConfigPath: routesConfigPath,
		secretsFilePath:  strings.TrimSpace(config.SecretsFilePath),
	}
	if proxy.routesConfigPath != "" {
		if err := proxy.ReloadRoutes(); err != nil {
			return nil, err
		}
	}
	return proxy, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.routesConfigPath != "" {
		p.serveDynamic(w, r)
		return
	}
	p.serveLegacy(w, r)
}

func (p *Proxy) serveLegacy(w http.ResponseWriter, r *http.Request) {
	decision := RouteDecisionFor(r.Method, r.URL.Path)
	if !decision.PathKnown {
		writeJSONError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if !decision.MethodAllowed {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	key, ok := NormalizeAPIKey(r.Header.Get("Authorization"))
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "token_not_provided", "missing api key")
		return
	}
	token, err := p.tokenResolver(key)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "database_error", "database error")
		return
	}
	if token == nil {
		writeJSONError(w, http.StatusUnauthorized, "token_invalid", "invalid api key")
		return
	}

	p.proxyRequest(w, r, RouteMatch{
		UpstreamBaseURL:    p.upstream,
		UpstreamAuthType:   UpstreamAuthTypeHeader,
		UpstreamAPIKey:     p.upstreamAPIKey,
		UpstreamMethod:     r.Method,
		UpstreamPath:       r.URL.Path,
		UpstreamAuthHeader: "Authorization",
		UpstreamAuthPrefix: "Bearer",
	}, token.ID, "", "", nil)
}

func (p *Proxy) serveDynamic(w http.ResponseWriter, r *http.Request) {
	router := p.CurrentRouter()
	if router == nil {
		writeJSONError(w, http.StatusInternalServerError, "configuration_error", "routes config is not loaded")
		return
	}
	action, decision := router.LookupAction(r.Method, r.URL.Path)
	if !decision.PathKnown {
		writeJSONError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if !decision.MethodAllowed {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	key, ok := NormalizeAPIKey(r.Header.Get("Authorization"))
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "token_not_provided", "missing api key")
		return
	}
	token, err := p.tokenResolver(key)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "database_error", "database error")
		return
	}
	if token == nil {
		writeJSONError(w, http.StatusUnauthorized, "token_invalid", "invalid api key")
		return
	}

	modelName, body, err := extractRequestModel(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "failed to parse request body")
		return
	}
	route, ok, err := router.Match(token.ID, modelName, action)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "configuration_error", err.Error())
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no_upstream_route", "no upstream route for api key, model, and action")
		return
	}

	affinityPathValue := ""
	if action.Config.AffinityPathParam != "" {
		affinityPathValue = action.Params[action.Config.AffinityPathParam]
	}
	scopePathValue := ""
	if action.Config.ScopePathParam != "" {
		scopePathValue = action.Params[action.Config.ScopePathParam]
	}
	body, scopedRequest, emptyList, err := prepareScopedAssetRequest(
		action.Config.ScopeOperation,
		scopePathValue,
		body,
		route,
		token.ID,
	)
	if err != nil {
		var scopeErr *scopedAssetError
		if errors.As(err, &scopeErr) {
			writeJSONError(w, scopeErr.Status, scopeErr.Code, scopeErr.Message)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "database_error", "failed to apply asset scope")
		return
	}
	if emptyList {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(emptyScopedListResponse())
		return
	}
	if body != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	p.proxyRequest(
		w,
		r,
		route,
		token.ID,
		action.Config.AffinityResponseField,
		affinityPathValue,
		scopedRequest,
	)
}

func (p *Proxy) proxyRequest(
	w http.ResponseWriter,
	r *http.Request,
	route RouteMatch,
	tokenID int,
	affinityResponseField string,
	affinityPathValue string,
	scopedRequest *scopedAssetRequest,
) {
	upstreamURL := *r.URL
	rewriteRequestURL(&upstreamURL, route.UpstreamBaseURL, route.UpstreamPath)
	req, err := http.NewRequestWithContext(r.Context(), route.UpstreamMethod, upstreamURL.String(), r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", "failed to build upstream request")
		return
	}
	req.Header = r.Header.Clone()
	req.ContentLength = r.ContentLength
	removeHopByHopHeaders(req.Header)
	req.Host = route.UpstreamBaseURL.Host
	req.Header.Del("Authorization")
	authHeader := strings.TrimSpace(route.UpstreamAuthHeader)
	if authHeader == "" {
		authHeader = "Authorization"
	}
	req.Header.Del(authHeader)
	upstreamAuthType := strings.TrimSpace(route.UpstreamAuthType)
	if upstreamAuthType == "" {
		upstreamAuthType = UpstreamAuthTypeHeader
	}
	switch upstreamAuthType {
	case UpstreamAuthTypeHeader:
		authValue := route.UpstreamAPIKey
		if prefix := strings.TrimSpace(route.UpstreamAuthPrefix); prefix != "" {
			authValue = prefix + " " + authValue
		}
		req.Header.Set(authHeader, authValue)
	case UpstreamAuthTypeMobileCloudAKSK:
		nonce, nonceErr := newMobileCloudNonce()
		if nonceErr != nil {
			writeJSONError(w, http.StatusBadGateway, "upstream_error", "failed to create upstream request signature")
			return
		}
		if signErr := signMobileCloudRequest(req, route.UpstreamAccessKey, route.UpstreamSecretKey, time.Now(), nonce); signErr != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", signErr.Error())
			return
		}
	default:
		writeJSONError(w, http.StatusInternalServerError, "configuration_error", "unsupported upstream authentication type")
		return
	}

	client := p.client
	if upstreamAuthType == UpstreamAuthTypeMobileCloudAKSK {
		clientCopy := *p.client
		clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
		client = &clientCopy
	}
	resp, err := client.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", "failed to reach upstream server")
		return
	}
	defer resp.Body.Close()
	if upstreamAuthType == UpstreamAuthTypeMobileCloudAKSK &&
		resp.StatusCode >= http.StatusMultipleChoices &&
		resp.StatusCode < http.StatusBadRequest &&
		strings.TrimSpace(resp.Header.Get("Location")) != "" {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", "upstream redirect rejected")
		return
	}

	if affinityResponseField == "" && strings.TrimSpace(affinityPathValue) == "" && scopedRequest == nil {
		copyHeader(w.Header(), resp.Header)
		removeHopByHopHeaders(w.Header())
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", "failed to read upstream response")
		return
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices && canPersistAffinity(body) {
		if scopedRequest != nil {
			if err := persistScopedAssetResponse(body, scopedRequest); err != nil {
				var scopeErr *scopedAssetError
				if errors.As(err, &scopeErr) {
					writeJSONError(w, scopeErr.Status, scopeErr.Code, scopeErr.Message)
					return
				}
				writeJSONError(w, http.StatusInternalServerError, "database_error", "failed to persist scoped asset metadata")
				return
			}
			w.Header().Set("X-New-Api-Asset-Namespace", route.AssetNamespaceID)
		} else {
			externalIDs := extractAffinityIDs(body, affinityResponseField)
			if value := strings.TrimSpace(affinityPathValue); value != "" {
				externalIDs = append(externalIDs, value)
			}
			seen := make(map[string]bool, len(externalIDs))
			persisted := false
			for _, externalID := range externalIDs {
				externalID = strings.TrimSpace(externalID)
				if externalID == "" || seen[externalID] {
					continue
				}
				seen[externalID] = true
				if err := model.UpsertAssetChannelBinding(externalID, route.AssetNamespaceID, route.ChannelID, tokenID); err != nil {
					writeJSONError(w, http.StatusInternalServerError, "database_error", "failed to persist asset channel affinity")
					return
				}
				persisted = true
			}
			if persisted {
				w.Header().Set("X-New-Api-Asset-Namespace", route.AssetNamespaceID)
			}
		}
	}
	copyHeader(w.Header(), resp.Header)
	removeHopByHopHeaders(w.Header())
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func (p *Proxy) ReloadRoutes() error {
	if p.routesConfigPath == "" {
		return nil
	}
	config, err := LoadRoutesConfig(p.routesConfigPath)
	if err != nil {
		return fmt.Errorf("load routes config: %w", err)
	}
	secrets, err := LoadSecretStore(p.secretsFilePath)
	if err != nil {
		return fmt.Errorf("load proxy secrets: %w", err)
	}
	router, err := BuildRuntimeRouter(config, secrets.Lookup)
	if err != nil {
		return err
	}
	for _, route := range config.Routes {
		if route.AssetScopeID == "" {
			continue
		}
		for _, tokenID := range route.APIKeyIDs {
			if err := model.UpsertAssetScopeTokenBinding(
				route.AssetNamespaceID,
				route.AssetScopeID,
				route.ChannelID,
				tokenID,
			); err != nil {
				return fmt.Errorf("register route %s asset scope for api_key_id %d: %w", route.Name, tokenID, err)
			}
		}
	}
	p.runtimeRouter.Store(router)
	return nil
}

func (p *Proxy) CurrentRouter() *RuntimeRouter {
	if p == nil {
		return nil
	}
	value := p.runtimeRouter.Load()
	if value == nil {
		return nil
	}
	router, _ := value.(*RuntimeRouter)
	return router
}

func (p *Proxy) ConfigVersion() string {
	router := p.CurrentRouter()
	if router == nil {
		return "legacy"
	}
	return router.Version()
}

type RouteDecision struct {
	PathKnown     bool
	MethodAllowed bool
}

func RouteDecisionFor(method string, path string) RouteDecision {
	method = strings.ToUpper(method)
	exactRoutes := map[string]map[string]bool{
		"/api/openapi-maas/exp/aicc/v2/asset-group":                                 {"POST": true},
		"/api/openapi-maas/exp/aicc/v2/asset-group/":                                {"POST": true},
		"/api/openapi-maas/exp/aicc/v2/asset":                                       {"POST": true},
		"/api/openapi-maas/exp/aicc/v2/asset/":                                      {"POST": true},
		"/api/openapi-maas/exp/aicc/v2/asset/query":                                 {"POST": true},
		"/api/openapi-maas/exp/aicc/v2/asset-group/query":                           {"POST": true},
		"/api/openapi-maas/exp/aicc/v2/real-person-auth/sessions":                   {"POST": true},
		"/api/openapi-maas/exp/aicc/v2/real-person-auth/asset-group/by-byted-token": {"POST": true},
		"/api/v3/ark/assets":                                                        {"GET": true, "POST": true},
		"/api/v3/ark/assets/groups":                                                 {"POST": true},
		"/api/v3/ark/real-person/assets":                                            {"POST": true},
		"/api/v3/ark/real-person/validate/sessions":                                 {"POST": true},
		"/api/v3/open/CreateAsset":                                                  {"POST": true},
		"/api/v3/open/GetAsset":                                                     {"POST": true},
		"/api/v3/open/CreateVisualValidateSession":                                  {"POST": true},
		"/api/v3/open/GetVisualValidateResult":                                      {"POST": true},
		"/api/v3/open/CreateAssetGroup":                                             {"POST": true},
	}
	if methods, ok := exactRoutes[path]; ok {
		return RouteDecision{
			PathKnown:     true,
			MethodAllowed: methods[method],
		}
	}

	pathRoutes := []struct {
		prefix  string
		methods map[string]bool
	}{
		{
			prefix:  "/api/openapi-maas/exp/aicc/v2/asset/",
			methods: map[string]bool{"GET": true, "PUT": true, "DELETE": true},
		},
		{
			prefix:  "/api/openapi-maas/exp/aicc/v2/asset-group/",
			methods: map[string]bool{"GET": true, "PUT": true, "DELETE": true},
		},
		{
			prefix:  "/api/v3/ark/assets/",
			methods: map[string]bool{"GET": true},
		},
		{
			prefix:  "/api/v3/ark/real-person/assets/",
			methods: map[string]bool{"GET": true},
		},
		{
			prefix:  "/api/v3/ark/real-person/validate/sessions/",
			methods: map[string]bool{"GET": true},
		},
	}
	for _, route := range pathRoutes {
		if hasSinglePathSegmentAfterPrefix(path, route.prefix) {
			return RouteDecision{
				PathKnown:     true,
				MethodAllowed: route.methods[method],
			}
		}
	}

	return RouteDecision{}
}

func NormalizeAPIKey(authHeader string) (string, bool) {
	key := strings.TrimSpace(authHeader)
	if strings.HasPrefix(strings.ToLower(key), "bearer ") {
		key = strings.TrimSpace(key[7:])
	}
	key = strings.TrimPrefix(key, "sk-")
	parts := strings.Split(key, "-")
	if len(parts) > 0 {
		key = parts[0]
	}
	if key == "" {
		return "", false
	}
	return key, true
}

func ResolveTokenInDatabase(key string) (*TokenIdentity, error) {
	token, err := model.ValidateUserToken(key)
	if err != nil {
		if errors.Is(err, model.ErrTokenInvalid) || errors.Is(err, model.ErrTokenNotProvided) {
			return nil, nil
		}
		return nil, err
	}

	user, err := model.GetUserCache(token.UserId)
	if err != nil {
		return nil, err
	}
	if user.Status != common.UserStatusEnabled {
		return nil, nil
	}

	return &TokenIdentity{ID: token.Id}, nil
}

func TokenExistsInDatabase(key string) (bool, error) {
	token, err := ResolveTokenInDatabase(key)
	if err != nil {
		return false, err
	}
	return token != nil, nil
}

func hasSinglePathSegmentAfterPrefix(path string, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := strings.TrimPrefix(path, prefix)
	return rest != "" && !strings.Contains(rest, "/")
}

func rewriteRequestURL(reqURL *url.URL, target *url.URL, upstreamPath string) {
	targetQuery := target.RawQuery
	reqURL.Scheme = target.Scheme
	reqURL.Host = target.Host
	reqURL.Path = singleJoiningSlash(target.Path, upstreamPath)
	reqURL.RawPath = ""
	if targetQuery == "" || reqURL.RawQuery == "" {
		reqURL.RawQuery = targetQuery + reqURL.RawQuery
	} else {
		reqURL.RawQuery = targetQuery + "&" + reqURL.RawQuery
	}
}

func extractAffinityIDs(body []byte, field string) []string {
	field = strings.TrimSpace(field)
	if field == "" {
		return nil
	}
	result := gjson.GetBytes(body, field)
	if !result.Exists() {
		return nil
	}
	if result.IsArray() {
		values := result.Array()
		ids := make([]string, 0, len(values))
		for _, value := range values {
			if value.Type == gjson.String {
				ids = append(ids, value.String())
			}
		}
		return ids
	}
	if result.Type != gjson.String {
		return nil
	}
	return []string{result.String()}
}

func canPersistAffinity(body []byte) bool {
	if _, _, isBusinessError := parseUpstreamBusinessError(body); isBusinessError {
		return false
	}
	state := gjson.GetBytes(body, "state")
	return state.Type != gjson.String || !strings.EqualFold(strings.TrimSpace(state.String()), "ERROR")
}

type requestModelPayload struct {
	Model string `json:"model"`
}

func extractRequestModel(r *http.Request) (string, []byte, error) {
	modelName := strings.TrimSpace(r.URL.Query().Get("model"))
	if r.Body == nil {
		return modelName, nil, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return modelName, body, nil
	}
	var payload requestModelPayload
	if err := common.Unmarshal(body, &payload); err != nil {
		return "", body, err
	}
	if strings.TrimSpace(payload.Model) != "" {
		modelName = strings.TrimSpace(payload.Model)
	}
	return modelName, body, nil
}

func singleJoiningSlash(a string, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}

type proxyErrorResponse struct {
	Error proxyError `json:"error"`
}

type proxyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func parseUpstreamBusinessError(body []byte) (string, string, bool) {
	codeResult := gjson.GetBytes(body, "data.Code")
	if codeResult.Type != gjson.String {
		return "", "", false
	}
	code := strings.TrimSpace(codeResult.Str)
	if code == "" {
		return "", "", false
	}
	codeRunes := []rune(code)
	if len(codeRunes) > 128 {
		code = string(codeRunes[:128])
	}

	message := "upstream rejected the request"
	messageResult := gjson.GetBytes(body, "data.Message")
	if messageResult.Type == gjson.String {
		if upstreamMessage := strings.TrimSpace(messageResult.Str); upstreamMessage != "" {
			messageRunes := []rune(upstreamMessage)
			if len(messageRunes) > 2048 {
				upstreamMessage = string(messageRunes[:2048])
			}
			message = upstreamMessage
		}
	}
	return code, message, true
}

func writeJSONError(w http.ResponseWriter, status int, code string, message string) {
	body, err := common.Marshal(proxyErrorResponse{
		Error: proxyError{
			Code:    code,
			Message: message,
		},
	})
	if err != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func copyHeader(dst http.Header, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Set-Cookie") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func removeHopByHopHeaders(header http.Header) {
	for _, key := range []string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		header.Del(key)
	}
}
