package lsget

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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

func TestReadmePreviewLimit(t *testing.T) {
	s := newTestServer(t)
	tests := []struct {
		name string
		size int
	}{
		{"below", readmePreviewLimit - 1},
		{"exact", readmePreviewLimit},
		{"above", readmePreviewLimit + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(s.rootAbs, tt.name)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			content := strings.Repeat("x", tt.size-1) + "!"
			if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			expected := content[:min(tt.size, readmePreviewLimit)]
			wantTruncated := tt.size > readmePreviewLimit
			check := func(route string, readme *string, docType string, truncated bool) {
				t.Helper()
				if readme == nil {
					t.Fatalf("%s: missing README", route)
				}
				if *readme != expected || docType != "text" || truncated != wantTruncated {
					t.Errorf("%s: unexpected README content, length %d, type %q, truncated %t", route, len(*readme), docType, truncated)
				}
			}

			response := httptest.NewRecorder()
			s.handleConfig(response, httptest.NewRequest("GET", "/api/config?path=/"+tt.name, nil))
			var config configResp
			if err := json.NewDecoder(response.Result().Body).Decode(&config); err != nil {
				t.Fatal(err)
			}
			check("config", config.Readme, config.DocType, config.ReadmeTruncated)
			exec := execJSON(t, s, "cd /"+tt.name)
			check("cd", exec.Readme, exec.DocType, exec.ReadmeTruncated)
		})
	}
}

func TestHandleExec_OmitReadme(t *testing.T) {
	s := newTestServer(t)
	writeWorkFixture(t, s.rootAbs, "docs/README.md", []byte("# Markdown preview\n"))
	legacy := execJSON(t, s, "cd /docs")
	if legacy.CWD != "/docs" || legacy.Readme == nil || *legacy.Readme != "# Markdown preview\n" || legacy.DocType != "markdown" {
		t.Fatalf("default cd response changed: %+v", legacy)
	}

	r := httptest.NewRequest(http.MethodPost, "/api/exec?cwd=/&readme=0", strings.NewReader(`{"input":"cd /docs"}`))
	w := httptest.NewRecorder()
	s.handleExec(w, r)
	var response map[string]string
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	output, hasOutput := response["output"]
	if w.Code != http.StatusOK || len(response) != 2 || response["cwd"] != "/docs" || !hasOutput || output != "" {
		t.Fatalf("cd opt-out returned HTTP %d: %+v", w.Code, response)
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

func TestHandleExec_PublicCommands(t *testing.T) {
	s := newTestServer(t)
	s.baseURL = "https://files.example.test/prefix"
	if err := os.Mkdir(filepath.Join(s.rootAbs, "team docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"team docs/README.md":    "# Team docs\n",
		"team docs/.hidden":      "hidden\n",
		"team docs/note one.txt": "Alpha Beta\n",
	} {
		if err := os.WriteFile(filepath.Join(s.rootAbs, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	const (
		download = "/api/download?path=%2Fteam+docs%2Fnote+one.txt"
		share    = "https://files.example.test/prefix/team%20docs/note%20one.txt"
		hashes   = "MD5:    1570f3b486c8a35a4dce763227dc3d70\nSHA256: ed220039e906e32c27a646de68be9036c5020df646a82f3565a40bba6a6fdef2"
	)
	tests := []struct {
		name       string
		args       string
		want       execResp
		contains   string
		wantReadme string
		listedRows int
	}{
		{name: "help", want: execResp{Help: true}},
		{name: "pwd", want: execResp{Output: "/", CWD: "/"}},
		{name: "ls", args: ` -lh "/team docs"`, contains: "11B", listedRows: 2},
		{name: "dir", args: ` -a "/team docs"`, contains: "/team docs/.hidden", listedRows: 3},
		{name: "cd", args: ` "team docs"`, want: execResp{CWD: "/team docs", DocType: "markdown"}, wantReadme: "# Team docs\n"},
		{name: "cat", args: ` "/team docs/note one.txt"`, want: execResp{Output: "Alpha Beta\n"}},
		{name: "get", args: ` "/team docs/note one.txt"`, want: execResp{Download: download}},
		{name: "rget", args: ` "/team docs/note one.txt"`, want: execResp{Download: download}},
		{name: "wget", args: ` "/team docs/note one.txt"`, want: execResp{Download: download}},
		{name: "download", args: ` "/team docs/note one.txt"`, want: execResp{Download: download}},
		{name: "url", args: ` "/team docs/note one.txt"`, want: execResp{Clipboard: share}, contains: "URL copied to clipboard!"},
		{name: "share", args: ` "/team docs/note one.txt"`, want: execResp{Clipboard: share}, contains: "URL copied to clipboard!"},
		{name: "sum", args: ` "/team docs/note one.txt"`, want: execResp{Output: hashes}},
		{name: "checksum", args: ` "/team docs/note one.txt"`, want: execResp{Output: hashes}},
		{name: "tree", args: ` -L1 -a "/team docs"`, contains: "0 directories, 3 files", listedRows: 3},
		{name: "find", args: ` "/team docs" -name "note *.txt" -type f`, want: execResp{Output: colorWhite + "/team docs/note one.txt" + colorReset}},
		{name: "grep", args: ` -rin "alpha beta" "/team docs"`, contains: "Alpha Beta"},
		// The README lists stats, which currently reaches the unknown-command response.
		{name: "stats", want: execResp{Output: "sh: stats: command not found"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := execJSON(t, s, test.name+test.args)
			if test.contains != "" {
				if !strings.Contains(got.Output, test.contains) {
					t.Fatalf("output %q does not contain %q", got.Output, test.contains)
				}
				got.Output = ""
			}
			if test.want.Help {
				if !strings.Contains(got.HTML, "Available commands") {
					t.Fatalf("missing help in %q", got.HTML)
				}
				got.HTML = ""
			}
			if test.wantReadme != "" {
				if got.Readme == nil || *got.Readme != test.wantReadme {
					t.Fatalf("unexpected README: %+v", got)
				}
				got.Readme = nil
			}
			if test.listedRows > 0 {
				if got.Listing == nil || len(got.Listing.Rows) != test.listedRows {
					t.Fatalf("listing: %+v, want %d rows", got.Listing, test.listedRows)
				}
				got.Listing = nil
			}
			if got != test.want {
				t.Fatalf("response: %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestHandleExec_QuotedArgumentsAndErrors(t *testing.T) {
	s := newTestServer(t)
	for _, name := range []string{"two words.txt", "double\"quote.txt", "single'quote.txt"} {
		if err := os.WriteFile(filepath.Join(s.rootAbs, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, input, output string
	}{
		{"double_quotes", `cat "two words.txt"`, "two words.txt"},
		{"single_quotes", `cat 'two words.txt'`, "two words.txt"},
		{"double_quote_escape", `cat "double\"quote.txt"`, `double"quote.txt`},
		{"single_quote_in_double_quotes", `cat "single'quote.txt"`, "single'quote.txt"},
		{"unterminated_quote", `cat "two words.txt`, "two words.txt"},
		{"empty_quoted_operand", `cat ""`, "cat: missing operand"},
		{"empty_command", " \t ", ""},
		{"unknown_command", `"two words"`, "sh: two words: command not found"},
		{"ls_missing_file", `dir "missing name"`, "ls: cannot access 'missing name': No such file or directory"},
		{"cd_missing_directory", `cd "missing name"`, "cd: no such file or directory"},
		{"cd_on_file", `cd "two words.txt"`, "cd: not a directory"},
		{"cat_missing_file", `cat "missing name"`, "cat: no such file or directory"},
		{"download_missing_operand", "rget", "download: missing operand"},
		{"download_no_matches", `wget "missing *.txt"`, "download: no matching files found"},
		{"tree_on_file", `tree "two words.txt"`, "tree: not a directory"},
		{"find_invalid_type", `find / -type "not a type"`, "find: invalid type filter (use 'f' for files or 'd' for directories)"},
		{"grep_missing_pattern", "grep -in", "grep: missing pattern"},
		{"grep_missing_file", `grep "two words" "missing name"`, "grep: missing name: no such file or directory"},
		{"share_missing_operand", "share", "url: missing file operand"},
		{"checksum_on_directory", "checksum /", "sum: is a directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := execJSON(t, s, test.input)
			if want := (execResp{Output: test.output}); got != want {
				t.Fatalf("response for %q: %+v, want %+v", test.input, got, want)
			}
		})
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

	t.Run("filename with query characters", func(t *testing.T) {
		const filename = "notes+review&comments.txt"
		if err := os.WriteFile(filepath.Join(s.rootAbs, filename), []byte("notes"), 0o644); err != nil {
			t.Fatal(err)
		}
		result := execJSON(t, s, "download "+filename)
		if result.Download == "" {
			t.Fatalf("expected a download URL: %#v", result)
		}
		w := httptest.NewRecorder()
		s.handleDownload(w, httptest.NewRequest(http.MethodGet, result.Download, nil))
		if w.Code != http.StatusOK || w.Body.String() != "notes" {
			t.Fatalf("download response: %d %q", w.Code, w.Body.String())
		}
	})
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

func TestDownloadRoutesApplyIgnoreRules(t *testing.T) {
	s := newServer(t.TempDir(), 4*1024, "", "")
	for name, content := range map[string]string{
		".lsgetignore":            "docs/hidden.txt\ndocs/private\n",
		"docs/allowed.txt":        "public document",
		"docs/hidden.txt":         "ignored document",
		"docs/private/hidden.txt": "ignored by parent directory",
	} {
		filename := filepath.Join(s.rootAbs, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/static/", s.handleStaticFile)
	mux.HandleFunc("/api/download", s.handleDownload)
	request := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		return w
	}

	for _, prefix := range []string{"/", "/?file=", "/api/static/", "/api/download?path="} {
		t.Run(prefix, func(t *testing.T) {
			public := request(prefix + "docs/allowed.txt")
			if public.Code != http.StatusOK || public.Body.String() != "public document" {
				t.Fatalf("public file response: %d %q", public.Code, public.Body.String())
			}
			for _, name := range []string{"docs/hidden.txt", "docs/private/hidden.txt"} {
				if got := request(prefix + name).Code; got != http.StatusNotFound {
					t.Errorf("ignored file %s returned %d, want 404", name, got)
				}
			}
		})
	}

	for _, tt := range []struct{ target, entry string }{
		{"/api/download?dir=/docs", "docs/allowed.txt"},
		{"/api/download?pattern=docs/*.txt", "allowed.txt"},
	} {
		t.Run(tt.target, func(t *testing.T) {
			w := request(tt.target)
			if w.Code != http.StatusOK {
				t.Fatalf("archive response: %d %q", w.Code, w.Body.String())
			}
			archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
			if err != nil {
				t.Fatal(err)
			}
			if len(archive.File) != 1 || archive.File[0].Name != tt.entry {
				t.Fatalf("archive did not contain exactly %q", tt.entry)
			}
			content, err := archive.Open(tt.entry)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = content.Close() }()
			data, err := io.ReadAll(content)
			if err != nil || string(data) != "public document" {
				t.Fatalf("archive entry: %q, %v", data, err)
			}
		})
	}
	for _, target := range []string{"/api/download?pattern=docs/hidden.txt", "/api/download?dir=/docs/private"} {
		if got := request(target).Code; got != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", target, got)
		}
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

	// Set CWD via config, then use the issued cookie for relative completion.
	config := httptest.NewRecorder()
	s.handleConfig(config, httptest.NewRequest(http.MethodGet, "/api/config?path=/comp", nil))
	cookies := config.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("config session cookies: %v", cookies)
	}
	req2 := completeReq{Path: "fi", TextOnly: true}
	b2, _ := json.Marshal(req2)
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("POST", "/api/complete", strings.NewReader(string(b2)))
	r2.Header.Set("Content-Type", "application/json")
	r2.AddCookie(cookies[0])
	s.handleComplete(w2, r2)
	if w2.Code != 200 {
		t.Fatalf("complete2 status: %d", w2.Code)
	}
	var cr2 completeResp
	if err := json.NewDecoder(w2.Result().Body).Decode(&cr2); err != nil {
		t.Fatal(err)
	}
	if len(cr2.Items) != 1 || cr2.Items[0].Name != "file.txt" || cr2.Items[0].Dir {
		t.Fatalf("relative completion items: %+v", cr2.Items)
	}
	// An absolute completion path works with no session.
	req3 := completeReq{Path: "/comp/fi", TextOnly: true}
	b3, _ := json.Marshal(req3)
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest("POST", "/api/complete", strings.NewReader(string(b3)))
	s.handleComplete(w3, r3)
	if w3.Code != 200 {
		t.Fatalf("complete3 status: %d", w3.Code)
	}
	data, _ := io.ReadAll(w3.Result().Body)
	if !strings.Contains(string(data), "file.txt") {
		t.Fatalf("complete text-only: %s", string(data))
	}
}

func TestEmbeddedShellPolicy(t *testing.T) {
	s := newTestServer(t)
	serve := func(uri string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		s.handleIndex(recorder, httptest.NewRequest(http.MethodGet, uri, nil))
		return recorder
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"first working directory", "second working directory"} {
		t.Chdir(t.TempDir())
		write("index.html", value)
		response := serve("/")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "<title>📂 lsget</title>") ||
			strings.Contains(response.Body.String(), value) {
			t.Fatalf("request from %q did not serve the embedded shell, status %d", value, response.Code)
		}
	}

	const enforced = "Content-Security-Policy"
	const reportOnly = "Content-Security-Policy-Report-Only"
	first := serve("/")
	second := serve("/")
	policy := first.Header().Get(enforced)
	before, rest, found := strings.Cut(policy, "'nonce-")
	nonce, _, closed := strings.Cut(rest, "'")
	if !found || !closed || nonce == "" || before != "default-src 'none'; script-src " ||
		!strings.Contains(policy, "style-src 'nonce-"+nonce+"'") {
		t.Fatal("embedded shell policy did not use the same nonce for scripts and styles")
	}
	tags := regexp.MustCompile(`(?i)<(?:script|style)(?:\s[^>]*)?>`).FindAllString(first.Body.String(), -1)
	tagNames := make(map[string]bool)
	for _, tag := range tags {
		var block struct {
			XMLName xml.Name
			Nonce   string `xml:"nonce,attr"`
		}
		decoder := xml.NewDecoder(strings.NewReader(strings.TrimSuffix(tag, ">") + "/>"))
		decoder.Strict = false
		if err := decoder.Decode(&block); err != nil {
			t.Fatalf("could not read embedded tag %q: %v", tag, err)
		}
		if block.Nonce != nonce {
			t.Fatalf("embedded tag %q did not use the nonce from the policy", tag)
		}
		tagNames[strings.ToLower(block.XMLName.Local)] = true
	}
	if !tagNames["script"] || !tagNames["style"] {
		t.Fatal("embedded shell did not contain both script and style tags")
	}
	if first.Header().Get(reportOnly) != "" || first.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(policy, "script-src 'nonce-"+nonce+"';") ||
		strings.Contains(second.Header().Get(enforced), nonce) {
		t.Fatalf("unexpected enforcement or nonce reuse: %s", policy)
	}

	s.shellCSPReportOnly = true
	trial := serve("/")
	trialPolicy := trial.Header().Get(reportOnly)
	if trial.Header().Get(enforced) != "" || !strings.Contains(trialPolicy, "default-src 'none'; script-src 'nonce-") ||
		strings.Contains(trialPolicy, "'unsafe-eval'") {
		t.Fatalf("unexpected report-only policy: %s", trialPolicy)
	}
	if fallback := serve("/?nojs=1"); fallback.Code != http.StatusOK ||
		!strings.Contains(fallback.Body.String(), "<h1>Index of /</h1>") ||
		fallback.Header().Get(enforced) != "" || fallback.Header().Get(reportOnly) != "" {
		t.Fatalf("unexpected no-JS response: status %d, headers %v", fallback.Code, fallback.Header())
	}

	s.devTemplate = "index.html"
	dev := serve("/")
	if dev.Code != http.StatusOK || dev.Body.String() != "second working directory" ||
		dev.Header().Get(enforced) != "" || dev.Header().Get(reportOnly) != "" {
		t.Fatalf("unexpected development response: status %d, headers %v", dev.Code, dev.Header())
	}
	if err := os.Mkdir(filepath.Join(s.rootAbs, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(s.rootAbs, "site", "index.html"), "<h1>Published site</h1>")
	published := serve("/site/?nojs=1")
	if published.Code != http.StatusOK || published.Body.String() != "<h1>Published site</h1>" ||
		published.Header().Get(enforced) != "" || published.Header().Get(reportOnly) != "" {
		t.Fatalf("unexpected published response: status %d, headers %v", published.Code, published.Header())
	}
	s.devTemplate = "missing.html"
	if missing := serve("/"); missing.Code != http.StatusInternalServerError {
		t.Fatalf("missing development template: status %d", missing.Code)
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

	const inline = `inline; filename="cryptographers-feedback-june2024.pdf"`
	tests := []struct {
		name        string
		target      string
		status      int
		body        string
		disposition string
	}{
		{"direct path", "/eudi/cryptographers-feedback-june2024.pdf", http.StatusOK, pdf, inline},
		{"query path", "/?file=eudi/cryptographers-feedback-june2024.pdf", http.StatusOK, pdf, inline},
		{"root-relative query path", "/?file=/eudi/cryptographers-feedback-june2024.pdf", http.StatusOK, pdf, inline},
		{"encoded filename", "/?file=eudi%2Ffeedback%20%2B%20notes.pdf", http.StatusOK, pdf, `inline; filename="feedback + notes.pdf"`},
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

	t.Run("canonical index redirect", func(t *testing.T) {
		w := httptest.NewRecorder()
		s.handleIndex(w, httptest.NewRequest(http.MethodGet, "/index.html?nojs=1", nil))
		if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "./?nojs=1" {
			t.Fatalf("redirect = %d %q, want 301 ./?nojs=1", w.Code, w.Header().Get("Location"))
		}
	})

	t.Run("symlink outside the served root", func(t *testing.T) {
		outsideFile := filepath.Join(t.TempDir(), "outside.pdf")
		if err := os.WriteFile(outsideFile, []byte(pdf), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideFile, filepath.Join(s.rootAbs, "link.pdf")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/", s.handleIndex)
		mux.HandleFunc("/api/static/", s.handleStaticFile)
		mux.HandleFunc("/api/download", s.handleDownload)
		for _, target := range []string{
			"/link.pdf", "/?file=link.pdf", "/api/static/link.pdf",
			"/api/download?path=/link.pdf", "/api/download?pattern=link*.pdf",
		} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
			if w.Code != http.StatusNotFound {
				t.Errorf("%s returned %d, want 404", target, w.Code)
			}
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
		{`test"file.txt`, false, `href="/test"file.txt"`}, // quote should be escaped
		{`test'file.txt`, false, `href="/test'file.txt"`}, // apostrophe should be escaped
		{`test<file.txt`, false, `href="/test<file.txt"`}, // angle bracket should be escaped
		{`test>file.txt`, false, `href="/test>file.txt"`}, // angle bracket should be escaped
		{`test&file.txt`, false, `href="/test&file.txt"`}, // ampersand should be escaped (but URL-escaped)
		{`test"dir`, true, `href="/test"dir?nojs=1"`},     // quote in directory name
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
	s.handleIndex(w, r)
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
