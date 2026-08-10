package evidence

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"gorm.io/gorm"
)

const (
	formatVersion = 1
	fileMagic     = "CLWEV1\x00\x00"
)

var allowedTypes = map[string]string{
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".txt":  "text/plain",
	".csv":  "text/csv",
	".json": "application/json",
}

type Service struct {
	db       *gorm.DB
	root     string
	aead     cipher.AEAD
	maxBytes int64
	scanner  Scanner
}

type UploadCommand struct {
	CustomerID   *uint64
	Filename     string
	DeclaredMIME string
	Content      io.Reader
	Actor        string
	RequestID    string
	Context      context.Context
}

type Metadata struct {
	EvidenceRef  string  `json:"evidence_ref"`
	CustomerID   *uint64 `json:"customer_id,omitempty"`
	Filename     string  `json:"filename"`
	MIMEType     string  `json:"mime_type"`
	SizeBytes    int64   `json:"size_bytes"`
	ContentHash  string  `json:"content_sha256"`
	Status       string  `json:"status"`
	CreatedBy    string  `json:"created_by"`
	CreatedAtISO string  `json:"created_at"`
}

type Download struct {
	Metadata Metadata
	Content  []byte
}

func New(db *gorm.DB, root string, masterKey []byte, maxBytes int64, scanner Scanner) (*Service, error) {
	if db == nil {
		return nil, errors.New("evidence database is required")
	}
	if len(masterKey) != 32 {
		return nil, errors.New("evidence master key must contain exactly 32 bytes")
	}
	if maxBytes <= 0 || maxBytes > 100<<20 {
		return nil, errors.New("evidence maximum size must be between 1 and 104857600 bytes")
	}
	if scanner == nil {
		return nil, errors.New("evidence malware scanner is required")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("evidence storage root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve evidence storage root: %w", err)
	}
	if err := os.MkdirAll(absoluteRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create evidence storage root: %w", err)
	}
	if err := os.Chmod(absoluteRoot, 0o700); err != nil {
		return nil, fmt.Errorf("secure evidence storage root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve evidence storage root links: %w", err)
	}
	keyCopy := append([]byte(nil), masterKey...)
	block, err := aes.NewCipher(keyCopy)
	clear(keyCopy)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, root: resolvedRoot, aead: aead, maxBytes: maxBytes, scanner: scanner}, nil
}

func (s *Service) MaxUploadBytes() int64 { return s.maxBytes }

func (s *Service) Upload(command UploadCommand) (*Metadata, error) {
	if command.Content == nil {
		return nil, domain.Invalid("evidence file is required")
	}
	filename, canonicalMIME, err := validateFilenameAndMIME(command.Filename, command.DeclaredMIME)
	if err != nil {
		return nil, err
	}
	content, err := io.ReadAll(io.LimitReader(command.Content, s.maxBytes+1))
	if err != nil {
		return nil, domain.Invalid("evidence file is unreadable")
	}
	if len(content) == 0 || int64(len(content)) > s.maxBytes {
		return nil, domain.Invalid("evidence file must contain between 1 and %d bytes", s.maxBytes)
	}
	if err := validateContent(filename, canonicalMIME, content); err != nil {
		return nil, err
	}
	ctx := command.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.scanner.Scan(ctx, content); err != nil {
		return nil, err
	}
	if command.CustomerID != nil {
		var count int64
		if err := s.db.Model(&model.Customer{}).Where("id = ?", *command.CustomerID).Count(&count).Error; err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, domain.NotFound("customer not found")
		}
	}

	publicID := support.PublicID("evidence")
	sum := sha256.Sum256(content)
	contentHash := "sha256:" + hex.EncodeToString(sum[:])
	storageKeyBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, storageKeyBytes); err != nil {
		return nil, err
	}
	storageKey := hex.EncodeToString(storageKeyBytes)
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	aad := evidenceAAD(publicID, command.CustomerID, filename, canonicalMIME, int64(len(content)), contentHash)
	ciphertext := s.aead.Seal(nil, nonce, content, aad)
	encoded := make([]byte, 0, len(fileMagic)+len(nonce)+len(ciphertext))
	encoded = append(encoded, fileMagic...)
	encoded = append(encoded, nonce...)
	encoded = append(encoded, ciphertext...)

	temporaryPath := filepath.Join(s.root, ".tmp-"+storageKey)
	finalPath := filepath.Join(s.root, storageKey+".aead")
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create encrypted evidence object: %w", err)
	}
	cleanupTemporary := true
	defer func() {
		_ = file.Close()
		if cleanupTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := file.Write(encoded); err != nil {
		return nil, fmt.Errorf("write encrypted evidence object: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync encrypted evidence object: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close encrypted evidence object: %w", err)
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return nil, fmt.Errorf("publish encrypted evidence object: %w", err)
	}
	cleanupTemporary = false
	if err := os.Chmod(finalPath, 0o600); err != nil {
		_ = os.Remove(finalPath)
		return nil, fmt.Errorf("secure encrypted evidence object: %w", err)
	}

	object := &model.EvidenceObject{
		PublicID: publicID, CustomerID: command.CustomerID, OriginalFilename: filename,
		MIMEType: canonicalMIME, SizeBytes: int64(len(content)), ContentSHA256: contentHash,
		StorageKey: storageKey, FormatVersion: formatVersion, Status: model.EvidenceStatusActive,
		CreatedBy: strings.TrimSpace(command.Actor),
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(object).Error; err != nil {
			return err
		}
		return support.Audit(tx, command.CustomerID, command.Actor, "evidence.upload", "evidence", publicID, nil, project(object), "", command.RequestID)
	})
	if err != nil {
		_ = os.Remove(finalPath)
		return nil, err
	}
	metadata := project(object)
	return &metadata, nil
}

func (s *Service) Get(evidenceRef string) (*Download, error) {
	evidenceRef = strings.TrimSpace(evidenceRef)
	if evidenceRef == "" {
		return nil, domain.Invalid("evidence_ref is required")
	}
	var object model.EvidenceObject
	if err := s.db.Where("public_id = ? AND status = ?", evidenceRef, model.EvidenceStatusActive).First(&object).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.NotFound("evidence not found")
		}
		return nil, err
	}
	if object.FormatVersion != formatVersion || len(object.StorageKey) != 64 {
		return nil, errors.New("evidence metadata is invalid")
	}
	for _, value := range object.StorageKey {
		if !strings.ContainsRune("0123456789abcdef", value) {
			return nil, errors.New("evidence metadata is invalid")
		}
	}
	filePath := filepath.Join(s.root, object.StorageKey+".aead")
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("read encrypted evidence object: %w", err)
	}
	defer file.Close()
	minimumSize := len(fileMagic) + s.aead.NonceSize() + s.aead.Overhead()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < int64(minimumSize) || info.Size() > s.maxBytes+int64(minimumSize) {
		return nil, errors.New("encrypted evidence object is invalid")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, s.maxBytes+int64(minimumSize)+1))
	if err != nil {
		return nil, fmt.Errorf("read encrypted evidence object: %w", err)
	}
	if len(encoded) < minimumSize || int64(len(encoded)) > s.maxBytes+int64(minimumSize) || !bytes.Equal(encoded[:len(fileMagic)], []byte(fileMagic)) {
		return nil, errors.New("encrypted evidence object is invalid")
	}
	nonceStart := len(fileMagic)
	nonceEnd := nonceStart + s.aead.NonceSize()
	aad := evidenceAAD(object.PublicID, object.CustomerID, object.OriginalFilename, object.MIMEType, object.SizeBytes, object.ContentSHA256)
	content, err := s.aead.Open(nil, encoded[nonceStart:nonceEnd], encoded[nonceEnd:], aad)
	if err != nil {
		return nil, errors.New("encrypted evidence object failed authentication")
	}
	if int64(len(content)) != object.SizeBytes {
		return nil, errors.New("evidence object size does not match its metadata")
	}
	sum := sha256.Sum256(content)
	if "sha256:"+hex.EncodeToString(sum[:]) != object.ContentSHA256 {
		return nil, errors.New("evidence object hash does not match its metadata")
	}
	return &Download{Metadata: project(&object), Content: content}, nil
}

func (s *Service) GetForAuthorizedDownload(evidenceRef, actor, requestID string) (*Download, error) {
	download, err := s.Get(evidenceRef)
	if err != nil {
		return nil, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return support.Audit(tx, download.Metadata.CustomerID, actor, "evidence.download", "evidence", download.Metadata.EvidenceRef,
			nil, map[string]string{"evidence_ref": download.Metadata.EvidenceRef, "content_sha256": download.Metadata.ContentHash}, "", requestID)
	})
	if err != nil {
		return nil, err
	}
	return download, nil
}

func (s *Service) List(customerID *uint64, limit int) ([]Metadata, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := s.db.Where("status = ?", model.EvidenceStatusActive).Order("id desc").Limit(limit)
	if customerID != nil {
		query = query.Where("customer_id = ?", *customerID)
	}
	var objects []model.EvidenceObject
	if err := query.Find(&objects).Error; err != nil {
		return nil, err
	}
	result := make([]Metadata, 0, len(objects))
	for index := range objects {
		result = append(result, project(&objects[index]))
	}
	return result, nil
}

func validateFilenameAndMIME(filename, declaredMIME string) (string, string, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" || len(filename) > 255 || !utf8.ValidString(filename) || strings.ContainsAny(filename, "\x00\r\n") {
		return "", "", domain.Invalid("evidence filename is invalid")
	}
	normalizedSeparators := strings.ReplaceAll(filename, "\\", "/")
	if path.Base(normalizedSeparators) != normalizedSeparators || normalizedSeparators == "." || normalizedSeparators == ".." {
		return "", "", domain.Invalid("evidence filename must not contain a path")
	}
	extension := strings.ToLower(path.Ext(filename))
	canonicalMIME, supported := allowedTypes[extension]
	if !supported {
		return "", "", domain.Invalid("evidence file extension is not supported")
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(declaredMIME))
	if err != nil || !strings.EqualFold(mediaType, canonicalMIME) {
		return "", "", domain.Invalid("evidence MIME type does not match its extension")
	}
	return filename, canonicalMIME, nil
}

func validateContent(filename, canonicalMIME string, content []byte) error {
	extension := strings.ToLower(path.Ext(filename))
	sniffed := http.DetectContentType(content)
	switch extension {
	case ".pdf":
		if !bytes.HasPrefix(content, []byte("%PDF-")) || sniffed != canonicalMIME {
			return domain.Invalid("evidence content is not a valid PDF signature")
		}
	case ".png", ".jpg", ".jpeg", ".webp":
		if sniffed != canonicalMIME {
			return domain.Invalid("evidence image content does not match its declared type")
		}
	case ".txt", ".csv":
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 || !strings.HasPrefix(sniffed, "text/plain") {
			return domain.Invalid("evidence text content is invalid")
		}
	case ".json":
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
			return domain.Invalid("evidence JSON content is invalid")
		}
		var value any
		if err := jsonx.Unmarshal(content, &value); err != nil {
			return domain.Invalid("evidence JSON content is invalid")
		}
	}
	return nil
}

func evidenceAAD(publicID string, customerID *uint64, filename, mimeType string, size int64, contentHash string) []byte {
	customer := "account_only"
	if customerID != nil {
		customer = fmt.Sprintf("customer:%d", *customerID)
	}
	return []byte(strings.Join([]string{fileMagic, publicID, customer, filename, mimeType, fmt.Sprintf("%d", size), contentHash}, "\n"))
}

func project(object *model.EvidenceObject) Metadata {
	return Metadata{
		EvidenceRef: object.PublicID, CustomerID: object.CustomerID, Filename: object.OriginalFilename,
		MIMEType: object.MIMEType, SizeBytes: object.SizeBytes, ContentHash: object.ContentSHA256,
		Status: object.Status, CreatedBy: object.CreatedBy, CreatedAtISO: object.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
	}
}
