package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectFilesForDownload(t *testing.T) {
	// Create test directory structure
	testDir := "test_download_dir"
	_ = os.RemoveAll(testDir)
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(testDir) }()

	// Create test files
	if err := os.WriteFile(filepath.Join(testDir, "file1.png"), []byte("content1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "file2.png"), []byte("content2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "file3.txt"), []byte("content3"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create subdirectory with files
	subDir := filepath.Join(testDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file4.png"), []byte("content4"), 0644); err != nil {
		t.Fatal(err)
	}

	rootAbs, _ := filepath.Abs(testDir)
	s := newServer(rootAbs, 256*1024, "")

	// Test wildcard pattern
	files, err := s.collectFilesForDownload("/", "*.png")
	if err != nil {
		t.Errorf("Failed to collect files: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("Expected 2 PNG files, got %d", len(files))
	}

	// Test directory download
	files, err = s.collectFilesForDownload("/", ".")
	if err != nil {
		t.Errorf("Failed to collect directory files: %v", err)
	}
	if len(files) != 4 {
		t.Errorf("Expected 4 files in directory, got %d", len(files))
	}
}

func TestCollectFilesFromDirectory(t *testing.T) {
	// Create test directory structure
	testDir := "test_dir_collect"
	_ = os.RemoveAll(testDir)
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(testDir) }()

	// Create test files
	if err := os.WriteFile(filepath.Join(testDir, "file1.txt"), []byte("content1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "file2.txt"), []byte("content2"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create subdirectory with files
	subDir := filepath.Join(testDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file3.txt"), []byte("content3"), 0644); err != nil {
		t.Fatal(err)
	}

	rootAbs, _ := filepath.Abs(testDir)
	s := newServer(rootAbs, 256*1024, "")

	// Test collecting files from directory
	files, err := s.collectFilesFromDirectory("/", rootAbs)
	if err != nil {
		t.Errorf("Failed to collect files from directory: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("Expected 3 files, got %d", len(files))
	}

	// Check that relative paths are correct
	for _, file := range files {
		if file.relativePath == "" {
			t.Errorf("File has empty relative path")
		}
	}
}
