package lsget

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

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
