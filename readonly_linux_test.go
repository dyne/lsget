package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const readOnlyPDF = `%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources <<>> >>
endobj
xref
0 4
` + "0000000000 65535 f \n" +
	"0000000009 00000 n \n" +
	"0000000058 00000 n \n" +
	"0000000115 00000 n \n" +
	`trailer
<< /Size 4 /Root 1 0 R >>
startxref
202
%%EOF
`

const readOnlyManualSitemap = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>https://manual.example/nested/note.txt</loc></url></urlset>
`

const readOnlyREADME = "# Read-only fixture\n"

func TestReadOnlyServer(t *testing.T) {
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Fatal("run TestReadOnlyServer as an ordinary user to verify the serving permissions")
	}

	work := t.TempDir()
	binary := filepath.Join(work, "lsget")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build lsget: %v\n%s", err, output)
	}

	note := "Read-only fixture " + filepath.Base(filepath.Dir(work)) + "\n"
	content := seedReadOnlyContent(t, note)
	before := readOnlyManifest(t, content)
	t.Logf("manifest before startup:\n%s", strings.Join(before, "\n"))

	// The binary inherits this test process's identity, so a positive write
	// probe would invalidate the permissions used by the HTTP test.
	probe := filepath.Join(content, "creation-probe")
	if err := os.WriteFile(probe, []byte("permission probe\n"), 0o600); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("create %s as UID %d: got %v, want permission denied", probe, os.Geteuid(), err)
	}
	t.Logf("creation denied as UID %d", os.Geteuid())

	testReadOnlyServerRuns(t, binary, content, note, before)
}

func checkReadOnlyManifestAfterCleanup(t *testing.T, content string, before []string) {
	t.Helper()
	t.Cleanup(func() {
		after := readOnlyManifest(t, content)
		t.Logf("manifest after process exit:\n%s", strings.Join(after, "\n"))
		if !slices.Equal(after, before) {
			t.Error("the served content changed")
		}
	})
}

func seedReadOnlyContent(t *testing.T, note string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "content")
	dirs := []string{root, filepath.Join(root, "nested"), filepath.Join(root, "site")}
	for _, dir := range dirs {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Error(err)
			}
		})
	}

	for name, content := range map[string]string{
		".lsgetignore":       "ignored.txt\n",
		"README.md":          readOnlyREADME,
		"nested/note.txt":    note,
		"nested/report.pdf":  readOnlyPDF,
		"nested/ignored.txt": "hidden fixture entry\n",
		"site/index.html":    "<!doctype html><title>Fixture index</title><p>Hosted index</p>\n",
		"sitemap.xml":        readOnlyManualSitemap,
	} {
		name := filepath.Join(root, name)
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(name, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range dirs {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readOnlyManifest(t *testing.T, root string) []string {
	t.Helper()
	var manifest []string
	err := fs.WalkDir(os.DirFS(root), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch mode {
		case fs.ModeDir | 0o555:
			manifest = append(manifest, fmt.Sprintf("%q %s %d -", name, mode, info.Size()))
		case 0o444:
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				return err
			}
			manifest = append(manifest, fmt.Sprintf("%q %s %d %x", name, mode, info.Size(), sha256.Sum256(data)))
		default:
			return fmt.Errorf("unexpected mode %v for %s", mode, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func configureReadOnlyCommand(cmd *exec.Cmd, work string) {
	cmd.Dir = work
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "LSGET_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
}

func startReadOnlyServer(t *testing.T, binary, work, content, note string, flags ...string) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	log, err := os.Create(filepath.Join(work, "console.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Error(err)
		}
	})
	args := append([]string{"-addr", addr, "-dir", content}, flags...)
	cmd := exec.Command(binary, args...)
	configureReadOnlyCommand(cmd, work)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Errorf("send SIGTERM to lsget: %v", err)
		}
		select {
		case <-done:
			if waitErr != nil {
				t.Errorf("lsget shutdown: %v", waitErr)
			}
		case <-time.After(8 * time.Second):
			t.Error("lsget survived the SIGTERM deadline")
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("kill lsget: %v", err)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("lsget was not reaped after SIGKILL")
			}
		}
		if t.Failed() {
			output, err := os.ReadFile(log.Name())
			if err != nil {
				t.Error(err)
			}
			t.Logf("lsget output:\n%s", output)
		}
	})

	// Require this fixture's bytes before calling the reserved port ready.
	// This avoids treating another listener as our child after releasing the port.
	baseURL := "http://" + addr
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/nested/note.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			t.Fatalf("lsget exited before readiness: %v", waitErr)
		case <-ctx.Done():
			t.Fatal("lsget did not serve the fixture before the readiness deadline")
		case <-ticker.C:
			response, err := client.Do(request)
			if err != nil {
				continue
			}
			body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if err == nil && response.StatusCode == http.StatusOK && string(body) == note {
				t.Logf("lsget PID %d served the fixture over HTTP", cmd.Process.Pid)
				return baseURL, cmd.Process.Pid
			}
		}
	}
}
