package lsget

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

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

func urlEscapeVirtual(v string) string {
	// Keep it URL-safe while preserving slashes in the virtual path.
	parts := strings.Split(strings.TrimPrefix(cleanVirtual(v), "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/" + strings.Join(parts, "/")
}
