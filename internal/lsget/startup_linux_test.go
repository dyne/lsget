package lsget

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testReadOnlyCLILifecycle(t *testing.T, binary, content, note string, before []string) {
	t.Helper()
	t.Run("signal-drains-and-restarts", func(t *testing.T) {
		checkReadOnlyManifestAfterCleanup(t, content, before)
		pidFile := filepath.Join(t.TempDir(), "lsget.pid")
		args := []string{"-pid", pidFile, "-baseurl", readOnlySitemapBase, "-sitemap", "1"}
		baseURL, process := startReadOnlyServer(t, binary, t.TempDir(), content, note, args...)
		addr := strings.TrimPrefix(baseURL, "http://")
		assertPID(t, pidFile, process.Pid)
		checkCLIStartupFailure(t, binary, content, []string{"-pid", pidFile}, "configuration error", pidFile, "file exists")
		assertPID(t, pidFile, process.Pid)

		conn, reader, req := holdCLIRequest(t, addr)
		if err := process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		waitForClosedListener(t, addr)
		assertPID(t, pidFile, process.Pid)
		if _, err := io.WriteString(conn, cliRequestBody); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, req)
		if err != nil {
			t.Fatalf("read request released after SIGTERM: %v", err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || !strings.Contains(string(body), `"cwd":"/"`) {
			t.Fatalf("released request: HTTP %d, body %q, errors %v and %v", response.StatusCode, body, readErr, closeErr)
		}
		process.awaitExit(t)
		assertPIDAbsent(t, pidFile)
		t.Log("SIGTERM stopped new connections, drained the confirmed request, and removed the owned PID file after draining")

		_, restarted := startReadOnlyServerAt(t, binary, t.TempDir(), content, note, addr, args...)
		assertPID(t, pidFile, restarted.Pid)
		if err := restarted.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		restarted.awaitExit(t)
		assertPIDAbsent(t, pidFile)
		t.Log("restarted on the same address and PID destination, served the read-only fixture, and reaped both children")
	})
	t.Run("interrupt-forces-close-and-preserves-replacement", func(t *testing.T) {
		checkReadOnlyManifestAfterCleanup(t, content, before)
		work := t.TempDir()
		pidFile := filepath.Join(work, "lsget.pid")
		baseURL, process := startReadOnlyServer(t, binary, work, content, note, "-pid", pidFile)
		addr := strings.TrimPrefix(baseURL, "http://")
		assertPID(t, pidFile, process.Pid)
		_, reader, req := holdCLIRequest(t, addr)
		// Use the same contents in a different file to check ownership by identity.
		replacement := filepath.Join(work, "replacement.pid")
		if err := os.WriteFile(replacement, []byte(strconv.Itoa(process.Pid)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, pidFile); err != nil {
			t.Fatal(err)
		}
		if err := process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		waitForClosedListener(t, addr)
		process.awaitExit(t)
		if response, err := http.ReadResponse(reader, req); !errors.Is(err, io.ErrUnexpectedEOF) {
			if response != nil {
				_ = response.Body.Close()
			}
			t.Fatalf("connection held past the shutdown deadline: response %v, error %v", response, err)
		}
		assertPID(t, pidFile, process.Pid)
		console, err := os.ReadFile(filepath.Join(work, "console.log"))
		if err != nil || !strings.Contains(string(console), "server shutdown error: context deadline exceeded") {
			t.Fatalf("shutdown deadline diagnostic: %q, %v", console, err)
		}
		checkCLIStartupFailure(t, binary, content, []string{"-pid", pidFile}, "configuration error", pidFile, "stale PID file")
		assertPID(t, pidFile, process.Pid)
		t.Log("SIGINT forced the held connection closed at the shutdown deadline and reaped the child; the replacement PID file survived shutdown and another startup")
	})
	t.Run("bind-failure-cleans-owned-pid", func(t *testing.T) {
		checkReadOnlyManifestAfterCleanup(t, content, before)
		occupied, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = occupied.Close() })
		addr := occupied.Addr().String()
		pidFile := filepath.Join(t.TempDir(), "lsget.pid")
		checkCLIStartupFailure(t, binary, content, []string{"-addr", addr, "-pid", pidFile}, "server error: failed to listen", addr, "address already in use")
		assertPIDAbsent(t, pidFile)
		t.Log("occupied listener produced a runtime error and the failed child removed its PID file")
	})

	t.Run("trusted-proxy-and-command-log", func(t *testing.T) {
		checkReadOnlyManifestAfterCleanup(t, content, before)
		logPath := filepath.Join(t.TempDir(), "access.log")
		endpoint, process := startReadOnlyServer(t, binary, t.TempDir(), content, note, "-logfile", logPath, "-trusted-proxies", "127.0.0.1/32")
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/api/exec?private=query-fixture", strings.NewReader(`{"input":"url /nested/note.txt"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "files.example"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "2001:db8::5")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Cookie", "sid=private-cookie-fixture")
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var execResponse execResp
		decodeErr := json.NewDecoder(response.Body).Decode(&execResponse)
		closeErr := response.Body.Close()
		if decodeErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || execResponse.Clipboard != "https://files.example/nested/note.txt" {
			t.Fatalf("trusted proxy real HTTP request: status %d, clipboard %q, errors %v and %v", response.StatusCode, execResponse.Clipboard, decodeErr, closeErr)
		}
		if err := process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		process.awaitExit(t)
		var matches []loggedAccess
		for _, record := range readAccessLog(t, logPath) {
			if strings.HasPrefix(record.request, "POST /api/exec") {
				matches = append(matches, record)
			}
		}
		if len(matches) != 2 || matches[0].request != "POST /api/exec?cmd=url&file=%2Fnested%2Fnote.txt HTTP/1.1" || matches[1].request != "POST /api/exec HTTP/1.1" ||
			matches[0].peer != "2001:db8::5" || matches[1].peer != "2001:db8::5" || matches[0].status != "200" || matches[1].status != "200" {
			t.Fatalf("trusted proxy command and access records %+v", matches)
		}
		data, err := os.ReadFile(logPath)
		if err != nil || strings.Contains(string(data), "query-fixture") || strings.Contains(string(data), "private-cookie-fixture") {
			t.Fatalf("unexpected query or cookie in native log %q, %v", data, err)
		}
		t.Log("built CLI used validated HTTPS forwarding, logged one command and one request with IPv6 and HTTP 200, and excluded query and cookie data")
	})
}

const cliRequestBody = `{"input":"pwd"}`

func holdCLIRequest(t *testing.T, addr string) (net.Conn, *bufio.Reader, *http.Request) {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/api/exec", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "POST /api/exec HTTP/1.1\r\nHost: %s\r\nExpect: 100-continue\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", addr, len(cliRequestBody)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatalf("wait for server to request the held body: %v", err)
	}
	if err := response.Body.Close(); err != nil || response.StatusCode != http.StatusContinue {
		t.Fatalf("held request precondition: HTTP %d, close %v", response.StatusCode, err)
	}
	t.Log("HTTP 100 Continue confirmed the CLI entered the handler with the body held")
	return conn, reader, req
}

func waitForClosedListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("listener continued accepting connections after the signal")
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp4", addr, 100*time.Millisecond)
			if errors.Is(err, syscall.ECONNREFUSED) {
				return
			}
			if err == nil {
				_ = conn.Close()
			}
		}
	}
}

func assertPID(t *testing.T, path string, pid int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != strconv.Itoa(pid) {
		t.Fatalf("PID file %q = %q, %v; want %d", path, data, err, pid)
	}
}

func assertPIDAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PID file %q after child exit: %v; want missing", path, err)
	}
}
