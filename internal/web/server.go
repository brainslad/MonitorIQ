// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

// Package web serves the dashboard, the back-office and the public status pages.
package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brainslad/monitoriq/internal/auth"
	"github.com/brainslad/monitoriq/internal/config"
	"github.com/brainslad/monitoriq/internal/notify"
	"github.com/brainslad/monitoriq/internal/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	sessionCookie = "miq_session"
	flashCookie   = "miq_flash"
	oidcCookie    = "miq_oidc"
	sessionTTL    = 7 * 24 * time.Hour
	// partialTTL bounds how long a session may wait for its 2FA code.
	partialTTL = 10 * time.Minute
)

// Version is set by main.
var Version = "dev"

type Server struct {
	Store *store.Store
	Cfg   *config.Config
	Log   *slog.Logger
	Box   *auth.Box
	OIDC  *auth.OIDC
	// Notifier sends e-mail (set by main; tests may leave it nil).
	Notifier *notify.Notifier

	pages   map[string]*template.Template
	limiter *loginLimiter
}

func New(st *store.Store, cfg *config.Config, box *auth.Box, log *slog.Logger) (*Server, error) {
	s := &Server{Store: st, Cfg: cfg, Log: log, Box: box, OIDC: &auth.OIDC{},
		pages: map[string]*template.Template{}, limiter: newLoginLimiter()}
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) loadTemplates() error {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		return err
	}
	base := template.New("").Funcs(funcMap())
	base, err = base.ParseFS(templatesFS, "templates/_*.html")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "_") {
			continue
		}
		t, err := base.Clone()
		if err != nil {
			return err
		}
		if _, err := t.ParseFS(templatesFS, "templates/"+name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		s.pages[strings.TrimSuffix(name, ".html")] = t
	}
	return nil
}

// Handler builds the HTTP routing table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	fileServer := http.StripPrefix("/static/", http.FileServer(http.FS(static)))
	mux.Handle("GET /static/", cacheFor(24*time.Hour, fileServer))
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// Public
	mux.HandleFunc("GET /status/{slug}", s.handlePublicStatus)
	mux.HandleFunc("GET /api/status/{slug}", s.handlePublicStatusJSON)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /auth/oidc/login", s.handleOIDCLogin)
	mux.HandleFunc("GET /auth/oidc/callback", s.handleOIDCCallback)

	// Second factor: reachable by sessions that still wait for 2FA.
	mux.Handle("GET /login/2fa", s.requireSession(true, s.handleTwoFactorForm))
	mux.Handle("POST /login/2fa", s.requireSession(true, s.handleTwoFactor))
	mux.Handle("POST /login/cancel", s.requireSession(true, s.handleLogout))

	// Authenticated
	auth := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(false, h)) }
	auth("POST /logout", s.handleLogout)
	auth("GET /{$}", s.handleDashboard)
	auth("GET /monitors/{id}", s.handleMonitorDetail)
	auth("POST /monitors/{id}/check", s.handleMonitorCheckNow)
	auth("GET /api/monitors/{id}/series", s.handleSeries)
	auth("GET /api/dashboard/series", s.handleDashboardSeries)

	auth("GET /admin/hosts", s.handleHosts)
	auth("GET /admin/hosts/new", s.handleHostForm)
	auth("POST /admin/hosts/new", s.handleHostSave)
	auth("GET /admin/hosts/{id}/edit", s.handleHostForm)
	auth("POST /admin/hosts/{id}/edit", s.handleHostSave)
	auth("POST /admin/hosts/{id}/delete", s.handleHostDelete)

	auth("GET /admin/monitors", s.handleMonitors)
	auth("GET /admin/monitors/new", s.handleMonitorForm)
	auth("POST /admin/monitors/new", s.handleMonitorSave)
	auth("GET /admin/monitors/{id}/edit", s.handleMonitorForm)
	auth("POST /admin/monitors/{id}/edit", s.handleMonitorSave)
	auth("POST /admin/monitors/{id}/delete", s.handleMonitorDelete)
	auth("POST /admin/monitors/{id}/toggle", s.handleMonitorToggle)

	auth("GET /admin/status-pages", s.handleStatusPages)
	auth("GET /admin/status-pages/new", s.handleStatusPageForm)
	auth("POST /admin/status-pages/new", s.handleStatusPageSave)
	auth("GET /admin/status-pages/{id}/edit", s.handleStatusPageForm)
	auth("POST /admin/status-pages/{id}/edit", s.handleStatusPageSave)
	auth("POST /admin/status-pages/{id}/delete", s.handleStatusPageDelete)

	auth("GET /admin/users", s.handleUsers)
	auth("POST /admin/users", s.handleUserCreate)
	auth("POST /admin/users/{id}/delete", s.handleUserDelete)
	auth("POST /admin/users/{id}/reset-2fa", s.handleUserReset2FA)

	auth("GET /account", s.handleAccount)
	auth("POST /account/password", s.handlePasswordChange)
	auth("POST /account/2fa/enable", s.handleTOTPEnable)
	auth("POST /account/2fa/disable", s.handleTOTPDisable)
	auth("POST /account/2fa/recovery", s.handleRecoveryRegenerate)
	auth("POST /account/sessions/revoke", s.handleRevokeSessions)

	auth("GET /admin/settings", s.handleSettings)
	auth("POST /admin/settings", s.handleSettingsSave)
	auth("POST /admin/settings/purge", s.handlePurgeNow)
	auth("GET /admin/auth", s.handleAuthSettings)
	auth("POST /admin/auth", s.handleAuthSettingsSave)
	auth("POST /admin/auth/ldap-test", s.handleLDAPTest)
	auth("GET /admin/email", s.handleEmailSettings)
	auth("POST /admin/email", s.handleEmailSettingsSave)
	auth("POST /admin/email/test", s.handleEmailTest)
	auth("POST /account/email", s.handleAccountEmail)

	return s.securityHeaders(s.logRequests(mux))
}

// ---------------------------------------------------------------- middleware

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSession
	ctxToken
)

func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}

func sessionFrom(r *http.Request) *store.Session {
	sess, _ := r.Context().Value(ctxSession).(*store.Session)
	return sess
}

func csrfFrom(r *http.Request) string {
	if sess := sessionFrom(r); sess != nil {
		return sess.CSRF
	}
	return ""
}

func tokenFrom(r *http.Request) string {
	t, _ := r.Context().Value(ctxToken).(string)
	return t
}

// requireSession lets a request through only with a valid session. Sessions
// that still have to pass 2FA (or set it up) are sent to /login/2fa unless
// partial is true.
func (s *Server) requireSession(partial bool, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err == nil && c.Value != "" {
			if sess, err := s.Store.GetSession(r.Context(), c.Value); err == nil {
				if r.Method == http.MethodPost {
					if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(sess.CSRF)) != 1 {
						http.Error(w, "Invalid request (CSRF). Reload the page and try again.", http.StatusForbidden)
						return
					}
				}
				if sess.MFA != store.MFADone && !partial {
					if strings.HasPrefix(r.URL.Path, "/api/") || r.Method != http.MethodGet {
						http.Error(w, "Two-factor authentication required", http.StatusUnauthorized)
						return
					}
					http.Redirect(w, r, "/login/2fa?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
					return
				}
				ctx := context.WithValue(r.Context(), ctxSession, sess)
				ctx = context.WithValue(ctx, ctxToken, c.Value)
				if sess.MFA == store.MFADone {
					ctx = context.WithValue(ctx, ctxUser, sess.User)
				}
				next(w, r.WithContext(ctx))
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.Method != http.MethodGet {
			http.Error(w, "Not authenticated", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors "+
			map[bool]string{true: "*", false: "'self'"}[strings.HasPrefix(r.URL.Path, "/status/")])
		if !strings.HasPrefix(r.URL.Path, "/status/") {
			h.Set("X-Frame-Options", "SAMEORIGIN")
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) { r.code = code; r.ResponseWriter.WriteHeader(code) }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" {
			return
		}
		s.Log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.code,
			"ms", time.Since(start).Milliseconds(), "ip", s.clientIP(r))
	})
}

func cacheFor(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(d.Seconds())))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) clientIP(r *http.Request) string {
	if s.Cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------------------------------------------------------- login limiter

type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{failures: map[string][]time.Time{}} }

const (
	loginWindow   = 15 * time.Minute
	loginMaxFails = 10
)

func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-loginWindow)
	list := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if t.After(cut) {
			list = append(list, t)
		}
	}
	if len(list) == 0 {
		delete(l.failures, ip)
		return false
	}
	l.failures[ip] = list
	return len(list) >= loginMaxFails
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	l.failures[ip] = append(l.failures[ip], time.Now())
	l.mu.Unlock()
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.failures, ip)
	l.mu.Unlock()
}

// ---------------------------------------------------------------- rendering

type page struct {
	Title   string
	Active  string
	User    *store.User
	CSRF    string
	Flash   string
	FlashOK bool
	Version string
	Refresh int
	Nav     store.NavCounts
	Data    map[string]any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title, active string, data map[string]any) {
	s.renderStatus(w, r, http.StatusOK, name, &page{Title: title, Active: active, Data: data})
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, code int, name string, p *page) {
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "missing template: "+name, http.StatusInternalServerError)
		return
	}
	p.User = userFrom(r)
	p.CSRF = csrfFrom(r)
	if p.User != nil {
		if n, err := s.Store.NavCounts(r.Context()); err == nil {
			p.Nav = n
		}
	}
	p.Version = Version
	if p.Flash == "" {
		if c, err := r.Cookie(flashCookie); err == nil {
			if v, err := url.QueryUnescape(c.Value); err == nil && len(v) > 1 {
				p.FlashOK = v[0] == '+'
				p.Flash = v[1:]
			}
			http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1})
		}
	}
	if p.Data == nil {
		p.Data = map[string]any{}
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name+".html", p); err != nil {
		s.Log.Error("template error", "template", name, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = buf.WriteTo(w)
}

// flash sets a one-shot message and redirects.
func (s *Server) flash(w http.ResponseWriter, r *http.Request, to string, ok bool, msg string) {
	prefix := "-"
	if ok {
		prefix = "+"
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: url.QueryEscape(prefix + msg), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Cfg.CookieSecure, MaxAge: 60})
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.Log.Error("internal error", "err", err)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func formInt(r *http.Request, key string, def int) int {
	v := strings.TrimSpace(r.PostFormValue(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func formBool(r *http.Request, key string) bool {
	v := r.PostFormValue(key)
	return v == "1" || v == "on" || v == "true"
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.Store.Pool.Ping(ctx); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]string{"status": "ok", "version": Version})
}
