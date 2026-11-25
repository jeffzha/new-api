package modelhealth

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const workerStateID = "executor-v1"

const (
	workerCycleLimit = 60 * time.Second
	workerLease      = 75 * time.Second
)

type workerState struct {
	Owner       string   `json:"owner"`
	ExpiresAt   int64    `json:"expires_at"`
	Credentials []string `json:"credentials"`
	Receivers   []string `json:"receivers"`
	BindingMode string   `json:"binding_mode,omitempty"`
}

func (s workerState) active(now int64) bool {
	return s.Owner != "" && s.ExpiresAt > now && s.ExpiresAt-now <= int64(workerLease/time.Second)
}

func (s workerState) hasProbe(p Template, t Target) bool {
	u, err := checkedURL(t.base)
	if err != nil {
		return false
	}
	if s.BindingMode == "platform-target-v2" {
		return slices.Contains(s.Credentials, probeTargetBinding(p, t, u.Host))
	}
	return slices.Contains(s.Credentials, hash(p.Credential, "https://"+u.Host))
}

// Gateway credentials authorize a particular template revision. An unsupported
// model on the same upstream must neither block other models nor inherit their
// readiness. Legacy direct-upstream bindings remain readable during rollout.
func probeTargetBinding(p Template, t Target, host string) string {
	return hash("platform-target-v2", p.Credential, "https://"+host, t.ID, p.Endpoint, templateRevision(p))
}

func receiverBinding(d Destination) string { return hash(d.ID, d.Kind, d.URLRef, d.SignRef) }

// A legacy worker-lock write only serializes queue access. It can also be
// written by the Gateway, so it must never be treated as executor liveness.
func readWorkerState(db *gorm.DB) (workerState, error) {
	return model.ReadModelHealthConfig(db, workerStateID, workerState{})
}

func WorkerAvailable(ctx context.Context) bool {
	s, err := readWorkerState(model.DB.WithContext(ctx))
	return err == nil && s.active(time.Now().Unix())
}

func workerBindings(db *gorm.DB) (workerState, error) {
	s := workerState{}
	if os.Getenv("MODEL_HEALTH_GATEWAY_URL") != "" {
		s.BindingMode = "platform-target-v2"
		var err error
		s.Credentials, err = gatewayProbeBindings(db)
		if err != nil {
			return workerState{}, err
		}
	} else {
		for _, entry := range os.Environ() {
			name, value, _ := strings.Cut(entry, "=")
			if value == "" || !credentialRef.MatchString(name) {
				continue
			}
			if origin := os.Getenv(name + "_ORIGIN"); origin != "" {
				s.Credentials = append(s.Credentials, hash(name, origin))
			}
		}
	}
	receivers, err := destinations(db)
	if err != nil {
		return workerState{}, err
	}
	for _, d := range receivers {
		if validateDestination(os.Getenv(d.URLRef), d.Kind) == nil && (d.SignRef == "" || os.Getenv(d.SignRef) != "") {
			s.Receivers = append(s.Receivers, receiverBinding(d))
		}
	}
	return s, nil
}

func acquireWorker(ctx context.Context, owner string, bindings workerState) (bool, error) {
	owned := false
	err := model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
		s, err := readWorkerState(tx)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		if s.Owner != owner && s.ExpiresAt > now {
			return nil
		}
		bindings.Owner, bindings.ExpiresAt = owner, now+int64(workerLease/time.Second)
		if err := model.SaveModelHealthConfig(tx, workerStateID, "internal", bindings); err != nil {
			return err
		}
		owned = true
		return nil
	})
	return owned && err == nil, err
}

func releaseWorker(ctx context.Context, owner string) error {
	return model.ModelHealthTransaction(ctx, func(tx *gorm.DB) error {
		s, err := readWorkerState(tx)
		if err != nil || s.Owner != owner {
			return err
		}
		return tx.Delete(&model.ModelHealthConfig{}, "id = ?", workerStateID).Error
	})
}

func checkProbeWorker(db *gorm.DB, p Template, t Target, now int64) error {
	s, err := readWorkerState(db)
	if err != nil {
		return err
	}
	if !s.active(now) {
		return invalid("Monitoring worker is disabled")
	}
	if !s.hasProbe(p, t) {
		return invalid("Dedicated probe credential missing or not bound to this upstream")
	}
	return nil
}

func legacyScheduledAll(db *gorm.DB) (bool, error) {
	var options []model.Option
	const enabledKey = "monitor_setting.auto_test_channel_enabled"
	const modeKey = "monitor_setting.channel_test_mode"
	keys := []any{enabledKey, modeKey}
	if err := db.Where(clause.IN{Column: clause.Column{Name: "key"}, Values: keys}).Find(&options).Error; err != nil {
		return false, err
	}
	var enabled bool
	var mode string
	for _, option := range options {
		if option.Key == modeKey {
			mode = option.Value
		} else {
			var err error
			enabled, err = strconv.ParseBool(option.Value)
			if err != nil {
				return false, err
			}
		}
	}
	// Retain the Gateway's legacy environment precedence when the worker is
	// configured with the same channel-test overrides as the Gateway.
	if frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY")); err == nil && frequency > 0 {
		enabled, mode = true, operation_setting.ChannelTestModeScheduledAll
	}
	if override, err := strconv.ParseBool(os.Getenv("CHANNEL_TEST_ENABLED")); err == nil {
		enabled = override
	}
	return enabled && mode != operation_setting.ChannelTestModeAutoBanOnly && mode != operation_setting.ChannelTestModePassiveRecovery, nil
}
