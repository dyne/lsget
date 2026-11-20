package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

var version = "dev"

// Indirections for testability
var (
	exitFunc       = os.Exit
	listenAndServe = func(srv *http.Server) error { return srv.ListenAndServe() }
	pidFile        = ""
	logFile        = ""
	logMutex       sync.Mutex
)

// ===== ANSI Color Codes =====

const (
	// Reset all attributes
	colorReset = "\033[0m"

	// Text colors
	colorRed     = "\033[31m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorMagenta = "\033[35m"
	colorCyan    = "\033[36m"
	colorWhite   = "\033[37m"

	// Bright text colors
	colorBrightBlack  = "\033[90m"
	colorBrightGreen  = "\033[92m"
	colorBrightYellow = "\033[93m"
	colorBrightCyan   = "\033[96m"

	// Text attributes
	colorBold = "\033[1m"
)

const helpTpl = `Welcome to <span class="ps1">lsget</span> <span class="help-version">v{{.Version}}</span>!
<span class="help-note">Type one of the commands below to get started.</span>
<br/>

<span class="help-heading">Available commands:</span>
• <strong>help</strong> - <span class="help-description">print this message again</span>
• <strong>pwd</strong> - <span class="help-description">print working directory</span>
• <strong>ls</strong> <span class="help-note">[-l] [-h]</span>|<strong>dir</strong> <span class="help-note">[-l] [-h]</span> - <span class="help-description">list files (-h for human readable sizes)</span>
• <strong>cd</strong> <span class="help-note">DIR</span> - <span class="help-description">change directory</span>
• <strong>cat</strong> <span class="help-note">FILE</span> - <span class="help-description">view a text file</span>
• <strong>sum</strong>|<strong>checksum</strong> <span class="help-note">FILE</span> - <span class="help-description">print MD5 and SHA256 checksums</span>
• <strong>get</strong>|<strong>wget</strong>|<strong>download</strong> <span class="help-note">FILE</span> - <span class="help-description">download a file</span>
• <strong>url</strong>|<strong>share</strong> <span class="help-note">FILE</span> - <span class="help-description">get shareable URL (copies to clipboard)</span>
• <strong>tree</strong> <span class="help-note">[-L&lt;DEPTH&gt;] [-a]</span> - <span class="help-description">directory structure</span>
• <strong>find</strong> <span class="help-note">[PATH] [-name PATTERN] [-type f|d]</span> - <span class="help-description">search for files and directories</span>
• <strong>grep</strong> <span class="help-note">[-r] [-i] [-n] PATTERN [FILE...]</span> - <span class="help-description">search for text patterns in files</span>

<br/><br/>
<span class="help-heading">Hint: to autocomplete filenames and dir use</span> <kbd class="ps1">Tab</kbd>
`

func renderHelp() string {
	helpMessage := template.Must(template.New("help").Parse(helpTpl))
	var b bytes.Buffer
	_ = helpMessage.Execute(&b, struct{ Version string }{Version: version})
	return b.String()
}

// getFileColor returns the appropriate ANSI color code for a file based on its type and permissions
func getFileColor(info os.FileInfo, name string) string {
	mode := info.Mode()

	// Directories
	if mode.IsDir() {
		return colorBlue + colorBold
	}

	// Executable files
	if mode&0o111 != 0 {
		return colorGreen
	}

	// Symbolic links
	if mode&os.ModeSymlink != 0 {
		return colorCyan
	}

	// Special files
	if mode&os.ModeNamedPipe != 0 {
		return colorYellow
	}
	if mode&os.ModeSocket != 0 {
		return colorMagenta
	}
	if mode&os.ModeDevice != 0 {
		return colorYellow + colorBold
	}

	// Regular files - color by extension
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".tar", ".tgz", ".tar.gz", ".tar.bz2", ".tar.xz", ".zip", ".rar", ".7z", ".gz", ".bz2", ".xz":
		return colorRed
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".svg", ".ico", ".tiff", ".webp":
		return colorMagenta
	case ".mp3", ".wav", ".flac", ".aac", ".ogg", ".wma", ".m4a":
		return colorGreen
	case ".mp4", ".avi", ".mkv", ".mov", ".wmv", ".flv", ".webm", ".m4v":
		return colorBrightGreen
	case ".pdf", ".doc", ".docx", ".txt", ".md", ".rst", ".tex":
		return colorWhite
	case ".py", ".js", ".ts", ".jsx", ".tsx", ".go", ".rs", ".cpp", ".c", ".h", ".java", ".kt", ".swift":
		return colorYellow
	case ".html", ".htm", ".css", ".scss", ".sass", ".xml", ".json", ".yaml", ".yml":
		return colorBrightYellow
	case ".sh", ".bash", ".zsh", ".fish", ".ps1", ".bat", ".cmd":
		return colorGreen
	case ".sql", ".db", ".sqlite", ".sqlite3":
		return colorBrightCyan
	case ".log", ".tmp", ".temp", ".bak", ".backup":
		return colorBrightBlack
	default:
		return colorReset
	}
}

// colorizeName wraps a filename with appropriate ANSI color codes
func colorizeName(info os.FileInfo, name string) string {
	color := getFileColor(info, name)
	// Add trailing / for directories (Unix style)
	if info.IsDir() {
		return color + name + "/" + colorReset
	}
	return color + name + colorReset
}

// FileCategory represents supported file types for different commands
type FileCategory int

const (
	FileCategoryUnknown FileCategory = iota
	FileCategoryText
	FileCategoryImage
	FileCategoryArchive
	FileCategoryVideo
	FileCategoryAudio
	FileCategoryDocument
)

func (fc FileCategory) String() string {
	switch fc {
	case FileCategoryText:
		return "text"
	case FileCategoryImage:
		return "image"
	case FileCategoryArchive:
		return "archive"
	case FileCategoryVideo:
		return "video"
	case FileCategoryAudio:
		return "audio"
	case FileCategoryDocument:
		return "document"
	default:
		return "unknown"
	}
}

// getFileCategory determines the category of a file by extension
func getFileCategory(filename string) FileCategory {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	// Text files
	case ".txt", ".md", ".rst", ".log", ".json", ".xml", ".yaml", ".yml", ".toml", ".ini", ".conf", ".cfg":
		return FileCategoryText
	case ".html", ".htm", ".css", ".js", ".ts", ".jsx", ".tsx":
		return FileCategoryText
	case ".c", ".cpp", ".h", ".hpp", ".go", ".rs", ".py", ".rb", ".java", ".php", ".sh", ".bash":
		return FileCategoryText

	// Images
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg", ".ico":
		return FileCategoryImage

	// Archives
	case ".zip", ".tar", ".gz", ".bz2", ".xz", ".7z", ".rar", ".tgz", ".tbz2":
		return FileCategoryArchive

	// Video
	case ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".m4v":
		return FileCategoryVideo

	// Audio
	case ".mp3", ".wav", ".flac", ".ogg", ".m4a", ".aac", ".wma":
		return FileCategoryAudio

	// Documents
	case ".pdf", ".doc", ".docx", ".odt", ".xls", ".xlsx", ".ppt", ".pptx":
		return FileCategoryDocument

	default:
		return FileCategoryUnknown
	}
}

const readmePreviewLimit = 256 * 1024

func readDocPreview(ctx context.Context, root *os.Root, name string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	file, _, err := openVisibleFile(root, name)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = file.Close() }()

	body, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, readmePreviewLimit+1))
	if err != nil {
		return "", false, err
	}
	if len(body) <= readmePreviewLimit {
		return string(body), false, nil
	}
	return string(body[:readmePreviewLimit]), true, nil
}

// readDocFile selects the most relevant visible document and previews its contents.
func readDocFile(ctx context.Context, root *os.Root, dir string) (string, string, bool) {
	ents, err := readVisibleDir(ctx, newWorkBudget(), root, dir)
	if err != nil && !errors.Is(err, errWorkLimit) {
		return "", "", false
	}

	// Priority order for documentation files
	docFiles := []struct {
		pattern  string
		fileType string
	}{
		{"README.md", "markdown"},
		{"README.txt", "text"},
		{"README.rst", "rst"},
		{"README.nfo", "nfo"},
	}

	// First, try exact matches in priority order
	for _, docFile := range docFiles {
		if errors.Is(err, errWorkLimit) {
			// A canonical README may be beyond the bounded directory sample.
			body, truncated, previewErr := readDocPreview(ctx, root, path.Join(dir, docFile.pattern))
			if previewErr == nil {
				return body, docFile.fileType, truncated
			}
		}
		for _, e := range ents {
			if ctx.Err() != nil {
				return "", "", false
			}
			if strings.EqualFold(e.Name(), docFile.pattern) {
				body, truncated, err := readDocPreview(ctx, root, path.Join(dir, e.Name()))
				if err != nil {
					continue
				}
				return body, docFile.fileType, truncated
			}
		}
	}

	// Then try any file with supported extensions
	for _, e := range ents {
		if ctx.Err() != nil {
			return "", "", false
		}
		ext := path.Ext(strings.ToLower(e.Name()))
		for _, docFile := range docFiles {
			if ext == path.Ext(docFile.pattern) {
				body, truncated, err := readDocPreview(ctx, root, path.Join(dir, e.Name()))
				if err == nil {
					return body, docFile.fileType, truncated
				}
				break
			}
		}
	}

	return "", "", false
}

// ===== Embedded application shell and assets =====

//go:embed index.html
var embeddedIndex []byte

//go:embed assets/js/marked.min.js
var embeddedMarkedJS []byte

//go:embed assets/js/datastar.js
var embeddedDatastarJS []byte

// ===== Server state =====

const (
	sessionIdleTimeout = 30 * time.Minute
	sessionSweepPeriod = time.Minute
	maxSessions        = 4096
)

type session struct {
	mu  sync.Mutex
	cwd string // virtual path, guarded by mu

	// The session map mutex guards request accounting and idle time.
	active   int
	idleFrom time.Time
}

func (s *session) workingDirectory() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func (s *session) changeDirectory(root *os.Root, target string) (string, error) {
	// Resolve and validate relative changes against the last committed CWD.
	s.mu.Lock()
	defer s.mu.Unlock()
	newV := joinVirtual(s.cwd, target)
	info, err := visibleInfo(root, rootedName(newV))
	if err != nil {
		return "", errors.New("no such file or directory")
	}
	if !info.IsDir() {
		return "", errors.New("not a directory")
	}
	s.cwd = newV
	return newV, nil
}

type server struct {
	rootAbs string // absolute filesystem root we expose
	catMax  int64  // max bytes allowed for `cat`
	logfile string // path to log file for statistics
	baseURL string // optional: public base URL (e.g., https://files.example.com) - auto-detects from request if empty

	sessionsMu          sync.Mutex
	sessions            map[string]*session
	sessionNow          func() time.Time
	sessionLimit        int
	secureSessionCookie bool
	workSlots           chan struct{}

	devTemplate        string
	shellCSPReportOnly bool

	sitemapMu sync.Mutex
	sitemap   atomic.Pointer[sitemapSnapshot]
}

func newServer(rootAbs string, catMax int64, logfile, baseURL string) *server {
	return &server{
		rootAbs:      rootAbs,
		catMax:       catMax,
		sessions:     make(map[string]*session),
		sessionNow:   time.Now,
		sessionLimit: maxSessions,
		workSlots:    make(chan struct{}, maxWorkJobs),
		logfile:      logfile,
		baseURL:      baseURL,
	}
}

// ===== .lsgetignore support =====

// parseIgnoreFile reads and parses a .lsgetignore file, returning a slice of patterns
func parseIgnoreFile(files fs.FS, ignoreFilePath string) ([]string, error) {
	info, err := fs.Stat(files, ignoreFilePath)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !info.Mode().IsRegular() {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	file, err := files.Open(ignoreFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No ignore file is fine
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() {
		return nil, nil
	}

	var patterns []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}

	return patterns, scanner.Err()
}

func matchesIgnoreRules(files fs.FS, filePath string) bool {
	if filePath == "." {
		return false
	}
	parts := strings.Split(filePath, "/")
	for depth := range parts {
		dir := strings.Join(parts[:depth], "/")
		patterns, err := parseIgnoreFile(files, path.Join(dir, ".lsgetignore"))
		if err != nil || len(patterns) == 0 {
			continue
		}
		for candidate := strings.Join(parts[depth:], "/"); candidate != "."; candidate = path.Dir(candidate) {
			for _, pattern := range patterns {
				matchesName, _ := filepath.Match(pattern, path.Base(candidate))
				matchesPath, _ := filepath.Match(pattern, filepath.FromSlash(candidate))
				if matchesName || matchesPath {
					return true
				}
			}
		}
	}
	return false
}

// ===== Utilities =====

type sitemapSnapshot struct {
	content     string
	generatedAt time.Time
}

func (s *server) getSitemap(ctx context.Context, refresh bool) (*sitemapSnapshot, error) {
	if cached := s.sitemap.Load(); cached != nil && !refresh {
		return cached, nil
	}

	if !s.sitemapMu.TryLock() {
		return nil, errWorkBusy
	}
	defer s.sitemapMu.Unlock()
	if cached := s.sitemap.Load(); cached != nil && !refresh {
		return cached, nil
	}

	release, err := s.acquireWork(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	content, err := s.generateSitemap(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := &sitemapSnapshot{content: content, generatedAt: time.Now()}
	s.sitemap.Store(snapshot)
	return snapshot, nil
}

// generateSitemap builds the XML from the files currently in the served root.
func (s *server) generateSitemap(ctx context.Context) (string, error) {
	base, err := sitemapBaseURL(s.baseURL)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(s.rootAbs)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()

	info, err := visibleInfo(root, ".")
	if err != nil {
		return "", err
	}
	output := &sitemapOutput{}
	enc := xml.NewEncoder(contextWriter{ctx, output})
	enc.Indent("", "  ")
	if err := enc.EncodeToken(xml.ProcInst{Target: "xml", Inst: []byte(`version="1.0" encoding="UTF-8"`)}); err != nil {
		return "", err
	}
	urlset := xml.StartElement{
		Name: xml.Name{Local: "urlset"},
		Attr: []xml.Attr{
			{Name: xml.Name{Local: "xmlns"}, Value: "http://www.sitemaps.org/schemas/sitemap/0.9"},
			{Name: xml.Name{Local: "xmlns:image"}, Value: "http://www.google.com/schemas/sitemap-image/1.1"},
			{Name: xml.Name{Local: "xmlns:video"}, Value: "http://www.google.com/schemas/sitemap-video/1.1"},
			{Name: xml.Name{Local: "xmlns:xhtml"}, Value: "http://www.w3.org/1999/xhtml"},
		},
	}
	if err := enc.EncodeToken(urlset); err != nil {
		return "", err
	}
	if err := writeSitemapEntry(enc, base, ".", info); err != nil {
		return "", err
	}
	err = walkVisibleDir(ctx, newWorkBudget(), root, ".", func(entry visibleEntry) (bool, error) {
		if entry.name == "sitemap.xml" {
			return false, nil
		}
		return true, writeSitemapEntry(enc, base, entry.name, entry.info)
	})
	if err != nil {
		return "", err
	}
	if err := enc.EncodeToken(urlset.End()); err != nil {
		return "", err
	}
	if err := enc.EncodeToken(xml.CharData("\n")); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return output.content.String(), nil
}

func sitemapBaseURL(raw string) (string, error) {
	if len(raw) > maxSitemapBytes {
		return "", errWorkLimit
	}
	invalid := errors.New("invalid sitemap base URL: expected a canonical absolute HTTP(S) URL without credentials, query or fragment")
	base, err := url.Parse(raw)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" ||
		base.User != nil || base.ForceQuery || base.RawQuery != "" || strings.Contains(raw, "#") ||
		strings.HasSuffix(base.Host, ":") || strings.Contains(strings.ToLower(base.EscapedPath()), "%2f") {
		return "", invalid
	}
	if port := base.Port(); port != "" {
		if number, err := strconv.ParseUint(port, 10, 16); err != nil || number == 0 {
			return "", invalid
		}
	}
	if prefix := strings.TrimRight(base.Path, "/"); prefix != "" && path.Clean(prefix) != prefix {
		return "", invalid
	}
	return strings.TrimRight(base.String(), "/"), nil
}

type sitemapOutput struct {
	content strings.Builder
}

func (out *sitemapOutput) Write(p []byte) (int, error) {
	if len(p) > maxSitemapBytes-out.content.Len() {
		return 0, errWorkLimit
	}
	return out.content.Write(p)
}

type sitemapField struct {
	name  string
	value string
}

func writeSitemapFields(enc *xml.Encoder, fields ...sitemapField) error {
	for _, field := range fields {
		element := xml.StartElement{Name: xml.Name{Local: field.name}}
		if err := enc.EncodeToken(element); err != nil {
			return err
		}
		if err := enc.EncodeToken(xml.CharData(field.value)); err != nil {
			return err
		}
		if err := enc.EncodeToken(element.End()); err != nil {
			return err
		}
	}
	return nil
}

func writeSitemapExtension(enc *xml.Encoder, name string, fields ...sitemapField) error {
	element := xml.StartElement{Name: xml.Name{Local: name}}
	if err := enc.EncodeToken(element); err != nil {
		return err
	}
	if err := writeSitemapFields(enc, fields...); err != nil {
		return err
	}
	return enc.EncodeToken(element.End())
}

func sitemapLocation(base, name string) string {
	if name == "." {
		return base + "/"
	}
	var location strings.Builder
	location.Grow(len(base) + len(name) + 1)
	location.WriteString(base)
	// The rooted walk supplies a clean slash-separated name.
	for name != "" {
		var segment string
		segment, name, _ = strings.Cut(name, "/")
		location.WriteByte('/')
		location.WriteString(url.PathEscape(segment))
	}
	return location.String()
}

func writeSitemapEntry(enc *xml.Encoder, base, name string, info fs.FileInfo) error {
	location := sitemapLocation(base, name)
	filename := path.Base(name)
	category := FileCategoryUnknown
	frequency, priority := "weekly", "0.5"
	if info.IsDir() {
		frequency, priority = "daily", "0.8"
	} else {
		category = getFileCategory(filename)
		switch category {
		case FileCategoryImage:
			frequency, priority = "monthly", "0.6"
		case FileCategoryVideo:
			frequency, priority = "monthly", "0.7"
		case FileCategoryDocument:
			frequency, priority = "monthly", "0.9"
		}
	}

	element := xml.StartElement{Name: xml.Name{Local: "url"}}
	if err := enc.EncodeToken(element); err != nil {
		return err
	}
	if err := writeSitemapFields(enc,
		sitemapField{"loc", location},
		sitemapField{"lastmod", info.ModTime().Format(time.RFC3339)},
		sitemapField{"changefreq", frequency},
		sitemapField{"priority", priority},
	); err != nil {
		return err
	}
	var err error
	switch category {
	case FileCategoryImage:
		err = writeSitemapExtension(enc, "image:image", sitemapField{"image:loc", location})
	case FileCategoryVideo:
		err = writeSitemapExtension(enc, "video:video",
			sitemapField{"video:content_loc", location},
			sitemapField{"video:title", strings.TrimSuffix(filename, path.Ext(filename))},
			sitemapField{"video:description", "Video file: " + filename},
		)
	case FileCategoryDocument:
		label := url.PathEscape(filename)
		if strings.Contains(label, "--") {
			// XML comments cannot contain adjacent hyphens.
			label = strings.ReplaceAll(label, "-", "%2D")
		}
		err = enc.EncodeToken(xml.Comment(" Document: " + label + " (" + formatHumanSize(info.Size()) + ") "))
	}
	if err != nil {
		return err
	}
	return enc.EncodeToken(element.End())
}

// startSitemapGenerator starts periodic sitemap regeneration
func (s *server) startSitemapGenerator(ctx context.Context, interval time.Duration) {
	if interval <= 0 || s.baseURL == "" {
		return
	}

	if _, err := s.getSitemap(ctx, true); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate initial sitemap: %v\n", err)
	} else {
		fmt.Println("Generated sitemap.xml in memory")
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.getSitemap(ctx, true); err != nil {
					fmt.Fprintf(os.Stderr, "failed to regenerate sitemap: %v\n", err)
				}
			}
		}
	}()
}

// getClientIP extracts the real client IP, checking X-Forwarded-For first
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (for reverse proxies)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can contain multiple IPs: "client, proxy1, proxy2"
		// The first one is the original client
		if comma := strings.Index(xff, ","); comma > 0 {
			return strings.TrimSpace(xff[:comma])
		}
		return strings.TrimSpace(xff)
	}

	// Fallback to RemoteAddr
	ip := r.RemoteAddr
	if colon := strings.LastIndex(ip, ":"); colon != -1 {
		return ip[:colon]
	}
	return ip
}

// logCommand writes a command execution to the log file
func (s *server) logCommand(cmd, filePath, ip string) {
	if s.logfile == "" {
		return
	}

	logMutex.Lock()
	defer logMutex.Unlock()

	// Ensure log directory exists
	logDir := filepath.Dir(s.logfile)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return
	}

	f, err := os.OpenFile(s.logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	timestamp := time.Now().Format("[02/Jan/2006:15:04:05 -0700]")
	// Format: ip - - timestamp "POST /api/exec?cmd=COMMAND&file=PATH HTTP/1.1" 200 0 "-" "-"
	logLine := fmt.Sprintf("%s - - %s \"POST /api/exec?cmd=%s&file=%s HTTP/1.1\" 200 0 \"-\" \"-\"\n",
		ip, timestamp, cmd, url.QueryEscape(filePath))
	_, _ = f.WriteString(logLine)
}

func newSID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b[:])
}

// acquireSession pins a session until the handler calls releaseSession.
func (s *server) acquireSession(w http.ResponseWriter, r *http.Request) *session {
	if ck, err := r.Cookie("sid"); err == nil && len(ck.Value) == 32 {
		if sess := s.resumeSession(ck.Value); sess != nil {
			return sess
		}
	}

	sess, id := s.createSession()
	if sess == nil {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "session capacity temporarily reached", http.StatusServiceUnavailable)
		return nil
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "sid",
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || s.secureSessionCookie,
	})
	return sess
}

func (s *server) resumeSession(id string) *session {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	sess := s.sessions[id]
	if sess == nil {
		return nil
	}
	if sess.expired(s.sessionNow()) {
		delete(s.sessions, id)
		return nil
	}
	sess.active++
	return sess
}

func (s *server) createSession() (*session, string) {
	id := newSID()
	s.sessionsMu.Lock()
	for s.sessions[id] != nil {
		s.sessionsMu.Unlock()
		id = newSID()
		s.sessionsMu.Lock()
	}
	defer s.sessionsMu.Unlock()
	if len(s.sessions) >= s.sessionLimit {
		var oldestID string
		var oldest *session
		for id, sess := range s.sessions {
			if sess.active == 0 && (oldest == nil || sess.idleFrom.Before(oldest.idleFrom)) {
				oldestID, oldest = id, sess
			}
		}
		if oldest == nil {
			return nil, ""
		}
		delete(s.sessions, oldestID)
	}
	sess := &session{cwd: "/", active: 1}
	s.sessions[id] = sess
	return sess, id
}

func (s *server) releaseSession(sess *session) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	sess.active--
	if sess.active == 0 {
		sess.idleFrom = s.sessionNow()
	}
}

func (s *session) expired(now time.Time) bool {
	return s.active == 0 && now.Sub(s.idleFrom) >= sessionIdleTimeout
}

func (s *server) expireSessions() {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	now := s.sessionNow()
	for id, sess := range s.sessions {
		if sess.expired(now) {
			delete(s.sessions, id)
		}
	}
}

func (s *server) runSessionCleanup(ctx context.Context) {
	ticker := time.NewTicker(sessionSweepPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.expireSessions()
		}
	}
}

// ensure virtual path always starts with "/" and is cleaned
func cleanVirtual(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

// join a virtual base with an argument (which can be absolute or relative),
// then clean and ensure it remains absolute (virtual)
func joinVirtual(base, arg string) string {
	if arg == "" {
		return cleanVirtual(base)
	}
	if strings.HasPrefix(arg, "/") {
		return cleanVirtual(arg)
	}
	if base == "" {
		base = "/"
	}
	return cleanVirtual(path.Join(base, arg))
}

// simple args parser: supports quotes ("", ”) and backslash escapes inside quotes
func parseArgs(line string) []string {
	var args []string
	var buf bytes.Buffer
	inSingle, inDouble := false, false

	flush := func() {
		if buf.Len() > 0 || inSingle || inDouble {
			args = append(args, buf.String())
			buf.Reset()
		}
	}

	for i := 0; i < len(line); i++ {
		c := line[i]
		if inSingle {
			if c == '\'' {
				inSingle = false
			} else {
				buf.WriteByte(c)
			}
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			} else if c == '\\' && i+1 < len(line) {
				i++
				buf.WriteByte(line[i])
			} else {
				buf.WriteByte(c)
			}
			continue
		}
		switch c {
		case ' ', '\t', '\n':
			if buf.Len() > 0 {
				args = append(args, buf.String())
				buf.Reset()
			}
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		default:
			buf.WriteByte(c)
		}
	}
	flush()
	return args
}

func formatLong(info os.FileInfo, name string, humanReadable bool) string {
	// mode, size, date, name (owner/group omitted for portability)
	mode := info.Mode().String()
	size := info.Size()
	mod := info.ModTime().Format("Jan _2 15:04")

	if humanReadable {
		sizeStr := formatHumanSize(size)
		return fmt.Sprintf("%s %10s %s %s", mode, sizeStr, mod, name)
	}
	return fmt.Sprintf("%s %10d %s %s", mode, size, mod, name)
}

// formatHumanSize formats byte size in human-readable format
func formatHumanSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	}
	const unit = 1024
	if size < unit*unit {
		return fmt.Sprintf("%.1fK", float64(size)/unit)
	}
	if size < unit*unit*unit {
		return fmt.Sprintf("%.1fM", float64(size)/(unit*unit))
	}
	if size < unit*unit*unit*unit {
		return fmt.Sprintf("%.1fG", float64(size)/(unit*unit*unit))
	}
	return fmt.Sprintf("%.1fT", float64(size)/(unit*unit*unit*unit))
}

// text/binary heuristic: reject if contains NUL or too many non-printables;
// accept if UTF-8 valid or printable ratio >= 0.85
func looksText(sample []byte) bool {
	if bytes.IndexByte(sample, 0x00) >= 0 {
		return false
	}
	if utf8.Valid(sample) {
		return true
	}
	printable := 0
	total := 0
	for _, b := range sample {
		total++
		if b == 9 || b == 10 || b == 13 || (b >= 32 && b <= 126) {
			printable++
		}
	}
	if total == 0 {
		return true
	}
	return float64(printable)/float64(total) >= 0.85
}

// ===== HTTP payloads =====

type execReq struct {
	Input string `json:"input"`
}

type execResp struct {
	Output          string     `json:"output"`
	Truncated       bool       `json:"truncated,omitempty"`
	Download        string     `json:"download,omitempty"`
	CWD             string     `json:"cwd,omitempty"`
	Readme          *string    `json:"readme,omitempty"`
	DocType         string     `json:"docType,omitempty"`
	ReadmeTruncated bool       `json:"readmeTruncated,omitempty"`
	Clipboard       string     `json:"clipboard,omitempty"`
	HTML            string     `json:"html,omitempty"`
	Help            bool       `json:"help,omitempty"`
	Image           *execImage `json:"image,omitempty"`
	Redirect        string     `json:"redirect,omitempty"`
}

type execImage struct {
	URL  string `json:"url"`
	Name string `json:"name"`
	Size string `json:"size"`
}

type completeReq struct {
	Path      string `json:"path"`
	DirsOnly  bool   `json:"dirsOnly"`
	FilesOnly bool   `json:"filesOnly"`
	TextOnly  bool   `json:"textOnly"`
	MaxSize   int64  `json:"maxSize"`
}

type completeItem struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
}

type completeResp struct {
	Items     []completeItem `json:"items"`
	Truncated bool           `json:"truncated,omitempty"`
}

type configResp struct {
	CatMax          int64   `json:"catMax"`
	Readme          *string `json:"readme,omitempty"`
	DocType         string  `json:"docType,omitempty"`
	ReadmeTruncated bool    `json:"readmeTruncated,omitempty"`
	CWD             string  `json:"cwd,omitempty"`
}

// ===== Handlers =====

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

var (
	errDownloadDirectory = errors.New("is a directory")
	errHiddenPath        = fmt.Errorf("path is not public: %w", fs.ErrNotExist)
)

// rootedName converts a virtual path to a name in the served filesystem.
func rootedName(virtualPath string) string {
	name := strings.TrimPrefix(cleanVirtual(virtualPath), "/")
	if name == "" {
		return "."
	}
	return name
}

func visibleInfo(root *os.Root, name string) (fs.FileInfo, error) {
	local, err := filepath.Localize(name)
	if err != nil {
		return nil, err
	}
	files := root.FS()
	if matchesIgnoreRules(files, name) {
		return nil, errHiddenPath
	}
	info, err := root.Stat(local)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, errHiddenPath
	}
	for prefix := name; prefix != "."; prefix = path.Dir(prefix) {
		entry, err := root.Lstat(filepath.FromSlash(prefix))
		if err != nil {
			return nil, err
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			if !visibleLinkTarget(root, local, info) {
				return nil, errHiddenPath
			}
			break
		}
	}
	return info, nil
}

// Go 1.24 has no Root.Readlink. Resolve the target name for the ignore rules,
// then confirm it identifies the same object using rooted metadata.
func visibleLinkTarget(root *os.Root, local string, info fs.FileInfo) bool {
	base, err := filepath.EvalSymlinks(root.Name())
	if err != nil {
		return false
	}
	target, err := filepath.EvalSymlinks(filepath.Join(base, local))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || !filepath.IsLocal(relative) {
		return false
	}
	name := filepath.ToSlash(relative)
	if matchesIgnoreRules(root.FS(), name) {
		return false
	}
	targetInfo, err := root.Stat(relative)
	return err == nil && os.SameFile(info, targetInfo)
}

func openVisibleFile(root *os.Root, filePath string) (*os.File, fs.FileInfo, error) {
	info, err := visibleInfo(root, filePath)
	if err != nil {
		return nil, nil, err
	}
	if info.IsDir() {
		return nil, nil, errDownloadDirectory
	}
	file, err := root.Open(filepath.FromSlash(filePath))
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err == nil && (!opened.Mode().IsRegular() || !os.SameFile(info, opened)) {
		err = errHiddenPath
	}
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, opened, nil
}

func readVisibleDir(ctx context.Context, budget *workBudget, root *os.Root, name string) ([]fs.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := visibleInfo(root, name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fs.ErrInvalid
	}
	dir, err := root.OpenRoot(filepath.FromSlash(name))
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	opened, err := dir.Stat(".")
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errHiddenPath
	}
	file, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return budget.readEntries(ctx, file)
}

type visibleEntry struct {
	name    string
	info    fs.FileInfo
	descend bool
}

func visibleChildren(ctx context.Context, budget *workBudget, root *os.Root, dir string) ([]visibleEntry, error) {
	entries, err := readVisibleDir(ctx, budget, root, dir)
	if err != nil && !errors.Is(err, errWorkLimit) {
		return nil, err
	}
	var children []visibleEntry
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, nameErr := budget.childName(dir, entry.Name())
		if nameErr != nil {
			return children, nameErr
		}
		info, err := visibleInfo(root, name)
		if err != nil {
			continue
		}
		// List visible directory links without recursing through them.
		children = append(children, visibleEntry{name, info, entry.IsDir() && info.IsDir()})
	}
	return children, err
}

// walkVisibleDir keeps the directory's root error and skips unreadable children.
// A false visit result prunes that entry's descendants. A visit error stops the walk.
func walkVisibleDir(ctx context.Context, budget *workBudget, root *os.Root, dir string, visit func(visibleEntry) (bool, error)) error {
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth == maxWalkDepth {
			return errWorkLimit
		}
		children, readErr := visibleChildren(ctx, budget, root, dir)
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			descend, err := visit(child)
			if err != nil {
				return err
			}
			if readErr == nil && descend && child.descend {
				if err := walk(child.name, depth+1); stopWork(err) {
					return err
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return readErr
	}
	return walk(dir, 0)
}

func serveDownloadFile(w http.ResponseWriter, r *http.Request, root *os.Root, filePath string, attachment bool) {
	file, info, err := openVisibleFile(root, filePath)
	if err != nil {
		if attachment && errors.Is(err, errDownloadDirectory) {
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	defer func() { _ = file.Close() }()

	// Keep the canonical directory redirect used by http.ServeFile.
	if strings.HasSuffix(r.URL.Path, "/index.html") {
		location := url.URL{Path: "./", RawQuery: r.URL.RawQuery}
		w.Header().Set("Location", location.String())
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}

	filename := path.Base(filePath)
	ext := path.Ext(filename)
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	switch strings.ToLower(ext) {
	case ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".zip", ".rar", ".7z", ".tar", ".gz":
		attachment = true
	}
	if attachment {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	}
	http.ServeContent(w, r, filename, info.ModTime(), file)
}

func (s *server) serveMainIndex(w http.ResponseWriter, initialPath string) {
	htmlContent := embeddedIndex
	var nonce string
	if s.devTemplate != "" {
		var err error
		htmlContent, err = os.ReadFile(s.devTemplate)
		if err != nil {
			http.Error(w, "Could not read the development template", http.StatusInternalServerError)
			return
		}
	} else {
		nonce = rand.Text()
		w.Header().Set("Cache-Control", "no-store")
		s.setShellCSP(w, nonce)
	}

	processedHTML := s.processHTMLTemplate(htmlContent, initialPath, nonce)

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

// handleVendoredMarked serves the vendored marked.min.js library
func (s *server) handleVendoredMarked(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000") // Cache for 1 year
	_, _ = w.Write(embeddedMarkedJS)
}

// handleVendoredDatastar serves the vendored datastar.js library
func (s *server) handleVendoredDatastar(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000") // Cache for 1 year
	_, _ = w.Write(embeddedDatastarJS)
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
	).Replace(string(htmlContent))
	return []byte(result)
}

func (s *server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	if s.baseURL == "" {
		// Allow a manually maintained sitemap when automatic generation is unconfigured.
		root, err := os.OpenRoot(s.rootAbs)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = root.Close() }()
		file, info, err := openVisibleFile(root, "sitemap.xml")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = file.Close() }()
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		http.ServeContent(w, r, "sitemap.xml", info.ModTime(), file)
		return
	}

	snapshot, err := s.getSitemap(r.Context(), false)
	if r.Context().Err() != nil {
		return
	}
	if errors.Is(err, errWorkBusy) {
		writeWorkBusy(w)
		return
	}
	if errors.Is(err, errWorkLimit) {
		http.Error(w, errWorkLimit.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		http.Error(w, "Failed to generate sitemap", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	http.ServeContent(w, r, "sitemap.xml", snapshot.generatedAt, strings.NewReader(snapshot.content))
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	initialPath := r.URL.Query().Get("path")
	if initialPath != "" && !s.acceptSessionOrigin(r) {
		http.Error(w, "forbidden request origin", http.StatusForbidden)
		return
	}
	release := s.reserveWork(w, r)
	if release == nil {
		return
	}
	defer release()
	sess := s.acquireSession(w, r)
	if sess == nil {
		return
	}
	defer s.releaseSession(sess)
	cwd := sess.workingDirectory()
	var readme, docType string
	var readmeTruncated bool
	if root, err := os.OpenRoot(s.rootAbs); err == nil {
		defer func() { _ = root.Close() }()
		if initialPath != "" {
			if newV, err := sess.changeDirectory(root, cleanVirtual(initialPath)); err == nil {
				cwd = newV
			}
		}
		readme, docType, readmeTruncated = readDocFile(r.Context(), root, rootedName(cwd))
	}
	if r.Context().Err() != nil {
		return
	}

	_ = json.NewEncoder(w).Encode(configResp{
		CatMax: s.catMax, Readme: &readme, DocType: docType, ReadmeTruncated: readmeTruncated, CWD: cwd,
	})
}

func (s *server) handleExec(w http.ResponseWriter, r *http.Request) {
	var req execReq
	if !s.readCommandRequest(w, r, &req) {
		return
	}
	args := parseArgs(strings.TrimSpace(req.Input))
	if len(args) == 0 {
		_ = json.NewEncoder(w).Encode(execResp{Output: ""})
		return
	}
	cmd := args[0]
	argv := args[1:]
	ctx := r.Context()
	if ctx.Err() != nil {
		return
	}
	switch cmd {
	case "ls", "dir", "cd", "cat", "tree", "find", "grep", "sum", "checksum":
		release := s.reserveWork(w, r)
		if release == nil {
			return
		}
		defer release()
	}
	budget := newWorkBudget()
	var sess *session
	var cwd string
	defer func() {
		if sess != nil {
			s.releaseSession(sess)
		}
	}()
	// A command may resolve several paths. Load one snapshot on its first relative path.
	cwdFor := func(target string) (string, bool) {
		if path.IsAbs(target) {
			return "/", true
		}
		if sess == nil {
			sess = s.acquireSession(w, r)
			if sess == nil {
				return "", false
			}
			cwd = sess.workingDirectory()
		}
		return cwd, true
	}
	var root *os.Root
	switch cmd {
	case "ls", "dir", "cd", "cat", "url", "share", "sum", "checksum", "tree", "find", "grep":
		var err error
		root, err = os.OpenRoot(s.rootAbs)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: cmd + ": cannot open served directory"})
			return
		}
		defer func() { _ = root.Close() }()
	}

	switch cmd {
	case "pwd":
		cwd, ok := cwdFor("")
		if !ok {
			return
		}
		_ = json.NewEncoder(w).Encode(execResp{Output: cwd, CWD: cwd})
		return

	case "help":
		_ = json.NewEncoder(w).Encode(execResp{HTML: renderHelp(), Help: true})
		return

	case "ls", "dir":
		// The parent marker depends on CWD even with an explicit listing path.
		cwd, ok := cwdFor("")
		if !ok {
			return
		}
		long := false
		showHidden := false
		humanReadable := false
		target := cwd
		// Parse arguments: flags and optional path
		for _, arg := range argv {
			if strings.HasPrefix(arg, "-") {
				// Handle flags
				if strings.Contains(arg, "l") {
					long = true
				}
				if strings.Contains(arg, "a") {
					showHidden = true
				}
				if strings.Contains(arg, "h") {
					humanReadable = true
				}
			} else {
				// First non-flag argument is the path
				target = arg
			}
		}
		virtualPath := joinVirtual(cwd, target)
		name := rootedName(virtualPath)
		info, err := visibleInfo(root, name)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "ls: cannot access '" + target + "': No such file or directory"})
			return
		}
		// If path is a file, show just the file
		if !info.IsDir() {
			// If it's a file, show the file in the listing with absolute virtual path
			displayName := virtualPath
			if long {
				_ = json.NewEncoder(w).Encode(execResp{Output: formatLong(info, colorizeName(info, displayName), humanReadable)})
			} else {
				_ = json.NewEncoder(w).Encode(execResp{Output: colorizeName(info, displayName)})
			}
			return
		}
		// It is a directory, show its contents
		children, err := visibleChildren(ctx, budget, root, name)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, errWorkLimit) {
			_ = json.NewEncoder(w).Encode(execResp{Output: "ls: error"})
			return
		}
		var output boundedText
		if cwd != "/" {
			parent := colorBlue + colorBold + "../" + colorReset
			if long {
				parent = "drwxr-xr-x          - " + parent
			}
			_ = output.line(parent)
		}
		for _, entry := range children {
			if ctx.Err() != nil {
				return
			}
			base := path.Base(entry.name)
			if !showHidden && strings.HasPrefix(base, ".") {
				continue
			}
			display := colorizeName(entry.info, path.Join(virtualPath, base))
			if long {
				display = formatLong(entry.info, display, humanReadable)
			}
			if appendErr := output.line(display); appendErr != nil {
				err = appendErr
				break
			}
		}
		_ = json.NewEncoder(w).Encode(output.response("", err))
		return

	case "cd":
		target := "/"
		if len(argv) == 1 {
			target = argv[0]
			if target == "" {
				target = "/"
			}
		}
		sess = s.acquireSession(w, r)
		if sess == nil {
			return
		}
		newV, err := sess.changeDirectory(root, target)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cd: " + err.Error()})
			return
		}
		name := rootedName(newV)

		// Check if directory contains index.html
		indexPath := path.Join(name, "index.html")
		if info, err := visibleInfo(root, indexPath); err == nil && info.Mode().IsRegular() {
			// Directory has index.html, redirect to it
			_ = json.NewEncoder(w).Encode(execResp{Redirect: newV})
			return
		}

		readme, docType, readmeTruncated := readDocFile(ctx, root, name)
		if ctx.Err() != nil {
			return
		}
		// Include the new CWD in the response so client can update URL
		_ = json.NewEncoder(w).Encode(execResp{
			CWD: newV, Readme: &readme, DocType: docType, ReadmeTruncated: readmeTruncated,
		})
		return

	case "cat":
		if len(argv) < 1 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: missing operand"})
			return
		}
		cwd, ok := cwdFor(argv[0])
		if !ok {
			return
		}
		vp := joinVirtual(cwd, argv[0])
		filePath := rootedName(vp)
		info, err := visibleInfo(root, filePath)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: no such file or directory"})
			return
		}
		if info.IsDir() {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: is a directory"})
			return
		}

		// Check if file type is supported by cat
		category := getFileCategory(argv[0])

		// Display images inline
		if category == FileCategoryImage {
			image := execImage{URL: urlEscapeVirtual(vp), Name: filepath.Base(argv[0]), Size: formatHumanSize(info.Size())}
			// Keep the HTML response for development templates using the previous protocol.
			escapedURL := template.HTMLEscapeString(image.URL)
			escapedName := template.HTMLEscapeString(image.Name)
			imgHTML := fmt.Sprintf(
				`<div style="margin: 0.5em 0;">`+
					`<a href="%s" target="_blank" style="display: inline-block; text-decoration: none;">`+
					`<img src="%s" style="max-width: 100%%; height: auto; display: block; border-radius: 4px; box-shadow: 0 2px 8px rgba(0,0,0,0.3);" alt="%s">`+
					`</a>`+
					`<div style="color: #a6d189; font-size: 0.85em; margin-top: 0.25em;">📷 %s (%s)</div>`+
					`</div>`,
				escapedURL, escapedURL, escapedName, escapedName, image.Size)
			_ = json.NewEncoder(w).Encode(execResp{HTML: imgHTML, Image: &image})
			return
		}

		// Only text files and unknown files (to be checked by content) can be displayed
		if category != FileCategoryText && category != FileCategoryUnknown {
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("cat: cannot display %s files (use 'get' to download)", category)})
			return
		}

		f, info, err := openVisibleFile(root, filePath)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: cannot open file"})
			return
		}
		defer func() { _ = f.Close() }()
		limit := min(s.catMax, int64(maxCommandText))
		if info.Size() > limit {
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("cat: file too large (%d > limit %d)", info.Size(), limit)})
			return
		}
		var buf bytes.Buffer
		_, err = io.CopyN(&buf, contextReader{ctx, f}, limit)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, io.EOF) {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: read error"})
			return
		}
		sample := buf.Bytes()
		if !looksText(sample) {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: binary file (use 'get' to download)"})
			return
		}
		_ = json.NewEncoder(w).Encode(execResp{Output: string(sample)})
		return

	case "get", "rget", "wget", "download":
		if len(argv) < 1 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "download: missing operand"})
			return
		}

		pattern := argv[0]
		root, err := os.OpenRoot(s.rootAbs)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "download: cannot open served directory"})
			return
		}
		defer func() { _ = root.Close() }()
		cwd, ok := cwdFor(pattern)
		if !ok {
			return
		}

		// Get IP address for logging
		ip := getClientIP(r)

		// Check if pattern contains wildcards or is a directory
		if strings.ContainsAny(pattern, "*?[") || pattern == "." {
			// Handle pattern-based download (multiple files)
			release := s.reserveWork(w, r)
			if release == nil {
				return
			}
			defer release()
			files, err := collectFilesForDownload(ctx, budget, root, cwd, pattern)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("download: %v", err), Truncated: errors.Is(err, errWorkLimit)})
				return
			}
			if len(files) == 0 {
				_ = json.NewEncoder(w).Encode(execResp{Output: "download: no matching files found"})
				return
			}
			if len(files) == 1 {
				// Single file, download directly
				s.logCommand("get", files[0].virtualPath, ip)
				downloadURL := "/api/download?path=" + url.QueryEscape(files[0].virtualPath)
				_ = json.NewEncoder(w).Encode(execResp{Output: "", Download: downloadURL})
				return
			}
			// Multiple files, create zip
			s.logCommand("get", "(pattern match)", ip)
			downloadURL := "/api/download?pattern=" + url.QueryEscape(pattern) + "&cwd=" + url.QueryEscape(cwd)
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("Downloading %d files as archive.zip", len(files)), Download: downloadURL})
			return
		}

		// Check if it's a directory
		vp := joinVirtual(cwd, pattern)
		name := rootedName(vp)
		info, err := visibleInfo(root, name)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "download: no such file"})
			return
		}

		if info.IsDir() {
			// Download directory as zip
			release := s.reserveWork(w, r)
			if release == nil {
				return
			}
			defer release()
			files, err := collectFilesFromDirectory(ctx, budget, root, name)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("download: %v", err), Truncated: errors.Is(err, errWorkLimit)})
				return
			}
			if len(files) == 0 {
				_ = json.NewEncoder(w).Encode(execResp{Output: "download: directory is empty"})
				return
			}
			dirName := downloadDirectoryName(root, name)
			s.logCommand("get", vp+" (dir)", ip)
			downloadURL := "/api/download?dir=" + url.QueryEscape(vp)
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("Downloading directory '%s' with %d files as %s.zip", dirName, len(files), dirName), Download: downloadURL})
			return
		}

		// Single file download
		if !info.Mode().IsRegular() {
			_ = json.NewEncoder(w).Encode(execResp{Output: "download: no such file"})
			return
		}
		s.logCommand("get", vp, ip)
		downloadURL := "/api/download?path=" + url.QueryEscape(vp)
		_ = json.NewEncoder(w).Encode(execResp{Output: "", Download: downloadURL})
		return

	case "tree":
		// Parse options
		showHidden := false
		maxDepth := -1 // unlimited by default
		target := ""

		for _, arg := range argv {
			if strings.HasPrefix(arg, "-") {
				if strings.Contains(arg, "a") {
					showHidden = true
				}
				if strings.HasPrefix(arg, "-L") && len(arg) > 2 {
					// Simple depth parsing for -L<number>
					depthStr := arg[2:]
					if d, err := fmt.Sscanf(depthStr, "%d", &maxDepth); d != 1 || err != nil {
						maxDepth = -1
					}
				}
			} else {
				// Directory argument
				target = arg
			}
		}

		cwd, ok := cwdFor(target)
		if !ok {
			return
		}
		name := rootedName(joinVirtual(cwd, target))
		info, err := visibleInfo(root, name)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "tree: no such file or directory"})
			return
		}

		if !info.IsDir() {
			_ = json.NewEncoder(w).Encode(execResp{Output: "tree: not a directory"})
			return
		}

		var result boundedText
		dirCount, fileCount, err := buildTree(ctx, budget, root, &result, name, showHidden, maxDepth)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, errWorkLimit) {
			_ = json.NewEncoder(w).Encode(execResp{Output: "tree: cannot read directory"})
			return
		}
		if err == nil {
			err = result.line(fmt.Sprintf("\n%d directories, %d files", dirCount, fileCount))
		}
		_ = json.NewEncoder(w).Encode(result.response("", err))
		return

	case "find":
		// Parse options
		searchPath := ""
		namePattern := "*"
		typeFilter := "" // "f" for files, "d" for directories, "" for both

		// Parse arguments
		for i := 0; i < len(argv); i++ {
			arg := argv[i]
			if arg == "-name" && i+1 < len(argv) {
				namePattern = argv[i+1]
				i++ // skip next argument
			} else if arg == "-type" && i+1 < len(argv) {
				typeFilter = argv[i+1]
				i++ // skip next argument
			} else if !strings.HasPrefix(arg, "-") {
				// Path argument
				searchPath = arg
			}
		}

		// Validate type filter
		if typeFilter != "" && typeFilter != "f" && typeFilter != "d" {
			_ = json.NewEncoder(w).Encode(execResp{Output: "find: invalid type filter (use 'f' for files or 'd' for directories)"})
			return
		}

		cwd, ok := cwdFor(searchPath)
		if !ok {
			return
		}
		name := rootedName(joinVirtual(cwd, searchPath))
		info, err := visibleInfo(root, name)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "find: no such file or directory"})
			return
		}

		if !info.IsDir() {
			_ = json.NewEncoder(w).Encode(execResp{Output: "find: not a directory"})
			return
		}

		var output boundedText
		err = findFiles(ctx, budget, root, name, namePattern, typeFilter, &output)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, errWorkLimit) {
			_ = json.NewEncoder(w).Encode(execResp{Output: "find: cannot read directory"})
			return
		}
		_ = json.NewEncoder(w).Encode(output.response("find: no matches found", err))
		return

	case "url", "share":
		if len(argv) < 1 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "url: missing file operand"})
			return
		}
		cwd, ok := cwdFor(argv[0])
		if !ok {
			return
		}

		vp := joinVirtual(cwd, argv[0])
		info, err := visibleInfo(root, rootedName(vp))
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "url: no such file or directory"})
			return
		}

		if info.IsDir() {
			_ = json.NewEncoder(w).Encode(execResp{Output: "url: cannot share directories (use 'get' to download as zip)"})
			return
		}

		// Build the full URL for the file
		var fileURL string
		escapedPath := urlEscapeVirtual(vp)
		if s.baseURL != "" {
			// Use configured base URL (clean, no /api/static)
			// Required when behind reverse proxy with different public URL
			fileURL = s.baseURL + escapedPath
		} else {
			// Auto-detect from request (works for most cases)
			// Uses Host header and X-Forwarded-Proto for protocol detection
			host := r.Host
			if host == "" {
				host = "localhost:8080"
			}
			protocol := "http"
			if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
				protocol = "https"
			}
			fileURL = fmt.Sprintf("%s://%s%s", protocol, host, escapedPath)
		}

		// Log the share command
		s.logCommand(cmd, vp, getClientIP(r))

		// Return the URL with clipboard instruction
		_ = json.NewEncoder(w).Encode(execResp{
			Output:    fmt.Sprintf("Shareable URL: %s\n%sURL copied to clipboard!%s", fileURL, colorGreen, colorReset),
			Clipboard: fileURL,
		})
		return

	case "grep":
		if len(argv) < 1 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "grep: missing pattern"})
			return
		}

		// Parse options
		var recursive bool
		var ignoreCase bool
		var showLineNumbers bool
		var pattern string
		var files []string

		// Parse arguments
		i := 0
		for i < len(argv) {
			arg := argv[i]
			if strings.HasPrefix(arg, "-") {
				if strings.Contains(arg, "r") {
					recursive = true
				}
				if strings.Contains(arg, "i") {
					ignoreCase = true
				}
				if strings.Contains(arg, "n") {
					showLineNumbers = true
				}
			} else {
				if pattern == "" {
					pattern = arg
				} else {
					files = append(files, arg)
				}
			}
			i++
		}

		if pattern == "" {
			_ = json.NewEncoder(w).Encode(execResp{Output: "grep: missing pattern"})
			return
		}

		// If no files specified and recursive, search current directory
		if len(files) == 0 {
			if recursive {
				files = []string{"."}
			} else {
				_ = json.NewEncoder(w).Encode(execResp{Output: "grep: no files specified"})
				return
			}
		}

		search := newGrepSearch(pattern, ignoreCase, showLineNumbers)
		var resultErr error
		for _, file := range files {
			if ctx.Err() != nil {
				return
			}
			if budget.entries == 0 {
				resultErr = errWorkLimit
				break
			}
			budget.entries--
			cwd, ok := cwdFor(file)
			if !ok {
				return
			}
			vp := joinVirtual(cwd, file)
			name := rootedName(vp)
			info, err := visibleInfo(root, name)
			switch {
			case err != nil:
				resultErr = search.output.line(fmt.Sprintf("grep: %s: no such file or directory", file))
			case info.IsDir() && recursive:
				resultErr = search.directory(ctx, budget, root, name)
				if resultErr != nil && !stopWork(resultErr) {
					resultErr = search.output.line(fmt.Sprintf("grep: %s: cannot read directory", file))
				}
			case info.IsDir():
				resultErr = search.output.line(fmt.Sprintf("grep: %s: is a directory", file))
			default:
				resultErr = search.file(ctx, budget, root, name, len(files) > 1)
				if resultErr != nil && !stopWork(resultErr) {
					resultErr = search.output.line(fmt.Sprintf("grep: %s: %v", file, resultErr))
				}
			}
			if resultErr != nil {
				break
			}
		}
		if ctx.Err() != nil {
			return
		}
		_ = json.NewEncoder(w).Encode(search.output.response("grep: no matches found", resultErr))
		return

	case "sum", "checksum":
		if len(argv) < 1 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "sum: missing file operand"})
			return
		}
		cwd, ok := cwdFor(argv[0])
		if !ok {
			return
		}

		vp := joinVirtual(cwd, argv[0])
		f, _, err := openVisibleFile(root, rootedName(vp))
		if errors.Is(err, errDownloadDirectory) {
			_ = json.NewEncoder(w).Encode(execResp{Output: "sum: is a directory"})
			return
		}
		if err != nil {
			message := "sum: cannot open file"
			if errors.Is(err, fs.ErrNotExist) {
				message = "sum: no such file or directory"
			}
			_ = json.NewEncoder(w).Encode(execResp{Output: message})
			return
		}
		defer func() { _ = f.Close() }()

		md5Hash := md5.New()
		sha256Hash := sha256.New()

		// Use MultiWriter to compute both hashes in one pass
		writer := io.MultiWriter(md5Hash, sha256Hash)
		_, err = io.Copy(writer, contextReader{ctx, f})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "sum: error reading file"})
			return
		}

		md5Sum := hex.EncodeToString(md5Hash.Sum(nil))
		sha256Sum := hex.EncodeToString(sha256Hash.Sum(nil))

		// Log the checksum command
		s.logCommand(cmd, vp, getClientIP(r))

		output := fmt.Sprintf("MD5:    %s\nSHA256: %s", md5Sum, sha256Sum)
		_ = json.NewEncoder(w).Encode(execResp{Output: output})
		return
	}

	_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("sh: %s: command not found", cmd)})
}

// findFiles recursively searches for files and directories matching the given pattern.
func findFiles(ctx context.Context, budget *workBudget, root *os.Root, dir, pattern, typeFilter string, output *boundedText) error {
	showHidden := strings.HasPrefix(pattern, ".")
	return walkVisibleDir(ctx, budget, root, dir, func(entry visibleEntry) (bool, error) {
		name := path.Base(entry.name)
		if !showHidden && strings.HasPrefix(name, ".") {
			return false, nil
		}
		matched, err := filepath.Match(pattern, name)
		if err != nil {
			return false, nil
		}
		if matched && (typeFilter == "" || (typeFilter == "d") == entry.descend) {
			return true, output.line(colorizeName(entry.info, cleanVirtual(entry.name)))
		}
		return true, nil
	})
}

type fileInfo struct {
	virtualPath  string
	relativePath string
}

// collectFilesForDownload collects files matching a name or pattern for download.
func collectFilesForDownload(ctx context.Context, budget *workBudget, root *os.Root, cwd, pattern string) ([]fileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := rootedName(joinVirtual(cwd, pattern))
	if !strings.ContainsAny(pattern, "*?[") {
		info, err := visibleInfo(root, name)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return collectFilesFromDirectory(ctx, budget, root, name)
		}
		return []fileInfo{{virtualPath: "/" + name, relativePath: path.Base(name)}}, nil
	}

	dir := rootedName(joinVirtual(cwd, path.Dir(pattern)))
	entries, err := readVisibleDir(ctx, budget, root, dir)
	if err != nil {
		return nil, err
	}

	var matches []fileInfo
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		matched, err := filepath.Match(path.Base(pattern), entry.Name())
		if err != nil || !matched || entry.IsDir() {
			continue
		}
		name, err := budget.childName(dir, entry.Name())
		if err != nil {
			return nil, err
		}
		info, err := visibleInfo(root, name)
		if err == nil && info.Mode().IsRegular() {
			matches = append(matches, fileInfo{virtualPath: "/" + name, relativePath: entry.Name()})
		}
	}
	return matches, nil
}

func downloadDirectoryName(root *os.Root, dir string) string {
	if dir == "." {
		name := filepath.Base(root.Name())
		if !filepath.IsLocal(name) {
			return "files"
		}
		return name
	}
	return path.Base(dir)
}

// collectFilesFromDirectory walks the served filesystem and keeps ZIP entry names relative.
func collectFilesFromDirectory(ctx context.Context, budget *workBudget, root *os.Root, dir string) ([]fileInfo, error) {
	var matches []fileInfo
	dirName := downloadDirectoryName(root, dir)
	err := walkVisibleDir(ctx, budget, root, dir, func(entry visibleEntry) (bool, error) {
		if !entry.info.Mode().IsRegular() || strings.HasPrefix(path.Base(entry.name), ".") {
			return true, nil
		}
		relativePath := entry.name
		if dir != "." {
			relativePath = strings.TrimPrefix(entry.name, dir+"/")
		}
		matches = append(matches, fileInfo{
			virtualPath:  "/" + entry.name,
			relativePath: path.Join(dirName, relativePath),
		})
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func sendZipArchive(ctx context.Context, w http.ResponseWriter, root *os.Root, files []fileInfo, filename string) error {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	zipWriter := zip.NewWriter(contextWriter{ctx, w})
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, info, err := openVisibleFile(root, rootedName(file.virtualPath))
		if err != nil {
			return fmt.Errorf("open ZIP entry %q: %w", file.relativePath, err)
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			_ = source.Close()
			return fmt.Errorf("prepare ZIP entry %q: %w", file.relativePath, err)
		}
		header.Name = file.relativePath
		header.Method = zip.Deflate

		entry, err := zipWriter.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(entry, contextReader{ctx, source})
		}
		if err := errors.Join(err, source.Close()); err != nil {
			return fmt.Errorf("write ZIP entry %q: %w", file.relativePath, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := zipWriter.Close(); err != nil {
		return fmt.Errorf("finalize ZIP: %w", err)
	}
	return nil
}

func serveZipArchive(ctx context.Context, w http.ResponseWriter, root *os.Root, files []fileInfo, filename string) {
	if err := sendZipArchive(ctx, w, root, files, filename); err != nil {
		if response, ok := w.(*responseLogger); ok {
			// Let the access log record the failure before aborting the response.
			response.archiveErr = err
			return
		}
		panic(http.ErrAbortHandler)
	}
}

// buildTree renders the visible children of a directory and their descendants.
func buildTree(ctx context.Context, budget *workBudget, root *os.Root, result *boundedText, dir string, showHidden bool, maxDepth int) (int, int, error) {
	var render func(string, string, int) (int, int, error)
	render = func(dir, prefix string, depth int) (int, int, error) {
		if depth == maxDepth {
			return 0, 0, nil
		}
		if depth == maxWalkDepth {
			return 0, 0, errWorkLimit
		}
		entries, readErr := visibleChildren(ctx, budget, root, dir)
		if !showHidden {
			shown := entries[:0]
			for _, entry := range entries {
				if !strings.HasPrefix(path.Base(entry.name), ".") {
					shown = append(shown, entry)
				}
			}
			entries = shown
		}
		// Keep each group alphabetical and show directories before files.
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].descend && !entries[j].descend
		})

		var dirCount, fileCount int
		for i, entry := range entries {
			if err := ctx.Err(); err != nil {
				return dirCount, fileCount, err
			}
			connector := "├── "
			childPrefix := prefix + "│   "
			if i == len(entries)-1 {
				connector = "└── "
				childPrefix = prefix + "    "
			}
			if err := result.line(prefix + connector + colorizeName(entry.info, path.Base(entry.name))); err != nil {
				return dirCount, fileCount, err
			}
			if !entry.descend {
				fileCount++
				continue
			}
			dirCount++
			if readErr == nil {
				dirs, files, err := render(entry.name, childPrefix, depth+1)
				if stopWork(err) {
					return dirCount, fileCount, err
				}
				dirCount += dirs
				fileCount += files
			}
		}
		return dirCount, fileCount, readErr
	}
	return render(dir, "", 0)
}

func urlEscapeVirtual(v string) string {
	// Keep it URL-safe while preserving slashes in the virtual path.
	parts := strings.Split(strings.TrimPrefix(cleanVirtual(v), "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/" + strings.Join(parts, "/")
}

func (s *server) handleDownload(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	ctx := r.Context()
	pathArg, dir, pattern := query.Get("path"), query.Get("dir"), query.Get("pattern")
	if pathArg == "" && (dir != "" || pattern != "") {
		release := s.reserveWork(w, r)
		if release == nil {
			return
		}
		defer release()
	}
	root, err := os.OpenRoot(s.rootAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = root.Close() }()

	if pathArg != "" {
		serveDownloadFile(w, r, root, rootedName(pathArg), true)
		return
	}

	if dir != "" {
		name := rootedName(dir)
		info, err := visibleInfo(root, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !info.IsDir() {
			http.Error(w, "not a directory", http.StatusBadRequest)
			return
		}
		files, err := collectFilesFromDirectory(ctx, newWorkBudget(), root, name)
		if ctx.Err() != nil || writeArchivePreparationError(w, err) {
			return
		}
		serveZipArchive(ctx, w, root, files, downloadDirectoryName(root, name)+".zip")
		return
	}

	if pattern != "" {
		cwd := query.Get("cwd")
		if cwd == "" && !path.IsAbs(pattern) {
			sess := s.acquireSession(w, r)
			if sess == nil {
				return
			}
			defer s.releaseSession(sess)
			cwd = sess.workingDirectory()
		}
		files, err := collectFilesForDownload(ctx, newWorkBudget(), root, cwd, pattern)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, fs.ErrNotExist) || (err == nil && len(files) == 0) {
			http.Error(w, "no matching files found", http.StatusNotFound)
			return
		}
		if writeArchivePreparationError(w, err) {
			return
		}
		serveZipArchive(ctx, w, root, files, "archive.zip")
		return
	}

	http.Error(w, "missing download parameters", http.StatusBadRequest)
}

func writeArchivePreparationError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, errWorkLimit) {
		http.Error(w, errWorkLimit.Error(), http.StatusUnprocessableEntity)
	} else if err != nil {
		http.Error(w, "failed to collect files", http.StatusInternalServerError)
	}
	return err != nil
}

func (s *server) handleComplete(w http.ResponseWriter, r *http.Request) {
	var req completeReq
	if !s.readCommandRequest(w, r, &req) {
		return
	}
	release := s.reserveWork(w, r)
	if release == nil {
		return
	}
	defer release()
	ctx := r.Context()

	arg := req.Path
	// Split into dir part + base (prefix)
	slash := strings.LastIndex(arg, "/")
	dirPart := ""
	basePart := arg
	if slash >= 0 {
		dirPart = arg[:slash+1] // keep trailing slash
		basePart = arg[slash+1:]
	}

	// Resolve base virtual directory
	var baseV string
	if strings.HasPrefix(arg, "/") {
		baseV = cleanVirtual(dirPart)
	} else {
		sess := s.acquireSession(w, r)
		if sess == nil {
			return
		}
		defer s.releaseSession(sess)
		baseV = joinVirtual(sess.workingDirectory(), dirPart)
	}
	root, err := os.OpenRoot(s.rootAbs)
	if err != nil {
		_ = json.NewEncoder(w).Encode(completeResp{Items: nil})
		return
	}

	defer func() { _ = root.Close() }()
	base := rootedName(baseV)
	budget := newWorkBudget()
	ents, err := readVisibleDir(ctx, budget, root, base)
	if ctx.Err() != nil {
		return
	}
	truncated := errors.Is(err, errWorkLimit)
	if err != nil && !truncated {
		_ = json.NewEncoder(w).Encode(completeResp{Items: nil})
		return
	}

	showHidden := strings.HasPrefix(basePart, ".")
	const maxItems = 200
	items := make([]completeItem, 0, 16)

	for _, e := range ents {
		if ctx.Err() != nil {
			return
		}
		name := e.Name()
		if !strings.HasPrefix(name, basePart) {
			continue
		}
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}

		child, err := budget.childName(base, name)
		if err != nil {
			truncated = true
			break
		}
		info, err := visibleInfo(root, child)
		if err != nil {
			continue
		}
		isDir := info.IsDir()
		if req.DirsOnly && !isDir {
			continue
		}

		if !isDir && (req.TextOnly || req.MaxSize > 0) {
			category := getFileCategory(name)
			// Images remain eligible for inline cat regardless of size.
			if category != FileCategoryImage && (req.MaxSize > 0 && info.Size() > req.MaxSize ||
				req.TextOnly && category != FileCategoryText) {
				continue
			}
		}

		if len(items) == maxItems {
			truncated = true
			break
		}
		items = append(items, completeItem{Name: name, Dir: isDir})
	}

	// Sort: directories first, then files; alphabetical within each
	sort.Slice(items, func(i, j int) bool {
		if items[i].Dir != items[j].Dir {
			return items[i].Dir && !items[j].Dir
		}
		return items[i].Name < items[j].Name
	})

	_ = json.NewEncoder(w).Encode(completeResp{Items: items, Truncated: truncated})
}

// ===== Main =====

func main() {
	// Environment variable helper functions
	getEnvOrDefault := func(key, defaultValue string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return defaultValue
	}
	getEnvOrDefaultInt64 := func(key string, defaultValue int64) int64 {
		if v := os.Getenv(key); v != "" {
			if i, err := fmt.Sscanf(v, "%d", &defaultValue); i == 1 && err == nil {
				return defaultValue
			}
		}
		return defaultValue
	}
	getEnvOrDefaultInt := func(key string, defaultValue int) int {
		if v := os.Getenv(key); v != "" {
			var result int
			if i, err := fmt.Sscanf(v, "%d", &result); i == 1 && err == nil {
				return result
			}
		}
		return defaultValue
	}

	// Define flags with environment variable support (LSGET_* prefix)
	secureCookieDefault, _ := strconv.ParseBool(os.Getenv("LSGET_SECURE_SESSION_COOKIE"))
	var (
		printVersion    = flag.Bool("version", false, "Print the version of this software and exits")
		addr            = flag.String("addr", getEnvOrDefault("LSGET_ADDR", "localhost:8080"), "address to listen on (env: LSGET_ADDR)")
		dir             = flag.String("dir", getEnvOrDefault("LSGET_DIR", "."), "directory to expose as root (env: LSGET_DIR)")
		catMax          = flag.Int64("catmax", getEnvOrDefaultInt64("LSGET_CATMAX", 4*1024), "max bytes printable via `cat` and used by completion (env: LSGET_CATMAX)")
		devTemplate     = flag.String("dev-template", getEnvOrDefault("LSGET_DEV_TEMPLATE", ""), "use a local application template for development (env: LSGET_DEV_TEMPLATE)")
		shellCSP        = flag.String("shell-csp", getEnvOrDefault("LSGET_SHELL_CSP", "enforce"), "application CSP mode: enforce or report-only (env: LSGET_SHELL_CSP)")
		secureCookie    = flag.Bool("secure-session-cookie", secureCookieDefault, "require HTTPS for session cookies behind an HTTPS-only proxy (env: LSGET_SECURE_SESSION_COOKIE)")
		pidFileFlag     = flag.String("pid", getEnvOrDefault("LSGET_PID", ""), "path to PID file (env: LSGET_PID)")
		logfileFlag     = flag.String("logfile", getEnvOrDefault("LSGET_LOGFILE", ""), "path to log file for statistics (env: LSGET_LOGFILE)")
		baseURL         = flag.String("baseurl", getEnvOrDefault("LSGET_BASEURL", ""), "base URL for the site - full URL without trailing slash (e.g., https://files.example.com) (env: LSGET_BASEURL)")
		sitemapInterval = flag.Int("sitemap", getEnvOrDefaultInt("LSGET_SITEMAP", 0), "refresh the in-memory sitemap every N minutes (0 = on demand only) (env: LSGET_SITEMAP)")
	)
	flag.Parse()

	if *printVersion {
		fmt.Printf("lsget %s\n", version)
		fmt.Println("A lightweight CDN UI: Self-hosted file server with a full‑screen, neon‑themed TUI in your browser.")
		fmt.Println()
		fmt.Println("Copyright © 2025 Dyne.org foundation, Amsterdam")
		fmt.Println("Licensed under GNU Affero General Public License v3.0")
		fmt.Println()
		fmt.Println("Repository: https://github.com/dyne/lsget")
		fmt.Println("Website:    https://dyne.org")
		exitFunc(0)
	}

	rootAbs, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve dir: %v\n", err)
		exitFunc(1)
	}
	info, err := os.Stat(rootAbs)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "dir is not a directory: %s\n", rootAbs)
		exitFunc(1)
	}

	if *shellCSP != "enforce" && *shellCSP != "report-only" {
		fmt.Fprintln(os.Stderr, "shell-csp must be enforce or report-only")
		exitFunc(1)
		return
	}

	// Set global log file path
	if *logfileFlag != "" {
		logFile = *logfileFlag
	}

	s := newServer(rootAbs, *catMax, *logfileFlag, *baseURL)
	s.devTemplate = *devTemplate
	s.shellCSPReportOnly = *shellCSP == "report-only"
	s.secureSessionCookie = *secureCookie

	// Create PID file if specified
	if *pidFileFlag != "" {
		pid := os.Getpid()
		pidStr := fmt.Sprintf("%d", pid)
		if err := os.WriteFile(*pidFileFlag, []byte(pidStr), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create PID file: %v\n", err)
			exitFunc(1)
		}
		// Store PID file path for cleanup
		pidFile = *pidFileFlag
	}

	background, cancelBackground := context.WithCancel(context.Background())
	defer cancelBackground()
	go s.runSessionCleanup(background)
	s.startSitemapGenerator(background, time.Duration(*sitemapInterval)*time.Minute)

	fmt.Printf("Serving %s on http://%s  (cat max = %d bytes)\n", rootAbs, *addr, *catMax)
	if s.logfile != "" {
		fmt.Printf("Logging to: %s\n", s.logfile)
	} else {
		fmt.Println("Logging disabled (use -logfile or LSGET_LOGFILE to enable)")
	}
	srv := s.httpServer(*addr)

	// Handle graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		for sig := range c {
			fmt.Printf("\nReceived signal %s, shutting down server...\n", sig)
			// Remove PID file if it exists
			if pidFile != "" {
				_ = os.Remove(pidFile)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := srv.Shutdown(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "server shutdown error: %v\n", err)
			}
			cancel()
			exitFunc(0)
		}
	}()

	if err := listenAndServe(srv); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		// Remove PID file on error
		if pidFile != "" {
			_ = os.Remove(pidFile)
		}
		exitFunc(1)
	}
}

// responseLogger wraps a ResponseWriter to capture status code and response size
type responseLogger struct {
	http.ResponseWriter
	statusCode int
	size       int
	archiveErr error
}

func (rl *responseLogger) WriteHeader(code int) {
	rl.statusCode = code
	rl.ResponseWriter.WriteHeader(code)
}

func (rl *responseLogger) Write(b []byte) (int, error) {
	if rl.statusCode == 0 {
		rl.statusCode = http.StatusOK
	}
	size, err := rl.ResponseWriter.Write(b)
	rl.size += size
	return size, err
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wrap the ResponseWriter to capture status code and size
		rl := &responseLogger{ResponseWriter: w}

		next.ServeHTTP(rl, r)

		// Get remote IP address
		ip := getClientIP(r)

		// Get user identifier (using "-" as we don't have user auth)
		user := "-"

		// Get timestamp in CLF format
		timestamp := time.Now().Format("[02/Jan/2006:15:04:05 -0700]")

		// Get request line
		requestLine := fmt.Sprintf("%s %s %s", r.Method, r.URL.RequestURI(), r.Proto)

		// Get status code and response size
		statusCode := rl.statusCode
		responseSize := rl.size

		// Get referer and user agent
		referer := r.Referer()
		if referer == "" {
			referer = "-"
		}
		userAgent := r.UserAgent()
		if userAgent == "" {
			userAgent = "-"
		}

		// Combined Log Format:
		// "%h %l %u %t \"%r\" %>s %b \"%{Referer}i\" \"%{User-agent}i"
		sizeStr := "-"
		if responseSize > 0 {
			sizeStr = fmt.Sprintf("%d", responseSize)
		}

		logLine := fmt.Sprintf("%s %s %s %s \"%s\" %d %s \"%s\" \"%s\"",
			ip, "-", user, timestamp, requestLine, statusCode, sizeStr, referer, userAgent)
		if rl.archiveErr != nil {
			logLine += fmt.Sprintf(" archive=incomplete error=%q", rl.archiveErr.Error())
		}
		logLine += "\n"

		fmt.Print(logLine)

		// Write to log file if specified
		if logFile != "" {
			logMutex.Lock()
			// Ensure log directory exists
			if logDir := filepath.Dir(logFile); logDir != "" {
				_ = os.MkdirAll(logDir, 0755)
			}
			f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				_, _ = f.WriteString(logLine)
				_ = f.Close()
			}
			logMutex.Unlock()
		}
		if rl.archiveErr != nil {
			// Prevent net/http from ending an incomplete ZIP as a successful response.
			panic(http.ErrAbortHandler)
		}
	})
}
