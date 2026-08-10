package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	"github.com/QuantumNous/new-api/claw-control/internal/adminquery"
	"github.com/QuantumNous/new-api/claw-control/internal/app"
	"github.com/QuantumNous/new-api/claw-control/internal/appmigration"
	"github.com/QuantumNous/new-api/claw-control/internal/approval"
	"github.com/QuantumNous/new-api/claw-control/internal/auditexport"
	"github.com/QuantumNous/new-api/claw-control/internal/billingimport"
	"github.com/QuantumNous/new-api/claw-control/internal/config"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/QuantumNous/new-api/claw-control/internal/httpapi"
	"github.com/QuantumNous/new-api/claw-control/internal/identity"
	"github.com/QuantumNous/new-api/claw-control/internal/maintenance"
	"github.com/QuantumNous/new-api/claw-control/internal/marginreport"
	"github.com/QuantumNous/new-api/claw-control/internal/metrics"
	"github.com/QuantumNous/new-api/claw-control/internal/migration"
	"github.com/QuantumNous/new-api/claw-control/internal/newapi"
	"github.com/QuantumNous/new-api/claw-control/internal/notification"
	"github.com/QuantumNous/new-api/claw-control/internal/outbox"
	"github.com/QuantumNous/new-api/claw-control/internal/plan"
	"github.com/QuantumNous/new-api/claw-control/internal/providerverify"
	"github.com/QuantumNous/new-api/claw-control/internal/resourcebinding"
	"github.com/QuantumNous/new-api/claw-control/internal/retention"
	"github.com/QuantumNous/new-api/claw-control/internal/secretintegrity"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/usageaudit"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load configuration: %v", err)
	}
	_ = os.Unsetenv("CLAW_EVIDENCE_MASTER_KEY")
	db, err := database.Open(cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := migration.Migrate(db); err != nil {
		log.Fatalf("migrate database: %v", err)
	}
	if cfg.Environment == "prod" {
		if info, statErr := os.Stat(filepath.Join(cfg.AdminAssetDir, "index.html")); statErr != nil || info.IsDir() {
			log.Fatalf("admin UI assets are unavailable at %s", cfg.AdminAssetDir)
		}
	}

	planService := plan.New(db)
	notificationService := notification.New(db)
	secretResolver := secrets.EnvironmentResolver{}
	providerVerifier, err := providerverify.NewTencentVerifier(cfg.ProviderVerificationTimeout)
	if err != nil {
		log.Fatalf("configure Tencent ADP verifier: %v", err)
	}
	identityVerifier, err := newapi.NewHTTPIdentityVerifier(
		cfg.NewAPIIdentityStatusURL, cfg.NewAPIAdminStatusURL, cfg.NewAPIIdentitySecret,
		cfg.IdentityStatusTimeout, cfg.InternalHMACTimeSkew,
	)
	if err != nil {
		log.Fatalf("configure new-api identity verifier: %v", err)
	}
	accessService := access.New(
		db, secretResolver, identityVerifier,
		cfg.EntryTicketTTL, cfg.SSOTicketTTL, cfg.ControlSessionTTL, cfg.AdminSessionTTL, cfg.AppContextTTL,
	)
	evidenceScanner, err := evidence.NewClamAVScanner(cfg.EvidenceClamAVAddress, cfg.EvidenceClamAVTimeout)
	if err != nil {
		log.Fatalf("configure evidence malware scanner: %v", err)
	}
	evidenceService, err := evidence.New(db, cfg.EvidenceRoot, cfg.EvidenceMasterKey, cfg.EvidenceMaxBytes, evidenceScanner)
	clear(cfg.EvidenceMasterKey)
	if err != nil {
		log.Fatalf("configure encrypted evidence store: %v", err)
	}
	usageService := usageaudit.New(db)
	var billingClient billingimport.Client
	if cfg.BillingImportEnabled {
		billingClient, err = billingimport.NewTencentClient(
			cfg.TencentBillingSecretID, cfg.TencentBillingSecretKey, cfg.TencentBillingPayerUIN,
			cfg.BillingImportTimeout, cfg.BillingImportMaxResponseBytes,
		)
		if err != nil {
			log.Fatalf("configure Tencent Billing client: %v", err)
		}
	}
	_ = os.Unsetenv("CLAW_TENCENT_BILLING_SECRET_ID")
	_ = os.Unsetenv("CLAW_TENCENT_BILLING_SECRET_KEY")
	_ = os.Unsetenv("CLAW_TENCENT_BILLING_PAYER_UIN")
	cfg.TencentBillingSecretID = ""
	cfg.TencentBillingSecretKey = ""
	billingImportService, err := billingimport.New(db, billingClient, evidenceService, usageService, billingimport.Config{
		Enabled: cfg.BillingImportEnabled, PayerUIN: cfg.TencentBillingPayerUIN,
		PageSize: cfg.BillingImportPageSize, MaxPages: cfg.BillingImportMaxPages,
		MaxRecords: cfg.BillingImportMaxRecords, MaxAttempts: cfg.BillingImportMaxAttempts,
		LeaseDuration: cfg.BillingImportLeaseDuration, MaxEvidenceBytes: cfg.EvidenceMaxBytes,
	})
	cfg.TencentBillingPayerUIN = ""
	if err != nil {
		log.Fatalf("configure Tencent Billing import: %v", err)
	}
	metricRegistry := metrics.New(db)
	handler := httpapi.New(httpapi.Services{
		DB: db, Customers: customer.New(db, cfg.Environment, identityVerifier), Identities: identity.New(db),
		Credentials: credential.New(db, secretResolver), Apps: app.New(db, true, secretResolver), Plans: planService,
		AppMigrations: appmigration.New(db, secretResolver),
		Usage:         usageService, Evidence: evidenceService, Margins: marginreport.New(db), Access: accessService,
		Resources:    resourcebinding.New(db),
		AdminQueries: adminquery.New(db), SecretResolver: secretResolver,
		ProviderVerifier: providerVerifier,
		Notifications:    notificationService, AuditExport: auditexport.New(db),
		Retention: retention.New(db), Approvals: approval.New(db, secretResolver), Metrics: metricRegistry,
		BillingImports:  billingImportService,
		SecretIntegrity: secretintegrity.New(db, secretResolver),
	}, cfg.AdminToken, httpapi.InternalAuth{
		ServiceKeys: cfg.InternalHMACKeys, TimeSkew: cfg.InternalHMACTimeSkew,
		NewAPIServiceName: cfg.NewAPIServiceName, ADPServiceName: cfg.ADPServiceName,
	}, httpapi.PublicConfig{
		ADPSSORedirectPath: cfg.ADPSSORedirectPath, AdminRedirectPath: cfg.AdminRedirectPath,
		AdminAssetDir: cfg.AdminAssetDir,
	})
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	var metricsServer *http.Server
	if cfg.MetricsAddr != "" {
		metricsServer = &http.Server{
			Addr: cfg.MetricsAddr, Handler: metricRegistry.Handler(),
			ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
			WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
		}
		metricsListener, err := net.Listen("tcp", cfg.MetricsAddr)
		if err != nil {
			log.Fatalf("listen for internal metrics: %v", err)
		}
		go func() {
			log.Printf("claw-control internal metrics listening on %s", cfg.MetricsAddr)
			if err := metricsServer.Serve(metricsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("internal metrics server failed: %v", err)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var outboxPublisher *outbox.RedisPublisher
	if cfg.OutboxInterval > 0 {
		outboxPublisher, err = outbox.NewRedisPublisherWithOptions(&redis.Options{
			Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB,
		}, cfg.RedisEventChannel, cfg.InternalHMACKeys[cfg.ADPServiceName])
		if err != nil {
			log.Fatalf("configure control outbox: %v", err)
		}
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = outboxPublisher.Ping(pingCtx)
		cancel()
		if err != nil {
			log.Fatalf("connect control outbox Redis: %v", err)
		}
		defer outboxPublisher.Close()
		go runOutboxWorker(ctx, outbox.New(db, outboxPublisher), metricRegistry, cfg.OutboxInterval)
	}
	if cfg.PeriodReconcileInterval > 0 {
		go runPeriodWorker(ctx, planService, notificationService, cfg.PeriodReconcileInterval)
	}
	if cfg.MaintenanceInterval > 0 {
		go runMaintenanceWorker(ctx, maintenance.New(db), cfg.MaintenanceInterval, cfg.EphemeralRetention, cfg.DeliveredOutboxRetention)
	}
	if cfg.BillingImportEnabled {
		go runBillingImportWorker(ctx, billingImportService, cfg.BillingImportInterval)
	}
	if cfg.RetentionCoordinatorInterval > 0 {
		retentionClient, clientErr := retention.NewHTTPDeliveryClient(
			cfg.ADPRetentionURL, cfg.InternalHMACKeys[cfg.ADPServiceName],
			cfg.RetentionCoordinatorTimeout, cfg.InternalHMACTimeSkew, cfg.Environment != "prod",
		)
		if clientErr != nil {
			log.Fatalf("configure ADP retention coordinator: %v", clientErr)
		}
		go runRetentionCoordinator(ctx, retention.NewCoordinator(db, retentionClient), cfg.RetentionCoordinatorInterval)
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
		if metricsServer != nil {
			if err := metricsServer.Shutdown(shutdownCtx); err != nil {
				log.Printf("metrics shutdown failed: %v", err)
			}
		}
	}()

	log.Printf("claw-control listening on %s with %s database", cfg.Addr, cfg.DBDriver)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

func runOutboxWorker(ctx context.Context, worker *outbox.Worker, metricRegistry *metrics.Registry, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for {
			processed, err := worker.ProcessOne(ctx)
			if err != nil {
				if metricRegistry != nil {
					metricRegistry.IncControlEventFailure("publish")
				}
				log.Printf("control outbox delivery failed: %v", err)
				break
			}
			if !processed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runRetentionCoordinator(ctx context.Context, coordinator *retention.Coordinator, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for {
			processed, err := coordinator.ProcessOne(ctx)
			if err != nil {
				log.Printf("ADP retention delivery deferred: %v", err)
				break
			}
			if !processed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runMaintenanceWorker(ctx context.Context, service *maintenance.Service, interval, ephemeralRetention, deliveredOutboxRetention time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := service.Cleanup(ctx, time.Now().UTC(), ephemeralRetention, deliveredOutboxRetention); err != nil {
			log.Printf("control maintenance cleanup failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runPeriodWorker(ctx context.Context, service *plan.Service, notifications *notification.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := service.ReconcilePeriods(now); err != nil {
				log.Printf("period reconciliation failed: %v", err)
			}
			if err := notifications.Reconcile(now); err != nil {
				log.Printf("plan notification reconciliation failed: %v", err)
			}
		}
	}
}

func runBillingImportWorker(ctx context.Context, service *billingimport.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for {
			processed, err := service.ProcessOne(ctx)
			if err != nil {
				log.Printf("Tencent Billing import worker failed with a sanitized internal error: %v", err)
				break
			}
			if !processed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
