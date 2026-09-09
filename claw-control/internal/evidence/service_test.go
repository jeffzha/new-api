package evidence_test

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var cleanScanner = evidence.ScannerFunc(func(context.Context, []byte) error { return nil })

func TestEncryptedEvidenceRoundTripAndSafeProjection(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	root := t.TempDir()
	service, err := evidence.New(db, root, bytes.Repeat([]byte{0x42}, 32), 1024, cleanScanner)
	require.NoError(t, err)
	customer := model.Customer{CustomerCode: "evidence-customer", DisplayName: "Evidence customer", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&customer).Error)
	content := []byte(`{"invoice":"INV-1","amount":"12.30"}`)

	metadata, err := service.Upload(evidence.UploadCommand{
		CustomerID: &customer.ID, Filename: "console-export.json", DeclaredMIME: "application/json",
		Content: bytes.NewReader(content), Actor: "reviewer", RequestID: "req-evidence",
	})
	require.NoError(t, err)
	assert.Regexp(t, `^evidence_[0-9a-f]{32}$`, metadata.EvidenceRef)
	assert.Equal(t, "sha256:367ae4a291d71dfd3149360932034040fc0c1c0d352b465be1a7f48a06ad0297", metadata.ContentHash)

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	encrypted, err := os.ReadFile(root + string(os.PathSeparator) + entries[0].Name())
	require.NoError(t, err)
	assert.NotContains(t, string(encrypted), string(content))
	if runtime.GOOS != "windows" {
		info, statErr := entries[0].Info()
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	download, err := service.Get(metadata.EvidenceRef)
	require.NoError(t, err)
	assert.Equal(t, content, download.Content)
	serialized, err := jsonx.Marshal(download.Metadata)
	require.NoError(t, err)
	assert.NotContains(t, string(serialized), root)
	assert.NotContains(t, string(serialized), entries[0].Name())
}

func TestEvidenceRejectsTypeAndSizeBoundary(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	service, err := evidence.New(db, t.TempDir(), bytes.Repeat([]byte{0x21}, 32), 16, cleanScanner)
	require.NoError(t, err)

	_, err = service.Upload(evidence.UploadCommand{Filename: "report.pdf", DeclaredMIME: "application/pdf", Content: strings.NewReader("not a pdf")})
	assert.ErrorContains(t, err, "valid PDF")
	_, err = service.Upload(evidence.UploadCommand{Filename: "report.exe", DeclaredMIME: "application/octet-stream", Content: strings.NewReader("MZ")})
	assert.ErrorContains(t, err, "extension")
	_, err = service.Upload(evidence.UploadCommand{Filename: "report.txt", DeclaredMIME: "text/plain", Content: strings.NewReader(strings.Repeat("a", 17))})
	assert.ErrorContains(t, err, "between 1 and 16")
	_, err = service.Upload(evidence.UploadCommand{Filename: "../report.txt", DeclaredMIME: "text/plain", Content: strings.NewReader("safe")})
	assert.ErrorContains(t, err, "must not contain a path")
}

func TestEvidenceRemovesPublishedFileWhenDatabaseWriteFails(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	root := t.TempDir()
	service, err := evidence.New(db, root, bytes.Repeat([]byte{0x61}, 32), 1024, cleanScanner)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = service.Upload(evidence.UploadCommand{Filename: "proof.txt", DeclaredMIME: "text/plain", Content: strings.NewReader("proof")})
	assert.Error(t, err)
	entries, readErr := os.ReadDir(root)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestEvidenceFailsClosedBeforePersistenceWhenMalwareScanIsNotClean(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	root := t.TempDir()
	scannerCalled := false
	service, err := evidence.New(db, root, bytes.Repeat([]byte{0x62}, 32), 1024, evidence.ScannerFunc(func(context.Context, []byte) error {
		scannerCalled = true
		return domain.Unavailable("scanner unavailable")
	}))
	require.NoError(t, err)

	_, err = service.Upload(evidence.UploadCommand{Filename: "proof.txt", DeclaredMIME: "text/plain", Content: strings.NewReader("proof")})
	assert.ErrorContains(t, err, "scanner unavailable")
	assert.True(t, scannerCalled)
	entries, readErr := os.ReadDir(root)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "unscanned plaintext or ciphertext must never be persisted")
	var count int64
	require.NoError(t, db.Model(&model.EvidenceObject{}).Count(&count).Error)
	assert.Zero(t, count)
}
