package lsget

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

func (c *terminalCommand) printWorkingDirectory() {
	cwd, ok := c.cwdFor("")
	if !ok {
		return
	}
	c.respond(execResp{Output: cwd, CWD: cwd})
}

func (c *terminalCommand) list() {
	ctx := c.request.Context()
	// The parent marker depends on CWD even with an explicit listing path.
	cwd, ok := c.cwdFor("")
	if !ok {
		return
	}
	long := false
	showHidden := false
	humanReadable := false
	target := cwd
	// Parse arguments: flags and optional path
	for _, arg := range c.args {
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
	info, err := visibleInfo(c.root, name)
	if err != nil {
		c.respond(execResp{Output: "ls: cannot access '" + target + "': No such file or directory"})
		return
	}
	// If path is a file, show just the file
	if !info.IsDir() {
		// If it's a file, show the file in the listing with absolute virtual path
		displayName := virtualPath
		if long {
			c.respond(execResp{Output: formatLong(info, colorizeName(info, displayName), humanReadable)})
		} else {
			c.respond(execResp{Output: colorizeName(info, displayName)})
		}
		return
	}
	// It is a directory, show its contents
	children, err := visibleChildren(ctx, c.budget, c.root, name)
	if ctx.Err() != nil {
		return
	}
	if err != nil && !errors.Is(err, errWorkLimit) {
		c.respond(execResp{Output: "ls: error"})
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
	c.respond(output.response("", err))
}

func (c *terminalCommand) changeDirectory() {
	ctx := c.request.Context()
	target := "/"
	if len(c.args) == 1 {
		target = c.args[0]
		if target == "" {
			target = "/"
		}
	}
	var newV string
	var err error
	if c.requestedCWD != "" {
		newV, err = resolveDirectory(c.root, c.requestedCWD, target)
	} else {
		c.session = c.server.acquireSession(c.writer, c.request)
		if c.session == nil {
			return
		}
		newV, err = c.session.changeDirectory(c.root, target)
	}
	if err != nil {
		c.respond(execResp{Output: "cd: " + err.Error()})
		return
	}
	name := rootedName(newV)

	// Check if directory contains index.html
	indexPath := path.Join(name, "index.html")
	if info, err := visibleInfo(c.root, indexPath); err == nil && info.Mode().IsRegular() {
		// Directory has index.html, redirect to it
		c.respond(execResp{Redirect: newV})
		return
	}

	// The browser asks for the preview separately so navigation does not wait for it.
	response := execResp{CWD: newV}
	if c.request.URL.Query().Get("readme") != "0" {
		readme, docType, truncated := readDocFile(ctx, c.root, name)
		response.Readme, response.DocType, response.ReadmeTruncated = &readme, docType, truncated
	}
	if ctx.Err() != nil {
		return
	}
	c.respond(response)
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
