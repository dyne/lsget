package main

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "lsget-test-")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func newTestServer(t *testing.T) *server {
	root := makeTempDir(t)
	return newServer(root, 4*1024, "") // small catMax for tests
}

func TestRenderHelp(t *testing.T) {
	s := renderHelp()
	if !strings.Contains(s, version) {
		t.Fatalf("help should contain version, got %q", s)
	}
}

func TestCleanJoinRealFromVirtual(t *testing.T) {
	s := newTestServer(t)
	// cleanVirtual
	if cleanVirtual("") != "/" {
		t.Fatal("cleanVirtual empty -> /")
	}
	if cleanVirtual("foo/..//bar") != "/bar" {
		t.Fatal("cleanVirtual normalize")
	}
	// joinVirtual
	if joinVirtual("/a", "b") != "/a/b" {
		t.Fatal("join relative")
	}
	if joinVirtual("/a", "/x") != "/x" {
		t.Fatal("join absolute wins")
	}
	// realFromVirtual root
	r, err := s.realFromVirtual("/")
	if err != nil || r != s.rootAbs {
		t.Fatalf("realFromVirtual root: %v %q", err, r)
	}
	// ensure normalization keeps within root
	if p, err := s.realFromVirtual("/../etc"); err != nil || !strings.HasPrefix(p, s.rootAbs) {
		t.Fatal("realFromVirtual should keep within root")
	}
}

func TestParseArgs(t *testing.T) {
	args := parseArgs(`cmd "a b" 'c d' e f`)
	exp := []string{"cmd", "a b", "c d", "e", "f"}
	if len(args) != len(exp) {
		t.Fatalf("args len: %v", args)
	}
	for i := range exp {
		if args[i] != exp[i] {
			t.Fatalf("arg %d = %q", i, args[i])
		}
	}
}

func TestLooksText(t *testing.T) {
	if looksText([]byte{0x00, 'a'}) {
		t.Fatal("NUL should be binary")
	}
	if !looksText([]byte("hello")) {
		t.Fatal("simple ascii is text")
	}
	if !looksText([]byte{}) {
		t.Fatal("empty is text")
	}
}

func TestGetFileColorAndColorizeName(t *testing.T) {
	root := makeTempDir(t)
	// dir
	dir := filepath.Join(root, "dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if c := getFileColor(info, "dir"); c != colorBlue+colorBold {
		t.Fatalf("dir color: %q", c)
	}
	// executable
	exe := filepath.Join(root, "exe.sh")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(exe)
	if c := getFileColor(info, "exe.sh"); c != colorGreen {
		t.Fatalf("exe color: %q", c)
	}
	// symlink
	ln := filepath.Join(root, "link")
	if err := os.Symlink(exe, ln); err != nil {
		t.Fatal(err)
	}
	linfo, _ := os.Lstat(ln)
	if c := getFileColor(linfo, "link"); c != colorGreen {
		t.Fatalf("symlink color: %q", c)
	}
	// extension mapping
	mp := filepath.Join(root, "f.zip")
	if err := os.WriteFile(mp, []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	minfo, _ := os.Stat(mp)
	if c := getFileColor(minfo, "f.zip"); c != colorRed {
		t.Fatalf("zip color: %q", c)
	}
	// colorize wraps
	name := colorizeName(minfo, "f.zip")
	if !strings.HasPrefix(name, colorRed) || !strings.HasSuffix(name, colorReset) {
		t.Fatalf("colorizeName not wrapped: %q", name)
	}
}

func TestFormatLong(t *testing.T) {
	root := makeTempDir(t)
	f := filepath.Join(root, "file.txt")
	data := []byte("abc")
	if err := os.WriteFile(f, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(f)
	line := formatLong(info, "name", false)
	if !strings.Contains(line, "name") || !strings.Contains(line, info.Mode().String()) {
		t.Fatalf("formatLong: %q", line)
	}
}

func TestURLEscapeHelpers(t *testing.T) {
	v := "/a b/c#d?e&f+g%h"
	esc := urlEscapeVirtual(v)
	if !strings.Contains(esc, "%20") || !strings.HasPrefix(esc, "/") {
		t.Fatalf("urlEscapeVirtual: %q", esc)
	}
	if urlQueryEscape("a b") != "a%20b" {
		t.Fatal("urlQueryEscape space")
	}
}

func TestNewSIDAndGetSession(t *testing.T) {
	id := newSID()
	if len(id) != 32 {
		t.Fatalf("sid length: %d", len(id))
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("sid hex: %v", err)
	}

	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	sess := s.getSession(w, r)
	if sess == nil || sess.cwd != "/" {
		t.Fatal("new session cwd")
	}
	ck := w.Result().Cookies()
	if len(ck) == 0 || ck[0].Name != "sid" {
		t.Fatal("sid cookie not set")
	}
	// Second time with cookie should return existing
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(ck[0])
	w2 := httptest.NewRecorder()
	sess2 := s.getSession(w2, r2)
	if sess2 != sess {
		t.Fatal("session not reused")
	}
}

func TestProcessHTMLTemplate(t *testing.T) {
	s := newTestServer(t)
	html := []byte("<html>{{HELP_MESSAGE}}::{{INITIAL_PATH}}</html>")
	out := s.processHTMLTemplate(html, "/foo/bar")
	so := string(out)
	if strings.Contains(so, "{{HELP_MESSAGE}}") {
		t.Fatal("HELP_MESSAGE not replaced")
	}
	if !strings.Contains(so, "/foo/bar") {
		t.Fatal("INITIAL_PATH not injected")
	}
}

func TestHandleIndexServesAndFile(t *testing.T) {
	s := newTestServer(t)
	// create a file in root
	fp := filepath.Join(s.rootAbs, "data.txt")
	_ = os.WriteFile(fp, []byte("DATA"), 0o644)
	// request /
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	s.handleIndex(w, r)
	if w.Result().StatusCode != 200 {
		t.Fatalf("index status: %d", w.Result().StatusCode)
	}
	// request file
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", path.Join("/", "data.txt"), nil)
	s.handleIndex(w2, r2)
	if ct := w2.Result().Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") && ct != "application/octet-stream" {
		t.Fatalf("serveFile content-type: %q", ct)
	}
}

func TestHandleStaticFile(t *testing.T) {
	s := newTestServer(t)
	fp := filepath.Join(s.rootAbs, "static.js")
	_ = os.WriteFile(fp, []byte("var x=1;"), 0o644)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/static/static.js", nil)
	s.handleStaticFile(w, r)
	if w.Code != 200 {
		t.Fatalf("static status: %d", w.Code)
	}
}

func TestLogRequests(t *testing.T) {
	h := httpHandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	wrapped := logRequests(h)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/ping", nil)
	wrapped.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("status: %d", w.Code)
	}
}

// small adapter to avoid importing net/http in top list twice
// (keeps imports tidy without aliasing)
type httpHandlerFunc func(http.ResponseWriter, *http.Request)

func (f httpHandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) { f(w, r) }

func TestFormatLongTimeStability(t *testing.T) {
	// Ensure formatLong uses ModTime without panics
	root := makeTempDir(t)
	f := filepath.Join(root, "t.bin")
	if err := os.WriteFile(f, bytes.Repeat([]byte{'A'}, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(f)
	_ = info.ModTime().Format("Jan _2 15:04")
	_ = formatLong(info, "x", false)
	_ = time.Now() // just touch time to ensure import used in test logic
}
