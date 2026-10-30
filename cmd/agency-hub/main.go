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
	if command == "migrate-writer-fence" {
		if err := model.MigrateAgencyWriterFence(model.DB); err != nil {
			return err
		}
		common.SysLog("Agency Hub writer fence migration completed")
		return nil
	}
	if command == "reconcile" {
		app := agencyhub.New(model.DB, nil, config)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		summary, err := app.Reconcile(ctx)
		if err != nil {
			return fmt.Errorf("agency reconciliation failed: %w", err)
		}
		encoded, err := common.Marshal(summary)
		if err != nil {
			return fmt.Errorf("encode reconciliation summary: %w", err)
		}
		fmt.Println(string(encoded))
		return nil
	}
	if command != "serve" {
		if command == "recover-pricing-sources" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			rootID, _ := strconv.ParseInt(os.Getenv("AGENCY_PRICING_REPAIR_ROOT_ID"), 10, 64)
			report, err := agencyhub.New(model.DB, nil, config).RecoverPricingSources(ctx, os.Getenv("AGENCY_PRICING_REPAIR_PLAN_HASH"), rootID)
			if err != nil {
				return err
			}
			encoded, err := common.Marshal(report)
			if err != nil {
				return err
			}
			fmt.Println(string(encoded))
			return nil
		}
		if command == "repair-pricing" || command == "rollback-pricing" || command == "restore-negotiated-pricing" {
			app := agencyhub.New(model.DB, nil, config)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			rootID, _ := strconv.ParseInt(os.Getenv("AGENCY_PRICING_REPAIR_ROOT_ID"), 10, 64)
			if command == "rollback-pricing" {
				return app.RollbackPricingRepair(ctx, os.Getenv("AGENCY_PRICING_REPAIR_PLAN_HASH"), rootID)
			}
			var report agencyhub.PricingRepairReport
			var err error
			if command == "restore-negotiated-pricing" {
				report, err = app.RestoreNegotiatedPricing(ctx, os.Getenv("AGENCY_PRICING_ORIGINAL_REPAIR_HASH"), os.Getenv("AGENCY_PRICING_REPAIR_PLAN_HASH"), rootID)
			} else {
				report, err = app.RepairPricing(ctx, os.Getenv("AGENCY_PRICING_REPAIR_PLAN_HASH"), rootID)
			}
			if err != nil {
				return fmt.Errorf("pricing repair failed: %w", err)
			}
			encoded, err := common.Marshal(report)
			if err != nil {
				return err
			}
			fmt.Println(string(encoded))
			return nil
		}
		return fmt.Errorf("unknown command %q; expected serve, migrate, migrate-writer-fence, reconcile, recover-pricing-sources, repair-pricing, restore-negotiated-pricing, or rollback-pricing", command)
	}
	if config.AutoMigrate {
		if err := model.MigrateAgency(model.DB); err != nil {
			return err
		}
	}
	// Credit statements are emailed with the platform's SMTP settings, which
	// live in the options table. Without this the sidecar would render its
	// email path as unconfigured even though the main service can send mail.
	model.InitOptionMap()
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
	if err := app.InitializeCommandTransport(); err != nil {
		return fmt.Errorf("initialize agency command transport: %w", err)
	}
	defer app.CloseCommandTransport()
	app.SetReady(true)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	if os.Getenv("AGENCY_HUB_BACKGROUND_DISABLED") != "true" {
		app.StartBackground(workerCtx)
	}
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
	return server.Shutdown(shutdownCtx)
}
