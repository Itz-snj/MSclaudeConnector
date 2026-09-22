package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:web
var webFS embed.FS

// csp is served on every response. Agent output is untrusted, so scripts are
// same-origin only, inline/remote scripts and objects are forbidden, and the
// page may not be framed or used as a form target.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self' wss: ws:; img-src 'self' data:; font-src 'self'; object-src 'none'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Handler serves the embedded single-page app shell. Unknown non-asset paths
// fall back to index.html so client-side routes survive deep links.
func Handler() (http.Handler, error) {
	fsys, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(index)
	return &spaHandler{
		fsys:  fsys,
		index: index,
		etag:  `"` + hex.EncodeToString(sum[:8]) + `"`,
	}, nil
}

type spaHandler struct {
	fsys  fs.FS
	index []byte
	etag  string
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.Contains(r.URL.Path, "..") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	clean := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")

	if strings.HasPrefix(clean, "assets/") {
		h.serveAsset(w, r, clean)
		return
	}

	if clean != "" && clean != "index.html" {
		if data, err := fs.ReadFile(h.fsys, clean); err == nil {
			h.serveBytes(w, r, clean, data)
			return
		}
	}

	h.serveIndex(w, r)
}

func (h *spaHandler) serveAsset(w http.ResponseWriter, r *http.Request, name string) {
	data, err := fs.ReadFile(h.fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(data))
}

func (h *spaHandler) serveBytes(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(data))
}

func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("ETag", h.etag)
	if r.Header.Get("If-None-Match") == h.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}
