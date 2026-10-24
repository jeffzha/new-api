package hwdramaproxy

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const materialFilesPath = "/api/v3/material-files"
const maxMaterialImageBytes = 10 << 20
const maxMaterialStorageBytes = 256 << 20

// MaterialFilesConfig is opt-in and must use storage exclusive to this region.
// PublicURL is an HTTPS origin whose material-files path routes to this proxy.
type MaterialFilesConfig struct {
	Directory string
	PublicURL string
	TTL       time.Duration
}

type materialFiles struct {
	directory string
	publicURL string
	ttl       time.Duration
	mu        sync.Mutex
}

func newMaterialFiles(config MaterialFilesConfig) (*materialFiles, error) {
	if config.Directory == "" && config.PublicURL == "" {
		return nil, nil
	}
	base, err := url.Parse(config.PublicURL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, errors.New("material files public URL must be an HTTPS origin without credentials, path, query or fragment")
	}
	if !filepath.IsAbs(config.Directory) {
		return nil, errors.New("material files directory must be absolute")
	}
	if config.TTL == 0 {
		config.TTL = 24 * time.Hour
	}
	if config.TTL < time.Minute || config.TTL > 7*24*time.Hour {
		return nil, errors.New("material files TTL must be between one minute and seven days")
	}
	if err := os.MkdirAll(config.Directory, 0700); err != nil {
		return nil, errors.New("failed to initialize material files directory")
	}
	return &materialFiles{directory: config.Directory, publicURL: strings.TrimRight(config.PublicURL, "/"), ttl: config.TTL}, nil
}

func (p *Proxy) serveMaterialFiles(w http.ResponseWriter, r *http.Request) {
	files := p.materialFiles
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path != materialFilesPath {
		files.serveImage(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	key, ok := NormalizeAPIKey(r.Header.Get("Authorization"))
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "token_invalid", "invalid api key")
		return
	}
	token, err := p.tokenResolver(key)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "database_error", "database error")
		return
	}
	if token == nil {
		writeJSONError(w, http.StatusUnauthorized, "token_invalid", "invalid api key")
		return
	}
	// Upload permission follows the same dedicated token route as CreateAsset.
	router := p.CurrentRouter()
	if router == nil {
		writeJSONError(w, http.StatusForbidden, "upload_not_allowed", "material route is required")
		return
	}
	action, decision := router.LookupAction(http.MethodPost, "/api/v3/open/CreateAsset")
	if !decision.MethodAllowed {
		writeJSONError(w, http.StatusForbidden, "upload_not_allowed", "material route is required")
		return
	}
	route, allowed, err := router.Match(token.ID, "", action)
	if err != nil || !allowed || route.UpstreamAuthType != UpstreamAuthTypeVolcengineAKSK || route.UpstreamAction != "CreateAsset" {
		writeJSONError(w, http.StatusForbidden, "upload_not_allowed", "no material route for this api key")
		return
	}
	// Bound multipart buffering and image decoding across concurrent uploads.
	if !files.mu.TryLock() {
		w.Header().Set("Retry-After", "1")
		writeJSONError(w, http.StatusTooManyRequests, "upload_busy", "another image upload is in progress")
		return
	}
	defer files.mu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, maxMaterialImageBytes+(64<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_upload", "invalid or oversized multipart upload")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil || len(r.MultipartForm.File["file"]) != 1 {
		if file != nil {
			_ = file.Close()
		}
		writeJSONError(w, http.StatusBadRequest, "invalid_upload", "one image file is required")
		return
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
		writeJSONError(w, http.StatusBadRequest, "invalid_upload", "only PNG and JPEG images are supported")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMaterialImageBytes+1))
	if err != nil || len(data) > maxMaterialImageBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "invalid_upload", "image exceeds 10 MiB")
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 || (format != "jpeg" && format != "png") || (format == "png") != (ext == ".png") {
		writeJSONError(w, http.StatusBadRequest, "invalid_upload", "invalid image type or dimensions; maximum is 16 megapixels")
		return
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_upload", "invalid image")
		return
	}
	var clean bytes.Buffer
	if format == "png" {
		err = png.Encode(&clean, img)
	} else {
		ext = ".jpg"
		err = jpeg.Encode(&clean, img, &jpeg.Options{Quality: 95})
	}
	if err != nil || clean.Len() > maxMaterialImageBytes {
		writeJSONError(w, http.StatusBadRequest, "invalid_upload", "normalized image exceeds size limit")
		return
	}
	root, err := os.OpenRoot(files.directory)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "storage_error", "image storage unavailable")
		return
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "storage_error", "image storage unavailable")
		return
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "storage_error", "image storage unavailable")
		return
	}
	var used int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if time.Since(info.ModTime()) >= files.ttl && validMaterialFilename(strings.TrimPrefix(entry.Name(), ".")) {
			if root.Remove(entry.Name()) == nil {
				continue
			}
		}
		used += info.Size()
	}
	if used+int64(clean.Len()) > maxMaterialStorageBytes {
		writeJSONError(w, http.StatusInsufficientStorage, "storage_full", "temporary image storage is full")
		return
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "storage_error", "failed to create image identifier")
		return
	}
	name := hex.EncodeToString(id) + ext
	output, err := root.OpenFile("."+name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "storage_error", "failed to save image")
		return
	}
	defer root.Remove("." + name)
	_, writeErr := output.Write(clean.Bytes())
	closeErr := output.Close()
	if writeErr != nil || closeErr != nil || root.Rename("."+name, name) != nil {
		writeJSONError(w, http.StatusInternalServerError, "storage_error", "failed to save image")
		return
	}
	body, _ := common.Marshal(map[string]any{"data": map[string]any{
		"url":        files.publicURL + materialFilesPath + "/" + name,
		"expires_at": time.Now().Add(files.ttl).UTC().Format(time.RFC3339),
	}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(body)
}

func validMaterialFilename(name string) bool {
	ext := filepath.Ext(name)
	if ext != ".jpg" && ext != ".png" {
		return false
	}
	id := strings.TrimSuffix(name, ext)
	if len(id) != 64 || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (files *materialFiles) serveImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, materialFilesPath+"/")
	if !validMaterialFilename(name) {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(files.directory)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) >= files.ttl {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.HasSuffix(name, ".png") {
		w.Header().Set("Content-Type", "image/png")
	} else {
		w.Header().Set("Content-Type", "image/jpeg")
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}
