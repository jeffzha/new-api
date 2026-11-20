// Package opsbilling reads OPS receipts without changing wallet ownership.
package opsbilling

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/service/opsclient"
)

type Config struct {
	BaseURL, KeyID, Secret string
	Currency               string
	Timeout                time.Duration
}

var Default *Reader

func Currency() string {
	if value := os.Getenv("OPS_BILLING_CURRENCY"); value != "" {
		return value
	}
	return "CNY"
}

func StartConfigured(ctx context.Context) error {
	if opsauthority.Enabled() && !common.LogConsumeEnabled {
		return errors.New("OPS wallet authority requires consumption logging")
	}
	if err := opsauthority.Validate(); err != nil {
		return err
	}
	if mode := os.Getenv("OPS_BILLING_RECEIPTS"); mode != "" && mode != "fees" && mode != "charges" {
		return errors.New("OPS_BILLING_RECEIPTS must be fees or charges")
	}
	if os.Getenv("OPS_BILLING_ENABLED") != "true" {
		if os.Getenv("OPS_BILLING_RECEIPTS") == "charges" {
			return errors.New("OPS charge receipts require OPS_BILLING_ENABLED=true")
		}
		return nil
	}
	c := Config{BaseURL: strings.TrimRight(os.Getenv("OPS_BILLING_URL"), "/"), KeyID: os.Getenv("OPS_BILLING_KEY_ID"), Secret: os.Getenv("OPS_BILLING_SECRET"), Currency: Currency(), Timeout: 5 * time.Second}
	if err := opsclient.ValidateBase(c.BaseURL); err != nil {
		return err
	}
	if c.KeyID == "" || len(c.KeyID) > 160 || len(c.Secret) < 16 {
		return errors.New("OPS billing read credentials missing")
	}
	if c.Currency != "CNY" && c.Currency != "USD" {
		return errors.New("OPS_BILLING_CURRENCY must be CNY or USD")
	}
	if c.Currency == "USD" && os.Getenv("OPS_PUSH_CURRENCY") != "USD" {
		return errors.New("USD wallet reads require an independent USD station")
	}
	Default = New(c)
	Default.restoreSnapshot(ctx, opsauthority.Scope())
	if opsauthority.Enabled() {
		Default.UserIDs = func(ctx context.Context, after, limit int) ([]int, error) {
			var ids []int
			err := model.DB.WithContext(ctx).Model(&model.User{}).Where("id > ?", after).Order("id").Limit(limit).Pluck("id", &ids).Error
			return ids, err
		}
	}
	go Default.Run(ctx)
	common.SysLog("OPS debit and balance reads enabled; wallet ownership unchanged")
	return nil
}
