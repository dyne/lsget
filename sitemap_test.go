package main

import (
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func readSitemap(t *testing.T, s *server) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleSitemap(w, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("sitemap response: %d %q", w.Code, w.Body.String())
	}
	return w
}

func TestSitemapServedFromMemory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "served")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report.pdf"), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	const baseURL = "https://files.example.com"
	s := newServer(root, 4096, "", baseURL)
	original := readSitemap(t, s)
	if got := original.Header().Get("Content-Type"); got != "application/xml; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var sitemap struct {
		Locations []string `xml:"url>loc"`
	}
	if err := xml.Unmarshal(original.Body.Bytes(), &sitemap); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(sitemap.Locations, baseURL+"/report.pdf") {
		t.Fatalf("sitemap locations = %v", sitemap.Locations)
	}
	if err := os.WriteFile(filepath.Join(root, "another.txt"), []byte("new file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readSitemap(t, s).Body.String(); got != original.Body.String() {
		t.Fatal("repeated request did not use the cached sitemap")
	}

	if _, err := s.getSitemap(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	refreshed := readSitemap(t, s).Body.String()
	if !strings.Contains(refreshed, baseURL+"/another.txt") {
		t.Fatal("refreshed sitemap missed the new file")
	}
	if _, err := os.Stat(filepath.Join(root, "sitemap.xml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no sitemap file, stat returned %v", err)
	}

	if err := os.Rename(root, root+".offline"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.getSitemap(t.Context(), true); err == nil {
		t.Fatal("refresh unexpectedly succeeded with the served directory offline")
	}
	if got := readSitemap(t, s).Body.String(); got != refreshed {
		t.Fatal("failed refresh replaced the last successful sitemap")
	}
}

func TestSitemapPeriodicRefresh(t *testing.T) {
	root := t.TempDir()
	s := newServer(root, 4096, "", "https://files.example.com")
	s.startSitemapGenerator(t.Context(), 10*time.Millisecond)
	if s.sitemap.Load() == nil {
		t.Fatal("generator did not cache the initial sitemap")
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("periodic refresh never added the new file")
		case <-ticker.C:
			if strings.Contains(readSitemap(t, s).Body.String(), "/new.txt") {
				return
			}
		}
	}
}

func TestSitemapManualFallback(t *testing.T) {
	root := t.TempDir()
	s := newServer(root, 4096, "", "")
	w := httptest.NewRecorder()
	s.handleSitemap(w, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing manual sitemap returned %d, want 404", w.Code)
	}

	const manual = "<urlset><url><loc>https://example.com/manual</loc></url></urlset>"
	filename := filepath.Join(root, "sitemap.xml")
	if err := os.WriteFile(filename, []byte(manual), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readSitemap(t, s).Body.String(); got != manual {
		t.Fatalf("manual sitemap = %q", got)
	}
	configured := newServer(root, 4096, "", "https://files.example.com")
	generated := readSitemap(t, configured).Body.String()
	if !strings.Contains(generated, "<loc>https://files.example.com/</loc>") || strings.Contains(generated, "/sitemap.xml") {
		t.Fatalf("generated sitemap with an existing file = %q", generated)
	}
	file, err := os.ReadFile(filename)
	if err != nil || string(file) != manual {
		t.Fatalf("previous sitemap file changed: %q, %v", file, err)
	}
}
