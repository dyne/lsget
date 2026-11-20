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
	"strings"
	"sync"
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

const helpTpl = `Welcome to <span class="ps1">lsget</span> <span style="color: #666;">v{{.Version}}</span>!
<span style="color: #888;">Type one of the commands below to get started.</span>
<br/>

<span style="color: #aaa;">Available commands:</span>
• <strong>help</strong> - <span style="color: #bbb;">print this message again</span>
• <strong>pwd</strong> - <span style="color: #bbb;">print working directory</span>
• <strong>ls</strong> <span style="color: #888;">[-l] [-h]</span>|<strong>dir</strong> <span style="color: #888;">[-l] [-h]</span> - <span style="color: #bbb;">list files (-h for human readable sizes)</span>
• <strong>cd</strong> <span style="color: #888;">DIR</span> - <span style="color: #bbb;">change directory</span>
• <strong>cat</strong> <span style="color: #888;">FILE</span> - <span style="color: #bbb;">view a text file</span>
• <strong>sum</strong>|<strong>checksum</strong> <span style="color: #888;">FILE</span> - <span style="color: #bbb;">print MD5 and SHA256 checksums</span>
• <strong>get</strong>|<strong>wget</strong>|<strong>download</strong> <span style="color: #888;">FILE</span> - <span style="color: #bbb;">download a file</span>
• <strong>url</strong>|<strong>share</strong> <span style="color: #888;">FILE</span> - <span style="color: #bbb;">get shareable URL (copies to clipboard)</span>
• <strong>tree</strong> <span style="color: #888;">[-L&lt;DEPTH&gt;] [-a]</span> - <span style="color: #bbb;">directory structure</span>
• <strong>find</strong> <span style="color: #888;">[PATH] [-name PATTERN] [-type f|d]</span> - <span style="color: #bbb;">search for files and directories</span>
• <strong>grep</strong> <span style="color: #888;">[-r] [-i] [-n] PATTERN [FILE...]</span> - <span style="color: #bbb;">search for text patterns in files</span>

<br/><br/>
<span style="color: #aaa;">Hint: to autocomplete filenames and dir use</span> <kbd class="ps1">Tab</kbd>
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

// readDocFile returns the raw contents of documentation files if present in dir.
// Supports README.md, .txt, .nfo, and .rst files in priority order.
func readDocFile(dir string) (string, string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}

	// Priority order for documentation files
	docFiles := []struct {
		pattern  string
		fileType string
	}{
		{"README.md", "markdown"},
		{"readme.md", "markdown"},
		{"README.txt", "text"},
		{"readme.txt", "text"},
		{"README.rst", "rst"},
		{"readme.rst", "rst"},
		{"README.nfo", "nfo"},
		{"readme.nfo", "nfo"},
	}

	// First, try exact matches in priority order
	for _, docFile := range docFiles {
		for _, e := range ents {
			if !e.Type().IsRegular() {
				continue
			}
			if strings.EqualFold(e.Name(), docFile.pattern) {
				b, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if err != nil {
					continue
				}
				return string(b), docFile.fileType
			}
		}
	}

	// Then try any file with supported extensions
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".md") {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			return string(b), "markdown"
		} else if strings.HasSuffix(name, ".txt") {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			return string(b), "text"
		} else if strings.HasSuffix(name, ".rst") {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			return string(b), "rst"
		} else if strings.HasSuffix(name, ".nfo") {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			return string(b), "nfo"
		}
	}

	return "", ""
}

// ===== Embed a fallback index.html (used only if the file isn't on disk) =====

//go:embed index.html
var embeddedIndex []byte

//go:embed assets/js/marked.min.js
var embeddedMarkedJS []byte

//go:embed assets/js/datastar.js
var embeddedDatastarJS []byte

// ===== Server state =====

type session struct {
	// virtual cwd like "/sub/dir"
	cwd string
}

type server struct {
	rootAbs  string // absolute filesystem root we expose
	catMax   int64  // max bytes allowed for `cat`
	sessions map[string]*session
	mu       sync.RWMutex
	logfile  string // path to log file for statistics
	baseURL  string // optional: public base URL (e.g., https://files.example.com) - auto-detects from request if empty
}

func newServer(rootAbs string, catMax int64, logfile, baseURL string) *server {
	return &server{
		rootAbs:  rootAbs,
		catMax:   catMax,
		sessions: make(map[string]*session),
		logfile:  logfile,
		baseURL:  baseURL,
	}
}

// ===== .lsgetignore support =====

// parseIgnoreFile reads and parses a .lsgetignore file, returning a slice of patterns
func parseIgnoreFile(files fs.FS, ignoreFilePath string) ([]string, error) {
	file, err := files.Open(ignoreFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No ignore file is fine
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

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

// shouldIgnore checks if a file/directory should be ignored based on .lsgetignore patterns
// It looks for .lsgetignore files in the current directory and all parent directories up to rootAbs
func (s *server) shouldIgnore(realPath, name string) bool {
	relativePath, err := filepath.Rel(s.rootAbs, realPath)
	if err != nil || !filepath.IsLocal(relativePath) {
		return false
	}
	return matchesIgnoreRules(os.DirFS(s.rootAbs), filepath.ToSlash(relativePath), name, false)
}

func matchesIgnoreRules(files fs.FS, filePath, name string, checkParents bool) bool {
	if filePath == "." {
		return false
	}
	for dir := path.Dir(filePath); ; dir = path.Dir(dir) {
		patterns, err := parseIgnoreFile(files, path.Join(dir, ".lsgetignore"))
		if err == nil {
			relativePath := filePath
			if dir != "." {
				relativePath = strings.TrimPrefix(filePath, dir+"/")
			}
			for _, pattern := range patterns {
				candidateName := name
				for candidate := relativePath; candidate != "."; candidate = path.Dir(candidate) {
					matchesName, _ := filepath.Match(pattern, candidateName)
					matchesPath, _ := filepath.Match(pattern, filepath.FromSlash(candidate))
					if matchesName || matchesPath {
						return true
					}
					if !checkParents {
						break
					}
					candidateName = path.Base(path.Dir(candidate))
				}
			}
		}
		if dir == "." {
			return false
		}
	}
}

// ===== Utilities =====

// sitemapEntry represents an entry in the sitemap
type sitemapEntry struct {
	loc         string
	lastmod     string
	isImage     bool
	imageURL    string
	isVideo     bool
	videoURL    string
	isDocument  bool
	isDir       bool
	fileSize    int64
}

// generateSitemap creates a sitemap.xml file in the root directory
func (s *server) generateSitemap() error {
	if s.baseURL == "" {
		return nil
	}

	var entries []sitemapEntry
	err := filepath.Walk(s.rootAbs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if s.shouldIgnore(path, filepath.Base(path)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		vp, err := s.virtualFromReal(path)
		if err != nil {
			return nil
		}

		// Format lastmod in W3C Datetime format (ISO 8601)
		lastmod := info.ModTime().Format(time.RFC3339)

		entry := sitemapEntry{
			loc:     s.baseURL + vp,
			lastmod: lastmod,
			isDir:   info.IsDir(),
		}

		// Check file category for special handling
		if !info.IsDir() {
			entry.fileSize = info.Size()
			category := getFileCategory(filepath.Base(path))
			switch category {
			case FileCategoryImage:
				entry.isImage = true
				entry.imageURL = s.baseURL + vp
			case FileCategoryVideo:
				entry.isVideo = true
				entry.videoURL = s.baseURL + vp
			case FileCategoryDocument:
				entry.isDocument = true
			}
		}

		entries = append(entries, entry)

		return nil
	})

	if err != nil {
		return err
	}

	// Build XML with proper namespaces
	var xml strings.Builder
	xml.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	xml.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"` + "\n")
	xml.WriteString(`        xmlns:image="http://www.google.com/schemas/sitemap-image/1.1"` + "\n")
	xml.WriteString(`        xmlns:video="http://www.google.com/schemas/sitemap-video/1.1"` + "\n")
	xml.WriteString(`        xmlns:xhtml="http://www.w3.org/1999/xhtml">` + "\n")

	for _, entry := range entries {
		xml.WriteString("  <url>\n")
		xml.WriteString("    <loc>" + template.HTMLEscapeString(entry.loc) + "</loc>\n")
		xml.WriteString("    <lastmod>" + entry.lastmod + "</lastmod>\n")

		// Add changefreq and priority based on type
		if entry.isDir {
			xml.WriteString("    <changefreq>daily</changefreq>\n")
			xml.WriteString("    <priority>0.8</priority>\n")
		} else if entry.isImage {
			xml.WriteString("    <changefreq>monthly</changefreq>\n")
			xml.WriteString("    <priority>0.6</priority>\n")
			// Add image-specific data using Google's image extension
			xml.WriteString("    <image:image>\n")
			xml.WriteString("      <image:loc>" + template.HTMLEscapeString(entry.imageURL) + "</image:loc>\n")
			xml.WriteString("    </image:image>\n")
		} else if entry.isVideo {
			xml.WriteString("    <changefreq>monthly</changefreq>\n")
			xml.WriteString("    <priority>0.7</priority>\n")
			// Add video-specific data using Google's video extension
			xml.WriteString("    <video:video>\n")
			xml.WriteString("      <video:content_loc>" + template.HTMLEscapeString(entry.videoURL) + "</video:content_loc>\n")
			// Extract filename without extension for title
			videoName := filepath.Base(entry.videoURL)
			videoTitle := videoName[:len(videoName)-len(filepath.Ext(videoName))]
			xml.WriteString("      <video:title>" + template.HTMLEscapeString(videoTitle) + "</video:title>\n")
			xml.WriteString("      <video:description>" + template.HTMLEscapeString("Video file: "+videoName) + "</video:description>\n")
			xml.WriteString("    </video:video>\n")
		} else if entry.isDocument {
			// Documents (PDFs, DOCs, etc.) get higher priority as they're often primary content
			xml.WriteString("    <changefreq>monthly</changefreq>\n")
			xml.WriteString("    <priority>0.9</priority>\n")
			// Add a comment to indicate this is a document (no official schema, but helpful for debugging)
			docName := filepath.Base(entry.loc)
			xml.WriteString("    <!-- Document: " + template.HTMLEscapeString(docName) + " (" + formatHumanSize(entry.fileSize) + ") -->\n")
		} else {
			xml.WriteString("    <changefreq>weekly</changefreq>\n")
			xml.WriteString("    <priority>0.5</priority>\n")
		}

		xml.WriteString("  </url>\n")
	}

	xml.WriteString("</urlset>\n")

	sitemapPath := filepath.Join(s.rootAbs, "sitemap.xml")
	return os.WriteFile(sitemapPath, []byte(xml.String()), 0644)
}

// startSitemapGenerator starts periodic sitemap regeneration
func (s *server) startSitemapGenerator(intervalMinutes int) {
	if intervalMinutes <= 0 || s.baseURL == "" {
		return
	}

	if err := s.generateSitemap(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate initial sitemap: %v\n", err)
	} else {
		fmt.Printf("Generated sitemap.xml (%s/sitemap.xml)\n", s.rootAbs)
	}

	go func() {
		ticker := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			if err := s.generateSitemap(); err != nil {
				fmt.Fprintf(os.Stderr, "failed to regenerate sitemap: %v\n", err)
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

func (s *server) getSession(w http.ResponseWriter, r *http.Request) *session {
	ck, err := r.Cookie("sid")
	if err == nil {
		s.mu.RLock()
		if sess, ok := s.sessions[ck.Value]; ok {
			s.mu.RUnlock()
			return sess
		}
		s.mu.RUnlock()
	}
	id := newSID()
	sess := &session{cwd: "/"}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     "sid",
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return sess
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

// convert a virtual path to a real filesystem path and ensure it is
// rooted inside s.rootAbs
func (s *server) realFromVirtual(v string) (string, error) {
	v = cleanVirtual(v)
	if v == "/" {
		return s.rootAbs, nil
	}
	rel := strings.TrimPrefix(v, "/")
	fsPath := filepath.Join(s.rootAbs, filepath.FromSlash(rel))
	abs, err := filepath.Abs(fsPath)
	if err != nil {
		return "", err
	}
	// prevent escaping the root via .. or symlinks
	// (best-effort: compare cleaned absolute paths)
	if abs == s.rootAbs {
		return abs, nil
	}
	rel2, err := filepath.Rel(s.rootAbs, abs)
	if err != nil || strings.HasPrefix(rel2, "..") || rel2 == ".." {
		return "", errors.New("permission denied")
	}
	return abs, nil
}

func (s *server) virtualFromReal(realPath string) (string, error) {
rel, err := filepath.Rel(s.rootAbs, realPath)
if err != nil {
return "", err
}
if rel == "." {
return "/", nil
}
return "/" + filepath.ToSlash(rel), nil
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
	Output    string  `json:"output"`
	Download  string  `json:"download,omitempty"`
	CWD       string  `json:"cwd,omitempty"`
	Readme    *string `json:"readme,omitempty"`
	DocType   string  `json:"docType,omitempty"`
	Clipboard string  `json:"clipboard,omitempty"`
	HTML      string  `json:"html,omitempty"`
	Redirect  string  `json:"redirect,omitempty"`
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
	Items []completeItem `json:"items"`
}

type configResp struct {
	CatMax  int64   `json:"catMax"`
	Readme  *string `json:"readme,omitempty"`
	DocType string  `json:"docType,omitempty"`
	CWD     string  `json:"cwd,omitempty"`
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
	filePath := downloadPath(requestPath)
	files := root.FS()
	info, err := fs.Stat(files, filePath)
	noJS := query.Get("nojs") == "1"
	if err != nil {
		if noJS || !errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
		} else {
			s.serveMainIndex(w, r, "/")
		}
		return
	}
	if !info.IsDir() {
		serveDownloadFile(w, r, root, filePath, false)
		return
	}

	indexPath := path.Join(filePath, "index.html")
	if info, err := fs.Stat(files, indexPath); err == nil && !info.IsDir() {
		serveDownloadFile(w, r, root, indexPath, false)
		return
	}
	if noJS {
		s.serveNoJSDirectory(w, r, requestPath)
	} else {
		s.serveMainIndex(w, r, requestPath)
	}
}

var errDownloadDirectory = errors.New("is a directory")

// downloadPath converts a virtual path to a name in the served filesystem.
func downloadPath(virtualPath string) string {
	name := strings.TrimPrefix(cleanVirtual(virtualPath), "/")
	if name == "" {
		return "."
	}
	return name
}

func downloadInfo(files fs.FS, filePath string) (fs.FileInfo, error) {
	info, err := fs.Stat(files, filePath)
	if err != nil {
		return nil, err
	}
	if matchesIgnoreRules(files, filePath, path.Base(filePath), true) {
		return nil, fs.ErrNotExist
	}
	return info, nil
}

func openDownloadFile(root *os.Root, filePath string) (*os.File, fs.FileInfo, error) {
	info, err := downloadInfo(root.FS(), filePath)
	if err != nil {
		return nil, nil, err
	}
	if info.IsDir() {
		return nil, nil, errDownloadDirectory
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fs.ErrNotExist
	}

	localPath, err := filepath.Localize(filePath)
	if err != nil {
		return nil, nil, err
	}
	file, err := root.Open(localPath)
	if err != nil {
		return nil, nil, err
	}
	info, err = file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fs.ErrNotExist
	}
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func serveDownloadFile(w http.ResponseWriter, r *http.Request, root *os.Root, filePath string, attachment bool) {
	file, info, err := openDownloadFile(root, filePath)
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

func (s *server) serveMainIndex(w http.ResponseWriter, r *http.Request, initialPath string) {
	var htmlContent []byte

	// Serve from disk if available so you can iterate quickly.
	if b, err := os.ReadFile("index.html"); err == nil {
		htmlContent = b
	} else {
		// Fallback to embedded.
		htmlContent = embeddedIndex
	}

	// Replace placeholder with actual help message and initial path
	processedHTML := s.processHTMLTemplate(htmlContent, initialPath)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(processedHTML)
}

// serveNoJSDirectory serves a plain HTML directory listing for no-JS fallback
func (s *server) serveNoJSDirectory(w http.ResponseWriter, r *http.Request, virtualPath string) {
	realPath, err := s.realFromVirtual(virtualPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	entries, err := os.ReadDir(realPath)
	if err != nil {
		http.Error(w, "Error reading directory", http.StatusInternalServerError)
		return
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

	var dirs []os.DirEntry
	var files []os.DirEntry

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		realFilePath := filepath.Join(realPath, name)
		if s.shouldIgnore(realFilePath, name) {
			continue
		}

		if entry.IsDir() {
			dirs = append(dirs, entry)
		} else {
			files = append(files, entry)
		}
	}

	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].Name() < dirs[j].Name()
	})
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})

	for _, dir := range dirs {
		dirPath := path.Join(virtualPath, dir.Name())
		_, _ = fmt.Fprintf(w, "<a href=\"%s?nojs=1\">%s/</a><br>\n", html.EscapeString(urlEscapeVirtual(dirPath)), template.HTMLEscapeString(dir.Name()))
	}

	for _, file := range files {
		filePath := path.Join(virtualPath, file.Name())
		info, _ := file.Info()
		var size string
		if info != nil {
			size = fmt.Sprintf(" (%d bytes)", info.Size())
		}
		_, _ = fmt.Fprintf(w, "<a href=\"%s\">%s</a>%s<br>\n", html.EscapeString(urlEscapeVirtual(filePath)), template.HTMLEscapeString(file.Name()), size)
	}

	_, _ = fmt.Fprintf(w, "</body>\n</html>\n")
}

func (s *server) handleStaticFile(w http.ResponseWriter, r *http.Request) {
	root, err := os.OpenRoot(s.rootAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = root.Close() }()

	filePath := downloadPath(strings.TrimPrefix(r.URL.Path, "/api/static"))
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
func (s *server) processHTMLTemplate(htmlContent []byte, requestPath string) []byte {
	// Split into lines and wrap each in HTML div tags
	lines := strings.Split(strings.TrimSpace(renderHelp()), "\n")
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

	result := strings.ReplaceAll(string(htmlContent), "{{HELP_MESSAGE}}", formattedHelpMessage)
	result = strings.ReplaceAll(result, "{{INITIAL_PATH}}", escapedInitialPath)
	return []byte(result)
}

func (s *server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	sitemapPath := filepath.Join(s.rootAbs, "sitemap.xml")

	// Check if sitemap exists
	if _, err := os.Stat(sitemapPath); os.IsNotExist(err) {
		// Generate on-demand if not exists
		if s.baseURL != "" {
			if err := s.generateSitemap(); err != nil {
				http.Error(w, "Failed to generate sitemap", http.StatusInternalServerError)
				return
			}
		} else {
			http.Error(w, "Sitemap not configured (missing -baseurl flag)", http.StatusNotFound)
			return
		}
	}

	// Serve the file
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	http.ServeFile(w, r, sitemapPath)
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	sess := s.getSession(w, r)

	// Check if there's an initial path from the query parameter
	initialPath := r.URL.Query().Get("path")
	if initialPath != "" {
		// Validate and set the initial path
		newV := cleanVirtual(initialPath)
		newReal, err := s.realFromVirtual(newV)
		if err == nil {
			info, err := os.Stat(newReal)
			if err == nil && info.IsDir() {
				sess.cwd = newV
			}
		}
	}

	// Get readme for current directory
	var readme string
	var docType string
	if sess.cwd == "/" {
		readme, docType = readDocFile(s.rootAbs)
	} else {
		realCwd, err := s.realFromVirtual(sess.cwd)
		if err == nil {
			readme, docType = readDocFile(realCwd)
		}
	}

	_ = json.NewEncoder(w).Encode(configResp{CatMax: s.catMax, Readme: &readme, DocType: docType, CWD: sess.cwd})
}

func (s *server) handleExec(w http.ResponseWriter, r *http.Request) {
	sess := s.getSession(w, r)

	var req execReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	line := strings.TrimSpace(req.Input)
	if line == "" {
		_ = json.NewEncoder(w).Encode(execResp{Output: ""})
		return
	}
	args := parseArgs(line)
	cmd := args[0]
	argv := args[1:]

	switch cmd {
	case "pwd":
		_ = json.NewEncoder(w).Encode(execResp{Output: sess.cwd, CWD: sess.cwd})
		return

	case "help":
		_ = json.NewEncoder(w).Encode(execResp{HTML: renderHelp()})
		return

	case "ls", "dir":
		long := false
		showHidden := false
		humanReadable := false
		target := sess.cwd
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
		// Get the real path of the directory to list
		virtualPath := joinVirtual(sess.cwd, target)
		realCwd, err := s.realFromVirtual(virtualPath)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "ls: permission denied"})
			return
		}
		// Get file info and check if it's a directory
		info, err := os.Stat(realCwd)
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
		ents, err := os.ReadDir(realCwd)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "ls: error"})
			return
		}
		var names []string
		var longs []string
		for _, e := range ents {
			name := e.Name()
			if !showHidden && strings.HasPrefix(name, ".") {
				continue // hide dotfiles unless -a flag is used
			}
			// Check if file should be ignored based on .lsgetignore
			realFilePath := filepath.Join(realCwd, name)
			if s.shouldIgnore(realFilePath, name) {
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)

		// Add ".." at the beginning if not at root
		if sess.cwd != "/" {
			names = append([]string{".."}, names...)
		}

		if !long {
			// Colorized simple listing
			var coloredNames []string
			for _, name := range names {
				if name == ".." {
					// Special handling for parent directory
					coloredNames = append(coloredNames, colorBlue+colorBold+"../"+colorReset)
					continue
				}
				info, err := os.Stat(filepath.Join(realCwd, name))
				if err != nil {
					coloredNames = append(coloredNames, name)
					continue
				}
				// Use absolute virtual path for clickable links
				// path.Join (not filepath.Join) ensures Unix-style forward slashes for virtual paths
				displayName := path.Join(virtualPath, name)
				coloredNames = append(coloredNames, colorizeName(info, displayName))
			}
			_ = json.NewEncoder(w).Encode(execResp{Output: strings.Join(coloredNames, "\n")})
			return
		}
		// Colorized long listing
		for _, name := range names {
			if name == ".." {
				// Special handling for parent directory in long format
				longs = append(longs, "drwxr-xr-x          - "+colorBlue+colorBold+"../"+colorReset)
				continue
			}
			info, err := os.Stat(filepath.Join(realCwd, name))
			if err != nil {
				continue
			}
			// Use absolute virtual path for clickable links
			// path.Join (not filepath.Join) ensures Unix-style forward slashes for virtual paths
			displayName := path.Join(virtualPath, name)
			// Format the long listing with colorized filename
			longEntry := formatLong(info, colorizeName(info, displayName), humanReadable)
			longs = append(longs, longEntry)
		}
		_ = json.NewEncoder(w).Encode(execResp{Output: strings.Join(longs, "\n")})
		return

	case "cd":
		target := "/"
		if len(argv) == 1 {
			target = argv[0]
			if target == "" {
				target = "/"
			}
		}
		newV := joinVirtual(sess.cwd, target)
		newReal, err := s.realFromVirtual(newV)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cd: permission denied"})
			return
		}
		info, err := os.Stat(newReal)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cd: no such file or directory"})
			return
		}
		if !info.IsDir() {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cd: not a directory"})
			return
		}
		sess.cwd = newV
		
		// Check if directory contains index.html
		indexPath := filepath.Join(newReal, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			// Directory has index.html, redirect to it
			_ = json.NewEncoder(w).Encode(execResp{Redirect: newV})
			return
		}
		
		readme, docType := readDocFile(newReal)
		// Include the new CWD in the response so client can update URL
		_ = json.NewEncoder(w).Encode(execResp{Output: "", CWD: sess.cwd, Readme: &readme, DocType: docType})
		return

	case "cat":
		if len(argv) < 1 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: missing operand"})
			return
		}
		vp := joinVirtual(sess.cwd, argv[0])
		rp, err := s.realFromVirtual(vp)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: permission denied"})
			return
		}
		info, err := os.Stat(rp)
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
			url := urlEscapeVirtual(vp)
			name := filepath.Base(argv[0])
			escapedURL := template.HTMLEscapeString(url)
			escapedName := template.HTMLEscapeString(name)
			imgHTML := fmt.Sprintf(
				`<div style="margin: 0.5em 0;">`+
					`<a href="%s" target="_blank" style="display: inline-block; text-decoration: none;">`+
					`<img src="%s" style="max-width: 100%%; height: auto; display: block; border-radius: 4px; box-shadow: 0 2px 8px rgba(0,0,0,0.3);" alt="%s">`+
					`</a>`+
					`<div style="color: #a6d189; font-size: 0.85em; margin-top: 0.25em;">📷 %s (%s)</div>`+
					`</div>`,
				escapedURL, escapedURL, escapedName, escapedName, formatHumanSize(info.Size()))
			_ = json.NewEncoder(w).Encode(execResp{HTML: imgHTML})
			return
		}

		// Only text files and unknown files (to be checked by content) can be displayed
		if category != FileCategoryText && category != FileCategoryUnknown {
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("cat: cannot display %s files (use 'get' to download)", category)})
			return
		}

		if info.Size() > s.catMax {
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("cat: file too large (%d > limit %d)", info.Size(), s.catMax)})
			return
		}
		f, err := os.Open(rp)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: cannot open file"})
			return
		}
		defer func() { _ = f.Close() }()
		// read up to catMax bytes
		var buf bytes.Buffer
		if _, err := io.CopyN(&buf, f, s.catMax); err != nil && !errors.Is(err, io.EOF) {
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

		// Get IP address for logging
		ip := getClientIP(r)

		// Check if pattern contains wildcards or is a directory
		if strings.ContainsAny(pattern, "*?[") || pattern == "." {
			// Handle pattern-based download (multiple files)
			files, err := collectFilesForDownload(root, sess.cwd, pattern)
			if err != nil {
				_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("download: %v", err)})
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
			downloadURL := "/api/download?pattern=" + url.QueryEscape(pattern) + "&cwd=" + url.QueryEscape(sess.cwd)
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("Downloading %d files as archive.zip", len(files)), Download: downloadURL})
			return
		}

		// Check if it's a directory
		vp := joinVirtual(sess.cwd, pattern)
		name := downloadPath(vp)
		info, err := downloadInfo(root.FS(), name)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "download: no such file"})
			return
		}

		if info.IsDir() {
			// Download directory as zip
			files, err := collectFilesFromDirectory(root, name)
			if err != nil {
				_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("download: %v", err)})
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
		target := sess.cwd

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
				target = joinVirtual(sess.cwd, arg)
			}
		}

		realTarget, err := s.realFromVirtual(target)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "tree: permission denied"})
			return
		}

		info, err := os.Stat(realTarget)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "tree: no such file or directory"})
			return
		}

		if !info.IsDir() {
			_ = json.NewEncoder(w).Encode(execResp{Output: "tree: not a directory"})
			return
		}

		var result strings.Builder
		dirCount, fileCount := s.buildTree(&result, realTarget, "", showHidden, maxDepth, 0)

		// Add summary
		result.WriteString(fmt.Sprintf("\n%d directories, %d files", dirCount, fileCount))

		_ = json.NewEncoder(w).Encode(execResp{Output: result.String()})
		return

	case "find":
		// Parse options
		searchPath := sess.cwd
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
				searchPath = joinVirtual(sess.cwd, arg)
			}
		}

		// Validate type filter
		if typeFilter != "" && typeFilter != "f" && typeFilter != "d" {
			_ = json.NewEncoder(w).Encode(execResp{Output: "find: invalid type filter (use 'f' for files or 'd' for directories)"})
			return
		}

		realSearchPath, err := s.realFromVirtual(searchPath)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "find: permission denied"})
			return
		}

		info, err := os.Stat(realSearchPath)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "find: no such file or directory"})
			return
		}

		if !info.IsDir() {
			_ = json.NewEncoder(w).Encode(execResp{Output: "find: not a directory"})
			return
		}

		var results []string
		err = s.findFiles(realSearchPath, searchPath, namePattern, typeFilter, &results)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("find: %v", err)})
			return
		}

		if len(results) == 0 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "find: no matches found"})
			return
		}

		_ = json.NewEncoder(w).Encode(execResp{Output: strings.Join(results, "\n")})
		return

	case "url", "share":
		if len(argv) < 1 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "url: missing file operand"})
			return
		}

		vp := joinVirtual(sess.cwd, argv[0])
		rp, err := s.realFromVirtual(vp)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "url: permission denied"})
			return
		}

		info, err := os.Stat(rp)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "url: no such file or directory"})
			return
		}

		if info.IsDir() {
			_ = json.NewEncoder(w).Encode(execResp{Output: "url: cannot share directories (use 'get' to download as zip)"})
			return
		}

		// Check if file should be ignored
		if s.shouldIgnore(rp, filepath.Base(rp)) {
			_ = json.NewEncoder(w).Encode(execResp{Output: "url: file is ignored"})
			return
		}

		// Build the full URL for the file
		var fileURL string
		if s.baseURL != "" {
			// Use configured base URL (clean, no /api/static)
			// Required when behind reverse proxy with different public URL
			fileURL = s.baseURL + vp
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
			fileURL = fmt.Sprintf("%s://%s/%s", protocol, host, vp)
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

		var results []string
		for _, file := range files {
			vp := joinVirtual(sess.cwd, file)
			rp, err := s.realFromVirtual(vp)
			if err != nil {
				results = append(results, fmt.Sprintf("grep: %s: permission denied", file))
				continue
			}

			info, err := os.Stat(rp)
			if err != nil {
				results = append(results, fmt.Sprintf("grep: %s: no such file or directory", file))
				continue
			}

			if info.IsDir() {
				if recursive {
					err := s.grepInDirectory(rp, vp, pattern, ignoreCase, showLineNumbers, &results)
					if err != nil {
						results = append(results, fmt.Sprintf("grep: %s: %v", file, err))
					}
				} else {
					results = append(results, fmt.Sprintf("grep: %s: is a directory", file))
				}
			} else {
				err := s.grepInFile(rp, vp, pattern, ignoreCase, showLineNumbers, len(files) > 1, &results)
				if err != nil {
					results = append(results, fmt.Sprintf("grep: %s: %v", file, err))
				}
			}
		}

		if len(results) == 0 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "grep: no matches found"})
			return
		}

		_ = json.NewEncoder(w).Encode(execResp{Output: strings.Join(results, "\n")})
		return

	case "sum", "checksum":
		if len(argv) < 1 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "sum: missing file operand"})
			return
		}

		vp := joinVirtual(sess.cwd, argv[0])
		rp, err := s.realFromVirtual(vp)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "sum: permission denied"})
			return
		}

		info, err := os.Stat(rp)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "sum: no such file or directory"})
			return
		}

		if info.IsDir() {
			_ = json.NewEncoder(w).Encode(execResp{Output: "sum: is a directory"})
			return
		}

		// Open file and compute hashes
		f, err := os.Open(rp)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "sum: cannot open file"})
			return
		}
		defer func() { _ = f.Close() }()

		md5Hash := md5.New()
		sha256Hash := sha256.New()

		// Use MultiWriter to compute both hashes in one pass
		writer := io.MultiWriter(md5Hash, sha256Hash)
		if _, err := io.Copy(writer, f); err != nil {
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

// findFiles recursively searches for files and directories matching the given pattern
func (s *server) findFiles(realPath, virtualPath, pattern, typeFilter string, results *[]string) error {
	entries, err := os.ReadDir(realPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		name := entry.Name()

		// Skip hidden files unless pattern starts with dot
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(pattern, ".") {
			continue
		}

		realEntryPath := filepath.Join(realPath, name)
		virtualEntryPath := path.Join(virtualPath, name)

		// Check if file should be ignored based on .lsgetignore
		if s.shouldIgnore(realEntryPath, name) {
			continue
		}

		// Check if name matches pattern
		matched, err := filepath.Match(pattern, name)
		if err != nil {
			continue // Invalid pattern, skip this entry
		}

		isDir := entry.IsDir()

		// Apply type filter and add to results if matched
		if matched {
			includeEntry := false
			switch typeFilter {
			case "f":
				includeEntry = !isDir
			case "d":
				includeEntry = isDir
			default:
				includeEntry = true
			}

			if includeEntry {
				// Get file info for colorization
				info, err := entry.Info()
				if err == nil {
					colorizedName := colorizeName(info, virtualEntryPath)
					*results = append(*results, colorizedName)
				} else {
					*results = append(*results, virtualEntryPath)
				}
			}
		}

		// Recursively search subdirectories
		if isDir {
			err := s.findFiles(realEntryPath, virtualEntryPath, pattern, typeFilter, results)
			if err != nil {
				// Continue searching other directories even if one fails
				continue
			}
		}
	}

	return nil
}

// grepInFile searches for a pattern within a single file
func (s *server) grepInFile(realPath, virtualPath, pattern string, ignoreCase, showLineNumbers, showFilename bool, results *[]string) error {
	file, err := os.Open(realPath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	// Check if file is likely to be text
	info, err := file.Stat()
	if err != nil {
		return err
	}

	// Skip very large files to avoid memory issues
	if info.Size() > 10*1024*1024 { // 10MB limit
		return fmt.Errorf("file too large")
	}

	// Read a sample to check if it's text
	sample := make([]byte, 4096)
	n, _ := file.Read(sample)
	if !looksText(sample[:n]) {
		return nil // Skip binary files silently
	}

	// Reset file position
	_, err = file.Seek(0, 0)
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(file)
	lineNum := 1
	searchPattern := pattern
	if ignoreCase {
		searchPattern = strings.ToLower(pattern)
	}

	for scanner.Scan() {
		line := scanner.Text()
		searchLine := line
		if ignoreCase {
			searchLine = strings.ToLower(line)
		}

		if strings.Contains(searchLine, searchPattern) {
			var result strings.Builder

			// Add filename if multiple files or recursive search
			if showFilename {
				result.WriteString(colorCyan)
				result.WriteString(virtualPath)
				result.WriteString(colorReset)
				result.WriteString(":")
			}

			// Add line number if requested
			if showLineNumbers {
				result.WriteString(colorGreen)
				result.WriteString(fmt.Sprintf("%d", lineNum))
				result.WriteString(colorReset)
				result.WriteString(":")
			}

			// Highlight the matching pattern in the line
			if ignoreCase {
				// Case insensitive highlighting
				lowerLine := strings.ToLower(line)
				start := strings.Index(lowerLine, searchPattern)
				if start >= 0 {
					end := start + len(searchPattern)
					highlighted := line[:start] +
						colorYellow + colorBold + line[start:end] + colorReset +
						line[end:]
					result.WriteString(highlighted)
				} else {
					result.WriteString(line)
				}
			} else {
				// Case sensitive highlighting
				highlighted := strings.ReplaceAll(line, pattern,
					colorYellow+colorBold+pattern+colorReset)
				result.WriteString(highlighted)
			}

			*results = append(*results, result.String())
		}
		lineNum++
	}

	return scanner.Err()
}

// grepInDirectory recursively searches for a pattern in all text files within a directory
func (s *server) grepInDirectory(realPath, virtualPath, pattern string, ignoreCase, showLineNumbers bool, results *[]string) error {
	entries, err := os.ReadDir(realPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		name := entry.Name()

		// Skip hidden files and directories
		if strings.HasPrefix(name, ".") {
			continue
		}

		realEntryPath := filepath.Join(realPath, name)
		virtualEntryPath := path.Join(virtualPath, name)

		// Check if file should be ignored based on .lsgetignore
		if s.shouldIgnore(realEntryPath, name) {
			continue
		}

		if entry.IsDir() {
			// Recursively search subdirectories
			err := s.grepInDirectory(realEntryPath, virtualEntryPath, pattern, ignoreCase, showLineNumbers, results)
			if err != nil {
				// Continue searching other directories even if one fails
				continue
			}
		} else {
			// Search in file
			err := s.grepInFile(realEntryPath, virtualEntryPath, pattern, ignoreCase, showLineNumbers, true, results)
			if err != nil {
				// Continue searching other files even if one fails
				continue
			}
		}
	}

	return nil
}

type fileInfo struct {
	virtualPath  string
	relativePath string
}

// collectFilesForDownload collects files matching a name or pattern for download.
func collectFilesForDownload(root *os.Root, cwd, pattern string) ([]fileInfo, error) {
	name := downloadPath(joinVirtual(cwd, pattern))
	files := root.FS()
	if !strings.ContainsAny(pattern, "*?[") {
		info, err := downloadInfo(files, name)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return collectFilesFromDirectory(root, name)
		}
		if !info.Mode().IsRegular() {
			return nil, fs.ErrNotExist
		}
		return []fileInfo{{virtualPath: "/" + name, relativePath: path.Base(name)}}, nil
	}

	dir := downloadPath(joinVirtual(cwd, path.Dir(pattern)))
	if _, err := downloadInfo(files, dir); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(files, dir)
	if err != nil {
		return nil, err
	}

	var matches []fileInfo
	for _, entry := range entries {
		matched, err := filepath.Match(path.Base(pattern), entry.Name())
		if err != nil || !matched || entry.IsDir() {
			continue
		}
		name := path.Join(dir, entry.Name())
		info, err := downloadInfo(files, name)
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
func collectFilesFromDirectory(root *os.Root, dir string) ([]fileInfo, error) {
	var matches []fileInfo
	files := root.FS()
	dirName := downloadDirectoryName(root, dir)
	err := fs.WalkDir(files, dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			if name == dir {
				return err
			}
			return nil
		}
		info, err := downloadInfo(files, name)
		if entry.IsDir() {
			if err != nil {
				return fs.SkipDir
			}
			return nil
		}
		if err != nil || !info.Mode().IsRegular() || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		relativePath := name
		if dir != "." {
			relativePath = strings.TrimPrefix(name, dir+"/")
		}
		matches = append(matches, fileInfo{
			virtualPath:  "/" + name,
			relativePath: path.Join(dirName, relativePath),
		})
		return nil
	})
	return matches, err
}

func sendZipArchive(w http.ResponseWriter, root *os.Root, files []fileInfo, filename string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	zipWriter := zip.NewWriter(w)
	defer func() { _ = zipWriter.Close() }()

	for _, file := range files {
		source, info, err := openDownloadFile(root, downloadPath(file.virtualPath))
		if err != nil {
			continue
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			_ = source.Close()
			continue
		}
		header.Name = file.relativePath
		header.Method = zip.Deflate

		entry, err := zipWriter.CreateHeader(header)
		if err == nil {
			_, _ = io.Copy(entry, source)
		}
		_ = source.Close()
	}
}

// buildTree recursively builds a tree representation of the directory structure
func (s *server) buildTree(result *strings.Builder, dirPath, prefix string, showHidden bool, maxDepth, currentDepth int) (int, int) {
	if maxDepth >= 0 && currentDepth >= maxDepth {
		return 0, 0
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return 0, 0
	}

	// Filter and sort entries
	var validEntries []os.DirEntry
	for _, entry := range entries {
		name := entry.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		validEntries = append(validEntries, entry)
	}

	// Sort: directories first, then files, alphabetically within each group
	sort.Slice(validEntries, func(i, j int) bool {
		iDir := validEntries[i].IsDir()
		jDir := validEntries[j].IsDir()
		if iDir != jDir {
			return iDir && !jDir
		}
		return validEntries[i].Name() < validEntries[j].Name()
	})

	dirCount := 0
	fileCount := 0

	for i, entry := range validEntries {
		name := entry.Name()
		isLast := i == len(validEntries)-1

		// Build the tree symbols
		var connector string
		if isLast {
			connector = "└── "
		} else {
			connector = "├── "
		}

		// Get file info for colorization
		fullPath := filepath.Join(dirPath, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Add colorized name
		coloredName := colorizeName(info, name)
		result.WriteString(prefix + connector + coloredName + "\n")

		if entry.IsDir() {
			dirCount++
			// Recursively process subdirectories
			var newPrefix string
			if isLast {
				newPrefix = prefix + "    "
			} else {
				newPrefix = prefix + "│   "
			}
			subDirCount, subFileCount := s.buildTree(result, fullPath, newPrefix, showHidden, maxDepth, currentDepth+1)
			dirCount += subDirCount
			fileCount += subFileCount
		} else {
			fileCount++
		}
	}

	return dirCount, fileCount
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
	sess := s.getSession(w, r)
	query := r.URL.Query()
	root, err := os.OpenRoot(s.rootAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = root.Close() }()

	if filePath := query.Get("path"); filePath != "" {
		serveDownloadFile(w, r, root, downloadPath(filePath), true)
		return
	}

	if dir := query.Get("dir"); dir != "" {
		name := downloadPath(dir)
		info, err := downloadInfo(root.FS(), name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !info.IsDir() {
			http.Error(w, "not a directory", http.StatusBadRequest)
			return
		}
		files, err := collectFilesFromDirectory(root, name)
		if err != nil {
			http.Error(w, "failed to collect files", http.StatusInternalServerError)
			return
		}
		sendZipArchive(w, root, files, downloadDirectoryName(root, name)+".zip")
		return
	}

	if pattern := query.Get("pattern"); pattern != "" {
		cwd := query.Get("cwd")
		if cwd == "" {
			cwd = sess.cwd
		}
		files, err := collectFilesForDownload(root, cwd, pattern)
		if errors.Is(err, fs.ErrNotExist) || (err == nil && len(files) == 0) {
			http.Error(w, "no matching files found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "failed to collect files", http.StatusInternalServerError)
			return
		}
		sendZipArchive(w, root, files, "archive.zip")
		return
	}

	http.Error(w, "missing download parameters", http.StatusBadRequest)
}

func (s *server) handleComplete(w http.ResponseWriter, r *http.Request) {
	sess := s.getSession(w, r)
	var req completeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	arg := req.Path
	if arg == "" {
		arg = ""
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
	} else {
		baseV = joinVirtual(sess.cwd, dirPart)
	}
	baseR, err := s.realFromVirtual(baseV)
	if err != nil {
		_ = json.NewEncoder(w).Encode(completeResp{Items: nil})
		return
	}

	ents, err := os.ReadDir(baseR)
	if err != nil {
		_ = json.NewEncoder(w).Encode(completeResp{Items: nil})
		return
	}

	showHidden := strings.HasPrefix(basePart, ".")
	maxItems := 200
	items := make([]completeItem, 0, 16)

	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, basePart) {
			continue
		}
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}

		isDir := e.IsDir()
		if req.DirsOnly && !isDir {
			continue
		}

		if req.TextOnly || req.MaxSize > 0 {
			if !isDir {
				info, err := e.Info()
				if err != nil {
					continue
				}
				// Use file category to check if viewable
				cat := getFileCategory(name)
				
				// Images are always included regardless of size limits
				if cat == FileCategoryImage {
					// Skip other filters for images
				} else {
					// For non-images, apply the filters
					if req.MaxSize > 0 && info.Size() > req.MaxSize {
						continue
					}
					if req.TextOnly && cat != FileCategoryText {
						continue
					}
				}
			}
		}

		items = append(items, completeItem{Name: name, Dir: isDir})
		if len(items) >= maxItems {
			break
		}
	}

	// Sort: directories first, then files; alphabetical within each
	sort.Slice(items, func(i, j int) bool {
		if items[i].Dir != items[j].Dir {
			return items[i].Dir && !items[j].Dir
		}
		return items[i].Name < items[j].Name
	})

	_ = json.NewEncoder(w).Encode(completeResp{Items: items})
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
	var (
		printVersion    = flag.Bool("version", false, "Print the version of this software and exits")
		addr            = flag.String("addr", getEnvOrDefault("LSGET_ADDR", "localhost:8080"), "address to listen on (env: LSGET_ADDR)")
		dir             = flag.String("dir", getEnvOrDefault("LSGET_DIR", "."), "directory to expose as root (env: LSGET_DIR)")
		catMax          = flag.Int64("catmax", getEnvOrDefaultInt64("LSGET_CATMAX", 4*1024), "max bytes printable via `cat` and used by completion (env: LSGET_CATMAX)")
		pidFileFlag     = flag.String("pid", getEnvOrDefault("LSGET_PID", ""), "path to PID file (env: LSGET_PID)")
		logfileFlag     = flag.String("logfile", getEnvOrDefault("LSGET_LOGFILE", ""), "path to log file for statistics (env: LSGET_LOGFILE)")
		baseURL         = flag.String("baseurl", getEnvOrDefault("LSGET_BASEURL", ""), "base URL for the site - full URL without trailing slash (e.g., https://files.example.com) (env: LSGET_BASEURL)")
		sitemapInterval = flag.Int("sitemap", getEnvOrDefaultInt("LSGET_SITEMAP", 0), "generate sitemap.xml every N minutes (0 = disabled) (env: LSGET_SITEMAP)")
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

	// Set global log file path
	if *logfileFlag != "" {
		logFile = *logfileFlag
	}

	s := newServer(rootAbs, *catMax, *logfileFlag, *baseURL)

	// Special case: if sitemap is 0 and baseURL is set, generate once and exit
	if *sitemapInterval != 0 && *baseURL != "" {
		fmt.Println("=== Generating sitemap (debug mode) ===")
		fmt.Printf("Base URL: %s\n", *baseURL)
		fmt.Printf("Root directory: %s\n", rootAbs)

		if err := s.generateSitemap(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to generate sitemap: %v\n", err)
			exitFunc(1)
		}

		sitemapPath := filepath.Join(rootAbs, "sitemap.xml")
		info, err := os.Stat(sitemapPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to stat sitemap: %v\n", err)
			exitFunc(1)
		}

		fmt.Printf("✓ Sitemap generated successfully: %s (%d bytes)\n", sitemapPath, info.Size())

		// Show first few lines
		content, err := os.ReadFile(sitemapPath)
		if err == nil {
			lines := strings.Split(string(content), "\n")
			fmt.Println("\n=== Sitemap preview (first 10 lines) ===")
			for i := 0; i < len(lines) && i < 10; i++ {
				fmt.Println(lines[i])
			}
			if len(lines) > 10 {
				fmt.Printf("... (%d more lines)\n", len(lines)-10)
			}
		}

		exitFunc(0)
	}

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

	// Start sitemap generator if configured
	if *sitemapInterval > 0 {
		s.startSitemapGenerator(*sitemapInterval)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/exec", s.handleExec)
	mux.HandleFunc("/api/complete", s.handleComplete)
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.HandleFunc("/api/static/", s.handleStaticFile)
	mux.HandleFunc("/sitemap.xml", s.handleSitemap)
	// Vendored JavaScript dependencies
	mux.HandleFunc("/assets/js/marked.min.js", s.handleVendoredMarked)
	mux.HandleFunc("/assets/js/datastar.js", s.handleVendoredDatastar)
	mux.HandleFunc("/", s.handleIndex) // Catch-all route must be last

	fmt.Printf("Serving %s on http://%s  (cat max = %d bytes)\n", rootAbs, *addr, *catMax)
	if s.logfile != "" {
		fmt.Printf("Logging to: %s\n", s.logfile)
	} else {
		fmt.Println("Logging disabled (use -logfile or LSGET_LOGFILE to enable)")
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

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

		logLine := fmt.Sprintf("%s %s %s %s \"%s\" %d %s \"%s\" \"%s\"\n",
			ip, "-", user, timestamp, requestLine, statusCode, sizeStr, referer, userAgent)

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
	})
}
