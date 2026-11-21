package lsget

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

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
