package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	maxLogPath   = 2048
	maxLogDetail = 512
)

type responseLogger struct {
	http.ResponseWriter
	statusCode  int
	size        int64
	writeErr    error
	archiveErr  error
	command     string
	commandFile string
}

func (w *responseLogger) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseLogger) WriteHeader(code int) {
	if w.statusCode == 0 && (code == http.StatusSwitchingProtocols || code >= 200) {
		w.statusCode = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseLogger) Write(body []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(body)
	w.size += int64(n)
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	return n, err
}

func recordCommand(w http.ResponseWriter, command, file string) {
	if response, ok := w.(*responseLogger); ok {
		response.command, response.commandFile = command, file
	}
}

func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := &responseLogger{ResponseWriter: w}
		finished := false
		defer func() { s.logOutcome(r, response, finished) }()
		next.ServeHTTP(response, r)
		finished = true
		if response.archiveErr != nil {
			// An incomplete archive must not terminate like a successful response.
			panic(http.ErrAbortHandler)
		}
	})
}

func (s *server) logOutcome(r *http.Request, response *responseLogger, finished bool) {
	status := response.statusCode
	outcome := ""
	switch {
	case r.Context().Err() != nil:
		outcome = " outcome=canceled"
	case !finished || response.archiveErr != nil:
		outcome = " outcome=aborted"
	case response.writeErr != nil:
		outcome = " outcome=write_error"
	case status == 0:
		// net/http sends 200 when the handler returns without writing.
		status = http.StatusOK
	}
	if response.archiveErr != nil {
		outcome += " archive=incomplete error=" + quoteLogField(response.archiveErr.Error(), maxLogDetail)
	}

	bytes := "-"
	if response.size != 0 && r.Method != http.MethodHead {
		bytes = strconv.FormatInt(response.size, 10)
	}
	ip := s.clientIP(r)
	stamp := time.Now().Format("[02/Jan/2006:15:04:05 -0700]")
	request := logRequestLine(r.Method, r.URL.EscapedPath(), r.Proto)
	line := formatAccessLine(ip, stamp, request, status, bytes, referrerOrigin(r.Referer()), r.UserAgent(), outcome)
	fmt.Print(line)
	if s.logfile == "" {
		return
	}
	if response.command != "" {
		// Preserve the command event understood by existing statistics clients.
		// Its query is constructed here from a recognized command and file, never
		// copied from the request URL or raw terminal input.
		file := boundLogField(response.commandFile, maxLogDetail)
		command := "/api/exec?cmd=" + url.QueryEscape(response.command) + "&file=" + url.QueryEscape(file)
		request := logRequestLine(r.Method, command, r.Proto)
		line = formatAccessLine(ip, stamp, request, status, "0", "", "", outcome) + line
	}
	s.appendAccessLog(line)
}

func logRequestLine(method, target, protocol string) string {
	return strconv.QuoteToASCII(boundLogField(method, 32) + " " + boundLogField(target, maxLogPath) + " " + boundLogField(protocol, 16))
}

func formatAccessLine(ip, stamp, request string, status int, bytes, referer, agent, outcome string) string {
	return fmt.Sprintf("%s - - %s %s %03d %s %s %s%s\n", ip, stamp, request, status, bytes,
		quoteLogField(referer, maxLogDetail), quoteLogField(agent, maxLogDetail), outcome)
}

func quoteLogField(value string, limit int) string {
	if value == "" {
		return `"-"`
	}
	return strconv.QuoteToASCII(boundLogField(value, limit))
}

func boundLogField(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit-3] + "..."
}

func referrerOrigin(referer string) string {
	u, err := url.Parse(referer)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func openAccessLog(name string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return nil, err
	}
	if info, err := os.Stat(name); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

func validateAccessLog(name string) error {
	file, err := openAccessLog(name)
	if err != nil {
		return err
	}
	return file.Close()
}

func (s *server) appendAccessLog(lines string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	// Reopen for every request so a rename-based rotation takes effect without
	// a signal and both records for a request use the same destination.
	file, err := openAccessLog(s.logfile)
	if err == nil {
		_, writeErr := file.WriteString(lines)
		err = errors.Join(writeErr, file.Close())
	}
	if now := time.Now(); err != nil && now.Sub(s.lastLogWarning) >= time.Minute {
		s.lastLogWarning = now
		message := strings.TrimSpace(err.Error())
		fmt.Fprintf(os.Stderr, "failed to append access log %q: %s\n", s.logfile, quoteLogField(message, maxLogDetail))
	}
}
