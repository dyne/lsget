package lsget

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestHTTPContract(t *testing.T) {
	t.Run("methods", func(t *testing.T) {
		s := newTestServer(t)
		handler := s.httpServer("").Handler
		for _, route := range []struct{ path, method, allow string }{
			{"/api/config?path=/", http.MethodPost, "GET"},
			{"/api/config?path=/", http.MethodHead, "GET"},
			{"/api/exec", http.MethodGet, "POST"},
			{"/api/complete", http.MethodGet, "POST"},
			{"/api/complete", http.MethodOptions, "POST"},
			{"/api/download?dir=/", http.MethodPost, "GET, HEAD"},
			{"/api/static/note.txt", http.MethodPost, "GET, HEAD"},
			{"/sitemap.xml", http.MethodPut, "GET, HEAD"},
			{"/assets/js/marked.min.js", http.MethodDelete, "GET, HEAD"},
			{"/assets/js/datastar.js", http.MethodDelete, "GET, HEAD"},
			{"/?file=note.txt", http.MethodPost, "GET, HEAD"},
			{"/published.html", http.MethodPost, "GET, HEAD"},
		} {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
			if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != route.allow {
				t.Errorf("%s %s: got status %d, Allow %q, want 405 with %q",
					route.method, route.path, w.Code, w.Header().Get("Allow"), route.allow)
			}
		}
		if len(s.sessions) != 0 {
			t.Fatalf("method rejections reached session allocation: %d sessions", len(s.sessions))
		}
	})

	t.Run("json", func(t *testing.T) {
		s := newTestServer(t)
		handler := s.httpServer("").Handler
		for _, api := range []struct{ path, valid, field string }{
			{"/api/exec", `{"input":"help"}`, `"help":true`},
			{"/api/complete", `{"path":"/n","dirsOnly":false,"filesOnly":true,"textOnly":true,"maxSize":4096}`, `"items"`},
		} {
			t.Run(strings.TrimPrefix(api.path, "/api/"), func(t *testing.T) {
				atLimit := api.valid + strings.Repeat(" ", maxCommandBodyBytes-len(api.valid))
				for _, tt := range []struct {
					name, body string
					status     int
					unknown    bool
				}{
					{"originless_cli", api.valid, 200, false},
					{"at_limit", atLimit, 200, false},
					{"empty", "", 400, false},
					{"malformed", "{", 400, false},
					{"null", "null", 400, false},
					{"array", "[]", 400, false},
					{"trailing_object", api.valid + "{}", 400, false},
					{"trailing_text", api.valid + ".", 400, false},
					{"declared_oversize", atLimit + " ", 413, false},
					{"unknown_length_oversize", atLimit + " ", 413, true},
				} {
					t.Run(tt.name, func(t *testing.T) {
						request := httptest.NewRequest(http.MethodPost, api.path, strings.NewReader(tt.body))
						if tt.unknown {
							request.ContentLength = -1
						}
						w := httptest.NewRecorder()
						handler.ServeHTTP(w, request)
						if w.Code != tt.status || tt.status == 200 && !strings.Contains(w.Body.String(), api.field) {
							t.Errorf("HTTP status = %d, want %d with expected success field %q: %s",
								w.Code, tt.status, api.field, w.Body.String())
						}
					})
				}
			})
		}
		if len(s.sessions) != 0 {
			t.Fatalf("stateless commands or invalid requests allocated %d sessions", len(s.sessions))
		}
	})

	t.Run("body_read_timeout", func(t *testing.T) {
		s := newTestServer(t)
		request := httptest.NewRequest(http.MethodPost, "/api/exec", iotest.ErrReader(os.ErrDeadlineExceeded))
		w := httptest.NewRecorder()
		s.httpServer("").Handler.ServeHTTP(w, request)
		if w.Code != http.StatusRequestTimeout || len(s.sessions) != 0 {
			t.Fatalf("body read timeout returned HTTP %d and allocated %d sessions", w.Code, len(s.sessions))
		}
	})

	t.Run("post_origin", testCommandOrigin)
	t.Run("config_origin", testConfigOrigin)
	t.Run("head_and_progressing_range", testProgressingRange)
}

func testCommandOrigin(t *testing.T) {
	s := newTestServer(t)
	for _, tt := range []struct {
		name, target, origin, fetchSite, baseURL string
		hasOrigin, proxyHTTPS                    bool
		want                                     int
	}{
		{"direct_http", "http://files.test/api/exec", "http://FILES.test:80", "", "", true, false, 200},
		{"direct_tls", "https://files.test/api/exec", "https://files.test", "same-origin", "", true, false, 200},
		{"recognized_fetch", "http://files.test/api/exec", "", "same-origin", "", false, false, 200},
		{"canonical_proxy_origin", "http://backend.test/api/exec", "https://public.test:8443", "", "https://public.test:8443", true, false, 200},
		{"configured_https_proxy", "http://public.test/api/exec", "https://public.test", "same-origin", "", true, true, 200},
		{"wrong_scheme_for_https_proxy", "http://public.test/api/exec", "http://public.test", "", "", true, true, 403},
		{"other_host", "http://files.test/api/exec", "http://other.test", "", "", true, false, 403},
		{"other_port", "http://files.test/api/exec", "http://files.test:8080", "", "", true, false, 403},
		{"opaque", "http://files.test/api/exec", "null", "", "", true, false, 403},
		{"empty_origin", "http://files.test/api/exec", "", "", "", true, false, 403},
		{"not_an_origin", "http://files.test/api/exec", "http://files.test/path", "", "", true, false, 403},
		{"same_site", "http://files.test/api/exec", "", "same-site", "", false, false, 403},
		{"cross_site", "http://files.test/api/exec", "", "cross-site", "", false, false, 403},
		{"contradictory_origin", "http://files.test/api/exec", "http://other.test", "same-origin", "", true, false, 403},
		{"contradictory_fetch", "http://files.test/api/exec", "http://files.test", "cross-site", "", true, false, 403},
		{"untrusted_forwarding_headers", "http://files.test/api/exec", "https://public.test", "", "", true, false, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s.baseURL, s.secureSessionCookie = tt.baseURL, tt.proxyHTTPS
			req := httptest.NewRequest(http.MethodPost, tt.target, strings.NewReader(`{"input":"help"}`))
			if tt.hasOrigin {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			req.Header.Set("Forwarded", "proto=https;host=public.test")
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-Host", "public.test")
			w := httptest.NewRecorder()
			s.httpServer("").Handler.ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Errorf("origin check returned %d, want %d: %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

func testConfigOrigin(t *testing.T) {
	s := newTestServer(t)
	if err := os.Mkdir(filepath.Join(s.rootAbs, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	handler := s.httpServer("").Handler
	request := func(method, target, body string, cookie *http.Cookie, headers http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://files.test"+target, strings.NewReader(body))
		if headers != nil {
			req.Header = headers.Clone()
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	first := request(http.MethodGet, "/api/config?path=/", "", nil, nil)
	cookies := first.Result().Cookies()
	if first.Code != http.StatusOK || len(cookies) != 1 {
		t.Fatalf("originless CLI did not create a session: status %d, %d cookies", first.Code, len(cookies))
	}
	cookie := cookies[0]

	for _, tt := range []struct {
		name    string
		headers http.Header
		want    int
	}{
		{"cross_site_path", http.Header{"Sec-Fetch-Site": {"cross-site"}}, 403},
		{"same_site_path", http.Header{"Sec-Fetch-Site": {"same-site"}}, 403},
		{"foreign_origin", http.Header{"Origin": {"http://other.test"}}, 403},
		{"bare_session", nil, 403},
		{"same_origin", http.Header{"Sec-Fetch-Site": {"same-origin"}}, 200},
		{"header_without_fetch_metadata", http.Header{"X-Lsget-Request": {"1"}}, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := request(http.MethodGet, "/api/config?path=/docs", "", cookie, tt.headers)
			if w.Code != tt.want {
				t.Fatalf("GET config returned %d, want %d: %s", w.Code, tt.want, w.Body.String())
			}
			w = request(http.MethodGet, "/api/config", "", cookie, nil)
			cfg := decodeSessionResponse[configResp](t, w)
			wantCWD := "/"
			if tt.want == http.StatusOK {
				wantCWD = "/docs"
			}
			if cfg.CWD != wantCWD || cfg.CatMax != s.catMax || cfg.Readme == nil {
				t.Fatalf("config after request: %+v, want cwd %q and terminal fields", cfg, wantCWD)
			}
		})
	}
	denied := request(http.MethodPost, "/api/complete", `{"path":""}`, cookie, nil)
	if denied.Code != http.StatusForbidden {
		t.Errorf("originless cookie-bearing POST without JSON type returned %d, want 403", denied.Code)
	}
	denied = request(http.MethodPost, "/api/exec", `{"input":"cd /"}`, cookie, http.Header{"Origin": {"http://other.test"}})
	if denied.Code != http.StatusForbidden {
		t.Errorf("foreign Origin POST returned %d, want 403", denied.Code)
	}
	w := request(http.MethodPost, "/api/exec", `{"input":"pwd"}`, cookie, http.Header{"Content-Type": {"application/json"}})
	resp := decodeSessionResponse[execResp](t, w)
	if resp.Output != "/docs" || resp.CWD != "/docs" {
		t.Fatalf("originless CLI JSON POST did not reuse session: %+v", resp)
	}
}

func testProgressingRange(t *testing.T) {
	s := newTestServer(t)
	content := strings.Repeat("sample range fixture\n", 1024)
	if err := os.WriteFile(filepath.Join(s.rootAbs, "note.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	config := s.httpServer("")
	if config.ReadHeaderTimeout != 5*time.Second || config.ReadTimeout != 10*time.Second ||
		config.IdleTimeout != time.Minute || config.WriteTimeout != 0 || config.MaxHeaderBytes != 64*1024 {
		t.Fatalf("unexpected HTTP server limits: header=%s read=%s idle=%s write=%s maxHeader=%d",
			config.ReadHeaderTimeout, config.ReadTimeout, config.IdleTimeout, config.WriteTimeout, config.MaxHeaderBytes)
	}
	// Shorten the read/idle limits in this fixture and pace flushed writes so
	// the response stays active longer than either limit.
	config.ReadTimeout = 250 * time.Millisecond
	config.IdleTimeout = 200 * time.Millisecond
	handler := config.Handler
	config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w = &pacedRangeWriter{ResponseWriter: w, ctx: r.Context()}
		}
		handler.ServeHTTP(w, r)
	})
	testServer := httptest.NewUnstartedServer(config.Handler)
	testServer.Config = config
	testServer.Start()
	t.Cleanup(testServer.Close)
	client := testServer.Client()
	client.Timeout = 5 * time.Second

	const start, end = 11, 16*1024 + 10
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		request, err := http.NewRequestWithContext(t.Context(), method, testServer.URL+"/api/download?path=/note.txt", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		began := time.Now()
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusPartialContent ||
			response.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)) ||
			response.Header.Get("Content-Length") != strconv.Itoa(end-start+1) ||
			response.Header.Get("Accept-Ranges") != "bytes" ||
			response.Header.Get("Content-Disposition") != `attachment; filename="note.txt"` {
			t.Fatalf("%s returned %d with headers %v", method, response.StatusCode, response.Header)
		}
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if method == http.MethodHead {
			if len(data) != 0 {
				t.Fatalf("HEAD returned %d body bytes", len(data))
			}
		} else if string(data) != content[start:end+1] || time.Since(began) <= config.ReadTimeout {
			t.Fatalf("paced range returned %d bytes in %s", len(data), time.Since(began))
		}
	}
}

type pacedRangeWriter struct {
	http.ResponseWriter
	ctx context.Context
}

func (w *pacedRangeWriter) Write(p []byte) (int, error) {
	const chunkSize = 4 * 1024
	var written int
	for len(p) > 0 {
		n, err := w.ResponseWriter.Write(p[:min(len(p), chunkSize)])
		written += n
		if err != nil {
			return written, err
		}
		if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
			return written, err
		}
		p = p[n:]
		if len(p) != 0 {
			select {
			case <-w.ctx.Done():
				return written, w.ctx.Err()
			case <-time.After(120 * time.Millisecond):
			}
		}
	}
	return written, nil
}
