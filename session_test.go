package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionConcurrency(t *testing.T) {
	s := newServer(t.TempDir(), 4*1024, "", "")
	for dir, label := range map[string]string{"/": "root", "/step": "first", "/step/step": "second", "/other": "other"} {
		name := filepath.Join(s.rootAbs, dir)
		if err := os.MkdirAll(name, 0o755); err != nil {
			t.Fatal(err)
		}
		for file, contents := range map[string]string{"README.txt": dir, "item-" + label + ".txt": label} {
			if err := os.WriteFile(filepath.Join(name, file), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/exec", s.handleExec)
	mux.HandleFunc("/api/complete", s.handleComplete)
	mux.HandleFunc("/api/download", s.handleDownload)
	request := func(w http.ResponseWriter, target, body string, cookie *http.Cookie) {
		method := http.MethodGet
		if body != "" {
			method = http.MethodPost
		}
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		mux.ServeHTTP(w, r)
	}
	newClient := func(t *testing.T, cwd string) *http.Cookie {
		t.Helper()
		w := httptest.NewRecorder()
		request(w, "/api/config?path="+cwd, "", nil)
		cfg := decodeSessionResponse[configResp](t, w)
		cookies := w.Result().Cookies()
		if cfg.CWD != cwd || len(cookies) != 1 || cookies[0].Name != "sid" {
			t.Fatalf("creating session at %q: config %+v, cookies %v", cwd, cfg, cookies)
		}
		return cookies[0]
	}
	const (
		cd       = `{"input":"cd step"}`
		complete = `{"path":"item-"}`
		download = "/api/download?pattern=item-*.txt"
	)

	t.Run("same_cookie", func(t *testing.T) {
		cookie := newClient(t, "/")
		start := make(chan struct{})
		release := sync.OnceFunc(func() { close(start) })
		defer release()
		type job struct {
			target, body string
			response     *httptest.ResponseRecorder
		}
		jobs := []job{
			{target: "/api/exec", body: cd},
			{target: "/api/exec", body: cd},
			{target: "/api/config"},
			{target: "/api/complete", body: complete},
			{target: download},
		}
		ready := make(chan struct{}, len(jobs))
		done := make(chan struct{}, len(jobs))
		for i := range jobs {
			jobs[i].response = httptest.NewRecorder()
			j := jobs[i]
			go func() {
				defer func() { done <- struct{}{} }()
				ready <- struct{}{}
				<-start
				request(j.response, j.target, j.body, cookie)
			}()
		}
		remaining := len(jobs)
		t.Cleanup(func() {
			for range remaining {
				awaitSessionSignal(t, done, "request cleanup")
			}
		})
		for range jobs {
			awaitSessionSignal(t, ready, "request readiness")
		}
		t.Log("five same-cookie requests ready before release")
		release()
		for remaining > 0 {
			awaitSessionSignal(t, done, "request completion")
			remaining--
		}
		t.Log("all five same-cookie requests settled")

		var changed []string
		for _, j := range jobs[:2] {
			resp := decodeSessionResponse[execResp](t, j.response)
			if resp.Readme == nil || *resp.Readme != resp.CWD {
				t.Fatalf("cd returned mismatched CWD and README: %+v", resp)
			}
			changed = append(changed, resp.CWD)
		}
		sort.Strings(changed)
		if changed[0] != "/step" || changed[1] != "/step/step" {
			t.Fatalf("relative cd results %q; want /step and /step/step", changed)
		}
		cfg := decodeSessionResponse[configResp](t, jobs[2].response)
		if cfg.Readme == nil || *cfg.Readme != cfg.CWD || (cfg.CWD != "/" && cfg.CWD != "/step" && cfg.CWD != "/step/step") {
			t.Fatalf("config returned mismatched CWD and README: %+v", cfg)
		}
		items := decodeSessionResponse[completeResp](t, jobs[3].response).Items
		if len(items) != 1 || items[0].Dir || !isSharedSessionFile(items[0].Name) {
			t.Fatalf("completion returned unexpected items: %+v", items)
		}
		if name := sessionArchiveFile(t, jobs[4].response); !isSharedSessionFile(name) {
			t.Fatalf("download returned unexpected file: %s", name)
		}
	})

	t.Run("independent_cookies", func(t *testing.T) {
		first := newClient(t, "/step")
		second := newClient(t, "/other")
		if first.Value == second.Value {
			t.Fatal("two clients received the same session")
		}
		archive := &sessionArchiveWriter{
			ResponseRecorder: httptest.NewRecorder(),
			started:          make(chan struct{}),
			resume:           make(chan struct{}),
		}
		release := sync.OnceFunc(func() { close(archive.resume) })
		archived := make(chan struct{})
		go func() {
			defer close(archived)
			request(archive, download, "", first)
		}()
		var otherDone <-chan struct{}
		t.Cleanup(func() {
			release()
			awaitSessionSignal(t, archived, "held archive cleanup")
			if otherDone != nil {
				awaitSessionSignal(t, otherDone, "overlapping requests cleanup")
			}
			t.Log("archive gate released and all requests settled")
		})
		awaitSessionSignal(t, archive.started, "first client's archive write")
		t.Log("first client's archive write is held")

		changed, completed := httptest.NewRecorder(), httptest.NewRecorder()
		finished := make(chan struct{})
		otherDone = finished
		go func() {
			defer close(finished)
			request(completed, "/api/complete", complete, second)
			request(changed, "/api/exec", cd, first)
		}()
		awaitSessionSignal(t, finished, "cd and second client's completion during archive write")
		if resp := decodeSessionResponse[execResp](t, changed); resp.CWD != "/step/step" {
			t.Fatalf("first client's CWD after cd: %q", resp.CWD)
		}
		if items := decodeSessionResponse[completeResp](t, completed).Items; len(items) != 1 || items[0].Name != "item-other.txt" {
			t.Fatalf("second client's completion: %+v", items)
		}
		release()
		awaitSessionSignal(t, archived, "first client's archive completion")
		if name := sessionArchiveFile(t, archive.ResponseRecorder); name != "item-first.txt" {
			t.Fatalf("first client's in-flight archive returned %q", name)
		}
	})
}

func decodeSessionResponse[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func isSharedSessionFile(name string) bool {
	return name == "item-root.txt" || name == "item-first.txt" || name == "item-second.txt"
}

func sessionArchiveFile(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download status %d, content type %q", w.Code, w.Header().Get("Content-Type"))
	}
	archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 {
		t.Fatalf("archive entries: %d; want one", len(archive.File))
	}
	entry := archive.File[0]
	r, err := entry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	if err != nil || entry.Name != "item-"+string(data)+".txt" {
		t.Fatalf("incoherent archive entry %q: %q, %v", entry.Name, data, err)
	}
	return entry.Name
}

type sessionArchiveWriter struct {
	*httptest.ResponseRecorder
	started, resume chan struct{}
	wrote           bool
}

func (w *sessionArchiveWriter) Write(data []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
		close(w.started)
		<-w.resume
	}
	return w.ResponseRecorder.Write(data)
}

func awaitSessionSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func TestSessionLifetime(t *testing.T) {
	newFixture := func(t *testing.T) (*server, func(time.Duration)) {
		t.Helper()
		s := newServer(t.TempDir(), 4*1024, "", "https://files.example")
		if err := os.Mkdir(filepath.Join(s.rootAbs, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.rootAbs, "docs", "sample.txt"), []byte("sample document"), 0o644); err != nil {
			t.Fatal(err)
		}
		start := time.Unix(1_700_000_000, 0)
		var elapsed atomic.Int64
		s.sessionNow = func() time.Time { return start.Add(time.Duration(elapsed.Load())) }
		return s, func(d time.Duration) { elapsed.Add(int64(d)) }
	}
	request := func(handler http.HandlerFunc, target, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		method := http.MethodGet
		if body != "" {
			method = http.MethodPost
		}
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	checkCWD := func(t *testing.T, w *httptest.ResponseRecorder, want string) {
		t.Helper()
		if got := decodeSessionResponse[configResp](t, w).CWD; got != want {
			t.Errorf("CWD %q, want %q", got, want)
		}
	}
	cookieFrom := func(t *testing.T, w *httptest.ResponseRecorder, secure bool) *http.Cookie {
		t.Helper()
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("got %d session cookies, want 1", len(cookies))
		}
		c := cookies[0]
		if c.Name != "sid" || c.Value == "" || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure != secure {
			t.Fatalf("unexpected session cookie attributes: %+v", c)
		}
		return c
	}
	newClient := func(t *testing.T, s *server) *http.Cookie {
		t.Helper()
		w := request(s.handleConfig, "/api/config?path=/docs", "", nil)
		checkCWD(t, w, "/docs")
		return cookieFrom(t, w, false)
	}
	checkReused := func(t *testing.T, s *server, c *http.Cookie) {
		t.Helper()
		w := request(s.handleConfig, "/api/config", "", c)
		checkCWD(t, w, "/docs")
		if v := w.Header().Get("Set-Cookie"); v != "" {
			t.Fatalf("existing client received a replacement cookie: %s", v)
		}
	}

	t.Run("idle_renewal_and_recovery", func(t *testing.T) {
		s, advance := newFixture(t)
		first := newClient(t, s)
		advance(sessionIdleTimeout - time.Minute)
		checkReused(t, s, first)
		advance(sessionIdleTimeout - time.Minute)
		w := request(s.handleDownload, "/api/download?path=/docs/sample.txt", "", first)
		if w.Code != http.StatusOK || w.Body.String() != "sample document" || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("stateless download returned %d, %q, %v", w.Code, w.Body, w.Header())
		}
		advance(time.Minute)
		w = request(s.handleConfig, "/api/config", "", first)
		checkCWD(t, w, "/")
		if replacement := cookieFrom(t, w, false); replacement.Value == first.Value || len(s.sessions) != 1 {
			t.Fatalf("expired session was not replaced, map size %d", len(s.sessions))
		}
		advance(sessionIdleTimeout)
		s.expireSessions()
		if len(s.sessions) != 0 {
			t.Fatalf("sweep retained %d expired sessions", len(s.sessions))
		}
	})

	t.Run("capacity_evicts_longest_idle", func(t *testing.T) {
		s, advance := newFixture(t)
		s.sessionLimit = 2
		first := newClient(t, s)
		advance(time.Minute)
		second := newClient(t, s)
		advance(time.Minute)
		checkReused(t, s, first)
		advance(time.Minute)
		third := newClient(t, s)
		if len(s.sessions) != 2 || s.sessions[first.Value] == nil || s.sessions[second.Value] != nil || s.sessions[third.Value] == nil {
			t.Fatalf("capacity did not evict the longest-idle session, map size %d", len(s.sessions))
		}
	})

	t.Run("in_flight_session_keeps_its_slot", func(t *testing.T) {
		s, advance := newFixture(t)
		s.sessionLimit = 1
		first := newClient(t, s)
		archive := &sessionArchiveWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), resume: make(chan struct{})}
		release := sync.OnceFunc(func() { close(archive.resume) })
		done := make(chan struct{})
		r := httptest.NewRequest(http.MethodGet, "/api/download?pattern=*.txt", nil)
		r.AddCookie(first)
		go func() {
			defer close(done)
			s.handleDownload(archive, r)
		}()
		checked := make(chan struct{})
		waitForChecks := false
		t.Cleanup(func() {
			release()
			awaitSessionSignal(t, done, "busy session cleanup")
			if waitForChecks {
				awaitSessionSignal(t, checked, "competing requests cleanup")
			}
			t.Log("archive gate released and competing requests settled")
		})
		awaitSessionSignal(t, archive.started, "held session archive write")
		t.Log("archive write held an in-flight session")
		advance(sessionIdleTimeout + time.Minute)
		var reused, busy, direct *httptest.ResponseRecorder
		waitForChecks = true
		go func() {
			defer close(checked)
			s.expireSessions()
			reused = request(s.handleConfig, "/api/config", "", first)
			busy = request(s.handleConfig, "/api/config", "", nil)
			direct = request(s.handleDownload, "/api/download?path=/docs/sample.txt", "", nil)
		}()
		awaitSessionSignal(t, checked, "competing requests during archive write")
		checkCWD(t, reused, "/docs")
		if reused.Header().Get("Set-Cookie") != "" || busy.Code != http.StatusServiceUnavailable || busy.Header().Get("Retry-After") != "1" || busy.Header().Get("Set-Cookie") != "" {
			t.Fatalf("busy session was not preserved: same-client headers %v, newcomer %d %v", reused.Header(), busy.Code, busy.Header())
		}
		if direct.Code != http.StatusOK || direct.Body.String() != "sample document" || direct.Header().Get("Set-Cookie") != "" {
			t.Fatalf("stateless download during saturation returned %d, %q, %v", direct.Code, direct.Body, direct.Header())
		}
		if len(s.sessions) != 1 || s.sessions[first.Value] == nil {
			t.Fatalf("busy session map size %d, want one original session", len(s.sessions))
		}
		advance(sessionIdleTimeout)
		release()
		awaitSessionSignal(t, done, "archive after release")
		if archive.Code != http.StatusOK || archive.Header().Get("Content-Type") != "application/zip" {
			t.Fatalf("original archive returned %d %v", archive.Code, archive.Header())
		}
		advance(sessionIdleTimeout - time.Minute)
		s.expireSessions()
		if len(s.sessions) != 1 {
			t.Fatal("idle timeout did not start when the last request finished")
		}
		advance(time.Minute)
		s.expireSessions()
		if len(s.sessions) != 0 {
			t.Fatal("session retained after becoming idle")
		}
	})

	t.Run("stateless_requests", func(t *testing.T) {
		s, _ := newFixture(t)
		tests := []struct {
			handler               http.HandlerFunc
			target, body, content string
			status                int
		}{
			{s.handleDownload, "/api/download?path=/docs/sample.txt", "", "sample document", 200},
			{s.handleDownload, "/api/download?dir=/docs", "", "docs/sample.txt", 200},
			{s.handleDownload, "/api/download?pattern=*.txt&cwd=/docs", "", "sample.txt", 200},
			{s.handleDownload, "/api/download?pattern=/docs/*.txt", "", "sample.txt", 200},
			{s.handleDownload, "/api/download", "", "missing download parameters", 400},
			{s.handleDownload, "/api/download?path=/missing", "", "404", 404},
			{s.handleComplete, "/api/complete", `{"path":"/docs/sam"}`, "sample.txt", 200},
			{s.handleComplete, "/api/complete", "{", "bad request", 400},
			{s.handleExec, "/api/exec", "{", "bad request", 400},
			{s.handleExec, "/api/exec", `{"input":"help"}`, `"help":true`, 200},
			{s.handleExec, "/api/exec", `{"input":"cat /docs/sample.txt"}`, "sample document", 200},
			{s.handleExec, "/api/exec", `{"input":"get /docs/sample.txt"}`, `"download":`, 200},
			{s.handleExec, "/api/exec", `{"input":"tree /docs"}`, "sample.txt", 200},
			{s.handleExec, "/api/exec", `{"input":"find /docs -type f"}`, "sample.txt", 200},
			{s.handleExec, "/api/exec", `{"input":"url /docs/sample.txt"}`, "files.example/docs/sample.txt", 200},
			{s.handleExec, "/api/exec", `{"input":"grep sample /docs/sample.txt"}`, " document", 200},
			{s.handleExec, "/api/exec", `{"input":"sum /docs/sample.txt"}`, "SHA256", 200},
		}
		for _, tt := range tests {
			w := request(tt.handler, tt.target, tt.body, nil)
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.content) || w.Header().Get("Set-Cookie") != "" || len(s.sessions) != 0 {
				t.Errorf("%s %s got %d, %q, %v, %d sessions", tt.target, tt.body, w.Code, w.Body, w.Header(), len(s.sessions))
			}
		}
	})

	t.Run("secure_cookie_transport", func(t *testing.T) {
		s, _ := newFixture(t)
		for _, tc := range []struct {
			name, url  string
			proxyHTTPS bool
			secure     bool
		}{
			{"forwarding_header_only", "http://backend.local/api/config", false, false},
			{"direct_tls", "https://backend.local/api/config", false, true},
			{"configured_https_proxy", "http://backend.local/api/config", true, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s.secureSessionCookie = tc.proxyHTTPS
				r := httptest.NewRequest(http.MethodGet, tc.url, nil)
				r.Header.Set("X-Forwarded-Proto", "https")
				r.Header.Set("Forwarded", "proto=https")
				w := httptest.NewRecorder()
				s.handleConfig(w, r)
				checkCWD(t, w, "/")
				_ = cookieFrom(t, w, tc.secure)
			})
		}
	})
}
