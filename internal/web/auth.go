package web

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	siteCookie = "site_pw"
	testCookie = "test_auth"

	sessionMaxAge = 60 * 60 * 24 * 30 // 30 days
	testMaxAge    = 60 * 60 * 2       // 2 hours

	loginLogFile = "login.log"
)

// secretEqual compares in constant time, so a wrong password reveals nothing
// through timing.
func secretEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// authed reports whether the request carries the site password cookie.
func (s *Server) authed(r *http.Request) bool {
	cookie, err := r.Cookie(siteCookie)
	if err != nil {
		return false
	}
	return secretEqual(cookie.Value, s.cfg.Application.Password)
}

// requirePassword wraps a handler so unauthenticated requests are sent to the
// login page, which returns them here afterwards.
func (s *Server) requirePassword(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Redirect(w, r, "/login?next="+urlQueryEscape(r.URL.Path), http.StatusFound)
			return
		}
		next(w, r)
	}
}

// setAuthCookie issues the site session cookie.
func setAuthCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     siteCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// loginLog appends login attempts to login.log. It is separate from the
// application log so failed attempts are easy to review on their own.
type loginLog struct {
	mu   sync.Mutex
	file *os.File
}

func newLoginLog() *loginLog {
	file, err := os.OpenFile(loginLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Error("could not open login log; login attempts will not be recorded", "error", err)
		return &loginLog{}
	}
	return &loginLog{file: file}
}

// record logs one login attempt. The attempted password is recorded only on
// failure, which is what makes the log useful for spotting probing.
func (l *loginLog) record(r *http.Request, success bool, attemptedPassword string) {
	if l.file == nil {
		return
	}

	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.RemoteAddr
	}
	userAgent, _ := json.Marshal(r.Header.Get("User-Agent"))

	message := fmt.Sprintf("%s Login attempt: success=%t ip=%s user_agent=%s",
		time.Now().Format("2006-01-02 15:04:05,000"), success, ip, userAgent)
	if !success && attemptedPassword != "" {
		attempted, _ := json.Marshal(attemptedPassword)
		message += " attempted_password=" + string(attempted)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := fmt.Fprintln(l.file, message); err != nil {
		slog.Error("could not write to login log", "error", err)
	}
}

func (l *loginLog) Close() error {
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}
