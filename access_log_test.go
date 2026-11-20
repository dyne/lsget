package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var combinedLogLine = regexp.MustCompile(`^(\S+) - - \[[^\]]+\] ("(?:[^"\\]|\\.)*") ([0-9]{3}) (\S+) ("(?:[^"\\]|\\.)*") ("(?:[^"\\]|\\.)*")(.*)$`)

type loggedAccess struct {
	peer, request, status, bytes, referer, agent, outcome string
}

func readAccessLog(t *testing.T, path string) []loggedAccess {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("access log has no complete record: %q", data)
	}
	var records []loggedAccess
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		fields := combinedLogLine.FindStringSubmatch(line)
		if fields == nil {
			t.Fatalf("invalid combined log record %q", line)
		}
		unquote := func(field string) string {
			t.Helper()
			value, err := strconv.Unquote(field)
			if err != nil {
				t.Fatal(err)
			}
			return value
		}
		records = append(records, loggedAccess{fields[1], unquote(fields[2]), fields[3], fields[4], unquote(fields[5]), unquote(fields[6]), fields[7]})
	}
	return records
}

func TestProxyAndAccessLog(t *testing.T) {
	t.Run("share and client provenance", testShareAndClientProvenance)
	t.Run("HTTP outcomes and redaction", testAccessOutcomesAndPrivacy)
	t.Run("cancellation and actual command status", testAccessCancellationAndCommandStatus)
	t.Run("rename rotation and write failure recovery", testAccessLogRotation)
}

func testShareAndClientProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, remote, host, canonical, wantBase, wantPeer string
		forwardedFor, forwardedProto                      []string
		tls                                               bool
	}{
		{name: "direct peer", remote: "198.51.100.7:1234", host: "files.example", wantBase: "http://files.example", wantPeer: "198.51.100.7"},
		{name: "forged direct headers", remote: "198.51.100.7:1234", host: "files.example", forwardedFor: []string{"203.0.113.8"}, forwardedProto: []string{"https"}, wantBase: "http://files.example", wantPeer: "198.51.100.7"},
		{name: "trusted chain ignores spoofed left prefix", remote: "10.0.0.5:1234", host: "files.example", forwardedFor: []string{"192.0.2.10, 203.0.113.9, 10.0.0.8"}, forwardedProto: []string{"https"}, wantBase: "https://files.example", wantPeer: "203.0.113.9"},
		{name: "duplicate and multi-value forwarders", remote: "10.0.0.5:1234", host: "files.example", forwardedFor: []string{"192.0.2.10", "203.0.113.9"}, forwardedProto: []string{"https,http"}, wantBase: "http://files.example", wantPeer: "10.0.0.5"},
		{name: "invalid forwarded address and duplicate scheme", remote: "10.0.0.5:1234", host: "files.example", forwardedFor: []string{"spoofed, 203.0.113.9"}, forwardedProto: []string{"https", "https"}, wantBase: "http://files.example", wantPeer: "10.0.0.5"},
		{name: "oversized chain", remote: "10.0.0.5:1234", host: "files.example", forwardedFor: []string{strings.Repeat("203.0.113.9,", 16) + "203.0.113.8"}, wantBase: "http://files.example", wantPeer: "10.0.0.5"},
		{name: "trusted IPv6", remote: "[2001:db8:1::5]:1234", host: "[2001:db8:2::8]:8443", forwardedFor: []string{"2001:db8:2::4"}, forwardedProto: []string{"https"}, wantBase: "https://[2001:db8:2::8]:8443", wantPeer: "2001:db8:2::4"},
		{name: "direct mapped IPv6 and actual TLS", remote: "[::ffff:198.51.100.7]:1234", host: "[2001:db8:2::8]", forwardedProto: []string{"http"}, tls: true, wantBase: "https://[2001:db8:2::8]", wantPeer: "198.51.100.7"},
		{name: "canonical path has priority", remote: "10.0.0.5:1234", host: "internal.example", canonical: "https://cdn.example/releases%20today/", forwardedFor: []string{"203.0.113.9"}, forwardedProto: []string{"http"}, wantBase: "https://cdn.example/releases%20today", wantPeer: "203.0.113.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			s.baseURL = tc.canonical
			s.trustedProxies, _ = parseTrustedProxies("10.0.0.0/24, 2001:db8:1::/64")
			filename := "/note one.txt"
			writeWorkFixture(t, s.rootAbs, filename[1:], []byte("fixture"))
			s.logfile = filepath.Join(t.TempDir(), "access.log")
			r := httptest.NewRequest(http.MethodPost, "/api/exec", strings.NewReader(`{"input":"share '/note one.txt'"}`))
			r.RemoteAddr, r.Host = tc.remote, tc.host
			r.Header["X-Forwarded-For"] = tc.forwardedFor
			r.Header["X-Forwarded-Proto"] = tc.forwardedProto
			r.Header.Set("X-Forwarded-Host", "spoof.example")
			r.Header.Set("Forwarded", "for=192.0.2.77;proto=https;host=spoof.example")
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			s.httpServer("").Handler.ServeHTTP(w, r)
			var response execResp
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if want := tc.wantBase + "/note%20one.txt"; w.Code != 200 || response.Clipboard != want {
				t.Fatalf("share status/URL = %d %q, want 200 %q", w.Code, response.Clipboard, want)
			}
			records := readAccessLog(t, s.logfile)
			if len(records) != 2 || records[0].peer != tc.wantPeer || records[1].peer != tc.wantPeer ||
				records[0].request != "POST /api/exec?cmd=share&file=%2Fnote+one.txt HTTP/1.1" || records[1].request != "POST /api/exec HTTP/1.1" ||
				records[0].status != "200" || records[1].status != "200" {
				t.Fatalf("unexpected command/access records %+v", records)
			}
		})
	}
}

func TestTrustedProxyConfig(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		args      []string
		want      []netip.Prefix
		wantErr   string
	}{
		{name: "unset"},
		{name: "environment", env: "10.0.0.123/24, 2001:db8:1::/64", want: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("2001:db8:1::/64")}},
		{name: "flag precedence", env: "invalid", args: []string{"-trusted-proxies=127.0.0.1/32"}, want: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}},
		{name: "flag clears environment", env: "10.0.0.0/24", args: []string{"-trusted-proxies="}},
		{name: "invalid environment address", env: "10.0.0.1", wantErr: "LSGET_TRUSTED_PROXIES: invalid trusted proxy CIDR"},
		{name: "invalid flag CIDR", args: []string{"-trusted-proxies=::/129"}, wantErr: "-trusted-proxies: invalid trusted proxy CIDR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseConfig(tc.args, func(key string) string {
				if key == "LSGET_TRUSTED_PROXIES" {
					return tc.env
				}
				return ""
			}, os.Stderr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parse config error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || len(cfg.trustedProxies) != len(tc.want) {
				t.Fatalf("trusted proxies = %v, %v; want %v", cfg.trustedProxies, err, tc.want)
			}
			for i, prefix := range tc.want {
				if cfg.trustedProxies[i] != prefix {
					t.Fatalf("trusted proxies = %v, want %v", cfg.trustedProxies, tc.want)
				}
			}
		})
	}
}

func testAccessOutcomesAndPrivacy(t *testing.T) {
	s := newTestServer(t)
	s.logfile = filepath.Join(t.TempDir(), "access.log")
	writeWorkFixture(t, s.rootAbs, "note.txt", []byte("fixture"))
	h := s.httpServer("").Handler
	agent := "client\"one\\line\nsecond\rline"
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		r := httptest.NewRequest(method, "/note.txt?auth=query-secret", nil)
		r.Header.Set("Referer", "https://user:ref-secret@ref.example/folder?session=ref-query-secret")
		r.Header.Set("User-Agent", agent+strings.Repeat("x", 10000))
		r.Header.Set("Cookie", "sid=cookie-secret")
		r.Header.Set("Authorization", "Bearer auth-secret")
		if method == http.MethodGet {
			r.Header.Set("If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := http.StatusOK
		if method == http.MethodGet {
			want = http.StatusNotModified
		}
		if w.Code != want || w.Body.Len() != 0 {
			t.Fatalf("%s: status %d and body length %d, want %d and empty body", method, w.Code, w.Body.Len(), want)
		}
	}
	got := readAccessLog(t, s.logfile)
	if len(got) != 2 || got[0].request != "HEAD /note.txt HTTP/1.1" || got[1].request != "GET /note.txt HTTP/1.1" || got[0].status != "200" || got[1].status != "304" {
		t.Fatalf("HEAD and 304 records %+v", got)
	}
	for _, record := range got {
		if record.bytes != "-" || record.referer != "https://ref.example" || !strings.HasPrefix(record.agent, agent) || len(record.agent) != maxLogDetail {
			t.Fatalf("invalid privacy or escaping metadata %+v", record)
		}
	}
	data, err := os.ReadFile(s.logfile)
	if err != nil || strings.Contains(string(data), "secret") || strings.Contains(string(data), "\r") || strings.Count(string(data), "\n") != 2 {
		t.Fatalf("invalid physical log bytes, err %v: %q", err, data)
	}
}

func testAccessCancellationAndCommandStatus(t *testing.T) {
	s := newTestServer(t)
	s.logfile = filepath.Join(t.TempDir(), "access.log")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h := s.logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/cancel", nil))
	h = s.logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		recordCommand(w, "get", "/note.txt")
		http.Error(w, "fixture failure", http.StatusUnprocessableEntity)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/exec", nil))
	got := readAccessLog(t, s.logfile)
	if len(got) != 3 || got[0].status != "000" || got[0].outcome != " outcome=canceled" ||
		got[1].request != "POST /api/exec?cmd=get&file=%2Fnote.txt HTTP/1.1" || got[1].status != "422" || got[2].status != "422" {
		t.Fatalf("cancellation or command outcomes %+v", got)
	}
}

func testAccessLogRotation(t *testing.T) {
	s := newTestServer(t)
	s.logfile = filepath.Join(t.TempDir(), "access.log")
	h := s.logRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := func(target string) {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
	}
	request("/before")
	rotated := s.logfile + ".1"
	if err := os.Rename(s.logfile, rotated); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.logfile, 0o755); err != nil {
		t.Fatal(err)
	}
	request("/file-unavailable")
	warning := s.lastLogWarning
	request("/still-unavailable")
	if warning.IsZero() || !s.lastLogWarning.Equal(warning) {
		t.Fatal("the file error was not reported once within the warning interval")
	}
	if err := os.Remove(s.logfile); err != nil {
		t.Fatal(err)
	}
	request("/after")
	before, after := readAccessLog(t, rotated), readAccessLog(t, s.logfile)
	if len(before) != 1 || len(after) != 1 || before[0].request != "GET /before HTTP/1.1" || after[0].request != "GET /after HTTP/1.1" || after[0].status != "200" {
		t.Fatalf("rotation recorded before %+v and after %+v", before, after)
	}
}
