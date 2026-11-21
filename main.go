package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

var version = "dev"

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
	newV, err := resolveDirectory(root, s.cwd, target)
	if err == nil {
		s.cwd = newV
	}
	return newV, err
}

func resolveDirectory(root *os.Root, cwd, target string) (string, error) {
	newV := joinVirtual(cwd, target)
	info, err := visibleInfo(root, rootedName(newV))
	if err != nil {
		return "", errors.New("no such file or directory")
	}
	if !info.IsDir() {
		return "", errors.New("not a directory")
	}
	return newV, nil
}

type server struct {
	rootAbs        string // absolute filesystem root we expose
	catMax         int64  // max bytes allowed for `cat`
	logfile        string // path to log file for statistics
	baseURL        string // optional: public base URL (e.g., https://files.example.com) - auto-detects from request if empty
	trustedProxies []netip.Prefix
	logMu          sync.Mutex
	lastLogWarning time.Time

	sessionsMu          sync.Mutex
	sessions            map[string]*session
	sessionNow          func() time.Time
	sessionLimit        int
	secureSessionCookie bool
	workSlots           chan struct{}

	devTemplate        string
	shellCSPReportOnly bool
	siteMetadata       siteMetadata

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

// startSitemapGenerator reports when the initial refresh and the worker finish.
func (s *server) startSitemapGenerator(ctx context.Context, interval time.Duration, stdout, stderr io.Writer) (ready, done <-chan struct{}) {
	initial := make(chan struct{})
	if interval <= 0 || s.baseURL == "" {
		close(initial)
		return initial, initial
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if _, err := s.getSitemap(ctx, true); err == nil {
			_, _ = fmt.Fprintln(stdout, "Generated sitemap.xml in memory")
		} else if ctx.Err() == nil {
			_, _ = fmt.Fprintf(stderr, "failed to generate initial sitemap: %v\n", err)
		}
		close(initial)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.getSitemap(ctx, true); err != nil && ctx.Err() == nil {
					_, _ = fmt.Fprintf(stderr, "failed to regenerate sitemap: %v\n", err)
				}
			}
		}
	}()
	return initial, finished
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
	requestedCWD := requestCWD(r)
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
	var sess *session
	cwd := requestedCWD
	if cwd == "" {
		sess = s.acquireSession(w, r)
		if sess == nil {
			return
		}
		defer s.releaseSession(sess)
		cwd = sess.workingDirectory()
	}
	var readme, docType string
	var readmeTruncated bool
	if root, err := os.OpenRoot(s.rootAbs); err == nil {
		defer func() { _ = root.Close() }()
		if requestedCWD != "" {
			if _, err := resolveDirectory(root, "/", cwd); err != nil {
				http.NotFound(w, r)
				return
			}
		} else if initialPath != "" {
			if newV, err := sess.changeDirectory(root, cleanVirtual(initialPath)); err == nil {
				cwd = newV
			}
		}
		readme, docType, readmeTruncated = readDocFile(r.Context(), root, rootedName(cwd))
	} else if requestedCWD != "" {
		http.Error(w, "cannot open served directory", http.StatusServiceUnavailable)
		return
	}
	if r.Context().Err() != nil {
		return
	}

	_ = json.NewEncoder(w).Encode(configResp{
		CatMax: s.catMax, Readme: &readme, DocType: docType, ReadmeTruncated: readmeTruncated, CWD: cwd,
	})
}

func urlEscapeVirtual(v string) string {
	// Keep it URL-safe while preserving slashes in the virtual path.
	parts := strings.Split(strings.TrimPrefix(cleanVirtual(v), "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/" + strings.Join(parts, "/")
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
	requestedCWD := requestCWD(r)
	if requestedCWD != "" && req.TextOnly && req.MaxSize == 0 {
		req.MaxSize = s.catMax
	}
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
	} else if requestedCWD != "" {
		baseV = joinVirtual(requestedCWD, dirPart)
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
