package testutil

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/config"
	"github.com/QuantumNous/new-api/claw-control/internal/database"
	"github.com/QuantumNous/new-api/claw-control/internal/migration"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type SecretResolver map[string]string

func (resolver SecretResolver) Resolve(reference string) (string, error) {
	value, ok := resolver[reference]
	if !ok || value == "" {
		return "", errors.New("test secret is unavailable")
	}
	return value, nil
}

type IdentityVerifier struct {
	mu              sync.RWMutex
	identityVersion string
	err             error
}

func NewIdentityVerifier(identityVersion string) *IdentityVerifier {
	return &IdentityVerifier{identityVersion: identityVersion}
}

func (v *IdentityVerifier) Set(identityVersion string, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.identityVersion = identityVersion
	v.err = err
}

func (v *IdentityVerifier) ResolveEnabled(_ context.Context, _ int64) (string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.identityVersion, v.err
}

func (v *IdentityVerifier) Verify(_ context.Context, _ int64, expectedIdentityVersion string) error {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.err != nil {
		return v.err
	}
	if expectedIdentityVersion != v.identityVersion {
		return fmt.Errorf("identity version changed")
	}
	return nil
}

func (v *IdentityVerifier) VerifyFresh(ctx context.Context, userID int64, expectedIdentityVersion string) error {
	return v.Verify(ctx, userID, expectedIdentityVersion)
}

func (v *IdentityVerifier) VerifyAdmin(ctx context.Context, userID int64, expectedIdentityVersion string) error {
	return v.Verify(ctx, userID, expectedIdentityVersion)
}

func NewDatabase() (*gorm.DB, error) {
	db, err := database.Open(config.Config{
		DBDriver:       "sqlite",
		DBDSN:          fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(1)", uuid.NewString()),
		DBMaxOpenConns: 1, DBMaxIdleConns: 1, DBConnMaxLifetime: time.Minute,
	})
	if err != nil {
		return nil, err
	}
	if err := migration.Migrate(db); err != nil {
		return nil, err
	}
	return db, nil
}
