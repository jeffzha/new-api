// Package opsauthority contains process-local project accounting ownership.
// It never changes shared user settings or a product service's billing mode.
package opsauthority

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
)

func Enabled() bool { return os.Getenv("OPS_WALLET_AUTHORITY") == "ops" }

// Product keeps domestic task tables and the existing product push cursor.
// The default is the previously deployed, isolated project profile.
func Product() bool { return Enabled() && os.Getenv("OPS_WALLET_PROFILE") == "product" }

// FundingEnabled is separate from project accounting so existing deployments
// keep their contract until the funding outbox has been explicitly prepared.
func FundingEnabled() bool {
	return Enabled() && os.Getenv("OPS_WALLET_FUNDING_ENABLED") == "true"
}

// FinancialOutboxEnabled also drains accepted events during a configuration
// rollback. Its scope remains explicit; current new-call ownership is irrelevant.
func FinancialOutboxEnabled() bool {
	return FundingEnabled() || os.Getenv("OPS_FINANCIAL_OUTBOX_ENABLED") == "true"
}

func FinancialOutboxScope() string {
	if FundingEnabled() {
		return Scope()
	}
	return os.Getenv("OPS_FINANCIAL_OUTBOX_SCOPE")
}

func Currency() string {
	if currency := os.Getenv("OPS_BILLING_CURRENCY"); currency != "" {
		return currency
	}
	return "CNY"
}
func Scope() string {
	if Enabled() && !Product() {
		return os.Getenv("OPS_PROJECT_SCOPE")
	}
	return ""
}
func Validate() error {
	if FinancialOutboxEnabled() {
		scope := FinancialOutboxScope()
		if scope != "" && !regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`).MatchString(scope) || os.Getenv("OPS_PUSH_SPEC") != "v1.1" {
			return errors.New("OPS financial outbox requires v1.1 and a valid explicit ledger scope")
		}
	}
	mode := os.Getenv("OPS_WALLET_AUTHORITY")
	if mode != "" && mode != "gateway" && mode != "ops" {
		return errors.New("invalid OPS_WALLET_AUTHORITY")
	}
	if !Enabled() {
		// Disabling authority selects native tables and the product cursor.
		// Never pretend that this can drain isolated project tasks: doing so
		// would silently scan the wrong tables under the wrong lease.
		if os.Getenv("OPS_PROJECT_SCOPE") != "" && (os.Getenv("OPS_TASK_OUTBOX_ENABLED") == "true" || os.Getenv("OPS_PUSH_URL") != "") {
			return errors.New("project rollback requires its scoped worker; native tables and the product cursor cannot drain a project ledger")
		}
		return nil
	}
	if profile := os.Getenv("OPS_WALLET_PROFILE"); profile != "" && profile != "project" && profile != "product" {
		return errors.New("invalid OPS_WALLET_PROFILE")
	}
	if Currency() != "CNY" {
		return errors.New("this wallet integration requires CNY billing")
	}
	if FundingEnabled() && (os.Getenv("OPS_PUSH_SPEC") != "v1.1" || os.Getenv("OPS_USER_SYNC_BALANCES_ENABLED") != "false" || os.Getenv("OPS_INGEST_BUDGET_ENABLED") != "true") {
		return errors.New("OPS funding requires v1.1 events, shared ingest budget and profile sync without source balances")
	}
	if os.Getenv("OPS_BRIDGE_ENABLED") == "true" || os.Getenv("OPS_ENFORCE_CALLS") == "true" || os.Getenv("OPS_CN_DURABLE_TASK_ENABLED") == "true" || os.Getenv("OPS_CN_DURABLE_MIDJOURNEY_ENABLED") == "true" {
		return errors.New("project OPS accounting cannot run with local money command or durable funding writers")
	}
	if os.Getenv("OPS_BILLING_SECRET") != os.Getenv("OPS_PUSH_SECRET") || len(os.Getenv("OPS_PUSH_SECRET")) < 16 {
		return errors.New("project OPS credentials do not match")
	}
	id, err := strconv.Atoi(os.Getenv("OPS_PUSH_STATE_ID"))
	if Product() {
		if !FundingEnabled() || os.Getenv("OPS_PROJECT_SCOPE") != "" || os.Getenv("OPS_PUSH_STATE_ID") != "" && (err != nil || id != 1) {
			return errors.New("OPS product wallet requires native funding, no project scope and the existing product cursor")
		}
		// Existing project custody starts with isolated task tables. Product
		// custody must additionally reconcile live legacy writers, opening
		// balances and persisted task ownership. This draft has no such online
		// handoff protocol; a candidate must not become ready on flags alone.
		return errors.New("OPS product wallet activation requires an implemented and verified online custody handoff; existing service must remain active")
	} else if err != nil || id < 2 || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`).MatchString(Scope()) {
		return errors.New("OPS authority requires a project scope and independent OPS_PUSH_STATE_ID >= 2")
	}
	if os.Getenv("OPS_BILLING_ENABLED") != "true" || os.Getenv("OPS_BILLING_RECEIPTS") != "charges" || os.Getenv("OPS_PUSH_URL") == "" || os.Getenv("OPS_BILLING_KEY_ID") != os.Getenv("OPS_PUSH_KEY_ID") {
		return errors.New("OPS authority requires matching project push and charge-read credentials")
	}
	if strings.TrimRight(os.Getenv("OPS_PUSH_URL"), "/") != strings.TrimRight(os.Getenv("OPS_BILLING_URL"), "/") {
		return errors.New("OPS push and debit reads must use the same service")
	}
	return nil
}

// Separate async task tables prevent an older product worker on the shared
// database from settling a project task against its local wallet.
func TaskTable(product string) string {
	if Enabled() && !Product() {
		return "ops_project_" + product
	}
	return product
}
