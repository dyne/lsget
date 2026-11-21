package lsget

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func (c *terminalCommand) cat() {
	ctx := c.request.Context()
	if len(c.args) < 1 {
		c.respond(execResp{Output: "cat: missing operand"})
		return
	}
	cwd, ok := c.cwdFor(c.args[0])
	if !ok {
		return
	}
	vp := joinVirtual(cwd, c.args[0])
	filePath := rootedName(vp)
	info, err := visibleInfo(c.root, filePath)
	if err != nil {
		c.respond(execResp{Output: "cat: no such file or directory"})
		return
	}
	if info.IsDir() {
		c.respond(execResp{Output: "cat: is a directory"})
		return
	}

	// Check if file type is supported by cat
	category := getFileCategory(c.args[0])

	// Display images inline
	if category == FileCategoryImage {
		image := execImage{URL: urlEscapeVirtual(vp), Name: filepath.Base(c.args[0]), Size: formatHumanSize(info.Size())}
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
		c.respond(execResp{HTML: imgHTML, Image: &image})
		return
	}

	// Only text files and unknown files (to be checked by content) can be displayed
	if category != FileCategoryText && category != FileCategoryUnknown {
		c.respond(execResp{Output: fmt.Sprintf("cat: cannot display %s files (use 'get' to download)", category)})
		return
	}

	f, info, err := openVisibleFile(c.root, filePath)
	if err != nil {
		c.respond(execResp{Output: "cat: cannot open file"})
		return
	}
	defer func() { _ = f.Close() }()
	limit := min(c.server.catMax, int64(maxCommandText))
	if info.Size() > limit {
		c.respond(execResp{Output: fmt.Sprintf("cat: file too large (%d > limit %d)", info.Size(), limit)})
		return
	}
	var buf bytes.Buffer
	_, err = io.CopyN(&buf, contextReader{ctx, f}, limit)
	if ctx.Err() != nil {
		return
	}
	if err != nil && !errors.Is(err, io.EOF) {
		c.respond(execResp{Output: "cat: read error"})
		return
	}
	sample := buf.Bytes()
	if !looksText(sample) {
		c.respond(execResp{Output: "cat: binary file (use 'get' to download)"})
		return
	}
	c.respond(execResp{Output: string(sample)})
}

func (c *terminalCommand) download() {
	ctx := c.request.Context()
	if len(c.args) < 1 {
		c.respond(execResp{Output: "download: missing operand"})
		return
	}

	pattern := c.args[0]
	root, err := os.OpenRoot(c.server.rootAbs)
	if err != nil {
		c.respond(execResp{Output: "download: cannot open served directory"})
		return
	}
	defer func() { _ = root.Close() }()
	cwd, ok := c.cwdFor(pattern)
	if !ok {
		return
	}

	// Check if pattern contains wildcards or is a directory
	if strings.ContainsAny(pattern, "*?[") || pattern == "." {
		// Handle pattern-based download (multiple files)
		release := c.server.reserveWork(c.writer, c.request)
		if release == nil {
			return
		}
		defer release()
		files, err := collectFilesForDownload(ctx, c.budget, root, cwd, pattern)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.respond(execResp{Output: fmt.Sprintf("download: %v", err), Truncated: errors.Is(err, errWorkLimit)})
			return
		}
		if len(files) == 0 {
			c.respond(execResp{Output: "download: no matching files found"})
			return
		}
		if len(files) == 1 {
			// Single file, download directly
			recordCommand(c.writer, "get", files[0].virtualPath)
			downloadURL := "/api/download?path=" + url.QueryEscape(files[0].virtualPath)
			c.respond(execResp{Output: "", Download: downloadURL})
			return
		}
		// Multiple files, create zip
		recordCommand(c.writer, "get", "(pattern match)")
		downloadURL := "/api/download?pattern=" + url.QueryEscape(pattern) + "&cwd=" + url.QueryEscape(cwd)
		c.respond(execResp{Output: fmt.Sprintf("Downloading %d files as archive.zip", len(files)), Download: downloadURL})
		return
	}

	// Check if it's a directory
	vp := joinVirtual(cwd, pattern)
	name := rootedName(vp)
	info, err := visibleInfo(root, name)
	if err != nil {
		c.respond(execResp{Output: "download: no such file"})
		return
	}

	if info.IsDir() {
		// Download directory as zip
		release := c.server.reserveWork(c.writer, c.request)
		if release == nil {
			return
		}
		defer release()
		files, err := collectFilesFromDirectory(ctx, c.budget, root, name)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.respond(execResp{Output: fmt.Sprintf("download: %v", err), Truncated: errors.Is(err, errWorkLimit)})
			return
		}
		if len(files) == 0 {
			c.respond(execResp{Output: "download: directory is empty"})
			return
		}
		dirName := downloadDirectoryName(root, name)
		recordCommand(c.writer, "get", vp+" (dir)")
		downloadURL := "/api/download?dir=" + url.QueryEscape(vp)
		c.respond(execResp{Output: fmt.Sprintf("Downloading directory '%s' with %d files as %s.zip", dirName, len(files), dirName), Download: downloadURL})
		return
	}

	// Single file download
	if !info.Mode().IsRegular() {
		c.respond(execResp{Output: "download: no such file"})
		return
	}
	recordCommand(c.writer, "get", vp)
	downloadURL := "/api/download?path=" + url.QueryEscape(vp)
	c.respond(execResp{Output: "", Download: downloadURL})
}

func (c *terminalCommand) share() {
	if len(c.args) < 1 {
		c.respond(execResp{Output: "url: missing file operand"})
		return
	}
	cwd, ok := c.cwdFor(c.args[0])
	if !ok {
		return
	}

	vp := joinVirtual(cwd, c.args[0])
	info, err := visibleInfo(c.root, rootedName(vp))
	if err != nil {
		c.respond(execResp{Output: "url: no such file or directory"})
		return
	}

	if info.IsDir() {
		c.respond(execResp{Output: "url: cannot share directories (use 'get' to download as zip)"})
		return
	}

	base, err := c.server.publicBaseURL(c.request)
	if err != nil {
		c.respond(execResp{Output: "url: " + err.Error()})
		return
	}
	fileURL := base + urlEscapeVirtual(vp)
	recordCommand(c.writer, c.name, vp)

	output := "Shareable URL: " + fileURL
	if c.requestedCWD == "" {
		output += "\n" + colorGreen + "URL copied to clipboard!" + colorReset
	}
	c.respond(execResp{
		Output:    output,
		Clipboard: fileURL,
	})
}

func (c *terminalCommand) checksum() {
	ctx := c.request.Context()
	if len(c.args) < 1 {
		c.respond(execResp{Output: "sum: missing file operand"})
		return
	}
	cwd, ok := c.cwdFor(c.args[0])
	if !ok {
		return
	}

	vp := joinVirtual(cwd, c.args[0])
	f, _, err := openVisibleFile(c.root, rootedName(vp))
	if errors.Is(err, errDownloadDirectory) {
		c.respond(execResp{Output: "sum: is a directory"})
		return
	}
	if err != nil {
		message := "sum: cannot open file"
		if errors.Is(err, fs.ErrNotExist) {
			message = "sum: no such file or directory"
		}
		c.respond(execResp{Output: message})
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
		c.respond(execResp{Output: "sum: error reading file"})
		return
	}

	md5Sum := hex.EncodeToString(md5Hash.Sum(nil))
	sha256Sum := hex.EncodeToString(sha256Hash.Sum(nil))

	// Log the checksum command
	recordCommand(c.writer, c.name, vp)

	output := fmt.Sprintf("MD5:    %s\nSHA256: %s", md5Sum, sha256Sum)
	c.respond(execResp{Output: output})
}
