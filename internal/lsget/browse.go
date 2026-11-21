package lsget

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
)

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	root, err := os.OpenRoot(s.rootAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = root.Close() }()

	query := r.URL.Query()
	if r.URL.Path == "/" && query.Has("file") {
		serveDownloadFile(w, r, root, strings.TrimPrefix(query.Get("file"), "/"), false)
		return
	}

	requestPath := cleanVirtual(r.URL.Path)
	filePath := rootedName(requestPath)
	info, err := visibleInfo(root, filePath)
	noJS := query.Get("nojs") == "1"
	if err != nil {
		if noJS || errors.Is(err, errHiddenPath) || !errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
		} else {
			s.serveMainIndex(w, "/")
		}
		return
	}
	if !info.IsDir() {
		serveDownloadFile(w, r, root, filePath, false)
		return
	}

	indexPath := path.Join(filePath, "index.html")
	if info, err := visibleInfo(root, indexPath); err == nil && info.Mode().IsRegular() {
		serveDownloadFile(w, r, root, indexPath, false)
		return
	}
	if noJS {
		release := s.reserveWork(w, r)
		if release == nil {
			return
		}
		defer release()
		serveNoJSDirectory(r.Context(), w, root, requestPath)
	} else {
		s.serveMainIndex(w, requestPath)
	}
}

func (s *server) serveMainIndex(w http.ResponseWriter, initialPath string) {
	htmlContent := embeddedIndex
	var nonce string
	var metadata []byte
	if s.devTemplate != "" {
		var err error
		htmlContent, err = os.ReadFile(s.devTemplate)
		if err != nil {
			http.Error(w, "Could not read the development template", http.StatusInternalServerError)
			return
		}
	} else {
		var err error
		metadata, err = s.renderSiteMetadata(initialPath)
		if err != nil {
			http.Error(w, "Could not render site metadata", http.StatusInternalServerError)
			return
		}
		nonce = rand.Text()
		w.Header().Set("Cache-Control", "no-store")
		s.setShellCSP(w, nonce)
	}

	processedHTML := s.processHTMLTemplate(htmlContent, initialPath, nonce)
	if metadata != nil {
		processedHTML = bytes.Replace(processedHTML, []byte("<title>"+defaultSiteTitle+"</title>"), metadata, 1)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(processedHTML)
}

func (s *server) setShellCSP(w http.ResponseWriter, nonce string) {
	source := "'nonce-" + nonce + "'"
	script := "script-src " + source
	header := "Content-Security-Policy-Report-Only"
	if !s.shellCSPReportOnly {
		header = "Content-Security-Policy"
		// Vendored Datastar compiles application-owned data-* expressions with Function.
		script += " 'unsafe-eval'"
	}
	w.Header().Set(header, "default-src 'none'; "+script+
		"; script-src-attr 'none'; style-src "+source+
		"; style-src-attr 'none'; connect-src 'self'; img-src http: https:"+
		"; base-uri 'none'; object-src 'none'; form-action 'none'; frame-ancestors 'self'")
}

// serveNoJSDirectory serves a plain HTML directory listing for no-JS fallback
func serveNoJSDirectory(ctx context.Context, w http.ResponseWriter, root *os.Root, virtualPath string) {
	dir := rootedName(virtualPath)
	entries, err := visibleChildren(ctx, newWorkBudget(), root, dir)
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	truncated := errors.Is(err, errWorkLimit)
	if err != nil && !truncated {
		http.Error(w, "Error reading directory", http.StatusInternalServerError)
		return
	}
	// Show directories before files, keeping each group alphabetized.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].info.IsDir() && !entries[j].info.IsDir()
	})
	var listing boundedText
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		name := path.Base(entry.name)
		if strings.HasPrefix(name, ".") {
			continue
		}
		href := html.EscapeString(urlEscapeVirtual(path.Join(virtualPath, name)))
		label := template.HTMLEscapeString(name)
		var line string
		if entry.info.IsDir() {
			line = fmt.Sprintf("<a href=\"%s?nojs=1\">%s/</a><br>", href, label)
		} else {
			line = fmt.Sprintf("<a href=\"%s\">%s</a> (%d bytes)<br>", href, label, entry.info.Size())
		}
		if listing.line(line) != nil {
			truncated = true
			break
		}
	}

	escapedVirtualPath := template.HTMLEscapeString(virtualPath)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
<title>Index of %s</title>
<style>
body { font-family: monospace; margin: 20px; }
a { color: blue; text-decoration: underline; }
a:visited { color: blue; }
</style>
</head>
<body>
`, escapedVirtualPath)

	_, _ = fmt.Fprintf(w, "<h1>Index of %s</h1>\n", escapedVirtualPath)
	_, _ = fmt.Fprintf(w, "<hr>\n")

	if virtualPath != "/" {
		parentPath := path.Dir(virtualPath)
		_, _ = fmt.Fprintf(w, "<a href=\"%s?nojs=1\">[Parent Directory]</a><br>\n", urlEscapeVirtual(parentPath))
	}

	_, _ = io.WriteString(w, listing.text.String())
	if truncated {
		_, _ = fmt.Fprintf(w, "\n<p>%s</p>", truncationNote)
	}
	_, _ = io.WriteString(w, "\n</body>\n</html>\n")
}

func (s *server) handleStaticFile(w http.ResponseWriter, r *http.Request) {
	root, err := os.OpenRoot(s.rootAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = root.Close() }()

	filePath := rootedName(strings.TrimPrefix(r.URL.Path, "/api/static"))
	serveDownloadFile(w, r, root, filePath, false)
}

// processHTMLTemplate replaces placeholders in HTML with dynamic content
func (s *server) processHTMLTemplate(htmlContent []byte, requestPath, nonce string) []byte {
	help := strings.TrimSpace(renderHelp())
	// Split into lines and wrap each in HTML div tags
	lines := strings.Split(help, "\n")
	var htmlLines []string
	for _, line := range lines {
		if line == "" {
			htmlLines = append(htmlLines, "<div class=\\\"line out\\\"></div>")
		} else {
			// Escape double quotes for JavaScript double-quoted string
			escapedLine := strings.ReplaceAll(line, "\\", "\\\\")       // Escape backslashes first
			escapedLine = strings.ReplaceAll(escapedLine, "\"", "\\\"") // Escape double quotes
			htmlLines = append(htmlLines, fmt.Sprintf("<div class=\\\"line out\\\">%s</div>", escapedLine))
		}
	}
	htmlLines = append(htmlLines, "<br/>")

	// Join all HTML lines into a single string (no newlines between them)
	formattedHelpMessage := strings.Join(htmlLines, "")

	// Clean the request path for initial CWD
	initialPath := cleanVirtual(requestPath)
	if initialPath == "" {
		initialPath = "/"
	}

	escapedInitialPath := template.HTMLEscapeString(initialPath)

	result := strings.NewReplacer(
		"{{HELP_MESSAGE}}", formattedHelpMessage,
		"{{HELP_MARKUP}}", help,
		"{{INITIAL_PATH}}", escapedInitialPath,
		"{{CSP_NONCE}}", nonce,
		"{{SHELL_ASSET_PATH}}", shellAssetPath,
	).Replace(string(htmlContent))
	return []byte(result)
}
