// Package opsauthority contains process-local project accounting ownership.
// It never changes shared user settings or a product service's billing mode.
package opsauthority

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

func Enabled() bool { return os.Getenv("OPS_WALLET_AUTHORITY") == "ops" }

// Native funding is explicit and independent from project accounting. CNY
// project deployments retain their existing authority contract.
func USDFundingEnabled() bool {
	return os.Getenv("OPS_WALLET_FUNDING_ENABLED") == "true" && os.Getenv("OPS_BILLING_CURRENCY") == "USD" && os.Getenv("OPS_PUSH_CURRENCY") == "USD"
}
func Scope() string {
	if Enabled() {
		return os.Getenv("OPS_PROJECT_SCOPE")
	}
	return ""
}

// TaskRequestID matches the shared project contract: one terminal charge is
// distinct from all submission and adjustment log records.
func TaskRequestID(original string) string {
	return fmt.Sprintf("pt-%x", sha256.Sum256([]byte(Scope()+"\x00"+original)))
}
func Validate() error {
	mode := os.Getenv("OPS_WALLET_AUTHORITY")
	if mode != "" && mode != "gateway" && mode != "ops" {
		return errors.New("invalid OPS_WALLET_AUTHORITY")
	}
	if !Enabled() {
		return nil
	}
	for _, name := range []string{"OPS_ENFORCE_CALLS", "OPS_CN_FINANCE_V2_ENABLED", "OPS_CN_DURABLE_DIRECT_WALLET_ENABLED", "OPS_CN_DURABLE_WALLET_ENABLED", "OPS_CN_DURABLE_SUBSCRIPTION_ENABLED", "OPS_CN_DURABLE_TASK_ENABLED", "OPS_CN_DURABLE_MIDJOURNEY_ENABLED", "OPS_CN_WALLET_VALUE_ENABLED"} {
		if os.Getenv(name) == "true" {
			return fmt.Errorf("OPS wallet authority cannot run with %s", name)
		}
	}
	if os.Getenv("OPS_BILLING_CURRENCY") != "USD" || !USDFundingEnabled() {
		return errors.New("overseas OPS authority requires explicit USD billing and native funding")
	}
	if os.Getenv("OPS_USER_SYNC_ENABLED") != "true" || os.Getenv("OPS_USER_SYNC_BALANCES_ENABLED") != "false" {
		return errors.New("OPS custody requires profile sync without source wallet snapshots")
	}
	if os.Getenv("BATCH_UPDATE_ENABLED") == "true" || os.Getenv("AGENCY_CHARGE_CORRECTION_BACKFILL_ENABLED") == "true" {
		return errors.New("OPS custody cannot run with legacy wallet batch or correction writers")
	}
	if os.Getenv("OPS_BILLING_SECRET") != os.Getenv("OPS_PUSH_SECRET") || len(os.Getenv("OPS_PUSH_SECRET")) < 16 {
		return errors.New("project OPS credentials do not match")
	}
	id, err := strconv.Atoi(os.Getenv("OPS_PUSH_STATE_ID"))
	if err != nil || id < 2 || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`).MatchString(Scope()) {
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

// Existing overseas tasks must retain their tables. All old money writers must
// be drained before custody changes; a new prefix would strand live tasks.
func TaskTable(product string) string {
	return product
}
