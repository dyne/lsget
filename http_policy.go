package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	maxCommandBodyBytes = 8 * 1024
	maxRequestHeaders   = 64 * 1024
	requestHeaderLimit  = 5 * time.Second
	requestReadLimit    = 10 * time.Second
	keepAliveIdleLimit  = 60 * time.Second
)

func (s *server) httpServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", allowMethods(s.handleConfig, http.MethodGet))
	mux.HandleFunc("/api/exec", allowMethods(s.handleExec, http.MethodPost))
	mux.HandleFunc("/api/complete", allowMethods(s.handleComplete, http.MethodPost))
	for pattern, handler := range map[string]http.HandlerFunc{
		"/api/download":            s.handleDownload,
		"/api/static/":             s.handleStaticFile,
		"/sitemap.xml":             s.handleSitemap,
		"/assets/js/marked.min.js": s.handleVendoredMarked,
		"/assets/js/datastar.js":   s.handleVendoredDatastar,
		"/":                        s.handleIndex,
	} {
		mux.HandleFunc(pattern, allowMethods(handler, http.MethodGet, http.MethodHead))
	}
	return &http.Server{
		Addr:              addr,
		Handler:           s.logRequests(mux),
		MaxHeaderBytes:    maxRequestHeaders,
		ReadHeaderTimeout: requestHeaderLimit,
		ReadTimeout:       requestReadLimit,
		IdleTimeout:       keepAliveIdleLimit,
		// A fixed WriteTimeout would cut off progressing downloads.
	}
}

func allowMethods(handler http.HandlerFunc, methods ...string) http.HandlerFunc {
	allow := strings.Join(methods, ", ")
	return func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(methods, r.Method) {
			w.Header().Set("Allow", allow)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler(w, r)
	}
}

func (s *server) readCommandRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !s.acceptSessionOrigin(r) {
		http.Error(w, "forbidden request origin", http.StatusForbidden)
		return false
	}
	if r.ContentLength > maxCommandBodyBytes {
		rejectLargeCommand(w, r)
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCommandBodyBytes))
	var tooLarge *http.MaxBytesError
	var readErr net.Error
	switch {
	case errors.As(err, &tooLarge):
		rejectLargeCommand(w, r)
	case errors.As(err, &readErr) && readErr.Timeout():
		if r.ProtoMajor == 1 {
			w.Header().Set("Connection", "close")
		}
		http.Error(w, "request body read timed out", http.StatusRequestTimeout)
	case err != nil:
		http.Error(w, "bad request", http.StatusBadRequest)
	default:
		body = bytes.TrimLeft(body, " \t\r\n")
		if len(body) != 0 && body[0] == '{' && json.Unmarshal(body, dst) == nil {
			return true
		}
		http.Error(w, "bad request", http.StatusBadRequest)
	}
	return false
}

func rejectLargeCommand(w http.ResponseWriter, r *http.Request) {
	// Flush the rejection before net/http cleans up the unread HTTP/1 body.
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "close")
	}
	http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
}

func (s *server) acceptSessionOrigin(r *http.Request) bool {
	fetchSite := r.Header.Values("Sec-Fetch-Site")
	if len(fetchSite) != 0 && (len(fetchSite) != 1 || fetchSite[0] != "same-origin") {
		return false
	}
	if origins := r.Header.Values("Origin"); len(origins) != 0 {
		return len(origins) == 1 && s.matchesRequestOrigin(r, origins[0])
	}
	if len(fetchSite) == 1 {
		return true
	}
	if _, err := r.Cookie("sid"); errors.Is(err, http.ErrNoCookie) {
		return true
	}
	// A browser needs a successful CORS preflight to send either of these
	// headers cross-origin. Cookie-bearing CLI requests can send them directly.
	if r.Method == http.MethodPost {
		values := r.Header.Values("Content-Type")
		if len(values) != 1 {
			return false
		}
		mediaType, _, err := mime.ParseMediaType(values[0])
		return err == nil && mediaType == "application/json"
	}
	values := r.Header.Values("X-Lsget-Request")
	return len(values) == 1 && values[0] == "1"
}

func (s *server) matchesRequestOrigin(r *http.Request, value string) bool {
	origin, err := url.Parse(value)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") ||
		origin.Hostname() == "" || value != origin.Scheme+"://"+origin.Host {
		return false
	}
	scheme := "http"
	// The operator can assert HTTPS-only access through a proxy that preserves Host.
	if r.TLS != nil || s.secureSessionCookie {
		scheme = "https"
	}
	if sameHTTPOrigin(origin, &url.URL{Scheme: scheme, Host: r.Host}) {
		return true
	}
	publicURL, err := url.Parse(s.baseURL)
	return err == nil && publicURL.User == nil && sameHTTPOrigin(origin, publicURL)
}

func sameHTTPOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if explicit := u.Port(); explicit != "" {
			return explicit
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}
