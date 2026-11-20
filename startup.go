package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const shutdownTimeout = 5 * time.Second

type config struct {
	version             bool
	addr                string
	dir                 string
	catMax              int64
	devTemplate         string
	shellCSP            string
	secureSessionCookie bool
	pidFile             string
	logFile             string
	baseURL             string
	trustedProxies      []netip.Prefix
	sitemapInterval     time.Duration
}

func parseConfig(args []string, getenv func(string) string, usage io.Writer) (config, error) {
	var cfg config
	var sitemapMinutes int64
	var proxyCIDRs string
	flags := flag.NewFlagSet("lsget", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&cfg.version, "version", false, "Print the version of this software and exits")
	flags.StringVar(&cfg.addr, "addr", "localhost:8080", "address to listen on (env: LSGET_ADDR)")
	flags.StringVar(&cfg.dir, "dir", ".", "directory to expose as root (env: LSGET_DIR)")
	flags.Int64Var(&cfg.catMax, "catmax", 4*1024, "max bytes printable via `cat` and used by completion (env: LSGET_CATMAX)")
	flags.StringVar(&cfg.devTemplate, "dev-template", "", "use a local application template for development (env: LSGET_DEV_TEMPLATE)")
	flags.StringVar(&cfg.shellCSP, "shell-csp", "enforce", "application CSP mode: enforce or report-only (env: LSGET_SHELL_CSP)")
	flags.BoolVar(&cfg.secureSessionCookie, "secure-session-cookie", false, "require HTTPS for session cookies behind an HTTPS-only proxy (env: LSGET_SECURE_SESSION_COOKIE)")
	flags.StringVar(&cfg.pidFile, "pid", "", "path to PID file (env: LSGET_PID)")
	flags.StringVar(&cfg.logFile, "logfile", "", "path to log file for statistics (env: LSGET_LOGFILE)")
	flags.StringVar(&cfg.baseURL, "baseurl", "", "base URL for the site - full URL without trailing slash (e.g., https://files.example.com) (env: LSGET_BASEURL)")
	flags.StringVar(&proxyCIDRs, "trusted-proxies", "", "comma-separated CIDRs allowed to supply forwarded metadata (env: LSGET_TRUSTED_PROXIES)")
	flags.Int64Var(&sitemapMinutes, "sitemap", 0, "refresh the in-memory sitemap every N minutes (0 = on demand only) (env: LSGET_SITEMAP)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(usage)
			flags.Usage()
		}
		return config{}, err
	}
	if cfg.version {
		return cfg, nil
	}

	provided := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	source := func(name string) string {
		if provided[name] {
			return "-" + name
		}
		return "LSGET_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	}
	var envErr error
	flags.VisitAll(func(f *flag.Flag) {
		if envErr != nil || provided[f.Name] || f.Name == "version" {
			return
		}
		key := source(f.Name)
		raw := getenv(key)
		if raw == "" {
			return
		}
		if f.Name == "catmax" || f.Name == "sitemap" {
			// CLI integers use the flag package's base prefixes. Environment
			// integers have historically been decimal, including leading zeros.
			number, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil {
				envErr = fmt.Errorf("%s must be a decimal integer, got %q", key, raw)
				return
			}
			raw = strconv.FormatInt(number, 10)
		}
		if err := flags.Set(f.Name, raw); err != nil {
			envErr = fmt.Errorf("invalid %s value %q: %w", key, raw, err)
		}
	})
	if envErr != nil {
		return config{}, envErr
	}
	var err error
	cfg.trustedProxies, err = parseTrustedProxies(proxyCIDRs)
	if err != nil {
		return config{}, fmt.Errorf("%s: %w", source("trusted-proxies"), err)
	}
	if cfg.catMax < 0 {
		return config{}, fmt.Errorf("%s must be nonnegative, got %d", source("catmax"), cfg.catMax)
	}
	const maxSitemapMinutes = int64(math.MaxInt64 / time.Minute)
	if sitemapMinutes < 0 || sitemapMinutes > maxSitemapMinutes {
		return config{}, fmt.Errorf("%s must be between 0 and %d minutes, got %d", source("sitemap"), maxSitemapMinutes, sitemapMinutes)
	}
	cfg.sitemapInterval = time.Duration(sitemapMinutes) * time.Minute
	if cfg.baseURL != "" {
		if _, err := sitemapBaseURL(cfg.baseURL); err != nil {
			return config{}, fmt.Errorf("%s: %w", source("baseurl"), err)
		}
	}
	if cfg.shellCSP != "enforce" && cfg.shellCSP != "report-only" {
		return config{}, fmt.Errorf("%s must be enforce or report-only, got %q", source("shell-csp"), cfg.shellCSP)
	}
	if cfg.addr == "" {
		cfg.addr = ":http" // The default for an explicitly empty http.Server.Addr.
	}
	if _, _, err := net.SplitHostPort(cfg.addr); err != nil {
		return config{}, fmt.Errorf("invalid %s: %w", source("addr"), err)
	}
	return cfg, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runCLI(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func runCLI(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, err := parseConfig(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return 2
	}
	if cfg.version {
		_, _ = fmt.Fprintf(stdout, `lsget %s
A lightweight CDN UI: Self-hosted file server with a full‑screen, neon‑themed TUI in your browser.

Copyright © 2025 Dyne.org foundation, Amsterdam
Licensed under GNU Affero General Public License v3.0

Repository: https://github.com/dyne/lsget
Website:    https://dyne.org
`, version)
		return 0
	}
	if err := serveConfigured(ctx, cfg, stdout, stderr); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func serveConfigured(ctx context.Context, cfg config, stdout, stderr io.Writer) (result error) {
	root, err := filepath.Abs(cfg.dir)
	if err != nil {
		return fmt.Errorf("configuration error: failed to resolve dir %q: %w", cfg.dir, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("configuration error: failed to access dir %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("configuration error: dir is not a directory: %q", root)
	}

	if cfg.logFile != "" {
		if err := validateAccessLog(cfg.logFile); err != nil {
			return fmt.Errorf("configuration error: failed to open log file %q: %w", cfg.logFile, err)
		}
	}
	if cfg.pidFile != "" {
		pid, err := claimPIDFile(cfg.pidFile)
		if err != nil {
			return fmt.Errorf("configuration error: failed to create PID file: %w", err)
		}
		defer func() { result = errors.Join(result, pid.remove()) }()
	}

	var listenerConfig net.ListenConfig
	listener, err := listenerConfig.Listen(ctx, "tcp", cfg.addr)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("server error: failed to listen on %q: %w", cfg.addr, err)
	}
	defer func() { _ = listener.Close() }()

	s := newServer(root, cfg.catMax, cfg.logFile, cfg.baseURL)
	s.devTemplate = cfg.devTemplate
	s.shellCSPReportOnly = cfg.shellCSP == "report-only"
	s.secureSessionCookie = cfg.secureSessionCookie
	s.trustedProxies = cfg.trustedProxies
	return serveHTTP(ctx, s, listener, cfg.sitemapInterval, stdout, stderr)
}

func serveHTTP(ctx context.Context, s *server, listener net.Listener, interval time.Duration, stdout, stderr io.Writer) error {
	background, cancel := context.WithCancel(ctx)
	defer cancel()
	sessionsDone := make(chan struct{})
	go func() {
		defer close(sessionsDone)
		s.runSessionCleanup(background)
	}()
	ready, sitemapDone := s.startSitemapGenerator(background, interval, stdout, stderr)
	srv := s.httpServer(listener.Addr().String())
	var serveResult chan error
	var serveErr error
	select {
	case <-ready:
		if ctx.Err() == nil {
			_, _ = fmt.Fprintf(stdout, "Serving %s on http://%s  (cat max = %d bytes)\n", s.rootAbs, srv.Addr, s.catMax)
			if s.logfile != "" {
				_, _ = fmt.Fprintf(stdout, "Logging to: %s\n", s.logfile)
			} else {
				_, _ = fmt.Fprintln(stdout, "Logging disabled (use -logfile or LSGET_LOGFILE to enable)")
			}
			serveResult = make(chan error, 1)
			go func() { serveResult <- srv.Serve(listener) }()
			select {
			case serveErr = <-serveResult:
				serveResult = nil
			case <-ctx.Done():
			}
		}
	case <-ctx.Done():
	}

	if ctx.Err() != nil {
		_, _ = fmt.Fprintln(stdout, "Shutting down server...")
	}
	cancel()
	if serveResult == nil {
		// Shutdown only owns listeners registered with Serve.
		_ = listener.Close()
	}
	deadline, stop := context.WithTimeout(context.Background(), shutdownTimeout)
	defer stop()
	if err := srv.Shutdown(deadline); err != nil {
		_, _ = fmt.Fprintf(stderr, "server shutdown error: %v\n", err)
		if err := srv.Close(); err != nil {
			_, _ = fmt.Fprintf(stderr, "server close error: %v\n", err)
		}
	}
	if serveResult != nil {
		serveErr = <-serveResult
	}
	for _, done := range []<-chan struct{}{sessionsDone, sitemapDone} {
		select {
		case <-done:
		case <-deadline.Done():
			select {
			case <-done:
			default:
				// A filesystem operation blocked in the OS must not keep the
				// process from removing its PID file and exiting.
				return fmt.Errorf("server shutdown error: background work did not finish: %w", deadline.Err())
			}
		}
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("server error: %w", serveErr)
	}
	return nil
}
