package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkCommandWork(b *testing.B) {
	for _, fixture := range []struct {
		name        string
		depth, wide int
	}{
		{"wide", 1, 512},
		{"deep", 32, 8},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			root := b.TempDir()
			dir := root
			for depth := range fixture.depth {
				if depth > 0 {
					dir = filepath.Join(dir, "sub")
					if err := os.Mkdir(dir, 0o755); err != nil {
						b.Fatal(err)
					}
				}
				for i := range fixture.wide {
					name := filepath.Join(dir, fmt.Sprintf("item-%04d.txt", i))
					content := fmt.Sprintf("sample marker %d/%d\n", depth, i) + strings.Repeat("ordinary fixture text\n", 46)
					if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
						b.Fatal(err)
					}
				}
			}
			s := newServer(root, 4096, "", "")
			for _, cmd := range []struct{ name, input, want string }{
				{"find", "find / -name *.txt", "item-0000.txt"},
				{"grep", "grep -r marker /", "0000.txt"},
			} {
				b.Run(cmd.name, func(b *testing.B) {
					payload := fmt.Sprintf(`{"input":%q}`, cmd.input)
					for b.Loop() {
						w := httptest.NewRecorder()
						r := httptest.NewRequest(http.MethodPost, "/api/exec", strings.NewReader(payload))
						s.handleExec(w, r)
						if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), cmd.want) {
							b.Fatalf("%s failed: HTTP %d", cmd.name, w.Code)
						}
					}
				})
			}
		})
	}
}
