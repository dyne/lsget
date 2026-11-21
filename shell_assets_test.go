package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellAssets(t *testing.T) {
	s := newTestServer(t)
	handler := s.httpServer("").Handler
	request := func(method, path, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	shell := request(http.MethodGet, "/", "")
	if shell.Code != http.StatusOK || shell.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("shell response: status %d, cache %q", shell.Code, shell.Header().Get("Cache-Control"))
	}
	for _, name := range []string{"marked.min.js", "datastar.js", "terminal.js"} {
		if !strings.Contains(shell.Body.String(), `src="`+shellAssetPath+name+`"`) {
			t.Errorf("missing versioned script %s", name)
		}
	}
	for _, name := range []string{"marked.min.js", "datastar.js", "terminal.js", "terminal-view.js"} {
		t.Run(name, func(t *testing.T) {
			path := shellAssetPath + name
			get := request(http.MethodGet, path, "")
			etag := fmt.Sprintf(`"%x"`, sha256.Sum256(get.Body.Bytes()))
			if get.Code != http.StatusOK || get.Body.Len() == 0 ||
				get.Header().Get("Content-Type") != "application/javascript; charset=utf-8" ||
				get.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
				get.Header().Get("X-Content-Type-Options") != "nosniff" ||
				get.Header().Get("ETag") != etag {
				t.Fatalf("%s: status %d, headers %v, %d bytes", path, get.Code, get.Header(), get.Body.Len())
			}
			head := request(http.MethodHead, path, "")
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != etag ||
				head.Header().Get("Content-Length") != fmt.Sprint(get.Body.Len()) {
				t.Fatalf("HEAD %s: status %d, headers %v, %d bytes", path, head.Code, head.Header(), head.Body.Len())
			}
			cached := request(http.MethodGet, path, etag)
			if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 || cached.Header().Get("ETag") != etag {
				t.Fatalf("conditional GET %s: status %d, headers %v, %d bytes", path, cached.Code, cached.Header(), cached.Body.Len())
			}

			stale := request(http.MethodGet, shellAssetPrefix+strings.Repeat("0", 64)+"/"+name, etag)
			if stale.Code != http.StatusNotFound || stale.Header().Get("Cache-Control") != "no-store" ||
				stale.Header().Get("ETag") != "" {
				t.Fatalf("stale %s: status %d, headers %v", name, stale.Code, stale.Header())
			}
		})
	}
	missing := request(http.MethodGet, shellAssetPath+"missing.js", "")
	if missing.Code != http.StatusNotFound || missing.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing script: status %d, headers %v", missing.Code, missing.Header())
	}
	rejected := request(http.MethodPost, shellAssetPath+"terminal.js", "")
	if rejected.Code != http.StatusMethodNotAllowed || rejected.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("script method check: status %d, headers %v", rejected.Code, rejected.Header())
	}

	public := filepath.Join(s.rootAbs, "assets", "lsget-shell", "public")
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "data.txt"), []byte("published asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	published := request(http.MethodGet, shellAssetPrefix+"public/data.txt", "")
	if published.Code != http.StatusOK || published.Body.String() != "published asset" {
		t.Fatalf("published asset: status %d, body %q", published.Code, published.Body.String())
	}
}
