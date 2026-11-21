package lsget

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"time"
)

//go:embed index.html
var embeddedIndex []byte

//go:embed assets/js/marked.min.js
var embeddedMarkedJS []byte

//go:embed assets/js/datastar.js
var embeddedDatastarJS []byte

//go:embed assets/js/terminal.js
var embeddedTerminalJS []byte

//go:embed assets/js/terminal-view.js
var embeddedTerminalViewJS []byte

//go:embed assets/favicon.png
var embeddedFavicon []byte

const shellAssetPrefix = "/assets/lsget-shell/"
const shellFaviconName = "favicon.png"

type shellAsset struct {
	body        []byte
	etag        string
	contentType string
}

var shellAssets, shellAssetPath = indexShellAssets()
var shellFaviconDataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(embeddedFavicon)

func indexShellAssets() (map[string]shellAsset, string) {
	files := []struct {
		name        string
		body        []byte
		contentType string
	}{
		{"marked.min.js", embeddedMarkedJS, "application/javascript; charset=utf-8"},
		{"terminal.js", embeddedTerminalJS, "application/javascript; charset=utf-8"},
		{"terminal-view.js", embeddedTerminalViewJS, "application/javascript; charset=utf-8"},
		{shellFaviconName, embeddedFavicon, "image/png"},
	}
	// A shell and all its dependencies share one URL version. An older shell
	// can use its cached files, but cannot request a missing file from this build.
	bundle := sha256.New()
	shellDigest := sha256.Sum256(embeddedIndex)
	_, _ = bundle.Write(shellDigest[:])
	assets := make(map[string]shellAsset, len(files))
	for _, file := range files {
		digest := sha256.Sum256(file.body)
		_, _ = io.WriteString(bundle, file.name+"\x00")
		_, _ = bundle.Write(digest[:])
		assets[file.name] = shellAsset{file.body, `"` + hex.EncodeToString(digest[:]) + `"`, file.contentType}
	}
	return assets, shellAssetPrefix + hex.EncodeToString(bundle.Sum(nil)) + "/"
}

func (s *server) handleShellAsset(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if len(version) != sha256.Size*2 {
		s.handleIndex(w, r)
		return
	}
	if _, err := hex.DecodeString(version); err != nil {
		s.handleIndex(w, r)
		return
	}
	name := r.PathValue("name")
	asset, found := shellAssets[name]
	if shellAssetPrefix+version+"/" != shellAssetPath || !found {
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", asset.contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", asset.etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(asset.body))
}

// The stable vendor paths remain available to published sites and dev templates.
func (s *server) handleVendoredMarked(w http.ResponseWriter, r *http.Request) {
	serveVendoredScript(w, embeddedMarkedJS)
}

func (s *server) handleVendoredDatastar(w http.ResponseWriter, r *http.Request) {
	serveVendoredScript(w, embeddedDatastarJS)
}

func serveVendoredScript(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	_, _ = w.Write(body)
}
