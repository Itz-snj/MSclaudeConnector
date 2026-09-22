package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	h, err := Handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestDeepLinkServesIndexWithSecurityHeaders(t *testing.T) {
	srv := newServer(t)

	resp, err := http.Get(srv.URL + "/some/deep/route")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "<!DOCTYPE html>") {
		t.Fatal("deep link did not serve index.html")
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "object-src 'none'") {
		t.Fatalf("unexpected CSP: %q", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("expected nosniff, got %q", resp.Header.Get("X-Content-Type-Options"))
	}
	if resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("expected no-referrer, got %q", resp.Header.Get("Referrer-Policy"))
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-cache") {
		t.Fatalf("expected no-cache index, got %q", resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("expected ETag on index")
	}

	// Conditional request should return 304.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	cond, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("conditional get: %v", err)
	}
	cond.Body.Close()
	if cond.StatusCode != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", cond.StatusCode)
	}
}

func TestMissingAssetReturns404(t *testing.T) {
	srv := newServer(t)
	resp, err := http.Get(srv.URL + "/assets/missing.js")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing asset, got %d", resp.StatusCode)
	}
}

func TestNonGetMethodRejected(t *testing.T) {
	srv := newServer(t)
	resp, err := http.Post(srv.URL+"/", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Allow"), "GET") {
		t.Fatalf("expected Allow header, got %q", resp.Header.Get("Allow"))
	}
}

func TestPathTraversalRejected(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rec := httptest.NewRecorder()
	req := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: "/../secret"},
	}
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for traversal, got %d", rec.Code)
	}
}
