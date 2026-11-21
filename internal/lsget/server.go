package lsget

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

type server struct {
	rootAbs        string // absolute filesystem root we expose
	catMax         int64  // max bytes allowed for `cat`
	logfile        string // path to log file for statistics
	baseURL        string // optional: public base URL (e.g., https://files.example.com) - auto-detects from request if empty
	trustedProxies []netip.Prefix
	logMu          sync.Mutex
	lastLogWarning time.Time

	sessionsMu          sync.Mutex
	sessions            map[string]*session
	sessionNow          func() time.Time
	sessionLimit        int
	secureSessionCookie bool
	workSlots           chan struct{}

	devTemplate        string
	shellCSPReportOnly bool
	siteMetadata       siteMetadata

	sitemapMu sync.Mutex
	sitemap   atomic.Pointer[sitemapSnapshot]
}

func newServer(rootAbs string, catMax int64, logfile, baseURL string) *server {
	return &server{
		rootAbs:      rootAbs,
		catMax:       catMax,
		sessions:     make(map[string]*session),
		sessionNow:   time.Now,
		sessionLimit: maxSessions,
		workSlots:    make(chan struct{}, maxWorkJobs),
		logfile:      logfile,
		baseURL:      baseURL,
	}
}
