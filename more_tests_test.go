package main

import (
    "archive/zip"
    "bytes"
    "encoding/json"
    "errors"
    "flag"
    "io"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

// ---- Helpers ----

type fakeInfo struct {
    name string
    mode os.FileMode
    size int64
    mod  time.Time
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return f.mod }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

// ---- getFileColor / readDocFile ----

func TestGetFileColor_SpecialAndExts(t *testing.T) {
    // special modes
    if c := getFileColor(fakeInfo{mode: os.ModeNamedPipe}, "p"); c != colorYellow { t.Fatalf("fifo color: %q", c) }
    if c := getFileColor(fakeInfo{mode: os.ModeSocket}, "s"); c != colorMagenta { t.Fatalf("socket color: %q", c) }
    if c := getFileColor(fakeInfo{mode: os.ModeDevice}, "d"); c != colorYellow+colorBold { t.Fatalf("device color: %q", c) }
    // extensions
    if c := getFileColor(fakeInfo{mode: 0}, "movie.mkv"); c != colorBrightGreen { t.Fatalf("video color: %q", c) }
    if c := getFileColor(fakeInfo{mode: 0}, "script.sh"); c != colorGreen { t.Fatalf("shell color: %q", c) }
    if c := getFileColor(fakeInfo{mode: 0}, "code.go"); c != colorYellow { t.Fatalf("code color: %q", c) }
    if c := getFileColor(fakeInfo{mode: 0}, "data.yaml"); c != colorBrightYellow { t.Fatalf("yaml color: %q", c) }
    if c := getFileColor(fakeInfo{mode: 0}, "unknown.bin"); c != colorReset { t.Fatalf("default color: %q", c) }
}

func TestReadDocFile_Variants(t *testing.T) {
    dir := makeTempDir(t)
    // README.txt prioritized
    if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("T"), 0o644); err != nil { t.Fatal(err) }
    body, typ := readDocFile(dir)
    if body != "T" || typ != "text" { t.Fatalf("readme.txt: %q %q", body, typ) }
    // Second dir: extension scan fallback
    dir2 := makeTempDir(t)
    if err := os.WriteFile(filepath.Join(dir2, "guide.rst"), []byte("R"), 0o644); err != nil { t.Fatal(err) }
    b2, t2 := readDocFile(dir2)
    if b2 != "R" || t2 != "rst" { t.Fatalf("rst fallback: %q %q", b2, t2) }
    // nfo
    dir3 := makeTempDir(t)
    if err := os.WriteFile(filepath.Join(dir3, "file.nfo"), []byte("NFO"), 0o644); err != nil { t.Fatal(err) }
    b3, t3 := readDocFile(dir3)
    if b3 != "NFO" || t3 != "nfo" { t.Fatalf("nfo: %q %q", b3, t3) }
}

// ---- ignore logic ----

func TestParseIgnoreAndShouldIgnore(t *testing.T) {
    s := newTestServer(t)
    // layout: /a/.lsgetignore ("*.log"), /a/b/.lsgetignore ("secret/*")
    a := filepath.Join(s.rootAbs, "a")
    b := filepath.Join(a, "b")
    if err := os.MkdirAll(b, 0o755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(a, ".lsgetignore"), []byte("*.log\n# comment\n"), 0o644); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(b, ".lsgetignore"), []byte("secret/*\n"), 0o644); err != nil { t.Fatal(err) }

    f1 := filepath.Join(a, "x.log")
    _ = os.WriteFile(f1, []byte("x"), 0o644)
    if !s.shouldIgnore(f1, filepath.Base(f1)) { t.Fatal("*.log should be ignored") }

    sec := filepath.Join(b, "secret", "top.txt")
    if err := os.MkdirAll(filepath.Dir(sec), 0o755); err != nil { t.Fatal(err) }
    _ = os.WriteFile(sec, []byte("t"), 0o644)
    if !s.shouldIgnore(sec, filepath.Base(sec)) { t.Fatal("path pattern ignored in child") }

    // non-match
    keep := filepath.Join(a, "keep.txt")
    _ = os.WriteFile(keep, []byte("k"), 0o644)
    if s.shouldIgnore(keep, filepath.Base(keep)) { t.Fatal("keep should not be ignored") }
}

// ---- looksText ----

func TestLooksText_Heuristics(t *testing.T) {
    if !looksText([]byte("caf\xc3\xa9")) { t.Fatal("valid utf8") }
    // high printable ratio
    if !looksText([]byte(strings.Repeat("A", 90) + strings.Repeat("\x01", 10))) { t.Fatal("printable ratio true") }
    // low printable ratio
    if looksText([]byte(strings.Repeat("\x00\x01", 100))) { t.Fatal("too binary") }
}

// ---- static file not found ----

func TestHandleStaticFile_NotFound(t *testing.T) {
    s := newTestServer(t)
    w := httptest.NewRecorder()
    r := httptest.NewRequest("GET", "/api/static/missing.js", nil)
    s.handleStaticFile(w, r)
    if w.Code != http.StatusNotFound { t.Fatalf("expect 404, got %d", w.Code) }
}

// ---- download error branches ----

func TestHandleDownload_ErrorBranches(t *testing.T) {
    s := newTestServer(t)
    // path is dir -> 400
    if err := os.Mkdir(filepath.Join(s.rootAbs, "d"), 0o755); err != nil { t.Fatal(err) }
    w := httptest.NewRecorder()
    r := httptest.NewRequest("GET", "/api/download?path=/d", nil)
    s.handleDownload(w, r)
    if w.Code != http.StatusBadRequest { t.Fatalf("is a directory code: %d", w.Code) }

    // dir not a directory -> 400
    if err := os.WriteFile(filepath.Join(s.rootAbs, "f.txt"), []byte("x"), 0o644); err != nil { t.Fatal(err) }
    w2 := httptest.NewRecorder()
    r2 := httptest.NewRequest("GET", "/api/download?dir=/f.txt", nil)
    s.handleDownload(w2, r2)
    if w2.Code != http.StatusBadRequest { t.Fatalf("not a directory code: %d", w2.Code) }

    // permission denied
    w3 := httptest.NewRecorder()
    r3 := httptest.NewRequest("GET", "/api/download?path=/../etc/passwd", nil)
    s.handleDownload(w3, r3)
    // cleanVirtual removes "..", so this becomes a lookup under root and yields 404
    if w3.Code != http.StatusNotFound { t.Fatalf("expected 404, got %d", w3.Code) }
}

// ---- collectFilesForDownload branches ----

func TestCollectFilesForDownload_DotAndSubPattern(t *testing.T) {
    s := newTestServer(t)
    sub := filepath.Join(s.rootAbs, "sub")
    if err := os.Mkdir(sub, 0o755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("a"), 0o644); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(sub, "b.bin"), []byte("b"), 0o644); err != nil { t.Fatal(err) }

    // dot -> collect directory
    files, err := s.collectFilesForDownload("/sub", ".")
    if err != nil || len(files) == 0 { t.Fatalf("dot collect: %v %v", err, files) }

    // subdir pattern
    files2, err := s.collectFilesForDownload("/", "sub/*.txt")
    if err != nil || len(files2) != 1 || !strings.HasSuffix(files2[0].realPath, "a.txt") {
        t.Fatalf("sub pattern: %v %#v", err, files2)
    }
}

// ---- sendZipArchive ----

func TestSendZipArchive_Content(t *testing.T) {
    s := newTestServer(t)
    f1 := filepath.Join(s.rootAbs, "a.txt")
    f2 := filepath.Join(s.rootAbs, "b.txt")
    _ = os.WriteFile(f1, []byte("A"), 0o644)
    _ = os.WriteFile(f2, []byte("BB"), 0o644)
    files := []fileInfo{{realPath: f1, relativePath: "a.txt"}, {realPath: f2, relativePath: "b.txt"}}
    w := httptest.NewRecorder()
    s.sendZipArchive(w, files, "test.zip")
    if ct := w.Result().Header.Get("Content-Type"); ct != "application/zip" { t.Fatalf("ctype: %q", ct) }
    zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
    if err != nil { t.Fatal(err) }
    if len(zr.File) != 2 { t.Fatalf("zip entries: %d", len(zr.File)) }
    // read a.txt
    rc, _ := zr.File[0].Open()
    data, _ := io.ReadAll(rc)
    _ = rc.Close()
    if len(data) == 0 { t.Fatal("zip content empty") }
}

// ---- buildTree options ----

func TestBuildTree_HiddenAndDepth(t *testing.T) {
    s := newTestServer(t)
    // .hidden and nested
    _ = os.WriteFile(filepath.Join(s.rootAbs, ".hidden"), []byte("x"), 0o644)
    if err := os.Mkdir(filepath.Join(s.rootAbs, "d1"), 0o755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(s.rootAbs, "d1", "f.txt"), []byte("v"), 0o644); err != nil { t.Fatal(err) }
    if err := os.Mkdir(filepath.Join(s.rootAbs, "d1", "d2"), 0o755); err != nil { t.Fatal(err) }

    var b strings.Builder
    dirs, files := s.buildTree(&b, s.rootAbs, "", true, 1, 0)
    out := b.String()
    if !strings.Contains(out, ".hidden") { t.Fatalf("should include hidden: %q", out) }
    if dirs == 0 || files == 0 { t.Fatalf("counts should be >0: %d %d", dirs, files) }
}

// ---- handleComplete filters ----

func TestHandleComplete_Filters(t *testing.T) {
    s := newTestServer(t)
    // layout
    if err := os.Mkdir(filepath.Join(s.rootAbs, "c"), 0o755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(s.rootAbs, "c", "big.txt"), bytes.Repeat([]byte{'A'}, 10), 0o644); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(s.rootAbs, "c", "bin.bin"), []byte{0x00, 'A'}, 0o644); err != nil { t.Fatal(err) }

    // DirsOnly
    req := completeReq{Path: "/", DirsOnly: true}
    b, _ := json.Marshal(req)
    w := httptest.NewRecorder()
    r := httptest.NewRequest("POST", "/api/complete", strings.NewReader(string(b)))
    s.handleComplete(w, r)
    if w.Code != 200 { t.Fatalf("dirsOnly status: %d", w.Code) }

    // FilesOnly + TextOnly + MaxSize
    req2 := completeReq{Path: "/c/", FilesOnly: true, TextOnly: true, MaxSize: 5}
    b2, _ := json.Marshal(req2)
    w2 := httptest.NewRecorder()
    r2 := httptest.NewRequest("POST", "/api/complete", strings.NewReader(string(b2)))
    s.handleComplete(w2, r2)
    var cr completeResp
    _ = json.NewDecoder(w2.Result().Body).Decode(&cr)
    // should exclude big.txt (>5) and bin.bin (binary)
    for _, it := range cr.Items {
        if it.Name == "big.txt" || it.Name == "bin.bin" { t.Fatalf("filters failed: %#v", cr.Items) }
    }
}

// ---- main() ----

type exitPanic struct{ code int }

func TestMain_VersionFlag(t *testing.T) {
    oldExit := exitFunc
    defer func(){ exitFunc = oldExit }()
    exitFunc = func(code int){ panic(exitPanic{code}) }

    // reset flag set
    flag.CommandLine = flag.NewFlagSet("lsget", flag.ContinueOnError)
    os.Args = []string{"lsget", "-version"}
    defer func(){ if r := recover(); r != nil { if ep, ok := r.(exitPanic); ok { if ep.code != 0 { t.Fatalf("exit code: %d", ep.code) } } else { panic(r) } } }()
    main()
}

func TestMain_InvalidDir(t *testing.T) {
    oldExit := exitFunc; defer func(){ exitFunc = oldExit }()
    exitFunc = func(code int){ panic(exitPanic{code}) }
    flag.CommandLine = flag.NewFlagSet("lsget", flag.ContinueOnError)
    os.Args = []string{"lsget", "-dir", filepath.Join(os.TempDir(), "no_such_dir_hopefully___")}
    defer func(){ if r := recover(); r != nil { if ep, ok := r.(exitPanic); ok { if ep.code != 1 { t.Fatalf("exit code: %d", ep.code) } } else { panic(r) } } }()
    main()
}

func TestMain_ListenPaths(t *testing.T) {
    oldExit := exitFunc; defer func(){ exitFunc = oldExit }()
    oldListen := listenAndServe; defer func(){ listenAndServe = oldListen }()
    exitFunc = func(code int){ panic(exitPanic{code}) }

    // OK path (ErrServerClosed)
    listenAndServe = func(*http.Server) error { return http.ErrServerClosed }
    flag.CommandLine = flag.NewFlagSet("lsget", flag.ContinueOnError)
    dir := makeTempDir(t)
    os.Args = []string{"lsget", "-dir", dir}
    main() // should not panic

    // Error path
    listenAndServe = func(*http.Server) error { return errors.New("boom") }
    flag.CommandLine = flag.NewFlagSet("lsget", flag.ContinueOnError)
    os.Args = []string{"lsget", "-dir", dir}
    defer func(){ if r := recover(); r != nil { if ep, ok := r.(exitPanic); ok { if ep.code != 1 { t.Fatalf("exit code: %d", ep.code) } } else { panic(r) } } }()
    main()
}
