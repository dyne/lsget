package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleConfig_ReadmeAndPath(t *testing.T) {
	s := newTestServer(t)
	// add README.md in root
	readmePath := filepath.Join(s.rootAbs, "README.md")
	if err := os.WriteFile(readmePath, []byte("Hello README"), 0o644); err != nil {
		t.Fatal(err)
	}

	// request with path parameter to set cwd to "/"
	r := httptest.NewRequest("GET", "/api/config?path=/", nil)
	w := httptest.NewRecorder()
	s.handleConfig(w, r)
	if w.Code != 200 {
		t.Fatalf("config status: %d", w.Code)
	}
	var resp configResp
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.CWD != "/" {
		t.Fatalf("cwd: %q", resp.CWD)
	}
	if resp.Readme == nil || !strings.Contains(*resp.Readme, "Hello README") || resp.DocType != "markdown" {
		t.Fatalf("readme/docType not set: %#v", resp)
	}

	// create subdir and set as initial path
	sub := filepath.Join(s.rootAbs, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r2 := httptest.NewRequest("GET", "/api/config?path=/sub", nil)
	w2 := httptest.NewRecorder()
	s.handleConfig(w2, r2)
	if w2.Code != 200 {
		t.Fatalf("config2 status: %d", w2.Code)
	}
	var resp2 configResp
	if err := json.NewDecoder(w2.Result().Body).Decode(&resp2); err != nil {
		t.Fatal(err)
	}
	if resp2.CWD != "/sub" {
		t.Fatalf("cwd not updated: %q", resp2.CWD)
	}
}

func execJSON(t *testing.T, s *server, input string) execResp {
	t.Helper()
	body, _ := json.Marshal(execReq{Input: input})
	r := httptest.NewRequest("POST", "/api/exec", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.handleExec(w, r)
	if w.Code != 200 {
		t.Fatalf("exec status: %d", w.Code)
	}
	var resp execResp
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHandleExec_BasicPwdHelp(t *testing.T) {
	s := newTestServer(t)
	out := execJSON(t, s, "pwd")
	if out.Output != "/" || out.CWD != "/" {
		t.Fatalf("pwd: %#v", out)
	}
	help := execJSON(t, s, "help").HTML
	if !strings.Contains(help, "Available commands") {
		t.Fatalf("help: %q", help)
	}
}

func TestHandleExec_LsCdCatAndErrors(t *testing.T) {
	s := newTestServer(t)
	// files
	if err := os.WriteFile(filepath.Join(s.rootAbs, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.rootAbs, ".hidden"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.rootAbs, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// ls without -a should not include .hidden
	ls := execJSON(t, s, "ls").Output
	if strings.Contains(ls, ".hidden") {
		t.Fatalf("ls showed hidden: %q", ls)
	}
	// ls -a should include it
	lsa := execJSON(t, s, "ls -a").Output
	if !strings.Contains(lsa, ".hidden") {
		t.Fatalf("ls -a missing hidden: %q", lsa)
	}

	// cd errors
	if !strings.Contains(execJSON(t, s, "cd no-such").Output, "no such") {
		t.Fatal("cd no-such")
	}
	if !strings.Contains(execJSON(t, s, "cd a.txt").Output, "not a directory") {
		t.Fatal("cd file not dir")
	}

	// cd success
	if execJSON(t, s, "cd dir").CWD != "/dir" {
		t.Fatal("cd dir failed")
	}

	// cat errors and success
	if !strings.Contains(execJSON(t, s, "cat").Output, "missing operand") {
		t.Fatal("cat missing")
	}
	// too large
	big := make([]byte, s.catMax+1)
	if err := os.WriteFile(filepath.Join(s.rootAbs, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(execJSON(t, s, "cat /big.txt").Output, "file too large") {
		t.Fatal("cat large")
	}
	// binary skip
	if err := os.WriteFile(filepath.Join(s.rootAbs, "bin"), []byte{0x00, 0x01, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(execJSON(t, s, "cat /bin").Output, "binary file") {
		t.Fatal("cat binary")
	}
	// text success
	if !strings.Contains(execJSON(t, s, "cat /a.txt").Output, "alpha") {
		t.Fatal("cat text")
	}
}

func TestHandleExec_DownloadVariants(t *testing.T) {
	s := newTestServer(t)
	// create files
	if err := os.WriteFile(filepath.Join(s.rootAbs, "x1.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.rootAbs, "x2.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.rootAbs, "dd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.rootAbs, "dd", "f.bin"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}

	// single file
	r1 := execJSON(t, s, "download x1.txt")
	if !strings.HasPrefix(r1.Download, "/api/download?path=") {
		t.Fatalf("single download: %#v", r1)
	}

	// pattern multiple -> archive
	r2 := execJSON(t, s, "download *.txt")
	if !strings.HasPrefix(r2.Download, "/api/download?pattern=") || !strings.Contains(r2.Output, "Downloading") {
		t.Fatalf("pattern dl: %#v", r2)
	}

	// directory -> dir zip
	r3 := execJSON(t, s, "download dd")
	if !strings.HasPrefix(r3.Download, "/api/download?dir=") {
		t.Fatalf("dir dl: %#v", r3)
	}

	// no match
	r4 := execJSON(t, s, "download *.none")
	if !strings.Contains(r4.Output, "no matching files") {
		t.Fatalf("no match: %#v", r4)
	}
}

func TestHandleExec_TreeFindGrep(t *testing.T) {
	s := newTestServer(t)
	// layout: /t/a.txt, /t/b/b.txt
	if err := os.Mkdir(filepath.Join(s.rootAbs, "t"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.rootAbs, "t", "a.txt"), []byte("needle A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.rootAbs, "t", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.rootAbs, "t", "b", "b.txt"), []byte("bbb needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// tree summary
	tr := execJSON(t, s, "tree /t").Output
	if !strings.Contains(tr, "directories") || !strings.Contains(tr, "files") {
		t.Fatalf("tree: %q", tr)
	}

	// find files
	ff := execJSON(t, s, "find /t -name *.txt -type f").Output
	if !strings.Contains(ff, "/t/a.txt") || !strings.Contains(ff, "/t/b/b.txt") {
		t.Fatalf("find: %q", ff)
	}

	// grep recursive w/ line numbers and case-insensitive
	gr := execJSON(t, s, "grep -rin needle /t").Output
	if !strings.Contains(gr, "/t/") || !strings.Contains(gr, colorYellow) || !strings.Contains(gr, colorBold) {
		t.Fatalf("grep highlight: %q", gr)
	}
}

func TestHandleDownload_Endpoints(t *testing.T) {
	s := newTestServer(t)
	// single file
	fp := filepath.Join(s.rootAbs, "one.txt")
	if err := os.WriteFile(fp, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/download?path=/one.txt", nil)
	s.handleDownload(w, r)
	if w.Code != 200 {
		t.Fatalf("dl path status: %d", w.Code)
	}
	if ct := w.Result().Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") && ct != "application/octet-stream" {
		t.Fatalf("ctype: %q", ct)
	}

	// directory zip
	if err := os.Mkdir(filepath.Join(s.rootAbs, "zipd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.rootAbs, "zipd", "z.txt"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/api/download?dir=/zipd", nil)
	s.handleDownload(w2, r2)
	if w2.Code != 200 {
		t.Fatalf("dl dir status: %d", w2.Code)
	}
	if w2.Result().Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("zip ctype: %q", w2.Result().Header.Get("Content-Type"))
	}

	// pattern zip
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest("GET", "/api/download?pattern=*.txt&cwd=/", nil)
	s.handleDownload(w3, r3)
	if w3.Code != 200 {
		t.Fatalf("dl pattern status: %d", w3.Code)
	}
	if w3.Result().Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("zip ctype: %q", w3.Result().Header.Get("Content-Type"))
	}

	// missing params
	w4 := httptest.NewRecorder()
	r4 := httptest.NewRequest("GET", "/api/download", nil)
	s.handleDownload(w4, r4)
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("missing params: %d", w4.Code)
	}
}

func TestHandleComplete_Basic(t *testing.T) {
	s := newTestServer(t)
	// create files and dirs
	if err := os.Mkdir(filepath.Join(s.rootAbs, "comp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.rootAbs, "comp", "file.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	// complete for "/co"
	req := completeReq{Path: "/co", DirsOnly: false, FilesOnly: false}
	b, _ := json.Marshal(req)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/complete", strings.NewReader(string(b)))
	s.handleComplete(w, r)
	if w.Code != 200 {
		t.Fatalf("complete status: %d", w.Code)
	}
	var cr completeResp
	if err := json.NewDecoder(w.Result().Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	if len(cr.Items) == 0 || cr.Items[0].Name != "comp" || !cr.Items[0].Dir {
		t.Fatalf("complete items: %#v", cr.Items)
	}

	// now inside dir, files only
	// simulate cwd change
	s.sessions = map[string]*session{"x": {cwd: "/comp"}}
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("POST", "/api/complete", strings.NewReader(string(b)))
	// attach cookie so getSession uses existing
	r2.AddCookie(&http.Cookie{Name: "sid", Value: "x"})
	s.handleComplete(w2, r2)
	if w2.Code != 200 {
		t.Fatalf("complete2 status: %d", w2.Code)
	}
	var cr2 completeResp
	if err := json.NewDecoder(w2.Result().Body).Decode(&cr2); err != nil {
		t.Fatal(err)
	}
	// It should suggest the directory itself (no prefix match), but we can also ask for text-only
	req2 := completeReq{Path: "/comp/fi", TextOnly: true}
	b2, _ := json.Marshal(req2)
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest("POST", "/api/complete", strings.NewReader(string(b2)))
	s.handleComplete(w3, r3)
	if w3.Code != 200 {
		t.Fatalf("complete3 status: %d", w3.Code)
	}
	data, _ := io.ReadAll(w3.Result().Body)
	if !strings.Contains(string(data), "file.txt") {
		t.Fatalf("complete text-only: %s", string(data))
	}
}

func TestHandleIndex_InvalidPathDoesNotReflectIntoHTML(t *testing.T) {
	s := newTestServer(t)

	r := httptest.NewRequest("GET", "/%61%27%22%3e%3c%69%6e%6a%65%63%74%61%62%6c%65%3e", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, r)
	if w.Code != 200 {
		t.Fatalf("index status: %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "data-initial-path=\"/\"") {
		t.Fatalf("expected initial path '/', body: %q", body)
	}
	if strings.Contains(body, "injectable") {
		t.Fatalf("unexpected reflection, body: %q", body)
	}
}

func TestHandleIndex_ExistingDirSetsInitialPath(t *testing.T) {
	s := newTestServer(t)

	sub := filepath.Join(s.rootAbs, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/sub", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, r)
	if w.Code != 200 {
		t.Fatalf("index status: %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "data-initial-path=\"/sub\"") {
		t.Fatalf("expected initial path '/sub', body: %q", body)
	}
}

func TestHandleIndex_DirectFileQuery(t *testing.T) {
	s := newServer(t.TempDir(), 4*1024, "", "")
	const pdf = "%PDF-1.7\nexample document\n"
	const index = "<html>home page</html>"
	if err := os.Mkdir(filepath.Join(s.rootAbs, "eudi"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"index.html":   index,
		".lsgetignore": "eudi/hidden.pdf\n",
		"eudi/cryptographers-feedback-june2024.pdf": pdf,
		"eudi/feedback + notes.pdf":                 pdf,
		"eudi/hidden.pdf":                           pdf,
	} {
		if err := os.WriteFile(filepath.Join(s.rootAbs, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	const attachment = `attachment; filename="cryptographers-feedback-june2024.pdf"`
	tests := []struct {
		name        string
		target      string
		status      int
		body        string
		disposition string
	}{
		{"direct path", "/eudi/cryptographers-feedback-june2024.pdf", http.StatusOK, pdf, attachment},
		{"query path", "/?file=eudi/cryptographers-feedback-june2024.pdf", http.StatusOK, pdf, attachment},
		{"root-relative query path", "/?file=/eudi/cryptographers-feedback-june2024.pdf", http.StatusOK, pdf, attachment},
		{"encoded filename", "/?file=eudi%2Ffeedback%20%2B%20notes.pdf", http.StatusOK, pdf, `attachment; filename="feedback + notes.pdf"`},
		{"missing file", "/?file=eudi/missing.pdf", http.StatusNotFound, "", ""},
		{"directory", "/?file=eudi", http.StatusNotFound, "", ""},
		{"ignored file", "/?file=eudi/hidden.pdf", http.StatusNotFound, "", ""},
		{"empty file parameter", "/?file=", http.StatusNotFound, "", ""},
		{"root index", "/", http.StatusOK, index, ""},
		{"root-level query file", "/?file=index.html", http.StatusOK, index, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.handleIndex(w, httptest.NewRequest(http.MethodGet, tt.target, nil))
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			if tt.status == http.StatusOK && w.Body.String() != tt.body {
				t.Errorf("body = %q, want %q", w.Body.String(), tt.body)
			}
			if got := w.Header().Get("Content-Disposition"); got != tt.disposition {
				t.Errorf("Content-Disposition = %q, want %q", got, tt.disposition)
			}
		})
	}

	t.Run("symlink outside the served root", func(t *testing.T) {
		outsideFile := filepath.Join(t.TempDir(), "outside.pdf")
		if err := os.WriteFile(outsideFile, []byte(pdf), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideFile, filepath.Join(s.rootAbs, "link.pdf")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		w := httptest.NewRecorder()
		s.handleIndex(w, httptest.NewRequest(http.MethodGet, "/?file=link.pdf", nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
		}
	})
}

func TestNoJSHTMLEscaping(t *testing.T) {
	s := newTestServer(t)

	// Create files/dirs with HTML-sensitive characters
	testCases := []struct {
		name     string
		isDir    bool
		expected string // what should NOT appear in the output (unescaped)
	}{
		{`test"file.txt`, false, `href="/test"file.txt"`},      // quote should be escaped
		{`test'file.txt`, false, `href="/test'file.txt"`},      // apostrophe should be escaped
		{`test<file.txt`, false, `href="/test<file.txt"`},      // angle bracket should be escaped
		{`test>file.txt`, false, `href="/test>file.txt"`},      // angle bracket should be escaped
		{`test&file.txt`, false, `href="/test&file.txt"`},      // ampersand should be escaped (but URL-escaped)
		{`test"dir`, true, `href="/test"dir?nojs=1"`},          // quote in directory name
	}

	for _, tc := range testCases {
		var path string
		if tc.isDir {
			path = filepath.Join(s.rootAbs, tc.name)
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatalf("mkdir %q: %v", tc.name, err)
			}
		} else {
			path = filepath.Join(s.rootAbs, tc.name)
			if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
				t.Fatalf("write %q: %v", tc.name, err)
			}
		}
	}

	r := httptest.NewRequest("GET", "/?nojs=1", nil)
	w := httptest.NewRecorder()
	s.serveNoJSDirectory(w, r, "/")
	if w.Code != 200 {
		t.Fatalf("nojs status: %d", w.Code)
	}
	body := w.Body.String()

	// Check that unescaped dangerous patterns do NOT appear
	for _, tc := range testCases {
		if strings.Contains(body, tc.expected) {
			t.Errorf("Found unescaped pattern %q in nojs output for file %q", tc.expected, tc.name)
		}
	}

	// Verify that HTML escaping was applied (quotes should be &quot; or &#34;)
	if strings.Contains(body, `href="/test"file.txt"`) {
		t.Error("Found literal quote in href attribute - HTML escaping not applied")
	}
}
