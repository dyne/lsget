package lsget

import (
	"bytes"
	"errors"
	"html/template"
	"net/url"
	"strconv"
	"strings"
)

// Keep the title in index.html as the fallback for the editable dev template.
const defaultSiteTitle = "📂 lsget"

type siteMetadata struct {
	Title        string
	Description  string
	PreviewImage string
}

var siteMetadataTemplate = template.Must(template.New("site metadata").Parse(`<title>{{.Title}}</title>
    <meta name="description" content="{{.Description}}" />
    <meta property="og:title" content="{{.Title}}" />
    <meta property="og:description" content="{{.Description}}" />
    <meta property="og:type" content="website" />{{if .CanonicalURL}}
    <link rel="canonical" href="{{.CanonicalURL}}" />
    <meta property="og:url" content="{{.CanonicalURL}}" />{{end}}{{if .PreviewImage}}
    <meta property="og:image" content="{{.PreviewImage}}" />{{end}}`))

func (s *server) renderSiteMetadata(initialPath string) ([]byte, error) {
	data := struct {
		siteMetadata
		CanonicalURL string
	}{siteMetadata: s.siteMetadata}
	if data.Title == "" {
		data.Title = defaultSiteTitle
	}
	if data.Description == "" {
		data.Description = "Browse and download files with lsget."
	}
	data.CanonicalURL = s.directoryCanonicalURL(initialPath)
	var out bytes.Buffer
	err := siteMetadataTemplate.Execute(&out, data)
	return out.Bytes(), err
}

func (s *server) directoryCanonicalURL(virtualPath string) string {
	if s.baseURL != "" {
		if base, err := sitemapBaseURL(s.baseURL); err == nil {
			return base + urlEscapeVirtual(virtualPath)
		}
	}
	return ""
}

func parsePreviewImageURL(raw string) (string, error) {
	invalid := errors.New("expected an absolute HTTP(S) URL without credentials")
	image, err := url.Parse(raw)
	if err != nil || (image.Scheme != "http" && image.Scheme != "https") || image.Hostname() == "" ||
		image.User != nil || strings.ContainsAny(raw, " \\") || strings.HasSuffix(image.Host, ":") {
		return "", invalid
	}
	if port := image.Port(); port != "" {
		if number, err := strconv.ParseUint(port, 10, 16); err != nil || number == 0 {
			return "", invalid
		}
	}
	if _, err := url.QueryUnescape(image.RawQuery); err != nil {
		return "", invalid
	}
	return image.String(), nil
}
