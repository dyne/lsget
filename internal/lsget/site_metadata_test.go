package lsget

import (
	"bytes"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSiteMetadata(t *testing.T) {
	const folder = "café #50%2F&"
	const directory = "/caf%C3%A9%20%2350%252F%26/guide"
	const published = "<!doctype html><title>Operator page</title><p>{{CSP_NONCE}}</p>\n"
	const download = "ordinary download bytes\n"
	const dev = "<!doctype html><title>Developer title</title><p>{{INITIAL_PATH}}</p>\n"
	s := newTestServer(t)
	for _, dir := range []string{filepath.Join(folder, "guide"), "published"} {
		if err := os.MkdirAll(filepath.Join(s.rootAbs, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"published/index.html": published,
		"download.txt":         download,
		"favicon.ico":          "operator icon",
	} {
		if err := os.WriteFile(filepath.Join(s.rootAbs, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	devPath := filepath.Join(t.TempDir(), "dev.html")
	if err := os.WriteFile(devPath, []byte(dev), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := s.httpServer("").Handler
	request := func(method, target, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://untrusted.example"+target, nil)
		r.Header.Set("User-Agent", "lsget-metadata-fixture-crawler")
		r.Header.Set("Forwarded", `proto=http;host=forwarded.example`)
		r.Header.Set("X-Forwarded-Host", "forwarded.example")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Prefix", "/forged")
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	contains := func(t *testing.T, w *httptest.ResponseRecorder, tags ...string) {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		for _, tag := range tags {
			if !strings.Contains(w.Body.String(), tag) {
				t.Errorf("response missing %s", tag)
			}
		}
	}
	omits := func(t *testing.T, w *httptest.ResponseRecorder, forbidden ...string) {
		t.Helper()
		for _, value := range forbidden {
			if strings.Contains(w.Body.String(), value) {
				t.Errorf("unexpected %s in response", value)
			}
		}
	}

	iconPath := shellAssetPath + shellFaviconName
	t.Run("default initial HTML", func(t *testing.T) {
		w := request(http.MethodGet, "/", "")
		contains(t, w, "<title>📂 lsget</title>",
			`<meta name="description" content="Browse and download files with lsget." />`,
			`<meta property="og:description" content="Browse and download files with lsget." />`,
			`<meta property="og:title" content="📂 lsget" />`,
			`<meta property="og:type" content="website" />`,
			`<link rel="icon" type="image/png" sizes="32x32" href="`+iconPath+`" />`)
		omits(t, w, "og:url", "og:image", "canonical", "untrusted.example", "forwarded.example")
	})
	t.Run("local favicon", func(t *testing.T) {
		w := request(http.MethodGet, iconPath, "")
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" ||
			w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
			w.Header().Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(w.Body.Bytes(), embeddedFavicon) {
			t.Fatalf("icon: status %d, headers %v, %d bytes", w.Code, w.Header(), w.Body.Len())
		}
		icon, err := png.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
		if err != nil || icon.Width != 32 || icon.Height != 32 {
			t.Fatalf("icon dimensions: %+v, error: %v", icon, err)
		}
		etag := w.Header().Get("ETag")
		head := request(http.MethodHead, iconPath, "")
		if etag == "" || head.Code != http.StatusOK || head.Body.Len() != 0 ||
			head.Header().Get("ETag") != etag || head.Header().Get("Content-Length") != fmt.Sprint(w.Body.Len()) {
			t.Fatalf("icon HEAD: status %d, headers %v, %d bytes", head.Code, head.Header(), head.Body.Len())
		}
		if cached := request(http.MethodGet, iconPath, etag); cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
			t.Fatalf("conditional icon GET: status %d, %d bytes", cached.Code, cached.Body.Len())
		}
		stalePath := shellAssetPrefix + strings.Repeat("0", 64) + "/" + shellFaviconName
		if stale := request(http.MethodGet, stalePath, ""); stale.Code != http.StatusNotFound || stale.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("stale icon: status %d, headers %v", stale.Code, stale.Header())
		}
	})

	routes := map[string]string{
		"/published/":         published,
		"/published/?nojs=1":  published,
		"/download.txt":       download,
		"/?file=download.txt": download,
		"/favicon.ico":        "operator icon",
	}
	before := make(map[string]*httptest.ResponseRecorder, len(routes))
	for route := range routes {
		before[route] = request(http.MethodGet, route, "")
	}
	plainBefore := request(http.MethodGet, directory+"?nojs=1", "")
	s.devTemplate = devPath
	devBefore := request(http.MethodGet, directory, "")
	s.devTemplate = ""

	proxy, err := parseTrustedProxies("192.0.2.0/24") // httptest's default peer is 192.0.2.1.
	if err != nil {
		t.Fatal(err)
	}
	s.trustedProxies = proxy
	s.baseURL = "https://public.example:8443/releases%20here/"
	s.siteMetadata = siteMetadata{
		Title:        `Café 名 <b> & "quotes" 'single' {{CSP_NONCE}}`,
		Description:  `Files "on" & <em> résumé >`,
		PreviewImage: "https://img.example/caf%C3%A9.png?v=2&width=640",
	}
	const canonical = "https://public.example:8443/releases%20here/caf%C3%A9%20%2350%252F&amp;/guide"
	t.Run("escaped preview and canonical directory URL", func(t *testing.T) {
		w := request(http.MethodGet, directory+"/?from=preview", "")
		contains(t, w,
			`<title>Café 名 &lt;b&gt; &amp; &#34;quotes&#34; &#39;single&#39; {{CSP_NONCE}}</title>`,
			`<meta property="og:title" content="Café 名 &lt;b&gt; &amp; &#34;quotes&#34; &#39;single&#39; {{CSP_NONCE}}" />`,
			`<meta name="description" content="Files &#34;on&#34; &amp; &lt;em&gt; résumé &gt;" />`,
			`<meta property="og:description" content="Files &#34;on&#34; &amp; &lt;em&gt; résumé &gt;" />`,
			`<link rel="canonical" href="`+canonical+`" />`,
			`<meta property="og:url" content="`+canonical+`" />`,
			`<meta property="og:image" content="https://img.example/caf%C3%A9.png?v=2&amp;width=640" />`,
			`<noscript>`)
		omits(t, w, "untrusted.example", "forwarded.example", "/forged", "ZgotmplZ")
		for _, route := range []string{"/", "/missing"} {
			contains(t, request(http.MethodGet, route, ""), `<link rel="canonical" href="https://public.example:8443/releases%20here/" />`)
		}
	})
	t.Run("published files and fallback views", func(t *testing.T) {
		sameResponse := func(route, wantBody string, old, current *httptest.ResponseRecorder) {
			t.Helper()
			if old.Code != http.StatusOK || current.Code != old.Code || current.Body.String() != wantBody || old.Body.String() != wantBody {
				t.Fatalf("%s: status %d → %d, unexpected response bytes", route, old.Code, current.Code)
			}
			for _, key := range []string{"Content-Type", "Content-Disposition", "Content-Length", "Cache-Control", "Content-Security-Policy"} {
				if current.Header().Get(key) != old.Header().Get(key) {
					t.Errorf("%s: %s = %q, was %q", route, key, current.Header().Get(key), old.Header().Get(key))
				}
			}
		}
		for route, body := range routes {
			sameResponse(route, body, before[route], request(http.MethodGet, route, ""))
		}
		plain := request(http.MethodGet, directory+"?nojs=1", "")
		contains(t, plain, "<h1>Index of ")
		omits(t, plain, "og:title", "og:image")
		contains(t, plain, `<link rel="canonical" href="`+canonical+`">`)
		// Files now carries directory canonical metadata; other fallback bytes stay independent of site metadata.
		plain.Body = bytes.NewBufferString(strings.Replace(plain.Body.String(), `<link rel="canonical" href="`+canonical+`">`, "", 1))
		sameResponse("?nojs=1", plainBefore.Body.String(), plainBefore, plain)
		s.devTemplate = devPath
		currentDev := request(http.MethodGet, directory, "")
		contains(t, currentDev, "<title>Developer title</title>")
		omits(t, currentDev, "og:title", "canonical", "og:image")
		sameResponse("dev template", devBefore.Body.String(), devBefore, currentDev)
		sameResponse("published over dev template", published, before["/published/"], request(http.MethodGet, "/published/", ""))
	})
}
