package lsget

import (
	"net/http"
	"slices"
	"strings"
	"time"
)

const (
	maxRequestHeaders  = 64 * 1024
	requestHeaderLimit = 5 * time.Second
	requestReadLimit   = 10 * time.Second
	keepAliveIdleLimit = 60 * time.Second
)

func (s *server) httpServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", allowMethods(s.handleConfig, http.MethodGet))
	mux.HandleFunc("/api/exec", allowMethods(s.handleExec, http.MethodPost))
	mux.HandleFunc("/api/complete", allowMethods(s.handleComplete, http.MethodPost))
	for pattern, handler := range map[string]http.HandlerFunc{
		"/api/download":                       s.handleDownload,
		"/api/static/":                        s.handleStaticFile,
		"/sitemap.xml":                        s.handleSitemap,
		shellAssetPrefix + "{version}/{name}": s.handleShellAsset,
		"/assets/js/marked.min.js":            s.handleVendoredMarked,
		"/assets/js/datastar.js":              s.handleVendoredDatastar,
		"/":                                   s.handleIndex,
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
