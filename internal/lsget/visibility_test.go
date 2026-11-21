package lsget

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadVisibilityMatrix(t *testing.T) {
	s := newServer(t.TempDir(), 4096, "", "")
	const publicName = "résumé #1.txt"
	const publicPath = "/docs/" + publicName
	const publicBody = "A public document.\n"
	for name, content := range map[string]string{
		".lsgetignore":                "docs/parent-blocked\nwalk/deep-stop\n",
		"docs/.lsgetignore":           "README.md\nindex.html\nhidden.txt\nhidden.png\nprivate\n",
		"docs/README.md":              "excluded README",
		"docs/README.txt":             "public README",
		"docs/index.html":             "excluded index",
		"docs/hidden.txt":             "excluded text",
		"docs/hidden.png":             "excluded image",
		"docs/private/README.md":      "excluded nested README",
		"docs/parent-blocked/box.txt": "excluded by root rule",
		"docs/.public.txt":            "public dotfile",
		"docs/" + publicName:          publicBody,
		"shared/item.txt":             "public linked document",
		"shared/.lsgetignore":         "quiet.txt\n",
		"shared/quiet.txt":            "excluded linked document",
		"landing/index.html":          "<html>public index</html>",
		"walk/.lsgetignore":           "blocked\nnested/hidden.txt\n",
		"walk/.dot.txt":               "needle dot file",
		"walk/.dotdir/item.txt":       "needle dot directory",
		"walk/a.txt":                  "needle public A",
		"walk/blocked/excluded.txt":   "needle excluded local",
		"walk/deep-stop/excluded.txt": "needle excluded ancestor",
		"walk/nested/b.txt":           "needle public B",
		"walk/nested/hidden.txt":      "needle excluded nested",
	} {
		file := filepath.Join(s.rootAbs, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/exec", s.handleExec)
	mux.HandleFunc("/api/complete", s.handleComplete)
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.HandleFunc("/api/static/", s.handleStaticFile)
	request := func(t *testing.T, method, target string, input, output any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "http://example.test"+target, strings.NewReader(string(body)))
		mux.ServeHTTP(w, r)
		if output != nil {
			if w.Code != http.StatusOK {
				t.Fatalf("%s %s returned %d: %s", method, target, w.Code, w.Body)
			}
			if err := json.Unmarshal(w.Body.Bytes(), output); err != nil {
				t.Fatal(err)
			}
		}
		return w
	}
	exec := func(t *testing.T, command string) execResp {
		t.Helper()
		var output execResp
		request(t, http.MethodPost, "/api/exec", execReq{Input: command}, &output)
		return output
	}
	complete := func(t *testing.T, name string, dirsOnly bool) map[string]bool {
		t.Helper()
		var output completeResp
		request(t, http.MethodPost, "/api/complete", completeReq{Path: name, DirsOnly: dirsOnly}, &output)
		names := make(map[string]bool)
		for _, item := range output.Items {
			names[item.Name] = item.Dir
		}
		return names
	}

	t.Run("config and cd", func(t *testing.T) {
		var config configResp
		request(t, http.MethodGet, "/api/config?path=/docs", nil, &config)
		if config.CWD != "/docs" || config.Readme == nil || *config.Readme != "public README" || config.DocType != "text" {
			t.Fatalf("public config response: %s", mustJSON(t, config))
		}
		for _, name := range []string{"/docs/private", "/docs/parent-blocked"} {
			config = configResp{}
			request(t, http.MethodGet, "/api/config?path="+name, nil, &config)
			if config.CWD != "/" || config.Readme == nil || *config.Readme != "" {
				t.Errorf("excluded config response: %s", mustJSON(t, config))
			}
			if response := exec(t, "cd "+name); response.CWD != "" || response.Readme != nil || response.Redirect != "" {
				t.Errorf("excluded cd response: %s", mustJSON(t, response))
			}
		}
		cd := exec(t, "cd /docs")
		if cd.CWD != "/docs" || cd.Readme == nil || *cd.Readme != "public README" || cd.Redirect != "" {
			t.Fatalf("public cd response: %s", mustJSON(t, cd))
		}
	})

	t.Run("directory indexes", func(t *testing.T) {
		for _, target := range []string{"/landing", "/landing?nojs=1", "/landing?view=files"} {
			w := request(t, http.MethodGet, target, nil, nil)
			if w.Code != http.StatusOK || w.Body.String() != "<html>public index</html>" {
				t.Errorf("%s returned %d %q", target, w.Code, w.Body.String())
			}
		}
		if response := exec(t, "cd /landing"); response.Redirect != "/landing" {
			t.Errorf("cd did not redirect to public index: %s", mustJSON(t, response))
		}
		w := request(t, http.MethodGet, "/docs", nil, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `data-initial-path="/docs"`) {
			t.Errorf("directory with excluded index did not open in the terminal (HTTP %d)", w.Code)
		}
	})

	t.Run("directory listings and completion", func(t *testing.T) {
		noJS := request(t, http.MethodGet, "/docs?nojs=1", nil, nil)
		files := request(t, http.MethodGet, "/docs?view=files", nil, nil)
		if strings.ReplaceAll(noJS.Body.String(), "?nojs=1", "?view=files") != files.Body.String() {
			t.Error("Files and legacy no-JS listings differ")
		}
		listing := exec(t, "ls -la /docs").Output
		if noJS.Code != http.StatusOK || !strings.Contains(noJS.Body.String(), `href="/docs/r%C3%A9sum%C3%A9%20%231.txt"`) || !strings.Contains(listing, publicName) {
			t.Fatalf("public entry missing from browser or terminal listing")
		}
		if strings.Contains(noJS.Body.String(), ".public.txt") || !strings.Contains(listing, ".public.txt") {
			t.Error("listing dotfile behavior changed")
		}
		items := complete(t, "/docs/", false)
		if _, ok := items[publicName]; !ok {
			t.Error("public Unicode entry missing from completion")
		}
		if _, ok := complete(t, "/docs/.p", false)[".public.txt"]; !ok {
			t.Error("public dotfile missing from explicit completion")
		}
		for _, name := range []string{"README.md", "index.html", "hidden.txt", "hidden.png", "private", "parent-blocked"} {
			_, completed := items[name]
			if completed || strings.Contains(noJS.Body.String(), name) || strings.Contains(listing, name) {
				t.Errorf("excluded entry %q appears in a listing or completion", name)
			}
		}
		if got := complete(t, "/docs/private/", false); len(got) != 0 {
			t.Errorf("completion descended into excluded directory: %v", got)
		}
	})

	t.Run("direct reads and generated links", func(t *testing.T) {
		arg := `"` + publicPath + `"`
		if got := exec(t, "cat "+arg).Output; got != publicBody {
			t.Errorf("cat public file: %q", got)
		}
		if got := exec(t, "checksum "+arg).Output; !strings.Contains(got, fmt.Sprintf("%x", sha256.Sum256([]byte(publicBody)))) {
			t.Errorf("checksum public file: %q", got)
		}
		share := exec(t, "share "+arg).Clipboard
		const publicLink = "/docs/r%C3%A9sum%C3%A9%20%231.txt"
		if share != "http://example.test"+publicLink {
			t.Fatalf("shareable link: %q", share)
		}
		download := exec(t, "get "+arg).Download
		if download != "/api/download?path="+url.QueryEscape(publicPath) {
			t.Fatalf("download link: %q", download)
		}
		for _, link := range []string{publicLink, download, "/?file=" + url.QueryEscape(publicPath), "/api/static" + publicLink} {
			if w := request(t, http.MethodGet, link, nil, nil); w.Code != http.StatusOK || w.Body.String() != publicBody {
				t.Errorf("public link %s returned %d %q", link, w.Code, w.Body.String())
			}
		}
		for _, name := range []string{"/docs/hidden.txt", "/docs/hidden.png", "/docs/private/README.md", "/docs/parent-blocked/box.txt"} {
			for _, command := range []string{"cat", "ls", "checksum", "share", "get"} {
				response := exec(t, command+" "+name)
				if response.Clipboard != "" || response.Download != "" || response.HTML != "" || !strings.Contains(strings.ToLower(response.Output), "no such file") {
					t.Errorf("%s %s disclosed an excluded entry: %s", command, name, mustJSON(t, response))
				}
			}
			for _, link := range []string{name, "/?file=" + url.QueryEscape(name), "/api/static" + name, "/api/download?path=" + url.QueryEscape(name)} {
				if w := request(t, http.MethodGet, link, nil, nil); w.Code != http.StatusNotFound {
					t.Errorf("excluded link %s returned %d", link, w.Code)
				}
			}
		}
		for _, target := range []string{"/docs/private", "/docs/private?nojs=1", "/docs/parent-blocked"} {
			if w := request(t, http.MethodGet, target, nil, nil); w.Code != http.StatusNotFound {
				t.Errorf("excluded directory %s returned %d", target, w.Code)
			}
		}
	})

	t.Run("rooted links", func(t *testing.T) {
		for name, target := range map[string]string{"mirror": "../shared", "alias.txt": "../shared/item.txt", "quiet-alias.txt": "../shared/quiet.txt"} {
			if err := os.Symlink(filepath.FromSlash(target), filepath.Join(s.rootAbs, "docs", name)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		}
		if !complete(t, "/docs/", true)["mirror"] {
			t.Error("rooted directory link missing from directory completion")
		}
		if cd := exec(t, "cd /docs/mirror"); cd.CWD != "/docs/mirror" {
			t.Errorf("rooted link did not preserve virtual CWD: %s", mustJSON(t, cd))
		}
		for _, target := range []string{"/docs/mirror/item.txt", "/docs/alias.txt"} {
			if w := request(t, http.MethodGet, target, nil, nil); w.Code != http.StatusOK || w.Body.String() != "public linked document" {
				t.Errorf("public rooted link %s returned %d %q", target, w.Code, w.Body.String())
			}
		}
		for _, target := range []string{"/docs/mirror/quiet.txt", "/docs/quiet-alias.txt"} {
			if w := request(t, http.MethodGet, target, nil, nil); w.Code != http.StatusNotFound {
				t.Errorf("excluded rooted target %s returned %d", target, w.Code)
			}
			if got := exec(t, "cat "+target).Output; !strings.Contains(got, "no such file") {
				t.Errorf("cat resolved excluded rooted target: %q", got)
			}
		}
		if _, ok := complete(t, "/docs/mirror/", false)["quiet.txt"]; ok {
			t.Error("completion disclosed excluded rooted target")
		}
		if got := exec(t, "find /docs/mirror -name *.txt").Output; !strings.Contains(got, "/docs/mirror/item.txt") || strings.Contains(got, "quiet.txt") {
			t.Errorf("find from rooted link: %q", got)
		}
		if got := exec(t, "grep -r linked /docs/mirror").Output; !strings.Contains(got, "/docs/mirror/item.txt") || strings.Contains(got, "quiet.txt") {
			t.Errorf("grep from rooted link: %q", got)
		}
		if got := exec(t, "find /docs -name item.txt").Output; got != "find: no matches found" {
			t.Errorf("find recursed into a discovered directory symlink: %q", got)
		}
	})

	t.Run("non-regular entries", func(t *testing.T) {
		// AF_UNIX limits the bind name independently of the fixture directory.
		t.Chdir(s.rootAbs)
		listener, err := net.Listen("unix", "socket")
		if err != nil {
			t.Skipf("Unix socket fixture unavailable: %v", err)
		}
		defer func() { _ = listener.Close() }()
		for _, query := range []string{"?view=files", "?nojs=1"} {
			listing := request(t, http.MethodGet, "/"+query, nil, nil)
			if strings.Contains(listing.Body.String(), `href="/socket`) {
				t.Error("directory listing included a socket")
			}
			if got := request(t, http.MethodGet, "/socket"+query, nil, nil); got.Code != http.StatusNotFound {
				t.Errorf("socket served with %s: HTTP %d", query, got.Code)
			}
		}
		if _, ok := complete(t, "/sock", false)["socket"]; ok {
			t.Error("completion included a socket")
		}
		for _, command := range []string{"cat", "checksum", "share", "get"} {
			if got := exec(t, command+" /socket"); !strings.Contains(got.Output, "no such file") || got.Clipboard != "" || got.Download != "" {
				t.Errorf("%s linked to or attempted to read a socket: %s", command, mustJSON(t, got))
			}
		}
	})

	t.Run("recursive commands", func(t *testing.T) {
		for _, tc := range []struct {
			command string
			order   []string
		}{
			{"tree /walk", []string{"nested/", "b.txt", "a.txt"}},
			{"find /walk -name *.txt", []string{"/walk/a.txt", "/walk/nested/b.txt"}},
			{"grep -r needle /walk", []string{"/walk/a.txt", "public A", "/walk/nested/b.txt", "public B"}},
		} {
			output := exec(t, tc.command).Output
			at := 0
			for _, want := range tc.order {
				index := strings.Index(output[at:], want)
				if index < 0 {
					t.Fatalf("%s missing %q in order: %q", tc.command, want, output)
				}
				at += index + len(want)
			}
			for _, excluded := range []string{"blocked", "deep-stop", "excluded", "hidden.txt", ".dot"} {
				if strings.Contains(output, excluded) {
					t.Errorf("%s included %q: %q", tc.command, excluded, output)
				}
			}
		}
		if output := exec(t, "tree -a -L1 /walk").Output; !strings.Contains(output, ".dot.txt") || strings.Contains(output, "b.txt") || strings.Contains(output, "blocked") {
			t.Errorf("tree dotfile/depth options: %q", output)
		}
		if output := exec(t, "find /walk -name .dot*").Output; !strings.Contains(output, "/walk/.dot.txt") || !strings.Contains(output, "/walk/.dotdir") {
			t.Errorf("find dotfile option: %q", output)
		}
		if output := exec(t, "grep needle /walk/nested/hidden.txt").Output; !strings.Contains(output, "no such file") || strings.Contains(output, "excluded") {
			t.Errorf("grep explicit excluded file: %q", output)
		}
		for _, command := range []string{"tree", "find", "grep -r needle"} {
			if output := exec(t, command+" /walk/blocked").Output; !strings.Contains(output, "no such file or directory") {
				t.Errorf("%s on an excluded directory: %q", command, output)
			}
		}
	})

	t.Run("directory and pattern archives", func(t *testing.T) {
		for _, tc := range []struct {
			query string
			names []string
			data  []string
		}{
			{"dir=/walk", []string{"walk/.dotdir/item.txt", "walk/a.txt", "walk/nested/b.txt"}, []string{"needle dot directory", "needle public A", "needle public B"}},
			{"pattern=walk/*.txt", []string{".dot.txt", "a.txt"}, []string{"needle dot file", "needle public A"}},
			{"pattern=walk/nested/*.txt", []string{"b.txt"}, []string{"needle public B"}},
		} {
			w := request(t, http.MethodGet, "/api/download?"+tc.query, nil, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("archive %s returned %d: %s", tc.query, w.Code, w.Body)
			}
			archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
			if err != nil {
				t.Fatal(err)
			}
			var names, data []string
			for _, entry := range archive.File {
				names = append(names, entry.Name)
				r, err := entry.Open()
				if err != nil {
					t.Fatal(err)
				}
				b, err := io.ReadAll(r)
				_ = r.Close()
				if err != nil {
					t.Fatal(err)
				}
				data = append(data, string(b))
			}
			if !slices.Equal(names, tc.names) || !slices.Equal(data, tc.data) {
				t.Errorf("archive %s entries %q with contents %q", tc.query, names, data)
			}
		}
		for _, query := range []string{"dir=/walk/blocked", "pattern=walk/deep-stop/*.txt"} {
			if w := request(t, http.MethodGet, "/api/download?"+query, nil, nil); w.Code != http.StatusNotFound {
				t.Errorf("archive %s returned %d", query, w.Code)
			}
		}
	})

	t.Run("sitemap locations", func(t *testing.T) {
		const origin = "http://example.test"
		configured := newServer(s.rootAbs, 4096, "", origin)
		w := readSitemap(t, configured)
		var sitemap struct {
			Locations []string `xml:"url>loc"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &sitemap); err != nil {
			t.Fatal(err)
		}
		var walk []string
		for _, location := range sitemap.Locations {
			if strings.HasPrefix(location, origin+"/walk") {
				walk = append(walk, strings.TrimPrefix(location, origin))
			}
		}
		want := []string{"/walk", "/walk/.dot.txt", "/walk/.dotdir", "/walk/.dotdir/item.txt", "/walk/.lsgetignore", "/walk/a.txt", "/walk/nested", "/walk/nested/b.txt"}
		if !slices.Equal(walk, want) {
			t.Errorf("sitemap walk locations %v, want %v", walk, want)
		}
	})
}

func TestWalkVisibilityErrors(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a-child", "b-sibling", "z-empty"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "b-sibling", "item.txt"), []byte("public sibling"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := openTestDownloadRoot(t, dir)
	unexpected := func(entry visibleEntry) (bool, error) {
		t.Errorf("unexpected visit to %s", entry.name)
		return false, nil
	}
	if err := walkVisibleDir(t.Context(), newWorkBudget(), root, "does-not-exist", unexpected); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing starting directory error = %v", err)
	}
	if err := walkVisibleDir(t.Context(), newWorkBudget(), root, "b-sibling/item.txt", unexpected); !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("non-directory starting path error = %v", err)
	}
	if err := walkVisibleDir(t.Context(), newWorkBudget(), root, "z-empty", unexpected); err != nil {
		t.Errorf("empty starting directory error = %v", err)
	}
	var visited []string
	err := walkVisibleDir(t.Context(), newWorkBudget(), root, ".", func(entry visibleEntry) (bool, error) {
		visited = append(visited, entry.name)
		if entry.name == "a-child" {
			if err := os.Remove(filepath.Join(dir, "a-child")); err != nil {
				t.Fatal(err)
			}
		}
		return true, nil
	})
	if err != nil || !slices.Contains(visited, "b-sibling/item.txt") {
		t.Errorf("child disappearance interrupted the walk: visited %v, error %v", visited, err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
