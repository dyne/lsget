package main

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	Output   string `json:"output"`
	Download string `json:"download,omitempty"`
	CWD      string `json:"cwd,omitempty"`
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
	CatMax int64 `json:"catMax"`
}

// ===== Handlers =====

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Serve from disk if available so you can iterate quickly.
	if b, err := os.ReadFile("index.html"); err == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
		return
	}
	// Fallback to embedded.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(embeddedIndex)
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(configResp{CatMax: s.catMax})
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
		_ = json.NewEncoder(w).Encode(execResp{Output: "", CWD: sess.cwd})
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
		vp := joinVirtual(sess.cwd, argv[0])
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
			_ = json.NewEncoder(w).Encode(execResp{Output: "download: is a directory"})
			return
		}
		url := "/api/download?path=" + urlEscapeVirtual(vp)
		_ = json.NewEncoder(w).Encode(execResp{Output: "", Download: url})
		return
	}

	_ = json.NewEncoder(w).Encode(execResp{Output: fmt.Sprintf("sh: %s: command not found", cmd)})
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
	q := r.URL.Query().Get("path")
	if q == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	// q is a virtual path (possibly like "/a/b.txt")
	vp := cleanVirtual(q)
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
		fmt.Println(version)
		os.Exit(0)
	}

	rootAbs, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve dir: %v\n", err)
		os.Exit(1)
	}
	info, err := os.Stat(rootAbs)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "dir is not a directory: %s\n", rootAbs)
		os.Exit(1)
	}

	s := newServer(rootAbs, *catMax)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/exec", s.handleExec)
	mux.HandleFunc("/api/complete", s.handleComplete)
	mux.HandleFunc("/api/download", s.handleDownload)

	fmt.Printf("Serving %s on http://%s  (cat max = %d bytes)\n", rootAbs, *addr, *catMax)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
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
