package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

func TestSitemapCanonicalLocations(t *testing.T) {
	root := t.TempDir()
	const base = "https://files.example.com:8443/public%20files"
	const directory = "release & notes"
	const encodedDir = "/release%20&%20notes"
	const document = "brief--v2 #%.pdf"
	const image = "figure + & %.png"
	const video = "clip #&-- 50%.mp4"
	files := map[string]string{
		encodedDir + "/brief--v2%20%23%25.pdf":    document,
		encodedDir + "/figure%20+%20&%20%25.png":  image,
		encodedDir + "/clip%20%23&--%2050%25.mp4": video,
	}
	for _, name := range files {
		writeWorkFixture(t, root, directory+"/"+name, []byte(name))
	}
	s := newServer(root, 4096, "", base+"/")
	response := readSitemap(t, s)
	var sitemap struct {
		XMLName xml.Name `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
		URLs    []struct {
			Location  string `xml:"loc"`
			Lastmod   string `xml:"lastmod"`
			Frequency string `xml:"changefreq"`
			Priority  string `xml:"priority"`
			Image     *struct {
				Location string `xml:"loc"`
			} `xml:"http://www.google.com/schemas/sitemap-image/1.1 image"`
			Video *struct {
				Location    string `xml:"content_loc"`
				Title       string `xml:"title"`
				Description string `xml:"description"`
			} `xml:"http://www.google.com/schemas/sitemap-video/1.1 video"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(response.Body.Bytes(), &sitemap); err != nil {
		t.Fatal(err)
	}
	if len(sitemap.URLs) != len(files)+2 {
		t.Fatalf("sitemap has %d locations, want %d", len(sitemap.URLs), len(files)+2)
	}
	seen := make(map[string]bool)
	handler := s.httpServer("").Handler
	for _, entry := range sitemap.URLs {
		location, err := url.Parse(entry.Location)
		if err != nil || location.Scheme != "https" || location.Host != "files.example.com:8443" ||
			location.RawQuery != "" || location.Fragment != "" {
			t.Fatalf("invalid absolute location %q, error %v", entry.Location, err)
		}
		if _, err := time.Parse(time.RFC3339, entry.Lastmod); err != nil {
			t.Errorf("invalid lastmod for %q: %v", entry.Location, err)
		}
		if !strings.HasPrefix(entry.Location, base+"/") || seen[entry.Location] {
			t.Fatalf("location %q escaped its base or was repeated", entry.Location)
		}
		seen[entry.Location] = true
		portablePath := strings.TrimPrefix(entry.Location, base)
		if portablePath == "/" || portablePath == encodedDir {
			if entry.Frequency != "daily" || entry.Priority != "0.8" {
				t.Errorf("directory metadata = %q, %q", entry.Frequency, entry.Priority)
			}
			continue
		}
		name, ok := files[portablePath]
		if !ok || location.Path != "/public files/"+directory+"/"+name {
			t.Fatalf("location %q decoded to %q", entry.Location, location.Path)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, portablePath, nil))
		if w.Code != http.StatusOK || w.Body.String() != name {
			t.Fatalf("round trip for %q returned HTTP %d and %q", entry.Location, w.Code, w.Body.String())
		}
		switch name {
		case document:
			if entry.Frequency != "monthly" || entry.Priority != "0.9" ||
				!strings.Contains(response.Body.String(), "<!-- Document: brief%2D%2Dv2%20%23%25.pdf (") {
				t.Error("document metadata was missing or unsafe for an XML comment")
			}
		case image:
			if entry.Image == nil || entry.Image.Location != entry.Location || entry.Priority != "0.6" {
				t.Errorf("image metadata = %+v", entry.Image)
			}
		case video:
			if entry.Video == nil || entry.Video.Location != entry.Location || entry.Priority != "0.7" ||
				entry.Video.Title != "clip #&-- 50%" || entry.Video.Description != "Video file: "+video {
				t.Errorf("video metadata = %+v", entry.Video)
			}
		}
	}
}

func TestSitemapRejectsInvalidBaseURL(t *testing.T) {
	for _, tc := range []struct{ name, base string }{
		{"relative", "/prefix"},
		{"opaque", "https:prefix"},
		{"unsupported", "ftp://files.example.com/prefix"},
		{"no_host", "https://:8443/prefix"},
		{"bad_port", "https://files.example.com:65536/prefix"},
		{"credentials", "https://name:secret@files.example.com/prefix"},
		{"query", "https://files.example.com/prefix?query=one"},
		{"empty_query", "https://files.example.com/prefix?"},
		{"empty_fragment", "https://files.example.com/prefix#"},
		{"fragment", "https://files.example.com/prefix#part"},
		{"invalid_escape", "https://files.example.com/prefix%zz"},
		{"dot_segment", "https://files.example.com/prefix/%2e%2e/root"},
		{"encoded_slash", "https://files.example.com/prefix%2froot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t.TempDir(), 4096, "", tc.base)
			w := httptest.NewRecorder()
			s.handleSitemap(w, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
			if w.Code != http.StatusInternalServerError || s.sitemap.Load() != nil || len(s.workSlots) != 0 {
				t.Fatalf("invalid base returned HTTP %d, snapshot present=%t, held slots=%d",
					w.Code, s.sitemap.Load() != nil, len(s.workSlots))
			}
		})
	}
}

func TestSitemapConcurrentRefresh(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{
		{"publish", false},
		{"cancel", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t.TempDir(), 4096, "", "https://files.example.com")
			writeWorkFixture(t, s.rootAbs, "previous.txt", []byte("old snapshot"))
			previous := readSitemap(t, s).Body.String()
			before := s.sitemap.Load()
			writeWorkFixture(t, s.rootAbs, "next.txt", []byte("new snapshot"))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			gate := &sitemapRefreshContext{
				Context: ctx,
				slots:   s.workSlots,
				reached: make(chan struct{}),
				release: make(chan struct{}),
			}
			unblock := sync.OnceFunc(func() { close(gate.release) })
			refreshDone := make(chan struct{})
			var result *sitemapSnapshot
			var refreshErr error
			go func() {
				defer close(refreshDone)
				result, refreshErr = s.getSitemap(gate, true)
			}()
			t.Cleanup(func() {
				unblock()
				awaitSessionSignal(t, refreshDone, "refresh cleanup")
			})
			awaitSessionSignal(t, gate.reached, "refresh acquired its work slot")
			if len(s.workSlots) != 1 || s.sitemap.Load() != before {
				t.Fatal("refresh did not hold one slot with the previous snapshot published")
			}

			readDone := make(chan struct{})
			cached := httptest.NewRecorder()
			go func() {
				defer close(readDone)
				req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/sitemap.xml", nil)
				s.httpServer("").Handler.ServeHTTP(cached, req)
			}()
			t.Cleanup(func() {
				unblock()
				awaitSessionSignal(t, readDone, "cached read cleanup")
			})
			awaitSessionSignal(t, readDone, "cached HTTP read while refresh held")
			if cached.Code != http.StatusOK || cached.Body.String() != previous {
				t.Fatalf("cached read during refresh returned HTTP %d, want previous snapshot", cached.Code)
			}
			t.Log("cached HTTP read completed with the previous snapshot while refresh held its work slot")

			if tc.cancel {
				cancel()
			}
			unblock()
			awaitSessionSignal(t, refreshDone, "refresh settled")
			if len(s.workSlots) != 0 {
				t.Fatal("refresh retained its work slot")
			}
			after := readSitemap(t, s).Body.String()
			if tc.cancel {
				if !errors.Is(refreshErr, context.Canceled) || result != nil || s.sitemap.Load() != before || after != previous {
					t.Fatalf("canceled refresh changed the published snapshot or returned %v", refreshErr)
				}
				t.Log("canceled refresh settled, released its slot, and kept the previous snapshot")
				return
			}
			if refreshErr != nil || result == nil || s.sitemap.Load() != result || after != result.content ||
				!strings.Contains(after, "<loc>https://files.example.com/next.txt</loc>") ||
				!strings.Contains(after, "<loc>https://files.example.com/previous.txt</loc>") {
				t.Fatalf("next snapshot was not served after refresh settled: %v", refreshErr)
			}
			var parsed struct {
				Locations []string `xml:"url>loc"`
			}
			if err := xml.Unmarshal([]byte(after), &parsed); err != nil || len(parsed.Locations) != 3 {
				t.Fatalf("new snapshot has %d locations and XML error %v", len(parsed.Locations), err)
			}
			t.Log("refresh settled and released its slot, and the next HTTP read returned complete new XML")
		})
	}
}

type sitemapRefreshContext struct {
	context.Context
	slots   chan struct{}
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (gate *sitemapRefreshContext) Err() error {
	if len(gate.slots) != 0 {
		gate.once.Do(func() {
			close(gate.reached)
			<-gate.release
		})
	}
	return gate.Context.Err()
}

func TestSitemapSnapshotLimit(t *testing.T) {
	root := t.TempDir()
	base := "https://files.example.com/" + strings.Repeat("a", maxSitemapBytes/64)
	s := newServer(root, 4096, "", base)
	previous := readSitemap(t, s).Body.String()
	before := s.sitemap.Load()
	for i := range 64 {
		writeWorkFixture(t, root, fmt.Sprintf("entry%02d.txt", i), nil)
	}
	if _, err := s.getSitemap(t.Context(), true); !errors.Is(err, errWorkLimit) {
		t.Fatalf("oversized refresh returned %v, want work limit", err)
	}
	if s.sitemap.Load() != before || len(s.workSlots) != 0 || readSitemap(t, s).Body.String() != previous {
		t.Fatal("oversized refresh replaced the last good snapshot or retained a slot")
	}
	fresh := newServer(root, 4096, "", base)
	w := httptest.NewRecorder()
	fresh.handleSitemap(w, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
	if w.Code != http.StatusUnprocessableEntity || fresh.sitemap.Load() != nil || len(fresh.workSlots) != 0 ||
		w.Body.String() != errWorkLimit.Error()+"\n" {
		t.Fatalf("oversized first request returned HTTP %d and %d bytes", w.Code, w.Body.Len())
	}
}

func BenchmarkGenerateSitemap(b *testing.B) {
	root := b.TempDir()
	extensions := []string{".txt", ".jpg", ".pdf", ".mp4"}
	addFiles := func(dir string, count int) {
		b.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		for i := range count {
			name := fmt.Sprintf("file-%03d%s", i, extensions[i%len(extensions)])
			if err := os.WriteFile(filepath.Join(dir, name), []byte("sitemap fixture\n"), 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	for i := range 16 {
		addFiles(filepath.Join(root, fmt.Sprintf("wide-%02d", i)), 32)
	}
	dir := root
	for i := range 8 {
		dir = filepath.Join(dir, fmt.Sprintf("deep-%02d", i))
		addFiles(dir, 12)
	}
	s := newServer(root, 4096, "", "https://files.example.com/releases")
	var size int
	b.ReportAllocs()
	for b.Loop() {
		content, err := s.generateSitemap(b.Context())
		if err != nil {
			b.Fatal(err)
		}
		size = len(content)
	}
	b.ReportMetric(float64(size), "snapshot-bytes")
}
