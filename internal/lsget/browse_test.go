package lsget

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestFilesDirectory(t *testing.T) {
	s := newTestServer(t)
	for name, content := range map[string]string{
		"shared/.lsgetignore":       "ignored.txt\n",
		"shared/ignored.txt":        "private",
		"shared/.dot.txt":           "dot",
		"shared/a < & #.txt":        "public",
		"shared/z-folder/child.txt": "child",
		"published/index.html":      "<h1>Published</h1>",
	} {
		writeWorkFixture(t, s.rootAbs, name, []byte(content))
	}
	if err := os.Mkdir(filepath.Join(s.rootAbs, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	serve := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleIndex(w, httptest.NewRequest(http.MethodGet, target, nil))
		return w
	}
	for _, query := range []string{"?view=files", "?nojs=1", "?nojs=1&view=terminal"} {
		t.Run(query, func(t *testing.T) {
			w := serve("/shared" + query)
			body := w.Body.String()
			if w.Code != http.StatusOK {
				t.Fatalf("status %d", w.Code)
			}
			for _, want := range []string{`name="viewport" content="width=device-width, initial-scale=1"`, `<ul class="files"`, `a &lt; &amp; #.txt`, `href="/shared/a%20%3C%20&amp;%20%23.txt"`, `class="up"`, `aria-current="page">shared`, `</main></body>`} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			if strings.Contains(body, "ignored.txt") || strings.Contains(body, ".dot.txt") || strings.Contains(body, "<script") {
				t.Error("listing exposed hidden data or required scripts")
			}
			if strings.Index(body, ">z-folder/</a>") > strings.Index(body, ">a &lt; &amp; #.txt</a>") {
				t.Error("directory must precede file")
			}
			mode := "?view=files"
			if strings.Contains(query, "nojs=1") {
				mode = "?nojs=1"
			}
			if !strings.Contains(body, `href="/shared/z-folder`+mode+`"`) || !strings.Contains(body, `href="/`+mode+`"`) {
				t.Error("folder navigation lost explicit mode")
			}
		})
	}
	for _, query := range []string{"?view=files", "?nojs=1"} {
		if got := serve("/published/" + query); got.Body.String() != "<h1>Published</h1>" {
			t.Errorf("published index lost precedence: %d", got.Code)
		}
		if got := serve("/empty" + query); !strings.Contains(got.Body.String(), "This folder is empty.") {
			t.Error("empty folder has no explanation")
		}
		for _, missing := range []string{"/missing", "/shared/ignored.txt"} {
			if got := serve(missing + query); got.Code != http.StatusNotFound {
				t.Errorf("%s: status %d", missing, got.Code)
			}
		}
		if got := serve("/shared/a%20%3C%20%26%20%23.txt" + query); got.Body.String() != "public" {
			t.Error("view parameters changed direct file serving")
		}
	}
}

func TestFilesActions(t *testing.T) {
	s := newTestServer(t)
	fixtures := []struct {
		name, data string
		attachment bool
	}{
		{`résumé # <&".txt`, "Text fixture\n", false},
		{"image & #.png", "\x89PNG\r\n\x1a\nimage", false},
		{"document #.PDF", "%PDF-1.4\nfixture", false},
		{"archive &.zip", "PK\x03\x04archive", true},
		{"unknown #.bin", "\x00\x01\x02\x03\xffunknown", false},
	}
	for _, fixture := range fixtures {
		writeWorkFixture(t, s.rootAbs, "shared/"+fixture.name, []byte(fixture.data))
	}
	writeWorkFixture(t, s.rootAbs, "shared/folder/child.txt", nil)
	request := func(method, target, byteRange string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://files.example"+target, nil)
		r.Header.Set("Range", byteRange)
		w := httptest.NewRecorder()
		if strings.HasPrefix(target, "/api/download?") {
			s.handleDownload(w, r)
		} else {
			s.handleIndex(w, r)
		}
		return w
	}
	page := request(http.MethodGet, "/shared?view=files", "").Body.String()
	if strings.Count(page, `<details class="file-actions">`) != len(fixtures) || strings.Count(page, `<input class="share-url"`) != len(fixtures) {
		t.Fatal("each regular file needs actions and a script-independent share URL")
	}
	links := regexp.MustCompile(`class="download"[^>]*href="([^"]+)"`).FindAllStringSubmatch(page, -1)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			virtual := "/shared/" + fixture.name
			openURL := urlEscapeVirtual(virtual)
			downloadURL := "/api/download?path=" + url.QueryEscape(virtual)
			found := false
			for _, link := range links {
				found = found || html.UnescapeString(link[1]) == downloadURL
			}
			if !found || !strings.Contains(page, `value="`+html.EscapeString("http://files.example"+openURL)+`"`) || !strings.Contains(page, `aria-label="Actions for `+html.EscapeString(fixture.name)+`"`) {
				t.Fatal("missing correctly encoded Download/Share actions")
			}
			for _, target := range []string{openURL, downloadURL} {
				got := request(http.MethodGet, target, "")
				wantAttachment := target == downloadURL || fixture.attachment
				if got.Code != http.StatusOK || got.Body.String() != fixture.data || strings.HasPrefix(got.Header().Get("Content-Disposition"), "attachment;") != wantAttachment {
					t.Errorf("GET %s: status=%d disposition=%q bytes=%q", target, got.Code, got.Header().Get("Content-Disposition"), got.Body.String())
				}
				head := request(http.MethodHead, target, "")
				if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(len(fixture.data)) {
					t.Errorf("HEAD %s: status=%d bytes=%d", target, head.Code, head.Body.Len())
				}
				rangeResult := request(http.MethodGet, target, "bytes=1-3")
				if rangeResult.Code != http.StatusPartialContent || rangeResult.Body.String() != fixture.data[1:4] {
					t.Errorf("range %s: status=%d bytes=%q", target, rangeResult.Code, rangeResult.Body.String())
				}
			}
		})
	}
}

func TestFilesSharePublicAddress(t *testing.T) {
	s := newTestServer(t)
	writeWorkFixture(t, s.rootAbs, "shared/name # &.txt", nil)
	for _, tc := range []struct{ name, configured, proxy, host, want string }{
		{"request", "", "", "files.example:8080", "http://files.example:8080"},
		{"untrusted-forwarded", "", "", "files.example", "http://files.example"},
		{"trusted-forwarded", "", "127.0.0.0/8", "files.example", "https://files.example"},
		{"configured", "https://public.example/prefix/", "", "internal.example", "https://public.example/prefix"},
		{"invalid-host", "", "", "invalid@host", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.baseURL = tc.configured
			var err error
			s.trustedProxies, err = parseTrustedProxies(tc.proxy)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, "/shared?view=files", nil)
			r.Host, r.RemoteAddr = tc.host, "127.0.0.1:1234"
			r.Header.Set("X-Forwarded-Proto", "https")
			w := httptest.NewRecorder()
			s.handleIndex(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("listing unavailable: HTTP %d", w.Code)
			}
			if tc.want == "" {
				if strings.Contains(w.Body.String(), `class="share-url"`) || !strings.Contains(w.Body.String(), "Share URL unavailable") {
					t.Error("invalid host emitted a share URL")
				}
			} else if !strings.Contains(w.Body.String(), `value="`+html.EscapeString(tc.want+"/shared/name%20%23%20&.txt")+`"`) {
				t.Errorf("share did not use trusted public base %q", tc.want)
			}
		})
	}
}

func TestDirectoryViewLinks(t *testing.T) {
	s := newTestServer(t)
	s.baseURL = "https://public.example/base"
	const directory = "/nested # <&"
	writeWorkFixture(t, s.rootAbs, strings.TrimPrefix(directory, "/")+"/child/note.txt", []byte("nested bytes"))
	writeWorkFixture(t, s.rootAbs, "selected.txt", []byte("selected bytes"))
	writeWorkFixture(t, s.rootAbs, "published/index.html", []byte("published bytes"))
	serve := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleIndex(w, httptest.NewRequest(http.MethodGet, target, nil))
		return w
	}
	for _, mode := range []string{"files", "terminal", "nojs"} {
		t.Run(mode, func(t *testing.T) {
			query := "view=" + mode
			if mode == "nojs" {
				query = "nojs=1&view=terminal"
			}
			w := serve(urlEscapeVirtual(directory) + "?" + query + "&tag=one&tag=two&file=selected.txt")
			if w.Code != http.StatusOK {
				t.Fatalf("status %d", w.Code)
			}
			body := w.Body.String()
			for _, want := range []string{">Files</a>", ">Terminal</a>", `rel="canonical" href="https://public.example/base` + html.EscapeString(urlEscapeVirtual(directory)) + `"`} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, match := range regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(body, -1) {
				u, err := url.Parse(html.UnescapeString(match[1]))
				if err != nil {
					t.Fatal(err)
				}
				q := u.Query()
				if q.Has("file") {
					t.Errorf("directory link propagated file selector: %s", u)
				}
				if q.Has("view") || q.Has("nojs") {
					if strings.Join(q["tag"], ",") != "one,two" {
						t.Errorf("link lost unrelated query values: %s", u)
					}
					if u.Path == "/" && serve(u.RequestURI()).Body.String() == "selected bytes" {
						t.Error("root navigation selected file content")
					}
				}
			}
		})
	}
	for _, query := range []string{"view=files", "view=terminal", "nojs=1&view=terminal"} {
		for target, body := range map[string]string{
			"/?file=selected.txt&" + query:                           "selected bytes",
			"/published/?" + query:                                   "published bytes",
			urlEscapeVirtual(directory) + "/child/note.txt?" + query: "nested bytes",
		} {
			if w := serve(target); w.Code != http.StatusOK || w.Body.String() != body {
				t.Errorf("file/published precedence changed for %s", target)
			}
		}
	}
}

func TestFilesNavigationOutputBudget(t *testing.T) {
	s := newTestServer(t)
	directory := "/" + strings.Repeat("parent/", 20) + "current"
	for i := range 60 {
		writeWorkFixture(t, s.rootAbs, strings.TrimPrefix(directory, "/")+fmt.Sprintf("/child-%02d/note.txt", i), nil)
	}
	state := strings.Repeat("q", 7*1024)
	w := httptest.NewRecorder()
	s.handleIndex(w, httptest.NewRequest(http.MethodGet, directory+"?view=files&retained="+state, nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || len(body) > maxCommandText {
		t.Fatalf("navigation exceeded page budget: status=%d bytes=%d", w.Code, len(body))
	}
	for _, want := range []string{`aria-current="page">current`, "More parent folders", `class="up"`, "retained=" + state, truncationNote, "</html>\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("bounded page lost %q", want[:min(len(want), 80)])
		}
	}
	items := strings.Count(body, "<li>") + strings.Count(body, "<li ")
	if items != strings.Count(body, "</li>") || strings.Count(body, "<details") != strings.Count(body, "</details>") {
		t.Error("bounded navigation left partial rows or breadcrumbs")
	}
}

func TestFilesNavigationPrefixBudget(t *testing.T) {
	s := newTestServer(t)
	writeWorkFixture(t, s.rootAbs, "shared/child/note.txt", nil)
	for _, tc := range []struct{ name, query, base string }{
		{"expanding-query", "keep=ordinary&expanding=" + strings.Repeat("!", 12000), ""},
		{"oversized-canonical", "keep=ordinary", "https://public.example/" + strings.Repeat("q", maxCommandText)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.baseURL = tc.base
			w := httptest.NewRecorder()
			s.handleIndex(w, httptest.NewRequest(http.MethodGet, "/shared?view=files&"+tc.query, nil))
			body := w.Body.String()
			if w.Code != http.StatusOK || len(body) > maxCommandText || !strings.HasSuffix(body, "</html>\n") {
				t.Fatalf("encoded prefix exceeded budget: status=%d bytes=%d", w.Code, len(body))
			}
			for _, want := range []string{`class="up"`, ">Files</a>", ">Terminal</a>", "keep=ordinary", `aria-current="page">shared`, "Some navigation details were omitted"} {
				if !strings.Contains(body, want) {
					t.Errorf("missing essential navigation %q", want)
				}
			}
		})
	}
}

func TestFilesDirectoryBoundedRows(t *testing.T) {
	s := newTestServer(t)
	// Escaped names exhaust the output budget before the entry budget.
	for i := range 800 {
		name := fmt.Sprintf("%04d-%s.txt", i, strings.Repeat("&", 150))
		writeWorkFixture(t, s.rootAbs, name, nil)
	}
	w := httptest.NewRecorder()
	s.handleIndex(w, httptest.NewRequest(http.MethodGet, "/?view=files", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, truncationNote) {
		t.Fatal("large directory must report truncation")
	}
	items := strings.Count(body, "<li>") + strings.Count(body, "<li ")
	if items != strings.Count(body, "</li>") || !strings.HasSuffix(body, "</html>\n") {
		t.Error("truncated listing has incomplete markup")
	}
	start := strings.Index(body, `<ul class="files"`)
	end := strings.Index(body[start:], "</ul>") + start
	if end-start > maxCommandText || strings.Count(body[start:end], `<a class="file-row"`) == 0 {
		t.Error("listing must retain complete rows within output budget")
	}
	// A directly shared nested path must also escape breadcrumb data.
	name := `folder < & "quoted"`
	writeWorkFixture(t, s.rootAbs, name+"/nested/note.txt", nil)
	w = httptest.NewRecorder()
	s.handleIndex(w, httptest.NewRequest(http.MethodGet, "/"+url.PathEscape(name)+"/nested?view=files", nil))
	if !strings.Contains(w.Body.String(), `folder &lt; &amp; &#34;quoted&#34;`) || strings.Contains(w.Body.String(), `folder <`) {
		t.Error("shared folder breadcrumb was not escaped")
	}
}
