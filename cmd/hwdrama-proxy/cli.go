package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/hwdramaproxy"
	"github.com/joho/godotenv"
)

const defaultRoutesConfigPath = "/opt/new-api/hwdrama-proxy-routes.yml"

func runConfigCLI(args []string) error {
	_ = godotenv.Load(".env")
	if len(args) == 0 {
		return fmt.Errorf("usage: /hwdrama-proxy config <wizard|validate|reload|action|list-tokens|list-channels>")
	}
	switch args[0] {
	case "wizard":
		return runConfigWizard(args[1:])
	case "validate":
		return runConfigValidate(args[1:])
	case "reload":
		return runConfigReload(args[1:])
	case "action":
		return runConfigAction(args[1:])
	case "list-tokens":
		return runListTokens()
	case "list-channels":
		return runListChannels()
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

func runConfigValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	configPath := fs.String("config", routesConfigPathForCLI(), "routes config path")
	secretsPath := fs.String("secrets", strings.TrimSpace(os.Getenv("HWD_PROXY_SECRETS_FILE")), "secrets env file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	config, err := hwdramaproxy.LoadRoutesConfig(*configPath)
	if err != nil {
		return err
	}
	secrets, err := hwdramaproxy.LoadSecretStore(*secretsPath)
	if err != nil {
		return err
	}
	if _, err := hwdramaproxy.BuildRuntimeRouter(config, secrets.Lookup); err != nil {
		return err
	}
	fmt.Printf("OK: %s\n", *configPath)
	return nil
}

func runConfigReload(args []string) error {
	fs := flag.NewFlagSet("reload", flag.ContinueOnError)
	addr := fs.String("addr", "http://127.0.0.1:3001/-/reload", "local reload endpoint")
	adminToken := fs.String("admin-token", strings.TrimSpace(os.Getenv("HWD_PROXY_ADMIN_TOKEN")), "admin token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, *addr, nil)
	if err != nil {
		return err
	}
	if *adminToken != "" {
		req.Header.Set("X-Hwd-Proxy-Admin-Token", *adminToken)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("reload failed with status %s", resp.Status)
	}
	fmt.Println("Reload OK")
	return nil
}

func runConfigAction(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: /hwdrama-proxy config action <list|add>")
	}
	switch args[0] {
	case "list":
		return runActionList(args[1:])
	case "add":
		return runActionAdd(args[1:])
	default:
		return fmt.Errorf("unknown action command %q", args[0])
	}
}

func runActionList(args []string) error {
	fs := flag.NewFlagSet("action list", flag.ContinueOnError)
	configPath := fs.String("config", routesConfigPathForCLI(), "routes config path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	config, err := loadOrEmptyConfig(*configPath)
	if err != nil {
		return err
	}
	if len(config.Actions) == 0 {
		fmt.Println("No actions configured.")
		return nil
	}
	for key, action := range config.Actions {
		fmt.Printf("%s\t%s %s\tdefault upstream: %s %s\n", key, action.DownstreamMethod, action.DownstreamPath, action.DefaultUpstreamMethod, action.DefaultUpstreamPath)
	}
	return nil
}

func runActionAdd(args []string) error {
	fs := flag.NewFlagSet("action add", flag.ContinueOnError)
	configPath := fs.String("config", routesConfigPathForCLI(), "routes config path")
	key := fs.String("key", "", "action key")
	method := fs.String("method", "POST", "downstream method")
	path := fs.String("path", "", "downstream path")
	upstreamMethod := fs.String("upstream-method", "", "default upstream method")
	upstreamPath := fs.String("upstream-path", "", "default upstream path")
	affinityResponseField := fs.String("affinity-response-field", "", "JSON field containing an asset ID to bind to this route")
	affinityPathParam := fs.String("affinity-path-param", "", "downstream path parameter containing an asset ID to bind to this route")
	if err := fs.Parse(args); err != nil {
		return err
	}
	reader := bufio.NewReader(os.Stdin)
	if *key == "" {
		*key = promptString(reader, "Action key", "")
	}
	if *path == "" {
		*path = promptString(reader, "Downstream path", "")
	}
	if *upstreamMethod == "" {
		*upstreamMethod = *method
	}
	if *upstreamPath == "" {
		*upstreamPath = *path
	}
	config, err := loadOrEmptyConfig(*configPath)
	if err != nil {
		return err
	}
	if config.Actions == nil {
		config.Actions = map[string]hwdramaproxy.ActionConfig{}
	}
	config.Actions[*key] = hwdramaproxy.ActionConfig{
		DownstreamMethod:      *method,
		DownstreamPath:        *path,
		DefaultUpstreamMethod: *upstreamMethod,
		DefaultUpstreamPath:   *upstreamPath,
		AffinityResponseField: *affinityResponseField,
		AffinityPathParam:     *affinityPathParam,
	}
	if err := writeValidatedConfig(*configPath, "", config); err != nil {
		return err
	}
	fmt.Printf("Action %s saved.\n", *key)
	return nil
}

func runConfigWizard(args []string) error {
	fs := flag.NewFlagSet("wizard", flag.ContinueOnError)
	configPath := fs.String("config", routesConfigPathForCLI(), "routes config path")
	secretsPath := fs.String("secrets", strings.TrimSpace(os.Getenv("HWD_PROXY_SECRETS_FILE")), "secrets env file path")
	noReload := fs.Bool("no-reload", false, "skip calling local reload endpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := initCLIEnv(true); err != nil {
		return err
	}
	defer closeCLIDB()

	reader := bufio.NewReader(os.Stdin)
	config, err := loadOrEmptyConfig(*configPath)
	if err != nil {
		return err
	}
	if config.Actions == nil {
		config.Actions = map[string]hwdramaproxy.ActionConfig{}
	}

	fmt.Println("Available tokens:")
	if err := printTokens(); err != nil {
		return err
	}
	tokenIDs := promptIntList(reader, "API key IDs")

	fmt.Println("Available channels:")
	channels, err := model.GetAllChannels(0, 10000, false, true)
	if err != nil {
		return err
	}
	for _, channel := range channels {
		fmt.Printf("[%d] %s\tbase_url=%s\tmodels=%s\n", channel.Id, channel.Name, channel.GetBaseURL(), channel.Models)
	}
	channelID := promptInt(reader, "Channel ID")
	channel, err := model.GetChannelById(channelID, false)
	if err != nil {
		return err
	}

	routeName := promptString(reader, "Route name", slug(channel.Name)+"-route")
	modelsDefault := strings.Join(channel.GetModels(), ",")
	models := promptStringList(reader, "Models (comma-separated, or *)", modelsDefault)
	upstreamBaseURL := promptString(reader, "Upstream base URL", channel.GetBaseURL())
	upstreamAuthType := promptString(reader, "Upstream auth type (header or mobile_cloud_aksk)", hwdramaproxy.UpstreamAuthTypeHeader)
	upstreamAPIKeyEnv := ""
	upstreamAuthHeader := ""
	upstreamAuthPrefix := ""
	upstreamAccessKeyEnv := ""
	upstreamSecretKeyEnv := ""
	if strings.EqualFold(upstreamAuthType, hwdramaproxy.UpstreamAuthTypeMobileCloudAKSK) {
		upstreamAccessKeyEnv = promptString(reader, "Mobile Cloud access key env", "MOBILE_CLOUD_MAAS_ACCESS_KEY")
		upstreamSecretKeyEnv = promptString(reader, "Mobile Cloud secret key env", "MOBILE_CLOUD_MAAS_SECRET_KEY")
	} else {
		upstreamAPIKeyEnv = promptString(reader, "Upstream API key env", "HWD_"+strings.ToUpper(sanitizeEnvName(routeName))+"_API_KEY")
		upstreamAuthHeader = promptString(reader, "Upstream auth header", "Authorization")
		upstreamAuthPrefix = promptString(reader, "Upstream auth prefix (empty for a raw key)", map[bool]string{true: "Bearer", false: ""}[strings.EqualFold(upstreamAuthHeader, "Authorization")])
	}
	assetNamespaceID := promptString(reader, "Asset namespace ID", slug(channel.Name))
	assetScopeID := ""
	if strings.EqualFold(upstreamAuthType, hwdramaproxy.UpstreamAuthTypeMobileCloudAKSK) {
		assetScopeID = promptString(reader, "Asset scope ID (share one value across one customer's API keys)", slug(routeName))
	}

	for {
		if len(config.Actions) > 0 {
			fmt.Println("Configured actions:")
			for key, action := range config.Actions {
				fmt.Printf("- %s\t%s %s\n", key, action.DownstreamMethod, action.DownstreamPath)
			}
		}
		if !promptYesNo(reader, "Add a new action before creating route?", len(config.Actions) == 0) {
			break
		}
		actionKey := promptString(reader, "Action key", "")
		downstreamMethod := promptString(reader, "Downstream method", "POST")
		downstreamPath := promptString(reader, "Downstream path", "")
		defaultUpstreamMethod := promptString(reader, "Default upstream method", downstreamMethod)
		defaultUpstreamPath := promptString(reader, "Default upstream path", downstreamPath)
		affinityResponseField := promptString(reader, "Affinity response field (empty to disable)", "")
		affinityPathParam := promptString(reader, "Affinity path parameter (empty to disable)", "")
		scopeOperation := promptString(reader, "Asset scope operation (required for Mobile Cloud actions)", "")
		scopePathParam := promptString(reader, "Asset scope path parameter (required for get/update/delete actions)", "")
		config.Actions[actionKey] = hwdramaproxy.ActionConfig{
			DownstreamMethod:      downstreamMethod,
			DownstreamPath:        downstreamPath,
			DefaultUpstreamMethod: defaultUpstreamMethod,
			DefaultUpstreamPath:   defaultUpstreamPath,
			AffinityResponseField: affinityResponseField,
			AffinityPathParam:     affinityPathParam,
			ScopeOperation:        scopeOperation,
			ScopePathParam:        scopePathParam,
		}
	}

	enabledActions := promptStringList(reader, "Enabled action keys", strings.Join(actionKeys(config.Actions), ","))
	overrides := map[string]hwdramaproxy.UpstreamActionConfig{}
	for _, actionKey := range enabledActions {
		if !promptYesNo(reader, "Override upstream for "+actionKey+"?", false) {
			continue
		}
		action := config.Actions[actionKey]
		overrides[actionKey] = hwdramaproxy.UpstreamActionConfig{
			UpstreamMethod: promptString(reader, "Override upstream method", action.DefaultUpstreamMethod),
			UpstreamPath:   promptString(reader, "Override upstream path", action.DefaultUpstreamPath),
		}
	}
	if len(overrides) == 0 {
		overrides = nil
	}

	config.Routes = upsertRoute(config.Routes, hwdramaproxy.RouteConfig{
		Name:                    routeName,
		APIKeyIDs:               tokenIDs,
		ChannelID:               channelID,
		Models:                  models,
		UpstreamBaseURL:         upstreamBaseURL,
		UpstreamAuthType:        upstreamAuthType,
		UpstreamAPIKeyEnv:       upstreamAPIKeyEnv,
		UpstreamAuthHeader:      upstreamAuthHeader,
		UpstreamAuthPrefix:      upstreamAuthPrefix,
		UpstreamAccessKeyEnv:    upstreamAccessKeyEnv,
		UpstreamSecretKeyEnv:    upstreamSecretKeyEnv,
		AssetNamespaceID:        assetNamespaceID,
		AssetScopeID:            assetScopeID,
		EnabledActions:          enabledActions,
		UpstreamActionOverrides: overrides,
	})

	if err := writeValidatedConfig(*configPath, *secretsPath, config); err != nil {
		return err
	}
	fmt.Printf("Config saved: %s\n", *configPath)
	if !*noReload {
		if err := runConfigReload(nil); err != nil {
			return fmt.Errorf("config saved but reload failed: %w", err)
		}
	}
	return nil
}

func runListTokens() error {
	if err := initCLIEnv(true); err != nil {
		return err
	}
	defer closeCLIDB()
	return printTokens()
}

func runListChannels() error {
	if err := initCLIEnv(true); err != nil {
		return err
	}
	defer closeCLIDB()
	channels, err := model.GetAllChannels(0, 10000, false, true)
	if err != nil {
		return err
	}
	for _, channel := range channels {
		fmt.Printf("%d\t%s\tstatus=%d\tbase_url=%s\tmodels=%s\tgroups=%s\n", channel.Id, channel.Name, channel.Status, channel.GetBaseURL(), channel.Models, channel.Group)
	}
	return nil
}

func initCLIEnv(needsDB bool) error {
	_ = godotenv.Load(".env")
	common.InitEnv()
	common.IsMasterNode = false
	common.RedisEnabled = false
	logger.SetupLogger()
	if !needsDB {
		return nil
	}
	if err := model.InitDB(); err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	model.LOG_DB = model.DB
	return nil
}

func closeCLIDB() {
	if model.DB != nil {
		if err := model.CloseDB(); err != nil {
			common.SysError(fmt.Sprintf("failed to close database: %v", err))
		}
	}
}

func routesConfigPathForCLI() string {
	if value := strings.TrimSpace(os.Getenv("HWD_PROXY_ROUTES_CONFIG")); value != "" {
		return value
	}
	return defaultRoutesConfigPath
}

func loadOrEmptyConfig(path string) (*hwdramaproxy.RoutesConfig, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return &hwdramaproxy.RoutesConfig{
				Version: 1,
				Actions: map[string]hwdramaproxy.ActionConfig{},
				Routes:  []hwdramaproxy.RouteConfig{},
			}, nil
		}
		return nil, err
	}
	return hwdramaproxy.LoadRoutesConfig(path)
}

func writeValidatedConfig(path string, secretsPath string, config *hwdramaproxy.RoutesConfig) error {
	secrets, err := hwdramaproxy.LoadSecretStore(secretsPath)
	if err != nil {
		return err
	}
	configForValidation := config
	secretLookup := secrets.Lookup
	if len(config.Routes) == 0 && len(config.Actions) > 0 {
		firstAction := actionKeys(config.Actions)[0]
		configCopy := *config
		configCopy.Routes = []hwdramaproxy.RouteConfig{
			{
				Name:              "validation-placeholder",
				AllAPIKeys:        true,
				ChannelID:         1,
				Models:            []string{hwdramaproxy.WildcardModel},
				UpstreamBaseURL:   "https://example.com",
				UpstreamAPIKeyEnv: "HWD_PROXY_VALIDATION_PLACEHOLDER",
				EnabledActions:    []string{firstAction},
			},
		}
		configForValidation = &configCopy
		secretLookup = func(key string) string {
			if key == "HWD_PROXY_VALIDATION_PLACEHOLDER" {
				return "placeholder"
			}
			return secrets.Lookup(key)
		}
	}
	if _, err := hwdramaproxy.BuildRuntimeRouter(configForValidation, secretLookup); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := hwdramaproxy.SaveRoutesConfig(tmp, config); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func printTokens() error {
	var tokens []model.Token
	if err := model.DB.Omit("key").Order("id desc").Find(&tokens).Error; err != nil {
		return err
	}
	for _, token := range tokens {
		fmt.Printf("%d\t%s\tuser_id=%d\tgroup=%s\tstatus=%d\n", token.Id, token.Name, token.UserId, token.Group, token.Status)
	}
	return nil
}

func promptString(reader *bufio.Reader, label string, defaultValue string) string {
	for {
		if defaultValue == "" {
			fmt.Printf("%s: ", label)
		} else {
			fmt.Printf("%s [%s]: ", label, defaultValue)
		}
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text)
		if text != "" {
			return text
		}
		if defaultValue != "" {
			return defaultValue
		}
	}
}

func promptYesNo(reader *bufio.Reader, label string, defaultValue bool) bool {
	defaultText := "n"
	if defaultValue {
		defaultText = "y"
	}
	for {
		answer := strings.ToLower(promptString(reader, label+" (y/n)", defaultText))
		switch answer {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
	}
}

func promptInt(reader *bufio.Reader, label string) int {
	for {
		value := promptString(reader, label, "")
		parsed, err := strconv.Atoi(value)
		if err == nil && parsed > 0 {
			return parsed
		}
		fmt.Println("Please enter a positive integer.")
	}
}

func promptIntList(reader *bufio.Reader, label string) []int {
	for {
		value := promptString(reader, label, "")
		parts := strings.Split(value, ",")
		result := make([]int, 0, len(parts))
		ok := true
		for _, part := range parts {
			parsed, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || parsed <= 0 {
				ok = false
				break
			}
			result = append(result, parsed)
		}
		if ok && len(result) > 0 {
			return result
		}
		fmt.Println("Please enter comma-separated positive integers.")
	}
}

func promptStringList(reader *bufio.Reader, label string, defaultValue string) []string {
	for {
		value := promptString(reader, label, defaultValue)
		parts := strings.Split(value, ",")
		result := make([]string, 0, len(parts))
		seen := map[string]bool{}
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			result = append(result, part)
		}
		if len(result) > 0 {
			return result
		}
	}
}

func actionKeys(actions map[string]hwdramaproxy.ActionConfig) []string {
	keys := make([]string, 0, len(actions))
	for key := range actions {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func upsertRoute(routes []hwdramaproxy.RouteConfig, route hwdramaproxy.RouteConfig) []hwdramaproxy.RouteConfig {
	for i := range routes {
		if routes[i].Name == route.Name {
			routes[i] = route
			return routes
		}
	}
	return append(routes, route)
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(" ", "-", "_", "-", "/", "-", ".", "-").Replace(value)
	value = strings.Trim(value, "-")
	if value == "" {
		return "route"
	}
	return value
}

func sanitizeEnvName(value string) string {
	value = strings.ToUpper(value)
	var builder strings.Builder
	for _, r := range value {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('_')
		}
	}
	return strings.Trim(builder.String(), "_")
}
