package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/joho/godotenv"
)

func main() {
	if err := run(); err != nil {
		common.FatalLog(err.Error())
	}
}

func run() error {
	_ = godotenv.Load(".env")
	common.InitEnv()
	common.IsMasterNode = false
	logger.SetupLogger()
	if err := model.InitDB(); err != nil {
		return fmt.Errorf("initialize main database: %w", err)
	}
	config := agencyhub.LoadConfig()
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "migrate" {
		seconds := 60
		if value, err := strconv.Atoi(os.Getenv("AGENCY_HUB_MIGRATION_TIMEOUT_SECONDS")); err == nil && value > 0 {
			seconds = value
		}
		if err := model.MigrateAgencyWithTimeout(model.DB, time.Duration(seconds)*time.Second); err != nil {
			return err
		}
		common.SysLog("Agency Hub migration completed")
		return nil
	}
	if command != "serve" {
		return fmt.Errorf("unknown command %q; expected serve or migrate", command)
	}
	if config.AutoMigrate {
		if err := model.MigrateAgency(model.DB); err != nil {
			return err
		}
	}
	if err := model.InitLogDB(); err != nil {
		return fmt.Errorf("initialize log database: %w", err)
	}
	app := agencyhub.New(model.DB, model.LOG_DB, config)
	if config.SSOPublicKeyFile != "" {
		key, err := agencyhub.LoadSSOPublicKey(config.SSOPublicKeyFile)
		if err != nil {
			return fmt.Errorf("load agency SSO public key: %w", err)
		}
		app.SetSSOPublicKey(key)
	}
	if config.CommandServicePublicKeyFile != "" {
		key, err := agencyhub.LoadSSOPublicKey(config.CommandServicePublicKeyFile)
		if err != nil {
			return fmt.Errorf("load agency command service public key: %w", err)
		}
		app.SetCommandServicePublicKey(key)
	}
	if config.CommandServicePrivateKeyFile != "" {
		key, err := agencyhub.LoadSSOPrivateKey(config.CommandServicePrivateKeyFile)
		if err != nil {
			return fmt.Errorf("load agency command service private key: %w", err)
		}
		app.SetCommandServicePrivateKey(key)
	}
	app.SetReady(true)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	app.StartBackground(workerCtx)
	go runAgencyExportWorker(workerCtx, app)
	server := &http.Server{Addr: ":" + config.Port, Handler: app.Router(), ReadHeaderTimeout: 30 * time.Second}
	serverErrors := make(chan error, 1)
	go func() {
		common.SysLog("Agency Hub started on port " + config.Port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case signal := <-quit:
		common.SysLog(fmt.Sprintf("received signal %v", signal))
	case err := <-serverErrors:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stopWorker()
	return server.Shutdown(shutdownCtx)
}

func runAgencyExportWorker(ctx context.Context, app *agencyhub.App) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := app.ProcessExportJobs(4); err != nil {
				common.SysLog("agency export worker error: " + err.Error())
			}
			if _, err := app.CleanupExpiredExportJobs(16); err != nil {
				common.SysLog("agency export cleanup error: " + err.Error())
			}
		}
	}
}
