package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func (c *terminalCommand) tree() {
	ctx := c.request.Context()
	// Parse options
	showHidden := false
	maxDepth := -1 // unlimited by default
	target := ""

	for _, arg := range c.args {
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

	cwd, ok := c.cwdFor(target)
	if !ok {
		return
	}
	name := rootedName(joinVirtual(cwd, target))
	info, err := visibleInfo(c.root, name)
	if err != nil {
		c.respond(execResp{Output: "tree: no such file or directory"})
		return
	}

	if !info.IsDir() {
		c.respond(execResp{Output: "tree: not a directory"})
		return
	}

	var result boundedText
	dirCount, fileCount, err := buildTree(ctx, c.budget, c.root, &result, name, showHidden, maxDepth)
	if ctx.Err() != nil {
		return
	}
	if err != nil && !errors.Is(err, errWorkLimit) {
		c.respond(execResp{Output: "tree: cannot read directory"})
		return
	}
	if err == nil {
		err = result.line(fmt.Sprintf("\n%d directories, %d files", dirCount, fileCount))
	}
	c.respond(result.response("", err))
}

func (c *terminalCommand) find() {
	ctx := c.request.Context()
	// Parse options
	searchPath := ""
	namePattern := "*"
	typeFilter := "" // "f" for files, "d" for directories, "" for both

	// Parse arguments
	for i := 0; i < len(c.args); i++ {
		arg := c.args[i]
		if arg == "-name" && i+1 < len(c.args) {
			namePattern = c.args[i+1]
			i++ // skip next argument
		} else if arg == "-type" && i+1 < len(c.args) {
			typeFilter = c.args[i+1]
			i++ // skip next argument
		} else if !strings.HasPrefix(arg, "-") {
			// Path argument
			searchPath = arg
		}
	}

	// Validate type filter
	if typeFilter != "" && typeFilter != "f" && typeFilter != "d" {
		c.respond(execResp{Output: "find: invalid type filter (use 'f' for files or 'd' for directories)"})
		return
	}

	cwd, ok := c.cwdFor(searchPath)
	if !ok {
		return
	}
	name := rootedName(joinVirtual(cwd, searchPath))
	info, err := visibleInfo(c.root, name)
	if err != nil {
		c.respond(execResp{Output: "find: no such file or directory"})
		return
	}

	if !info.IsDir() {
		c.respond(execResp{Output: "find: not a directory"})
		return
	}

	var output boundedText
	err = findFiles(ctx, c.budget, c.root, name, namePattern, typeFilter, &output)
	if ctx.Err() != nil {
		return
	}
	if err != nil && !errors.Is(err, errWorkLimit) {
		c.respond(execResp{Output: "find: cannot read directory"})
		return
	}
	c.respond(output.response("find: no matches found", err))
}

func (c *terminalCommand) grep() {
	ctx := c.request.Context()
	if len(c.args) < 1 {
		c.respond(execResp{Output: "grep: missing pattern"})
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
	for i < len(c.args) {
		arg := c.args[i]
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
		c.respond(execResp{Output: "grep: missing pattern"})
		return
	}

	// If no files specified and recursive, search current directory
	if len(files) == 0 {
		if recursive {
			files = []string{"."}
		} else {
			c.respond(execResp{Output: "grep: no files specified"})
			return
		}
	}

	search := newGrepSearch(pattern, ignoreCase, showLineNumbers)
	var resultErr error
	for _, file := range files {
		if ctx.Err() != nil {
			return
		}
		if c.budget.entries == 0 {
			resultErr = errWorkLimit
			break
		}
		c.budget.entries--
		cwd, ok := c.cwdFor(file)
		if !ok {
			return
		}
		vp := joinVirtual(cwd, file)
		name := rootedName(vp)
		info, err := visibleInfo(c.root, name)
		switch {
		case err != nil:
			resultErr = search.output.line(fmt.Sprintf("grep: %s: no such file or directory", file))
		case info.IsDir() && recursive:
			resultErr = search.directory(ctx, c.budget, c.root, name)
			if resultErr != nil && !stopWork(resultErr) {
				resultErr = search.output.line(fmt.Sprintf("grep: %s: cannot read directory", file))
			}
		case info.IsDir():
			resultErr = search.output.line(fmt.Sprintf("grep: %s: is a directory", file))
		default:
			resultErr = search.file(ctx, c.budget, c.root, name, len(files) > 1)
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
	c.respond(search.output.response("grep: no matches found", resultErr))
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
