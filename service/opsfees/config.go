package opsfees

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/service/opsclient"
)

type Config struct {
	BaseURL, KeyID, Secret, Connection                                string
	Currency                                                          string
	ActiveInterval, IdleInterval, Timeout, RoundBudget, MinRequestGap time.Duration
	MaxPages                                                          int
	Since                                                             int64
}

func LoadConfig() (Config, error) {
	c := Config{BaseURL: strings.TrimRight(os.Getenv("OPS_FEES_URL"), "/"), KeyID: os.Getenv("OPS_FEES_KEY_ID"), Secret: os.Getenv("OPS_FEES_SECRET"), ActiveInterval: 5 * time.Second, IdleInterval: 5 * time.Minute, Timeout: 5 * time.Second, RoundBudget: 10 * time.Second, MinRequestGap: 3 * time.Second, MaxPages: 3}
	c.Currency = os.Getenv("OPS_FEES_CURRENCY")
	if c.Currency == "" {
		c.Currency = "CNY"
	}
	if c.Currency != "CNY" && c.Currency != "USD" {
		return c, errors.New("invalid OPS_FEES_CURRENCY")
	}
	if err := opsclient.ValidateBase(c.BaseURL); err != nil {
		return c, err
	}
	if c.KeyID == "" || len(c.KeyID) > 160 || len(c.Secret) < 16 {
		return c, errors.New("OPS fee credentials missing")
	}
	for _, entry := range []struct {
		name   string
		dest   *time.Duration
		lo, hi int
	}{
		{"OPS_FEES_ACTIVE_SECONDS", &c.ActiveInterval, 3, 300}, {"OPS_FEES_IDLE_SECONDS", &c.IdleInterval, 60, 3600},
		{"OPS_FEES_TIMEOUT_SECONDS", &c.Timeout, 1, 10}, {"OPS_FEES_REQUEST_GAP_SECONDS", &c.MinRequestGap, 3, 60},
	} {
		if raw := os.Getenv(entry.name); raw != "" {
			v, e := strconv.Atoi(raw)
			if e != nil || v < entry.lo || v > entry.hi {
				return c, errors.New("invalid " + entry.name)
			}
			*entry.dest = time.Duration(v) * time.Second
		}
	}
	if c.IdleInterval < c.ActiveInterval {
		return c, errors.New("OPS fee idle interval must be at least active interval")
	}
	if raw := os.Getenv("OPS_FEES_SINCE"); raw != "" {
		v, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || v < 0 {
			return c, errors.New("invalid OPS_FEES_SINCE")
		}
		c.Since = v
	}
	hash := sha256.Sum256([]byte(c.BaseURL + "\n" + c.KeyID + "\n" + os.Getenv("OPS_FEES_NAMESPACE")))
	if c.Currency == "USD" {
		hash = sha256.Sum256([]byte(c.BaseURL + "\n" + c.KeyID + "\n" + os.Getenv("OPS_FEES_NAMESPACE") + "\nUSD"))
	}
	c.Connection = hex.EncodeToString(hash[:])
	return c, nil
}
