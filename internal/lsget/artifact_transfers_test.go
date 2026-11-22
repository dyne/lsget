package lsget

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestArtifactTransfers(t *testing.T) {
	s := newTestServer(t)
	const archiveName = "release files.tar.gz"
	archivePath := "/release/" + archiveName
	archive := makeArtifactTarball(t)
	notes := []byte("A benign local release fixture.\n")
	pdf := []byte("%PDF-1.4\n% Local download fixture\n%%EOF\n")
	for path, data := range map[string][]byte{
		archivePath:               archive,
		"/release/docs/notes.txt": notes,
		"/release/ignored.txt":    []byte("ignore fixture"),
		"/guide.pdf":              pdf,
		"/.lsgetignore":           []byte("release/ignored.txt\n"),
	} {
		writeWorkFixture(t, s.rootAbs, strings.TrimPrefix(path, "/"), data)
	}
	modified := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	fixture := filepath.Join(s.rootAbs, filepath.FromSlash(strings.TrimPrefix(archivePath, "/")))
	if err := os.Chtimes(fixture, modified, modified); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(s.httpServer("").Handler)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 5 * time.Second
	request := func(t *testing.T, method, target string, headers http.Header) (*http.Response, []byte) {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, server.URL+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header = headers
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, body
	}

	wantDigest := sha256.Sum256(archive)
	wantMIME := mime.TypeByExtension(".gz")
	if wantMIME == "" {
		wantMIME = "application/octet-stream"
	}
	escapedPath := (&url.URL{Path: archivePath}).EscapedPath()
	for _, route := range []struct{ name, target string }{
		{"direct", escapedPath},
		{"query", "/?file=" + url.QueryEscape(archivePath)},
		{"api_download", "/api/download?path=" + url.QueryEscape(archivePath)},
		{"api_static", "/api/static" + escapedPath},
	} {
		t.Run(route.name, func(t *testing.T) {
			resp, body := request(t, http.MethodGet, route.target, nil)
			checkArtifactHeaders(t, resp, http.StatusOK, wantMIME, "attachment", archiveName)
			gotDigest := sha256.Sum256(body)
			if gotDigest != wantDigest {
				t.Fatalf("download digest %x, want %x", gotDigest, wantDigest)
			}
			lastModified := resp.Header.Get("Last-Modified")
			if lastModified != modified.Format(http.TimeFormat) || resp.Header.Get("Accept-Ranges") != "bytes" {
				t.Fatalf("Last-Modified = %q, Accept-Ranges = %q", lastModified, resp.Header.Get("Accept-Ranges"))
			}

			resp, body = request(t, http.MethodHead, route.target, nil)
			checkArtifactHeaders(t, resp, http.StatusOK, wantMIME, "attachment", archiveName)
			if len(body) != 0 || resp.Header.Get("Content-Length") != strconv.Itoa(len(archive)) {
				t.Fatalf("HEAD returned %d body bytes and Content-Length %q", len(body), resp.Header.Get("Content-Length"))
			}

			resp, body = request(t, http.MethodGet, route.target, http.Header{"Range": {"bytes=5-16"}})
			checkArtifactHeaders(t, resp, http.StatusPartialContent, wantMIME, "attachment", archiveName)
			if !bytes.Equal(body, archive[5:17]) || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 5-16/%d", len(archive)) {
				t.Fatalf("range returned %x and Content-Range %q", body, resp.Header.Get("Content-Range"))
			}

			resp, body = request(t, http.MethodGet, route.target, http.Header{"If-Modified-Since": {lastModified}})
			if resp.StatusCode != http.StatusNotModified || len(body) != 0 {
				t.Fatalf("conditional GET returned HTTP %d and %d bytes", resp.StatusCode, len(body))
			}
		})
	}

	t.Run("terminal_fallback_and_visibility", func(t *testing.T) {
		resp, body := request(t, http.MethodGet, "/release/missing.tar.gz", nil)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") ||
			resp.Header.Get("Content-Disposition") != "" || !bytes.Contains(body, []byte(`<template id="terminal-help">`)) {
			t.Fatalf("missing direct path did not return the terminal: HTTP %d, headers %v", resp.StatusCode, resp.Header)
		}
		for _, target := range []string{
			"/?file=/release/missing.tar.gz", "/api/download?path=/release/missing.tar.gz",
			"/release/ignored.txt", "/?file=/release/ignored.txt", "/api/download?path=/release/ignored.txt",
		} {
			resp, _ := request(t, http.MethodGet, target, nil)
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s returned HTTP %d, want 404", target, resp.StatusCode)
			}
		}
	})

	t.Run("pdf", func(t *testing.T) {
		for _, route := range []struct{ name, target, disposition string }{
			{"direct", "/guide.pdf", "inline"},
			{"query", "/?file=guide.pdf", "inline"},
			{"api_static", "/api/static/guide.pdf", "inline"},
			{"api_download", "/api/download?path=/guide.pdf", "attachment"},
		} {
			t.Run(route.name, func(t *testing.T) {
				resp, body := request(t, http.MethodGet, route.target, nil)
				checkArtifactHeaders(t, resp, http.StatusOK, "application/pdf", route.disposition, "guide.pdf")
				if !bytes.Equal(body, pdf) {
					t.Fatalf("%s returned %q, want %q", route.target, body, pdf)
				}
			})
		}
	})

	t.Run("zips", func(t *testing.T) {
		for _, route := range []struct {
			target   string
			filename string
			entries  map[string][]byte
		}{
			{"/api/download?dir=/release", "release.zip", map[string][]byte{"release/" + archiveName: archive, "release/docs/notes.txt": notes}},
			{"/api/download?pattern=/release/*.tar.gz", "archive.zip", map[string][]byte{archiveName: archive}},
		} {
			resp, body := request(t, http.MethodGet, route.target, nil)
			checkArtifactHeaders(t, resp, http.StatusOK, "application/zip", "attachment", route.filename)
			zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
			if err != nil {
				t.Fatal(err)
			}
			if len(zr.File) != len(route.entries) {
				t.Fatalf("%s ZIP contains %d entries, want %d", route.target, len(zr.File), len(route.entries))
			}
			for name, want := range route.entries {
				data, err := fs.ReadFile(zr, name)
				if err != nil || !bytes.Equal(data, want) {
					t.Errorf("entry %q: read %d bytes, want %d, error %v", name, len(data), len(want), err)
				}
			}
		}
	})
}

func checkArtifactHeaders(t *testing.T, resp *http.Response, status int, contentType, disposition, filename string) {
	t.Helper()
	gotDisposition, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if resp.StatusCode != status || resp.Header.Get("Content-Type") != contentType ||
		err != nil || gotDisposition != disposition || params["filename"] != filename {
		t.Fatalf("HTTP %d, MIME %q, disposition %q; want HTTP %d, MIME %q, %s %q",
			resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"), status, contentType, disposition, filename)
	}
}

func makeArtifactTarball(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	compressed := gzip.NewWriter(&data)
	archive := tar.NewWriter(compressed)
	const text = "Test release artifact.\n"
	if err := archive.WriteHeader(&tar.Header{Name: "README.txt", Mode: 0o644, Size: int64(len(text))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(archive, text); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestArtifactArchiveFinalization(t *testing.T) {
	s := newTestServer(t)
	logPath := captureArchiveLog(t, s)
	// The compressed archive fits in the ZIP writer's buffer until Close.
	writeWorkFixture(t, s.rootAbs, "release/README.txt", []byte("small release fixture"))
	handler := s.httpServer("").Handler
	attempts := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writer := &rejectArchiveWriter{ResponseWriter: w}
		defer func() { attempts <- writer.writes }()
		handler.ServeHTTP(writer, r)
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 5 * time.Second
	resp, err := client.Get(server.URL + "/api/download?pattern=/release/*.txt")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("failed ZIP finalization returned response %v and error %v, want transport EOF", resp, err)
	}
	select {
	case got := <-attempts:
		if got != 1 {
			t.Fatalf("ZIP finalization attempted %d response writes, want one", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ZIP handler did not settle")
	}
	checkArchiveFailureLog(t, logPath, "GET /api/download HTTP/1.1", "finalize ZIP", errArchiveFixtureWrite.Error())
}

func TestArtifactArchiveMissingEntry(t *testing.T) {
	root := openTestDownloadRoot(t, t.TempDir())
	w := httptest.NewRecorder()
	files := []fileInfo{{virtualPath: "/missing.txt", relativePath: "missing.txt"}}
	err := sendZipArchive(t.Context(), w, root, files, "archive.zip")
	if !errors.Is(err, fs.ErrNotExist) || w.Body.Len() != 0 {
		t.Fatalf("missing archive entry returned error %v and %d body bytes", err, w.Body.Len())
	}
}

type rejectArchiveWriter struct {
	http.ResponseWriter
	writes int
}

func (w *rejectArchiveWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errArchiveFixtureWrite
}

func captureArchiveLog(t *testing.T, s *server) string {
	t.Helper()
	s.logfile = filepath.Join(t.TempDir(), "access.log")
	return s.logfile
}

func checkArchiveFailureLog(t *testing.T, logPath string, causes ...string) {
	t.Helper()
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	const marker = " archive=incomplete error="
	if n := bytes.Count(got, []byte(marker)); n != 1 {
		t.Fatalf("access log marked %d transfers incomplete, want one", n)
	}
	for _, line := range strings.Split(string(got), "\n") {
		if strings.Contains(line, marker) {
			for _, cause := range causes {
				if !strings.Contains(line, cause) {
					t.Errorf("incomplete archive log %q lacks %q", line, cause)
				}
			}
			return
		}
	}
}
