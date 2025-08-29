package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var version = "dev"

// Indirections for testability
var exitFunc = os.Exit
var listenAndServe = func(srv *http.Server) error { return srv.ListenAndServe() }

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
• <strong>ls</strong> <span style="color: #888;">[-l]</span>|<strong>dir</strong> <span style="color: #888;">[-l]</span> - <span style="color: #bbb;">list files</span>
• <strong>cd</strong> <span style="color: #888;">DIR</span> - <span style="color: #bbb;">change directory</span>
• <strong>cat</strong> <span style="color: #888;">FILE</span> - <span style="color: #bbb;">view a text file</span>
• <strong>get</strong>|<strong>rget</strong>|<strong>download</strong> <span style="color: #888;">FILE</span> - <span style="color: #bbb;">download a file</span>
• <strong>url</strong>|<strong>share</strong> <span style="color: #888;">FILE</span> - <span style="color: #bbb;">get shareable URL (copies to clipboard)</span>
• <strong>tree</strong> <span style="color: #888;">[-L&lt;DEPTH&gt;] [-a]</span> - <span style="color: #bbb;">directory structure</span>
• <strong>find</strong> <span style="color: #888;">[PATH] [-name PATTERN] [-type f|d]</span> - <span style="color: #bbb;">search for files and directories</span>
• <strong>grep</strong> <span style="color: #888;">[-r] [-i] [-n] PATTERN [FILE...]</span> - <span style="color: #bbb;">search for text patterns in files</span>

<br/><br/>
<span style="color: #aaa;">Hint: to autocomplete filenames and dir use</span> <kbd class="ps1">Tab</kbd>
`

func renderHelp() string {
	var helpMessage = template.Must(template.New("help").Parse(helpTpl))
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
	if mode&0111 != 0 {
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
	return color + name + colorReset
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
}

func newServer(rootAbs string, catMax int64) *server {
	return &server{
		rootAbs:  rootAbs,
		catMax:   catMax,
		sessions: make(map[string]*session),
	}
}

// ===== .lsgetignore support =====

// parseIgnoreFile reads and parses a .lsgetignore file, returning a slice of patterns
func parseIgnoreFile(ignoreFilePath string) ([]string, error) {
	file, err := os.Open(ignoreFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No ignore file is fine
		}
		return nil, err
	}
	defer file.Close()

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
	// Start from the directory containing the file/directory
	currentDir := filepath.Dir(realPath)

	// Walk up the directory tree until we reach rootAbs
	for {
		// Check if we've gone above the root directory
		rel, err := filepath.Rel(s.rootAbs, currentDir)
		if err != nil || strings.HasPrefix(rel, "..") {
			break
		}

		// Look for .lsgetignore in current directory
		ignoreFile := filepath.Join(currentDir, ".lsgetignore")
		patterns, err := parseIgnoreFile(ignoreFile)
		if err == nil && len(patterns) > 0 {
			// Check if the file matches any pattern
			for _, pattern := range patterns {
				// Support both simple filename matching and path-based matching
				matched, err := filepath.Match(pattern, name)
				if err == nil && matched {
					return true
				}

				// Also check if the pattern matches the relative path from current directory
				relPath, err := filepath.Rel(currentDir, realPath)
				if err == nil {
					matched, err := filepath.Match(pattern, relPath)
					if err == nil && matched {
						return true
					}
					// Also check directory-based patterns
					if strings.Contains(relPath, "/") {
						matched, err := filepath.Match(pattern, filepath.Base(relPath))
						if err == nil && matched {
							return true
						}
					}
				}
			}
		}

		// Move up one directory
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			break // Reached root
		}
		currentDir = parentDir
	}

	return false
}

// ===== Utilities =====

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

func formatLong(info os.FileInfo, name string) string {
	// mode, size, date, name (owner/group omitted for portability)
	mode := info.Mode().String()
	size := info.Size()
	mod := info.ModTime().Format("Jan _2 15:04")
	return fmt.Sprintf("%s %10d %s %s", mode, size, mod, name)
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
	// Check for no-JS fallback query parameter
	noJS := r.URL.Query().Get("nojs") == "1"
	
	// For root path, check if we need no-JS fallback
	if r.URL.Path == "/" {
		if noJS {
			s.serveNoJSDirectory(w, r, "/")
		} else {
			s.serveMainIndex(w, r)
		}
		return
	}

	// For other paths, check if it's a file or directory
	requestPath := path.Clean(r.URL.Path)
	realPath, err := s.realFromVirtual(requestPath)
	if err != nil {
		// Path outside root, serve appropriate response
		if noJS {
			http.NotFound(w, r)
		} else {
			s.serveMainIndex(w, r)
		}
		return
	}

	// Check if path exists
	info, err := os.Stat(realPath)
	if err != nil {
		// Path doesn't exist
		if noJS {
			http.NotFound(w, r)
		} else {
			s.serveMainIndex(w, r)
		}
		return
	}

	if info.IsDir() {
		// It's a directory
		if noJS {
			s.serveNoJSDirectory(w, r, requestPath)
		} else {
			s.serveMainIndex(w, r)
		}
	} else {
		// It's a file, serve it directly for download
		s.serveFile(w, r, realPath, info)
	}
}

func (s *server) serveFile(w http.ResponseWriter, r *http.Request, realPath string, info os.FileInfo) {
	// Check if file should be ignored based on .lsgetignore patterns
	fileName := filepath.Base(realPath)
	if s.shouldIgnore(realPath, fileName) {
		http.NotFound(w, r)
		return
	}

	// Set appropriate content type based on file extension
	contentType := mime.TypeByExtension(filepath.Ext(realPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	
	// For certain file types, force download with Content-Disposition
	ext := strings.ToLower(filepath.Ext(realPath))
	switch ext {
	case ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".zip", ".rar", ".7z", ".tar", ".gz":
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	}

	// Serve the file
	http.ServeFile(w, r, realPath)
}

func (s *server) serveMainIndex(w http.ResponseWriter, r *http.Request) {
	var htmlContent []byte

	// Serve from disk if available so you can iterate quickly.
	if b, err := os.ReadFile("index.html"); err == nil {
		htmlContent = b
	} else {
		// Fallback to embedded.
		htmlContent = embeddedIndex
	}

	// Replace placeholder with actual help message and initial path
	processedHTML := s.processHTMLTemplate(htmlContent, r.URL.Path)

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

	// Start HTML document
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Write minimal HTML with monospace font and blue links
	fmt.Fprintf(w, `<!DOCTYPE html>
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
`, virtualPath)

	fmt.Fprintf(w, "<h1>Index of %s</h1>\n", virtualPath)
	fmt.Fprintf(w, "<hr>\n")

	// Add parent directory link if not at root
	if virtualPath != "/" {
		parentPath := path.Dir(virtualPath)
		fmt.Fprintf(w, "<a href=\"%s?nojs=1\">[Parent Directory]</a><br>\n", parentPath)
	}

	// List directories first, then files
	var dirs []os.DirEntry
	var files []os.DirEntry

	for _, entry := range entries {
		name := entry.Name()
		// Skip hidden files
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Check if should be ignored
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

	// Sort alphabetically
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].Name() < dirs[j].Name()
	})
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})

	// Display directories
	for _, dir := range dirs {
		dirPath := path.Join(virtualPath, dir.Name())
		fmt.Fprintf(w, "<a href=\"%s?nojs=1\">%s/</a><br>\n", dirPath, dir.Name())
	}

	// Display files
	for _, file := range files {
		filePath := path.Join(virtualPath, file.Name())
		info, _ := file.Info()
		var size string
		if info != nil {
			size = fmt.Sprintf(" (%d bytes)", info.Size())
		}
		fmt.Fprintf(w, "<a href=\"%s\">%s</a>%s<br>\n", filePath, file.Name(), size)
	}

	fmt.Fprintf(w, "</body>\n</html>\n")
}

func (s *server) handleStaticFile(w http.ResponseWriter, r *http.Request) {
	// Remove the /api/static prefix
	requestPath := strings.TrimPrefix(r.URL.Path, "/api/static")
	requestPath = path.Clean(requestPath)

	// Convert virtual path to real filesystem path
	realPath, err := s.realFromVirtual(requestPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Check if file exists and get info
	info, err := os.Stat(realPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Don't serve directories as static files
	if info.IsDir() {
		http.NotFound(w, r)
		return
	}

	// Use the common serveFile function
	s.serveFile(w, r, realPath, info)
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

	// Replace the placeholders in HTML
	result := strings.ReplaceAll(string(htmlContent), "{{HELP_MESSAGE}}", formattedHelpMessage)
	result = strings.ReplaceAll(result, "{{INITIAL_PATH}}", initialPath)
	return []byte(result)
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
		for _, arg := range argv {
			if strings.Contains(arg, "l") {
				long = true
			}
			if strings.Contains(arg, "a") {
				showHidden = true
			}
		}
		realCwd, err := s.realFromVirtual(sess.cwd)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "ls: error"})
			return
		}
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
		if !long {
			// Colorized simple listing
			var coloredNames []string
			for _, name := range names {
				info, err := os.Stat(filepath.Join(realCwd, name))
				if err != nil {
					coloredNames = append(coloredNames, name)
					continue
				}
				coloredNames = append(coloredNames, colorizeName(info, name))
			}
			_ = json.NewEncoder(w).Encode(execResp{Output: strings.Join(coloredNames, "\n")})
			return
		}
		// Colorized long listing
		for _, name := range names {
			info, err := os.Stat(filepath.Join(realCwd, name))
			if err != nil {
				continue
			}
			// Format the long listing with colorized filename
			longEntry := formatLong(info, colorizeName(info, name))
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
		if info.Size() > s.catMax {
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("cat: file too large (%d > limit %d)", info.Size(), s.catMax)})
			return
		}
		f, err := os.Open(rp)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: cannot open file"})
			return
		}
		defer f.Close()
		// read up to catMax bytes
		var buf bytes.Buffer
		if _, err := io.CopyN(&buf, f, s.catMax); err != nil && !errors.Is(err, io.EOF) {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: read error"})
			return
		}
		sample := buf.Bytes()
		if !looksText(sample) {
			_ = json.NewEncoder(w).Encode(execResp{Output: "cat: binary file (skipping)"})
			return
		}
		_ = json.NewEncoder(w).Encode(execResp{Output: string(sample)})
		return

	case "get", "rget", "download":
		if len(argv) < 1 {
			_ = json.NewEncoder(w).Encode(execResp{Output: "download: missing operand"})
			return
		}
		
		pattern := argv[0]
		
		// Check if pattern contains wildcards or is a directory
		if strings.ContainsAny(pattern, "*?[") || pattern == "." {
			// Handle pattern-based download (multiple files)
			files, err := s.collectFilesForDownload(sess.cwd, pattern)
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
				url := "/api/download?path=" + urlEscapeVirtual(files[0].virtualPath)
				_ = json.NewEncoder(w).Encode(execResp{Output: "", Download: url})
				return
			}
			// Multiple files, create zip
			url := "/api/download?pattern=" + urlQueryEscape(pattern) + "&cwd=" + urlEscapeVirtual(sess.cwd)
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("Downloading %d files as archive.zip", len(files)), Download: url})
			return
		}
		
		// Check if it's a directory
		vp := joinVirtual(sess.cwd, pattern)
		rp, err := s.realFromVirtual(vp)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "download: permission denied"})
			return
		}
		info, err := os.Stat(rp)
		if err != nil {
			_ = json.NewEncoder(w).Encode(execResp{Output: "download: no such file"})
			return
		}
		
		if info.IsDir() {
			// Download directory as zip
			files, err := s.collectFilesFromDirectory(vp, rp)
			if err != nil {
				_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("download: %v", err)})
				return
			}
			if len(files) == 0 {
				_ = json.NewEncoder(w).Encode(execResp{Output: "download: directory is empty"})
				return
			}
			dirName := filepath.Base(rp)
			url := "/api/download?dir=" + urlEscapeVirtual(vp)
			_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("Downloading directory '%s' with %d files as %s.zip", dirName, len(files), dirName), Download: url})
			return
		}
		
		// Single file download
		url := "/api/download?path=" + urlEscapeVirtual(vp)
		_ = json.NewEncoder(w).Encode(execResp{Output: "", Download: url})
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
		var searchPath string = sess.cwd
		var namePattern string = "*"
		var typeFilter string = "" // "f" for files, "d" for directories, "" for both

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
		
		// Get the host from the request
		host := r.Host
		if host == "" {
			host = "localhost:8080"
		}
		
		// Determine protocol (check if request came through HTTPS)
		protocol := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			protocol = "https"
		}
		
		// Build the full URL for the file
		fileURL := fmt.Sprintf("%s://%s/api/static%s", protocol, host, vp)
		
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
	defer file.Close()

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

// fileInfo holds information about a file for zip archive creation
type fileInfo struct {
	virtualPath string
	realPath    string
	relativePath string
}

// collectFilesForDownload collects files matching a pattern for download
func (s *server) collectFilesForDownload(cwd, pattern string) ([]fileInfo, error) {
	var files []fileInfo
	
	// Handle special case for current directory
	if pattern == "." {
		realCwd, err := s.realFromVirtual(cwd)
		if err != nil {
			return nil, err
		}
		return s.collectFilesFromDirectory(cwd, realCwd)
	}
	
	// Handle wildcard patterns
	if strings.ContainsAny(pattern, "*?[") {
		realCwd, err := s.realFromVirtual(cwd)
		if err != nil {
			return nil, err
		}
		
		// Check if pattern contains directory separator
		if strings.Contains(pattern, "/") {
			// Pattern includes path, need to handle directory traversal
			dir := filepath.Dir(pattern)
			filePattern := filepath.Base(pattern)
			
			vDir := joinVirtual(cwd, dir)
			rDir, err := s.realFromVirtual(vDir)
			if err != nil {
				return nil, err
			}
			
			entries, err := os.ReadDir(rDir)
			if err != nil {
				return nil, err
			}
			
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				
				matched, err := filepath.Match(filePattern, entry.Name())
				if err != nil || !matched {
					continue
				}
				
				realPath := filepath.Join(rDir, entry.Name())
				if s.shouldIgnore(realPath, entry.Name()) {
					continue
				}
				
				files = append(files, fileInfo{
					virtualPath:  path.Join(vDir, entry.Name()),
					realPath:     realPath,
					relativePath: entry.Name(),
				})
			}
		} else {
			// Pattern is just for files in current directory
			entries, err := os.ReadDir(realCwd)
			if err != nil {
				return nil, err
			}
			
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				
				matched, err := filepath.Match(pattern, entry.Name())
				if err != nil || !matched {
					continue
				}
				
				realPath := filepath.Join(realCwd, entry.Name())
				if s.shouldIgnore(realPath, entry.Name()) {
					continue
				}
				
				files = append(files, fileInfo{
					virtualPath:  path.Join(cwd, entry.Name()),
					realPath:     realPath,
					relativePath: entry.Name(),
				})
			}
		}
		
		return files, nil
	}
	
	// Not a pattern, might be a directory name
	vp := joinVirtual(cwd, pattern)
	rp, err := s.realFromVirtual(vp)
	if err != nil {
		return nil, err
	}
	
	info, err := os.Stat(rp)
	if err != nil {
		return nil, err
	}
	
	if info.IsDir() {
		return s.collectFilesFromDirectory(vp, rp)
	}
	
	// Single file
	files = append(files, fileInfo{
		virtualPath:  vp,
		realPath:     rp,
		relativePath: filepath.Base(rp),
	})
	
	return files, nil
}

// collectFilesFromDirectory recursively collects all files from a directory
func (s *server) collectFilesFromDirectory(virtualDir, realDir string) ([]fileInfo, error) {
	var files []fileInfo
	baseDir := filepath.Base(realDir)
	
	err := filepath.Walk(realDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}
		
		if info.IsDir() {
			return nil
		}
		
		// Check if file should be ignored
		if s.shouldIgnore(path, filepath.Base(path)) {
			return nil
		}
		
		// Skip hidden files
		if strings.HasPrefix(filepath.Base(path), ".") {
			return nil
		}
		
		relPath, err := filepath.Rel(realDir, path)
		if err != nil {
			return nil
		}
		
		// Create path with directory name as prefix
		archivePath := filepath.Join(baseDir, relPath)
		
		files = append(files, fileInfo{
			virtualPath:  path,
			realPath:     path,
			relativePath: archivePath,
		})
		
		return nil
	})
	
	if err != nil {
		return nil, err
	}
	
	return files, nil
}

// sendZipArchive creates and sends a zip archive containing the specified files
func (s *server) sendZipArchive(w http.ResponseWriter, files []fileInfo, filename string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	
	zipWriter := zip.NewWriter(w)
	defer zipWriter.Close()
	
	for _, file := range files {
		// Open the file
		f, err := os.Open(file.realPath)
		if err != nil {
			continue // Skip files we can't open
		}
		
		info, err := f.Stat()
		if err != nil {
			f.Close()
			continue
		}
		
		// Create zip file header
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			f.Close()
			continue
		}
		
		// Use the relative path for the archive
		header.Name = file.relativePath
		header.Method = zip.Deflate
		
		// Create the file in the zip
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			f.Close()
			continue
		}
		
		// Copy file content to zip
		_, err = io.Copy(writer, f)
		f.Close()
		
		if err != nil {
			continue // Skip files with copy errors
		}
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
		parts[i] = urlQueryEscape(p)
	}
	return "/" + strings.Join(parts, "/")
}

func urlQueryEscape(s string) string {
	// minimal escape to keep path segments safe in query
	repl := strings.NewReplacer(
		" ", "%20",
		"#", "%23",
		"?", "%3F",
		"&", "%26",
		"+", "%2B",
		"%", "%25",
	)
	return repl.Replace(s)
}

func (s *server) handleDownload(w http.ResponseWriter, r *http.Request) {
	sess := s.getSession(w, r)
	
	// Check if it's a single file download
	if path := r.URL.Query().Get("path"); path != "" {
		// Single file download
		vp := cleanVirtual(path)
		rp, err := s.realFromVirtual(joinVirtual(sess.cwd, vp))
		if err != nil {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		info, err := os.Stat(rp)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if info.IsDir() {
			http.Error(w, "is a directory", http.StatusBadRequest)
			return
		}
		f, err := os.Open(rp)
		if err != nil {
			http.Error(w, "cannot open", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		filename := filepath.Base(rp)
		ctype := mime.TypeByExtension(filepath.Ext(filename))
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		http.ServeContent(w, r, filename, info.ModTime(), f)
		return
	}
	
	// Check if it's a directory download
	if dir := r.URL.Query().Get("dir"); dir != "" {
		vp := cleanVirtual(dir)
		rp, err := s.realFromVirtual(vp)
		if err != nil {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		info, err := os.Stat(rp)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !info.IsDir() {
			http.Error(w, "not a directory", http.StatusBadRequest)
			return
		}
		
		files, err := s.collectFilesFromDirectory(vp, rp)
		if err != nil {
			http.Error(w, "failed to collect files", http.StatusInternalServerError)
			return
		}
		
		dirName := filepath.Base(rp)
		s.sendZipArchive(w, files, dirName+".zip")
		return
	}
	
	// Pattern-based download
	if pattern := r.URL.Query().Get("pattern"); pattern != "" {
		cwd := r.URL.Query().Get("cwd")
		if cwd == "" {
			cwd = sess.cwd
		}
		
		files, err := s.collectFilesForDownload(cwd, pattern)
		if err != nil {
			http.Error(w, "failed to collect files", http.StatusInternalServerError)
			return
		}
		
		if len(files) == 0 {
			http.Error(w, "no matching files found", http.StatusNotFound)
			return
		}
		
		s.sendZipArchive(w, files, "archive.zip")
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
		if req.FilesOnly && isDir {
			continue
		}

		if req.TextOnly || req.MaxSize > 0 {
			if !isDir {
				info, err := e.Info()
				if err != nil {
					continue
				}
				if req.MaxSize > 0 && info.Size() > req.MaxSize {
					continue
				}
				if req.TextOnly {
					// read a small sample to check if it looks like text
					fp := filepath.Join(baseR, name)
					f, err := os.Open(fp)
					if err != nil {
						continue
					}
					sample := make([]byte, 4096)
					n, _ := f.Read(sample)
					_ = f.Close()
					if !looksText(sample[:n]) {
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
	var (
		printVersion = flag.Bool("version", false, "Print the version of this software and exits")
		addr         = flag.String("addr", "localhost:8080", "address to listen on")
		dir          = flag.String("dir", ".", "directory to expose as root")
		catMax       = flag.Int64("catmax", 256*1024, "max bytes printable via `cat` and used by completion")
	)
	flag.Parse()

	if *printVersion {
		fmt.Printf("lsget %s\n", version)
		fmt.Println("Tiny Go-powered web server with a full‑screen, neon‑themed browser terminal.")
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

	s := newServer(rootAbs, *catMax)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/exec", s.handleExec)
	mux.HandleFunc("/api/complete", s.handleComplete)
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.HandleFunc("/api/static/", s.handleStaticFile)
	mux.HandleFunc("/", s.handleIndex) // Catch-all route must be last

	fmt.Printf("Serving %s on http://%s  (cat max = %d bytes)\n", rootAbs, *addr, *catMax)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := listenAndServe(srv); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		exitFunc(1)
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		d := time.Since(start)
		fmt.Printf("%s %s %s\n", r.Method, r.URL.Path, d.Truncate(time.Millisecond))
	})
}
