package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestCommandBudgets(t *testing.T) {
	t.Run("bounded_directory_read", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"f", "e", "d", "c", "b", "a"} {
			writeWorkFixture(t, root, name, nil)
		}
		dir, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = dir.Close() })
		budget := &workBudget{entries: 2}
		entries, err := budget.readEntries(t.Context(), dir)
		if !errors.Is(err, errWorkLimit) || len(entries) != 2 || budget.entries != 0 {
			t.Fatalf("bounded read returned %d entries, %d remaining and %v", len(entries), budget.entries, err)
		}
		if entries[0].Name() >= entries[1].Name() {
			t.Fatalf("partial results are out of order: %s, %s", entries[0].Name(), entries[1].Name())
		}
		_, err = dir.ReadDir(3)
		if err != nil {
			t.Fatalf("entries beyond the lookahead should remain unread: %v", err)
		}
	})

	t.Run("child_budget_propagates", func(t *testing.T) {
		dir := t.TempDir()
		writeWorkFixture(t, dir, "branch/a.txt", nil)
		writeWorkFixture(t, dir, "branch/b.txt", nil)
		writeWorkFixture(t, dir, "sibling.txt", nil)
		root := openTestDownloadRoot(t, dir)
		var visited []string
		budget := newWorkBudget()
		budget.entries = 3
		err := walkVisibleDir(t.Context(), budget, root, ".", func(entry visibleEntry) (bool, error) {
			visited = append(visited, entry.name)
			return true, nil
		})
		if !errors.Is(err, errWorkLimit) || len(visited) != 2 || visited[0] != "branch" || !strings.HasPrefix(visited[1], "branch/") {
			t.Fatalf("child should propagate its limit before visiting sibling: visited %v, result %v", visited, err)
		}
		budget = newWorkBudget()
		budget.entries = 3
		files, err := collectFilesFromDirectory(t.Context(), budget, root, ".")
		w := httptest.NewRecorder()
		if len(files) != 0 || !writeArchivePreparationError(w, err) ||
			w.Code != http.StatusUnprocessableEntity || w.Header().Get("Content-Disposition") != "" {
			t.Fatalf("archive preparation returned %v, %v and HTTP %d", files, err, w.Code)
		}
	})

	t.Run("resolved_path_budget", func(t *testing.T) {
		dir := t.TempDir()
		writeWorkFixture(t, dir, "branch/a.txt", nil)
		writeWorkFixture(t, dir, "branch/b.txt", nil)
		budget := newWorkBudget()
		budget.pathBytes = len("branch/a.txt")
		children, err := visibleChildren(t.Context(), budget, openTestDownloadRoot(t, dir), "branch")
		if !errors.Is(err, errWorkLimit) || len(children) != 1 || children[0].name != "branch/a.txt" {
			t.Fatalf("path budget returned %v and %v", children, err)
		}
	})

	t.Run("cancel_during_walk", func(t *testing.T) {
		dir := t.TempDir()
		writeWorkFixture(t, dir, "branch/leaf.txt", nil)
		writeWorkFixture(t, dir, "sibling.txt", nil)
		root := openTestDownloadRoot(t, dir)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started, release, settled := make(chan struct{}), make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		var names []string
		var walkErr error
		go func() {
			defer close(settled)
			walkErr = walkVisibleDir(ctx, newWorkBudget(), root, ".", func(entry visibleEntry) (bool, error) {
				names = append(names, entry.name)
				if len(names) == 1 {
					close(started)
					<-release
				}
				return true, nil
			})
		}()
		t.Cleanup(func() {
			unblock()
			awaitSessionSignal(t, settled, "walk cleanup")
		})
		awaitSessionSignal(t, started, "walk reached first entry")
		t.Log("walk entered first visitor and held before descending")
		cancel()
		unblock()
		awaitSessionSignal(t, settled, "canceled walk")
		t.Log("walk gate released and worker settled")
		if !errors.Is(walkErr, context.Canceled) || !slices.Equal(names, []string{"branch"}) {
			t.Fatalf("canceled walk visited %v and returned %v", names, walkErr)
		}
		lastCtx, cancelLast := context.WithCancel(t.Context())
		defer cancelLast()
		err := walkVisibleDir(lastCtx, newWorkBudget(), root, "branch", func(visibleEntry) (bool, error) {
			cancelLast()
			return false, nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation at the final visited entry returned %v", err)
		}
	})

	t.Run("grep_long_line", func(t *testing.T) {
		s := newTestServer(t)
		content := strings.Repeat("x", maxGrepLine+1) + "\nneedle after long line\n"
		writeWorkFixture(t, s.rootAbs, "long.txt", []byte(content))
		got := runWorkCommand(t, s, "grep -n needle /long.txt")
		if !got.Truncated || !strings.Contains(got.Output, colorGreen+"2"+colorReset) ||
			!strings.Contains(got.Output, " after long line\n"+truncationNote) {
			t.Fatalf("long-line search returned %+v", got)
		}
	})

	t.Run("grep_unicode_fold", func(t *testing.T) {
		s := newTestServer(t)
		writeWorkFixture(t, s.rootAbs, "text.txt", []byte("K temperature\n"))
		got := runWorkCommand(t, s, "grep -i k /text.txt")
		if got.Truncated || got.Output != colorYellow+colorBold+"K"+colorReset+" temperature" {
			t.Fatalf("case-insensitive search returned %+v", got)
		}
	})

	t.Run("grep_output_budget", func(t *testing.T) {
		s := newTestServer(t)
		content := strings.Repeat("needle "+strings.Repeat("sample ", 2000)+"\n", 21) + "needle after limit\n"
		writeWorkFixture(t, s.rootAbs, "wide.txt", []byte(content))
		got := runWorkCommand(t, s, "grep needle /wide.txt")
		if !got.Truncated || len(got.Output) == 0 || len(got.Output) > maxCommandText ||
			!strings.Contains(got.Output, "sample") || strings.Contains(got.Output, "after limit") ||
			!strings.HasSuffix(got.Output, truncationNote) {
			t.Fatalf("result exceeded its contract: %d bytes, truncated=%t", len(got.Output), got.Truncated)
		}
	})

	t.Run("grep_shared_scan_budget", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("needle\n" + strings.Repeat("x", 4000))
		writeWorkFixture(t, dir, "first.txt", content)
		writeWorkFixture(t, dir, "second.txt", content)
		search := newGrepSearch("needle", false, false)
		budget := newWorkBudget()
		budget.scanBytes = int64(len(content) + 10)
		err := search.directory(t.Context(), budget, openTestDownloadRoot(t, dir), ".")
		got := search.output.response("grep: no matches found", err)
		if !errors.Is(err, errWorkLimit) || !got.Truncated || budget.scanBytes != 0 ||
			!strings.Contains(got.Output, "first.txt") || strings.Contains(got.Output, "second.txt") {
			t.Fatalf("shared scan budget returned %v and %+v with %d bytes left", err, got, budget.scanBytes)
		}
	})

	t.Run("checksum_read_cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		input := &cancelAfterRead{cancel: cancel}
		n, err := io.Copy(io.Discard, contextReader{ctx, input})
		if n != 1 || input.reads != 1 || !errors.Is(err, context.Canceled) {
			t.Fatalf("copy read %d bytes in %d underlying reads, returned %v", n, input.reads, err)
		}
	})

	t.Run("completion_cap", func(t *testing.T) {
		s := newTestServer(t)
		for i := range 201 {
			writeWorkFixture(t, s.rootAbs, fmt.Sprintf("sample%03d.txt", i), nil)
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/complete", strings.NewReader(`{"path":"/sample"}`))
		s.httpServer("").Handler.ServeHTTP(w, req)
		got := decodeSessionResponse[completeResp](t, w)
		if !got.Truncated || len(got.Items) != 200 || got.Items[0].Name != "sample000.txt" ||
			got.Items[199].Name != "sample199.txt" {
			t.Fatalf("completion returned %d items and truncated=%t", len(got.Items), got.Truncated)
		}
	})

	t.Run("archive_cancellation", func(t *testing.T) { testArchiveWorkRelease(t, false) })
	t.Run("archive_writer_error", func(t *testing.T) { testArchiveWorkRelease(t, true) })
}

func writeWorkFixture(t *testing.T, root, name string, contents []byte) {
	t.Helper()
	fullName := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(fullName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullName, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runWorkCommand(t *testing.T, s *server, command string) execResp {
	t.Helper()
	body := fmt.Sprintf(`{"input":%q}`, command)
	r := httptest.NewRequest(http.MethodPost, "/api/exec", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.httpServer("").Handler.ServeHTTP(w, r)
	return decodeSessionResponse[execResp](t, w)
}

type cancelAfterRead struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelAfterRead) Read(p []byte) (int, error) {
	r.reads++
	r.cancel()
	p[0] = 'x'
	return 1, nil
}

var errArchiveFixtureWrite = errors.New("fixture output closed")

type gatedArchiveWriter struct {
	*httptest.ResponseRecorder
	started chan struct{}
	resume  chan struct{}
	fail    bool
	writes  int
}

func (w *gatedArchiveWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		close(w.started)
		<-w.resume
	}
	if w.fail {
		return 0, errArchiveFixtureWrite
	}
	return w.ResponseRecorder.Write(p)
}

func testArchiveWorkRelease(t *testing.T, writerError bool) {
	t.Helper()
	logPath := captureArchiveLog(t)
	s := newTestServer(t)
	s.workSlots = make(chan struct{}, 1)
	handler := s.httpServer("").Handler
	raw := make([]byte, 192*1024)
	// Keep the fixture large enough to trigger an actual write before finalization.
	gen := rand.New(rand.NewSource(30))
	if _, err := gen.Read(raw); err != nil {
		t.Fatal(err)
	}
	writeWorkFixture(t, s.rootAbs, "archive/sample.bin", raw)
	writeWorkFixture(t, s.rootAbs, "readme.txt", []byte("public fixture"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &gatedArchiveWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), resume: make(chan struct{}), fail: writerError}
	unblock := sync.OnceFunc(func() { close(w.resume) })
	settled := make(chan struct{})
	var terminal any
	go func() {
		defer close(settled)
		defer func() { terminal = recover() }()
		handler.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/download?dir=/archive", nil))
	}()
	t.Cleanup(func() {
		unblock()
		awaitSessionSignal(t, settled, "archive cleanup")
	})
	awaitSessionSignal(t, w.started, "archive reached its downstream writer")
	t.Log("archive held its only slot at the first downstream write")
	busy := httptest.NewRecorder()
	handler.ServeHTTP(busy, httptest.NewRequest(http.MethodGet, "/api/download?dir=/archive", nil))
	if busy.Code != http.StatusServiceUnavailable || busy.Header().Get("Retry-After") != "1" {
		t.Fatalf("concurrent ZIP returned %d and Retry-After %q", busy.Code, busy.Header().Get("Retry-After"))
	}
	command := httptest.NewRecorder()
	handler.ServeHTTP(command, httptest.NewRequest(http.MethodPost, "/api/exec", strings.NewReader(`{"input":"sum /readme.txt"}`)))
	if command.Code != http.StatusServiceUnavailable || command.Header().Get("Retry-After") != "1" {
		t.Fatalf("checksum did not share the ZIP job limit: HTTP %d", command.Code)
	}
	direct := httptest.NewRecorder()
	handler.ServeHTTP(direct, httptest.NewRequest(http.MethodGet, "/api/download?path=/readme.txt", nil))
	if direct.Code != http.StatusOK || direct.Body.String() != "public fixture" {
		t.Fatalf("direct download failed while ZIP held a slot: %d %q", direct.Code, direct.Body.String())
	}

	if !writerError {
		cancel()
	}
	unblock()
	awaitSessionSignal(t, settled, "archive termination")
	t.Log("archive gate released and worker settled")
	if terminal != http.ErrAbortHandler {
		t.Fatalf("interrupted ZIP terminated with %v, want http.ErrAbortHandler", terminal)
	}
	if w.writes != 1 || len(s.workSlots) != 0 {
		t.Fatalf("interrupted ZIP attempted %d downstream writes and retained %d slots", w.writes, len(s.workSlots))
	}
	if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); err == nil {
		t.Fatal("interrupted download unexpectedly contains a complete ZIP")
	}

	later := httptest.NewRecorder()
	handler.ServeHTTP(later, httptest.NewRequest(http.MethodGet, "/api/download?dir=/archive", nil))
	zipped, err := zip.NewReader(bytes.NewReader(later.Body.Bytes()), int64(later.Body.Len()))
	if later.Code != http.StatusOK || err != nil || len(zipped.File) != 1 || zipped.File[0].Name != "archive/sample.bin" {
		t.Fatalf("released slot did not admit a complete ZIP: status %d, error %v", later.Code, err)
	}
	cause := context.Canceled.Error()
	if writerError {
		cause = errArchiveFixtureWrite.Error()
	}
	checkArchiveFailureLog(t, logPath, "/api/download?dir=/archive", "write ZIP entry", cause)
	t.Log("a later request acquired the released slot and received a complete archive")
}
