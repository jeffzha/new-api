package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/hwdramaproxy"
	"github.com/joho/godotenv"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "config" {
		if err := runConfigCLI(os.Args[2:]); err != nil {
			common.FatalLog(err.Error())
		}
		return
	}
	if err := run(); err != nil {
		common.FatalLog(err.Error())
	}
}

func run() error {
	_ = godotenv.Load(".env")
	common.InitEnv()
	common.IsMasterNode = false
	common.RedisEnabled = false
	logger.SetupLogger()

	if err := model.InitDB(); err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	model.LOG_DB = model.DB
	defer func() {
		if err := model.CloseDB(); err != nil {
			common.SysError(fmt.Sprintf("failed to close database: %v", err))
		}
	}()

	port := strings.TrimSpace(os.Getenv("HWD_PROXY_PORT"))
	if port == "" {
		port = "3001"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("invalid HWD_PROXY_PORT: %w", err)
	}

	timeoutSeconds := common.GetEnvOrDefault("HWD_PROXY_REQUEST_TIMEOUT_SECONDS", 600)
	routesConfigPath := strings.TrimSpace(os.Getenv("HWD_PROXY_ROUTES_CONFIG"))
	proxyConfig := hwdramaproxy.Config{
		RoutesConfigPath: routesConfigPath,
		SecretsFilePath:  strings.TrimSpace(os.Getenv("HWD_PROXY_SECRETS_FILE")),
		Timeout:          time.Duration(timeoutSeconds) * time.Second,
		MaterialFiles: hwdramaproxy.MaterialFilesConfig{
			Directory: strings.TrimSpace(os.Getenv("HWD_PROXY_MATERIAL_FILES_DIR")),
			PublicURL: strings.TrimSpace(os.Getenv("HWD_PROXY_MATERIAL_FILES_PUBLIC_URL")),
			TTL:       time.Duration(common.GetEnvOrDefault("HWD_PROXY_MATERIAL_FILES_TTL_SECONDS", 86400)) * time.Second,
		},
	}
	if routesConfigPath == "" {
		proxyConfig.UpstreamBaseURL = strings.TrimSpace(os.Getenv("HWD_PROXY_UPSTREAM_BASE_URL"))
		proxyConfig.UpstreamAPIKey = strings.TrimSpace(os.Getenv("HWD_PROXY_UPSTREAM_API_KEY"))
	}
	proxy, err := hwdramaproxy.New(proxyConfig)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/-/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !allowAdminRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if err := proxy.ReloadRoutes(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(proxyConfigStatusBody(proxy.ConfigVersion()))
	})
	mux.HandleFunc("/-/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !allowAdminRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(proxyConfigStatusBody(proxy.ConfigVersion()))
	})
	mux.Handle("/", proxy)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}

	go func() {
		common.SysLog("hwdrama proxy started on port " + port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			common.FatalLog("failed to start hwdrama proxy: " + err.Error())
		}
	}()
	go watchRoutesConfig(proxy)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	common.SysLog(fmt.Sprintf("received signal: %v, shutting down hwdrama proxy...", sig))

	shutdownTimeout := time.Duration(common.GetEnvOrDefault("SHUTDOWN_TIMEOUT_SECONDS", 120)) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown hwdrama proxy: %w", err)
	}
	common.SysLog("hwdrama proxy exited")
	return nil
}

func proxyConfigStatusBody(version string) []byte {
	body, err := common.Marshal(map[string]any{
		"success":        true,
		"config_version": version,
	})
	if err != nil {
		return []byte(`{"success":true}` + "\n")
	}
	return append(body, '\n')
}

func allowAdminRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	adminToken := strings.TrimSpace(os.Getenv("HWD_PROXY_ADMIN_TOKEN"))
	if adminToken == "" {
		return true
	}
	return r.Header.Get("X-Hwd-Proxy-Admin-Token") == adminToken
}

func watchRoutesConfig(proxy *hwdramaproxy.Proxy) {
	configPath := strings.TrimSpace(os.Getenv("HWD_PROXY_ROUTES_CONFIG"))
	if configPath == "" {
		return
	}
	interval := time.Duration(common.GetEnvOrDefault("HWD_PROXY_CONFIG_RELOAD_INTERVAL_SECONDS", 5)) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	var lastModTime time.Time
	if stat, err := os.Stat(configPath); err == nil {
		lastModTime = stat.ModTime()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		stat, err := os.Stat(configPath)
		if err != nil {
			common.SysError("failed to stat hwdrama proxy routes config: " + err.Error())
			continue
		}
		modTime := stat.ModTime()
		if !modTime.After(lastModTime) {
			continue
		}
		if err := proxy.ReloadRoutes(); err != nil {
			common.SysError("failed to reload hwdrama proxy routes config: " + err.Error())
			continue
		}
		lastModTime = modTime
		common.SysLog("reloaded hwdrama proxy routes config: " + proxy.ConfigVersion())
	}
}
