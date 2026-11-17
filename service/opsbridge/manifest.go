package opsbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
)

type DeliveryHealth struct {
	Backlog     int
	NeedsRepair int
}

type SourceManifestSource interface {
	NextOpsSourceManifest(context.Context, int64, DeliveryHealth) (*SourceManifest, error)
}

type SourceManifest struct {
	Version     int64
	PayloadHash string
	RawPayload  string
}

// PublishSourceManifest persists the exact source payload before advancing its
// cursor. TokenOps independently verifies PayloadHash over these JSON bytes.
func (b *Bridge) PublishSourceManifest(ctx context.Context) (bool, error) {
	if b.manifestSource == nil {
		return false, nil
	}
	b.mu.Lock()
	cursor := b.state.ManifestCursor
	b.mu.Unlock()
	manifest, err := b.manifestSource.NextOpsSourceManifest(ctx, cursor, b.DeliveryHealth())
	if err != nil || manifest == nil {
		return false, err
	}
	if manifest.Version <= cursor || manifest.PayloadHash == "" || manifest.RawPayload == "" {
		return true, errors.New("invalid Ops source manifest")
	}
	var payload map[string]any
	if common.Unmarshal([]byte(manifest.RawPayload), &payload) != nil || fmt.Sprint(payload["manifestVersion"]) != strconv.FormatInt(manifest.Version, 10) {
		return true, errors.New("invalid Ops source manifest payload")
	}
	body := map[string]any{"sourceSystem": "new-api", "sourceProof": manifest.PayloadHash, "manifest": payload}
	key := "agency-source-manifest:" + strconv.FormatInt(manifest.Version, 10) + ":" + manifest.PayloadHash
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.ManifestCursor != cursor {
		return true, nil
	}
	if err := b.enqueueLocked("agency-center/source-manifests", key, body, "", ""); err != nil {
		return true, err
	}
	b.state.ManifestCursor = manifest.Version
	return true, b.saveLocked()
}
