package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseLogStats(t *testing.T) {
	// Create a temporary log file
	logContent := `127.0.0.1 - - [11/Dec/2025:10:15:30 +0000] "POST /api/exec?cmd=url&file=%2Ffile1.txt HTTP/1.1" 200 0 "-" "-"
127.0.0.1 - - [11/Dec/2025:10:16:45 +0000] "POST /api/exec?cmd=get&file=%2Ffile1.txt HTTP/1.1" 200 0 "-" "-"
127.0.0.1 - - [11/Dec/2025:10:17:20 +0000] "GET /file2.txt HTTP/1.1" 200 2048 "-" "Mozilla/5.0"
127.0.0.1 - - [11/Dec/2025:10:18:00 +0000] "POST /api/exec?cmd=share&file=%2Fdocs%2Freadme.md HTTP/1.1" 200 0 "-" "-"
127.0.0.1 - - [11/Dec/2025:10:19:15 +0000] "GET /api/download?path=%2Fdocs%2Freadme.md HTTP/1.1" 200 512 "-" "Mozilla/5.0"
127.0.0.1 - - [11/Dec/2025:10:20:30 +0000] "GET /api/static/file1.txt HTTP/1.1" 200 1024 "-" "Mozilla/5.0"
127.0.0.1 - - [11/Dec/2025:10:21:45 +0000] "POST /api/exec?cmd=url&file=%2Ffile1.txt HTTP/1.1" 200 0 "-" "-"
127.0.0.1 - - [11/Dec/2025:10:22:00 +0000] "GET /api/download?dir=%2Fdata HTTP/1.1" 200 5120 "-" "Mozilla/5.0"
127.0.0.1 - - [11/Dec/2025:10:25:45 +0000] "GET /docs/guide.pdf HTTP/1.1" 200 10240 "-" "Mozilla/5.0"
127.0.0.1 - - [11/Dec/2025:10:26:00 +0000] "GET /api/download?pattern=*.txt&cwd=%2F HTTP/1.1" 200 3072 "-" "Mozilla/5.0"
127.0.0.1 - - [11/Dec/2025:10:27:00 +0000] "POST /api/exec?cmd=sum&file=%2Ffile1.txt HTTP/1.1" 200 0 "-" "-"
127.0.0.1 - - [11/Dec/2025:10:28:00 +0000] "GET /api/static/docs/readme.md HTTP/1.1" 200 512 "-" "Mozilla/5.0"
`

	tmpFile, err := os.CreateTemp("", "test_log_*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	if _, err := tmpFile.WriteString(logContent); err != nil {
		t.Fatal(err)
	}
	_ = tmpFile.Close()

	// Parse the log file
	stats, err := parseLogStats(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to parse log: %v", err)
	}

	// Check shares stats
	if stats.shares["/file1.txt"] != 2 {
		t.Errorf("Expected 2 shares for /file1.txt, got %d", stats.shares["/file1.txt"])
	}
	if stats.shares["/docs/readme.md"] != 1 {
		t.Errorf("Expected 1 share for /docs/readme.md, got %d", stats.shares["/docs/readme.md"])
	}

	// Check gets stats
	if stats.gets["/file1.txt"] != 1 {
		t.Errorf("Expected 1 get for /file1.txt, got %d", stats.gets["/file1.txt"])
	}
	if stats.gets["/docs/readme.md"] != 1 {
		t.Errorf("Expected 1 get for /docs/readme.md, got %d", stats.gets["/docs/readme.md"])
	}
	if stats.gets["/data (dir)"] != 1 {
		t.Errorf("Expected 1 get for /data (dir), got %d", stats.gets["/data (dir)"])
	}
	if stats.gets["(pattern match)"] != 1 {
		t.Errorf("Expected 1 pattern get, got %d", stats.gets["(pattern match)"])
	}

	// Check direct access stats
	if stats.directAccess["/file2.txt"] != 1 {
		t.Errorf("Expected 1 direct access for /file2.txt, got %d", stats.directAccess["/file2.txt"])
	}
	if stats.directAccess["/file1.txt"] != 1 {
		t.Errorf("Expected 1 direct access for /file1.txt, got %d", stats.directAccess["/file1.txt"])
	}
	if stats.directAccess["/docs/guide.pdf"] != 1 {
		t.Errorf("Expected 1 direct access for /docs/guide.pdf, got %d", stats.directAccess["/docs/guide.pdf"])
	}
	if stats.directAccess["/docs/readme.md"] != 1 {
		t.Errorf("Expected 1 direct access for /docs/readme.md, got %d", stats.directAccess["/docs/readme.md"])
	}

	// Check checksums stats
	if stats.checksums["/file1.txt"] != 1 {
		t.Errorf("Expected 1 checksum for /file1.txt, got %d", stats.checksums["/file1.txt"])
	}
}

func TestRenderStatsTable(t *testing.T) {
	stats := &logStats{
		shares: map[string]int{
			"/file1.txt": 2,
		},
		gets: map[string]int{
			"/file1.txt": 1,
			"/docs/readme.md": 1,
		},
		directAccess: map[string]int{
			"/file2.txt": 1,
		},
		checksums: map[string]int{
			"/file1.txt": 1,
		},
	}

	output := renderStatsTable(stats)

	// Check that output contains expected elements
	if !strings.Contains(output, "File/Directory") {
		t.Error("Output should contain 'File/Directory' header")
	}
	if !strings.Contains(output, "Shares") {
		t.Error("Output should contain 'Shares' header")
	}
	if !strings.Contains(output, "Gets") {
		t.Error("Output should contain 'Gets' header")
	}
	if !strings.Contains(output, "Direct Access") {
		t.Error("Output should contain 'Direct Access' header")
	}
	if !strings.Contains(output, "Downloads") {
		t.Error("Output should contain 'Downloads' header")
	}
	if !strings.Contains(output, "Checksums") {
		t.Error("Output should contain 'Checksums' header")
	}
	if !strings.Contains(output, "TOTAL") {
		t.Error("Output should contain 'TOTAL' row")
	}
	if !strings.Contains(output, "/file1.txt") {
		t.Error("Output should contain '/file1.txt'")
	}
	if !strings.Contains(output, "/docs/readme.md") {
		t.Error("Output should contain '/docs/readme.md'")
	}
	if !strings.Contains(output, "/file2.txt") {
		t.Error("Output should contain '/file2.txt'")
	}
}

func TestURLDecode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"%2Ffile1.txt", "/file1.txt"},
		{"%2Fdocs%2Freadme.md", "/docs/readme.md"},
		{"%20file%20with%20spaces.txt", " file with spaces.txt"},
		{"%23%3F%26%2B", "#?&+"},
	}

	for _, test := range tests {
		result, err := urlDecode(test.input)
		if err != nil {
			t.Errorf("urlDecode(%q) returned error: %v", test.input, err)
		}
		if result != test.expected {
			t.Errorf("urlDecode(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}
