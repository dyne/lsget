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
	"net/url"
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
	filesView := noJS || query.Get("view") == "files"
	if err != nil {
		if filesView || errors.Is(err, errHiddenPath) || !errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
		} else {
			s.serveMainIndex(w, "/", query)
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
	if filesView {
		release := s.reserveWork(w, r)
		if release == nil {
			return
		}
		defer release()
		view := "files"
		if noJS {
			view = "nojs"
		}
		viewQuery := directoryViewQuery(query, view)
		// An invalid public address must not prevent local browsing/downloads.
		publicBase, _ := s.publicBaseURL(r)
		serveNoJSDirectory(r.Context(), w, root, requestPath, viewQuery, publicBase,
			directoryViewURL(requestPath, query, "terminal"), s.directoryCanonicalURL(requestPath))
	} else {
		s.serveMainIndex(w, requestPath, query)
	}
}

// Directory links retain unrelated state while dropping the root file selector.
func directoryViewQuery(query url.Values, view string) string {
	values := make(url.Values, len(query)+1)
	for key, items := range query {
		if key != "view" && key != "nojs" && key != "file" {
			values[key] = items
		}
	}
	if view == "nojs" {
		values.Set("nojs", "1")
	} else {
		values.Set("view", view)
	}
	return "?" + values.Encode()
}

func directoryViewURL(virtualPath string, query url.Values, view string) string {
	return urlEscapeVirtual(virtualPath) + directoryViewQuery(query, view)
}

func (s *server) serveMainIndex(w http.ResponseWriter, initialPath string, query url.Values) {
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
	processedHTML = []byte(strings.NewReplacer(
		"{{FILES_VIEW_URL}}", html.EscapeString(directoryViewURL(initialPath, query, "files")),
		"{{TERMINAL_VIEW_URL}}", html.EscapeString(directoryViewURL(initialPath, query, "terminal")),
		"{{NOJS_URL}}", html.EscapeString(directoryViewURL(initialPath, query, "nojs")),
	).Replace(string(processedHTML)))
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

// serveNoJSDirectory is the shared, script-independent Files renderer.
func serveNoJSDirectory(ctx context.Context, w http.ResponseWriter, root *os.Root, virtualPath, viewQuery, publicBase, terminalURL, canonicalURL string) {
	navigationLimited := false
	// Bound encoded attributes, including expansion of repeated query separators.
	// Normal query state and configured canonical addresses pass through unchanged.
	if len(html.EscapeString(viewQuery)) > maxCommandText/32 {
		values, _ := url.ParseQuery(strings.TrimPrefix(viewQuery, "?"))
		view := "files"
		if values.Get("nojs") == "1" {
			view = "nojs"
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			if key != "view" && key != "nojs" && key != "file" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		limited := make(url.Values)
		used := 32 // Reserve mode selection and separators in escaped form.
		for _, key := range keys {
			fieldBytes := len(html.EscapeString(url.Values{key: values[key]}.Encode())) + 5
			if used+fieldBytes <= maxCommandText/32 {
				limited[key] = values[key]
				used += fieldBytes
			}
		}
		viewQuery = directoryViewQuery(limited, view)
		terminalURL = directoryViewURL(virtualPath, limited, "terminal")
		navigationLimited = true
	}
	if len(html.EscapeString(canonicalURL)) > maxCommandText/32 {
		canonicalURL = ""
		navigationLimited = true
	}
	viewQuery = html.EscapeString(viewQuery)
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
	escapedVirtualPath := template.HTMLEscapeString(virtualPath)
	canonical := ""
	if canonicalURL != "" {
		canonical = `<link rel="canonical" href="` + html.EscapeString(canonicalURL) + `">`
	}

	var page bytes.Buffer
	_, _ = fmt.Fprintf(&page, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Index of %s</title>
%s
<style>
:root { color-scheme: dark; background: #303446; color: #c6d0f5; font: 1rem/1.5 system-ui, sans-serif; }
/* Leave room for the sticky Up link's minimum size or padded line, plus its border. */
html { scroll-padding-block-start: calc(max(44px, 2.5rem) + 1px); }
* { box-sizing: border-box; }
body { margin: 0; }
a { color: #babbf1; text-decoration: underline; text-underline-offset: .2em; }
a:visited { color: #c6d0f5; }
a:hover { background: #414559; }
a:active { background: #51576d; }
a:focus-visible, summary:focus-visible, input:focus-visible { outline: 3px solid #e5c890; outline-offset: -3px; }
::selection { background: #babbf1; color: #232634; }
header { position: sticky; top: 0; z-index: 1; background: #292c3c; border-bottom: 1px solid #626880; }
header nav, main { max-width: 56rem; margin-inline: auto; padding-inline: 1rem; }
.up, .view-link, .breadcrumbs a { display: inline-flex; align-items: center; min-height: 44px; padding: .5rem; }
.view-link[aria-current] { color: #c6d0f5; font-weight: 700; text-decoration-thickness: 2px; }
.up { font-weight: 600; }
.breadcrumbs { display: flex; flex-wrap: wrap; align-items: center; list-style: none; margin: 0; padding: 0 0 .5rem; }
.breadcrumbs li { min-width: 0; overflow-wrap: anywhere; }
.breadcrumbs li + li::before { content: '/'; padding-inline: .25rem; }
.breadcrumbs [aria-current] { padding: .5rem; }
main { padding-block: 1.5rem 3rem; }
h1 { margin: 0 0 1.5rem; font-size: 1.5rem; line-height: 1.3; overflow-wrap: anywhere; }
.files { list-style: none; margin: 0; padding: 0; }
.files > li { border-bottom: 1px solid #51576d; padding-bottom: .625rem; }
.file-row { display: block; min-height: 44px; padding: .75rem .5rem .5rem; overflow-wrap: anywhere; }
.file-meta { display: block; padding-inline: .5rem; color: #b5bfe2; font-size: .875rem; font-variant-numeric: tabular-nums; }
.file-actions { margin: .5rem .5rem 0; }
summary { min-height: 44px; padding: .625rem .5rem; cursor: pointer; color: #babbf1; text-decoration: underline; text-underline-offset: .2em; }
summary:hover { background: #414559; }
.action-content { padding: 0 .5rem .5rem; }
.download { display: inline-flex; align-items: center; min-height: 44px; padding-block: .625rem; }
label { display: block; padding-top: .5rem; }
.share-url { display: block; width: 100%%; min-width: 0; min-height: 44px; margin-top: .5rem; padding: .5rem; border: 1px solid #838ba7; border-radius: .25rem; background: #232634; color: #c6d0f5; font: inherit; }
.notice { margin-block: 1.5rem; }
</style>
</head>
<body>
`, escapedVirtualPath, canonical)

	_, _ = io.WriteString(&page, `<header><nav aria-label="Folder navigation">`)
	if virtualPath != "/" {
		_, _ = fmt.Fprintf(&page, `<a class="up" aria-label="Up to parent folder" href="%s%s">Up</a>`, html.EscapeString(urlEscapeVirtual(path.Dir(virtualPath))), viewQuery)
	}
	_, _ = io.WriteString(&page, `</nav></header><main><nav aria-label="Directory view">`)
	_, _ = fmt.Fprintf(&page, `<a id="files-view" class="view-link" href="%s%s" aria-current="page">Files</a><a id="terminal-view" class="view-link" href="%s">Terminal</a>`, html.EscapeString(urlEscapeVirtual(virtualPath)), viewQuery, html.EscapeString(terminalURL))
	_, _ = io.WriteString(&page, `</nav><nav aria-label="Breadcrumbs"><ol class="breadcrumbs">`)
	if virtualPath == "/" {
		_, _ = io.WriteString(&page, `<li aria-current="page">Shared files</li>`)
	} else {
		breadcrumbBytes := 0
		appendBreadcrumb := func(link string) bool {
			// Retained query state is repeated in each link. Bound that expansion.
			if breadcrumbBytes+len(link) > maxCommandText/16 {
				return false
			}
			breadcrumbBytes += len(link)
			_, _ = page.WriteString(link)
			return true
		}
		omitted := !appendBreadcrumb(fmt.Sprintf(`<li><a href="/%s">Shared files</a></li>`, viewQuery))
		parts := strings.Split(strings.TrimPrefix(virtualPath, "/"), "/")
		for i, part := range parts[:len(parts)-1] {
			if !appendBreadcrumb(fmt.Sprintf(`<li><a href="%s%s">%s</a></li>`, html.EscapeString(urlEscapeVirtual("/"+strings.Join(parts[:i+1], "/"))), viewQuery, html.EscapeString(part))) {
				omitted = true
				break
			}
		}
		if omitted {
			_, _ = page.WriteString(`<li>More parent folders</li>`)
		}
		_, _ = fmt.Fprintf(&page, `<li aria-current="page">%s</li>`, html.EscapeString(parts[len(parts)-1]))
	}
	_, _ = fmt.Fprintf(&page, "</ol></nav><h1>Index of %s</h1>\n", escapedVirtualPath)
	if navigationLimited {
		_, _ = page.WriteString(`<p class="notice">Some navigation details were omitted by server limits.</p>`)
	}
	_, _ = page.WriteString(`<ul class="files" aria-label="Files and folders">`)
	rowCount := 0
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		name := path.Base(entry.name)
		if strings.HasPrefix(name, ".") {
			continue
		}
		href := html.EscapeString(urlEscapeVirtual(path.Join(virtualPath, name)))
		label := html.EscapeString(name)
		var row string
		if entry.info.IsDir() {
			row = fmt.Sprintf(`<li><a class="file-row" href="%s%s">%s/</a><span class="file-meta">Folder</span></li>`, href, viewQuery, label)
		} else {
			download := html.EscapeString("/api/download?path=" + url.QueryEscape(path.Join(virtualPath, name)))
			share := `<p>Share URL unavailable for this address.</p>`
			if publicBase != "" {
				shareURL := html.EscapeString(publicBase + urlEscapeVirtual(path.Join(virtualPath, name)))
				share = fmt.Sprintf(`<label>Share URL<input class="share-url" aria-label="Share URL for %s" type="text" readonly value="%s"></label>`, label, shareURL)
			}
			actions := fmt.Sprintf(`<details class="file-actions"><summary aria-label="Actions for %s">Actions</summary><div class="action-content"><a class="download" aria-label="Download %s" href="%s">Download</a>%s</div></details>`, label, label, download, share)
			row = fmt.Sprintf(`<li><a class="file-row" href="%s">%s</a><span class="file-meta">%d bytes</span>%s</li>`, href, label, entry.info.Size(), actions)
		}
		// Reserve complete closing tags and the truncation/empty-state paragraph.
		if page.Len()+len(row)+256 > maxCommandText {
			truncated = true
			break
		}
		_, _ = page.WriteString(row)
		rowCount++
	}
	_, _ = page.WriteString("</ul>")
	if rowCount == 0 && !truncated {
		_, _ = page.WriteString(`<p class="notice">This folder is empty.</p>`)
	}
	if truncated {
		_, _ = fmt.Fprintf(&page, "\n<p class=\"notice\">%s</p>", truncationNote)
	}
	_, _ = page.WriteString("\n</main></body>\n</html>\n")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page.Bytes())
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
