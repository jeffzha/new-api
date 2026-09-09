package httpapi

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func adminSPAHandler(assetDir, mountPath string) http.Handler {
	root := filepath.Clean(assetDir)
	mountPath = strings.TrimSuffix(mountPath, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		setAdminAssetSecurityHeaders(w)
		relative := strings.TrimPrefix(r.URL.Path, mountPath)
		relative = strings.TrimPrefix(relative, "/")
		cleaned := path.Clean("/" + relative)
		if cleaned == "/." || cleaned == "/" {
			cleaned = "/index.html"
		}
		candidate := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(cleaned, "/")))
		if rel, err := filepath.Rel(root, candidate); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			http.NotFound(w, r)
			return
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			if path.Ext(cleaned) != "" {
				http.NotFound(w, r)
				return
			}
			cleaned = "/index.html"
			candidate = filepath.Join(root, "index.html")
			info, err = os.Stat(candidate)
			if err != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
		}
		if cleaned == "/index.html" {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		file, err := os.Open(candidate)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		http.ServeContent(w, r, filepath.Base(candidate), info.ModTime(), file)
	})
}

func setAdminAssetSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; object-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}
