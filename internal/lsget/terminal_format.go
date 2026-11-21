package lsget

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

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
