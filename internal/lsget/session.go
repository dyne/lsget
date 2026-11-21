package lsget

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	sessionIdleTimeout = 30 * time.Minute
	sessionSweepPeriod = time.Minute
	maxSessions        = 4096
)

type session struct {
	mu  sync.Mutex
	cwd string // virtual path, guarded by mu

	// The session map mutex guards request accounting and idle time.
	active   int
	idleFrom time.Time
}

func (s *session) workingDirectory() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func (s *session) changeDirectory(root *os.Root, target string) (string, error) {
	// Resolve and validate relative changes against the last committed CWD.
	s.mu.Lock()
	defer s.mu.Unlock()
	newV, err := resolveDirectory(root, s.cwd, target)
	if err == nil {
		s.cwd = newV
	}
	return newV, err
}

func newSID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b[:])
}

// acquireSession pins a session until the handler calls releaseSession.
func (s *server) acquireSession(w http.ResponseWriter, r *http.Request) *session {
	if ck, err := r.Cookie("sid"); err == nil && len(ck.Value) == 32 {
		if sess := s.resumeSession(ck.Value); sess != nil {
			return sess
		}
	}

	sess, id := s.createSession()
	if sess == nil {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "session capacity temporarily reached", http.StatusServiceUnavailable)
		return nil
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "sid",
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || s.secureSessionCookie,
	})
	return sess
}

func (s *server) resumeSession(id string) *session {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	sess := s.sessions[id]
	if sess == nil {
		return nil
	}
	if sess.expired(s.sessionNow()) {
		delete(s.sessions, id)
		return nil
	}
	sess.active++
	return sess
}

func (s *server) createSession() (*session, string) {
	id := newSID()
	s.sessionsMu.Lock()
	for s.sessions[id] != nil {
		s.sessionsMu.Unlock()
		id = newSID()
		s.sessionsMu.Lock()
	}
	defer s.sessionsMu.Unlock()
	if len(s.sessions) >= s.sessionLimit {
		var oldestID string
		var oldest *session
		for id, sess := range s.sessions {
			if sess.active == 0 && (oldest == nil || sess.idleFrom.Before(oldest.idleFrom)) {
				oldestID, oldest = id, sess
			}
		}
		if oldest == nil {
			return nil, ""
		}
		delete(s.sessions, oldestID)
	}
	sess := &session{cwd: "/", active: 1}
	s.sessions[id] = sess
	return sess, id
}

func (s *server) releaseSession(sess *session) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	sess.active--
	if sess.active == 0 {
		sess.idleFrom = s.sessionNow()
	}
}

func (s *session) expired(now time.Time) bool {
	return s.active == 0 && now.Sub(s.idleFrom) >= sessionIdleTimeout
}

func (s *server) expireSessions() {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	now := s.sessionNow()
	for id, sess := range s.sessions {
		if sess.expired(now) {
			delete(s.sessions, id)
		}
	}
}

func (s *server) runSessionCleanup(ctx context.Context) {
	ticker := time.NewTicker(sessionSweepPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.expireSessions()
		}
	}
}
