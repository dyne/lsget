package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const readOnlySitemapBase = "https://generated.example"

func testReadOnlyServerRuns(t *testing.T, binary, content, note string, before []string) {
	t.Helper()
	t.Run("manual-sitemap-and-browsing-without-state", func(t *testing.T) {
		checkReadOnlyManifestAfterCleanup(t, content, before)
		work := t.TempDir()
		t.Cleanup(func() {
			entries, err := os.ReadDir(work)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "console.log" {
				t.Errorf("unexpected files with log and PID unset: %v", entries)
			}
		})
		baseURL, _ := startReadOnlyServer(t, binary, work, content, note)
		browser := newReadOnlyBrowser(t, baseURL)
		browser.checkBrowsing(note)
		browser.checkDownloads(note)
		body, _ := browser.get("/sitemap.xml", http.StatusOK, "application/xml")
		if body != readOnlyManualSitemap {
			t.Fatalf("manual sitemap = %q", body)
		}
		checkReadOnlySitemap(t, body, "https://manual.example/nested/note.txt")
	})

	t.Run("on-demand-sitemap-and-writable-state", func(t *testing.T) {
		checkReadOnlyManifestAfterCleanup(t, content, before)
		state := t.TempDir()
		pidFile := filepath.Join(state, "lsget.pid")
		logFile := filepath.Join(state, "logs", "access.log")
		t.Cleanup(func() {
			if _, err := os.Stat(pidFile); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("PID file after SIGTERM: %v, want missing", err)
			}
			data, err := os.ReadFile(logFile)
			if err != nil {
				t.Fatal(err)
			}
			for _, logged := range []string{"GET /sitemap.xml", "POST /api/exec?cmd=checksum&file=%2Fnested%2Fnote.txt"} {
				if !strings.Contains(string(data), logged) {
					t.Errorf("configured log lacks %q", logged)
				}
			}
		})
		baseURL, process := startReadOnlyServer(t, binary, t.TempDir(), content, note,
			"-pid", pidFile, "-logfile", logFile, "-baseurl", readOnlySitemapBase, "-sitemap", "0")
		data, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != strconv.Itoa(process.Pid) {
			t.Fatalf("PID file = %q, want %d", data, process.Pid)
		}
		browser := newReadOnlyBrowser(t, baseURL)
		body, _ := browser.get("/sitemap.xml", http.StatusOK, "application/xml")
		checkReadOnlyGeneratedSitemap(t, body)
		browser.checkChecksum("/nested/note.txt", note)
	})

	t.Run("periodic-sitemap-refresh", func(t *testing.T) {
		checkReadOnlyManifestAfterCleanup(t, content, before)
		baseURL, _ := startReadOnlyServer(t, binary, t.TempDir(), content, note, "-baseurl", readOnlySitemapBase, "-sitemap", "1")
		newReadOnlyBrowser(t, baseURL).checkSitemapRefresh()
	})

	for _, tc := range []struct{ name, flag, path, message string }{
		{"unwritable-pid", "-pid", "lsget.pid", "configuration error: failed to create PID file"},
		{"unwritable-log", "-logfile", "logs/access.log", "configuration error: failed to open log file"},
		{"read-only-log-file", "-logfile", "nested/note.txt", "configuration error: failed to open log file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkReadOnlyManifestAfterCleanup(t, content, before)
			path := filepath.Join(content, tc.path)
			checkCLIStartupFailure(t, binary, content, []string{tc.flag, path}, tc.message, path, "permission denied")
			t.Logf("%s reported permission denied as UID %d", tc.flag, os.Geteuid())
		})
	}
	testReadOnlyCLILifecycle(t, binary, content, note, before)
}

func checkCLIStartupFailure(t *testing.T, binary, content string, args []string, messages ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, append([]string{"-addr", "127.0.0.1:0", "-dir", content}, args...)...)
	configureReadOnlyCommand(cmd, t.TempDir())
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || strings.Contains(string(output), "Serving ") {
		t.Fatalf("startup exit: %v, output %q", err, output)
	}
	for _, message := range messages {
		if !strings.Contains(string(output), message) {
			t.Fatalf("startup output %q lacks %q", output, message)
		}
	}
}

type readOnlyBrowser struct {
	t       *testing.T
	client  *http.Client
	baseURL string
}

func newReadOnlyBrowser(t *testing.T, baseURL string) readOnlyBrowser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
		Jar:       jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return readOnlyBrowser{t: t, client: client, baseURL: baseURL}
}

func (b readOnlyBrowser) get(path string, status int, mediaType string) (string, http.Header) {
	b.t.Helper()
	return b.request(http.MethodGet, path, nil, nil, status, mediaType)
}

func (b readOnlyBrowser) request(method, path string, payload io.Reader, headers http.Header, status int, mediaType string) (string, http.Header) {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.t.Context(), method, b.baseURL+path, payload)
	if err != nil {
		b.t.Fatal(err)
	}
	if headers != nil {
		req.Header = headers
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != status {
		b.t.Fatalf("%s %s returned %d, want %d", method, path, resp.StatusCode, status)
	}
	if mediaType != "" {
		contentType := resp.Header.Get("Content-Type")
		if got, _, err := mime.ParseMediaType(contentType); err != nil || got != mediaType {
			b.t.Fatalf("%s %s Content-Type = %q, want %s", method, path, contentType, mediaType)
		}
	}
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		b.t.Fatalf("read %s %s: %v (bytes=%d)", method, path, err, len(body))
	}
	return string(body), resp.Header
}

func (b readOnlyBrowser) post(path string, payload, result any) {
	b.t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		b.t.Fatal(err)
	}
	body, _ := b.request(http.MethodPost, path, strings.NewReader(string(data)), nil, http.StatusOK, "")
	if err := json.Unmarshal([]byte(body), result); err != nil {
		b.t.Fatalf("%s JSON: %v", path, err)
	}
}

func (b readOnlyBrowser) command(input string) execResp {
	b.t.Helper()
	var result execResp
	b.post("/api/exec", execReq{Input: input}, &result)
	return result
}

func (b readOnlyBrowser) checkBrowsing(note string) {
	b.t.Helper()
	for _, page := range []struct{ path, marker string }{
		{"/", `data-initial-path="/"`},
		{"/nested/", `data-initial-path="/nested"`},
		{"/site/", "<p>Hosted index</p>"},
		{"/?nojs=1", `href="/nested?nojs=1"`},
		{"/nested/?nojs=1", `href="/nested/note.txt"`},
	} {
		body, _ := b.get(page.path, http.StatusOK, "text/html")
		if !strings.Contains(body, page.marker) || strings.Contains(body, "ignored.txt") {
			b.t.Errorf("%s: expected %q and no ignored entry", page.path, page.marker)
		}
	}

	body, headers := b.get("/api/config", http.StatusOK, "")
	var config configResp
	if err := json.Unmarshal([]byte(body), &config); err != nil {
		b.t.Fatal(err)
	}
	if config.CWD != "/" || config.CatMax != 4096 || config.Readme == nil ||
		*config.Readme != readOnlyREADME || config.DocType != "markdown" ||
		!strings.HasPrefix(headers.Get("Set-Cookie"), "sid=") {
		b.t.Fatalf("root config did not return the expected defaults, README and session cookie: %s", body)
	}
	if cd := b.command("cd nested"); cd.CWD != "/nested" {
		b.t.Fatalf("cd result = %+v", cd)
	}
	var completion completeResp
	b.post("/api/complete", completeReq{Path: "no", TextOnly: true}, &completion)
	if !slices.Equal(completion.Items, []completeItem{{Name: "note.txt"}}) {
		b.t.Errorf("completion = %+v, want note.txt", completion.Items)
	}

	// Terminal output may decorate matching text and line numbers with ANSI colors.
	plain := strings.NewReplacer(colorYellow, "", colorBold, "", colorGreen, "", colorReset, "")
	for _, command := range []struct{ input, want string }{
		{"ls", "note.txt"},
		{"cat note.txt", note},
		{"find . -name *.txt -type f", "/nested/note.txt"},
		{"grep -n fixture note.txt", "1:" + strings.TrimSuffix(note, "\n")},
	} {
		output := plain.Replace(b.command(command.input).Output)
		if !strings.Contains(output, command.want) || strings.Contains(output, "ignored.txt") {
			b.t.Errorf("%s = %q, want %q and no ignored entry", command.input, output, command.want)
		}
	}
	b.checkChecksum("note.txt", note)
}

func (b readOnlyBrowser) checkChecksum(path, note string) {
	b.t.Helper()
	output := b.command("checksum " + path).Output
	if want := fmt.Sprintf("SHA256: %x", sha256.Sum256([]byte(note))); !strings.Contains(output, want) {
		b.t.Errorf("checksum = %q, want %q", output, want)
	}
}

func (b readOnlyBrowser) checkDownloads(note string) {
	b.t.Helper()
	for _, prefix := range []string{"/", "/?file=/", "/api/download?path=/"} {
		path := prefix + "nested/report.pdf"
		body, headers := b.get(path, http.StatusOK, "application/pdf")
		const disposition = `attachment; filename="report.pdf"`
		if body != readOnlyPDF || headers.Get("Content-Disposition") != disposition {
			b.t.Errorf("%s did not return the expected PDF attachment", path)
		}
		body, headers = b.request(http.MethodHead, path, nil, nil, http.StatusOK, "application/pdf")
		if body != "" || headers.Get("Content-Length") != strconv.Itoa(len(readOnlyPDF)) ||
			headers.Get("Content-Disposition") != disposition {
			b.t.Errorf("HEAD %s returned body length %d and headers %v", path, len(body), headers)
		}
		rangeHeader := http.Header{"Range": {"bytes=0-7"}}
		body, headers = b.request(http.MethodGet, path, nil, rangeHeader, http.StatusPartialContent, "application/pdf")
		if body != readOnlyPDF[:8] || headers.Get("Content-Range") != fmt.Sprintf("bytes 0-7/%d", len(readOnlyPDF)) {
			b.t.Errorf("range %s returned %q and headers %v", path, body, headers)
		}
		if modified := headers.Get("Last-Modified"); modified != "" {
			conditional := http.Header{"If-Modified-Since": {modified}}
			b.request(http.MethodGet, path, nil, conditional, http.StatusNotModified, "")
		} else {
			b.t.Errorf("%s omitted Last-Modified", path)
		}
		b.get(prefix+"nested/ignored.txt", http.StatusNotFound, "")
	}

	body, _ := b.get("/api/download?dir=/nested", http.StatusOK, "application/zip")
	archive, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
	if err != nil {
		b.t.Fatal(err)
	}
	entries := map[string]string{"nested/note.txt": note, "nested/report.pdf": readOnlyPDF}
	if len(archive.File) != len(entries) {
		b.t.Fatalf("ZIP has %d entries, want %d", len(archive.File), len(entries))
	}
	for _, file := range archive.File {
		want, ok := entries[file.Name]
		if !ok {
			b.t.Fatalf("unexpected ZIP entry %q", file.Name)
		}
		if file.UncompressedSize64 != uint64(len(want)) {
			b.t.Fatalf("ZIP entry %s size = %d, want %d", file.Name, file.UncompressedSize64, len(want))
		}
		reader, err := file.Open()
		if err != nil {
			b.t.Fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(reader, int64(len(want)+1)))
		_ = reader.Close()
		if err != nil || string(data) != want {
			b.t.Errorf("ZIP entry %s did not match its fixture: %v", file.Name, err)
		}
		delete(entries, file.Name)
	}
}

func checkReadOnlySitemap(t *testing.T, body string, want ...string) {
	t.Helper()
	var sitemap struct {
		XMLName   xml.Name `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
		Locations []string `xml:"url>loc"`
	}
	if err := xml.Unmarshal([]byte(body), &sitemap); err != nil {
		t.Fatal(err)
	}
	for _, url := range want {
		if !slices.Contains(sitemap.Locations, url) {
			t.Errorf("sitemap lacks %q: %v", url, sitemap.Locations)
		}
	}
	for _, url := range sitemap.Locations {
		if strings.HasSuffix(url, "/ignored.txt") || strings.HasSuffix(url, "/sitemap.xml") {
			t.Errorf("unexpected sitemap location %q", url)
		}
	}
}

func checkReadOnlyGeneratedSitemap(t *testing.T, body string) {
	t.Helper()
	checkReadOnlySitemap(t, body, readOnlySitemapBase+"/", readOnlySitemapBase+"/nested/note.txt",
		readOnlySitemapBase+"/nested/report.pdf", readOnlySitemapBase+"/site/index.html")
}

func (b readOnlyBrowser) checkSitemapRefresh() {
	b.t.Helper()
	original, headers := b.get("/sitemap.xml", http.StatusOK, "application/xml")
	checkReadOnlyGeneratedSitemap(b.t, original)
	lastModified, err := http.ParseTime(headers.Get("Last-Modified"))
	if err != nil {
		b.t.Fatal(err)
	}

	// The CLI interval is in whole minutes. The HTTP timestamp changes when a
	// new in-memory snapshot is published, even with identical fixture content.
	deadline := time.NewTimer(75 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			b.t.Fatalf("Last-Modified remained %s after the configured sitemap refresh interval", lastModified)
		case <-ticker.C:
			body, headers := b.get("/sitemap.xml", http.StatusOK, "application/xml")
			modified, err := http.ParseTime(headers.Get("Last-Modified"))
			if err != nil {
				b.t.Fatal(err)
			}
			if body != original {
				b.t.Fatal("generated sitemap changed with its fixture unchanged")
			}
			if modified.After(lastModified) {
				if modified.Sub(lastModified) < 59*time.Second {
					b.t.Fatalf("sitemap refreshed before the configured minute elapsed: %s to %s", lastModified, modified)
				}
				b.t.Logf("periodic sitemap refreshed from %s to %s", lastModified, modified)
				return
			}
		}
	}
}
