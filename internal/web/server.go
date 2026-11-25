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
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brainslad/monitoriq/internal/auth"
	"github.com/brainslad/monitoriq/internal/config"
	"github.com/brainslad/monitoriq/internal/geo"
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
	// Geo finds where monitored targets and this server are (set by main).
	Geo *geo.Service

	geoUpd *geoUpdate

	pages   map[string]*template.Template
	limiter *loginLimiter
	brand   brandCache
}

func New(st *store.Store, cfg *config.Config, box *auth.Box, log *slog.Logger) (*Server, error) {
	s := &Server{Store: st, Cfg: cfg, Log: log, Box: box, OIDC: &auth.OIDC{},
		pages: map[string]*template.Template{}, limiter: newLoginLimiter(), geoUpd: &geoUpdate{}}
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
	mux.Handle("GET /static/", staticCache(noListing(fileServer)))
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /branding/logo", s.handleLogo("light"))
	mux.HandleFunc("GET /branding/logo-dark", s.handleLogo("dark"))
	mux.HandleFunc("GET /branding/logo-mini", s.handleLogo("mini"))

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
	// admin routes change the configuration (or show the forms that do):
	// read-only users get "403 Administrators only".
	admin := func(pattern string, h http.HandlerFunc) { auth(pattern, s.requireAdmin(h)) }
	auth("POST /logout", s.handleLogout)
	auth("GET /{$}", s.handleDashboard)
	auth("GET /monitors/{id}", s.handleMonitorDetail)
	admin("POST /monitors/{id}/check", s.handleMonitorCheckNow)
	auth("GET /api/monitors/{id}/series", s.handleSeries)
	auth("GET /api/dashboard/series", s.handleDashboardSeries)
	auth("GET /api/dashboard/drill", s.handleDashboardDrill)
	auth("POST /dashboard/heat-monitors", s.handleHeatMonitorsSave)
	auth("GET /export", s.handleExportForm)
	auth("GET /export/download", s.handleExportDownload)

	auth("GET /admin/hosts", s.handleHosts)
	admin("GET /admin/hosts/new", s.handleHostForm)
	admin("POST /admin/hosts/new", s.handleHostSave)
	admin("GET /admin/hosts/{id}/edit", s.handleHostForm)
	admin("POST /admin/hosts/{id}/edit", s.handleHostSave)
	admin("POST /admin/hosts/{id}/delete", s.handleHostDelete)

	auth("GET /admin/monitors", s.handleMonitors)
	admin("GET /admin/monitors/new", s.handleMonitorForm)
	admin("POST /admin/monitors/new", s.handleMonitorSave)
	admin("GET /admin/monitors/{id}/edit", s.handleMonitorForm)
	admin("POST /admin/monitors/{id}/edit", s.handleMonitorSave)
	admin("POST /admin/monitors/{id}/delete", s.handleMonitorDelete)
	admin("POST /admin/monitors/{id}/toggle", s.handleMonitorToggle)
	admin("POST /admin/monitors/move", s.handleMonitorsMove)

	auth("GET /admin/groups", s.handleGroups)
	admin("GET /admin/groups/new", s.handleGroupForm)
	admin("POST /admin/groups/new", s.handleGroupSave)
	admin("GET /admin/groups/{id}/edit", s.handleGroupForm)
	admin("POST /admin/groups/{id}/edit", s.handleGroupSave)
	admin("POST /admin/groups/{id}/delete", s.handleGroupDelete)

	auth("GET /admin/status-pages", s.handleStatusPages)
	admin("GET /admin/status-pages/new", s.handleStatusPageForm)
	admin("POST /admin/status-pages/new", s.handleStatusPageSave)
	admin("GET /admin/status-pages/{id}/edit", s.handleStatusPageForm)
	admin("POST /admin/status-pages/{id}/edit", s.handleStatusPageSave)
	admin("POST /admin/status-pages/{id}/delete", s.handleStatusPageDelete)

	admin("GET /admin/users", s.handleUsers)
	admin("POST /admin/users", s.handleUserCreate)
	admin("POST /admin/users/{id}/delete", s.handleUserDelete)
	admin("POST /admin/users/{id}/reset-2fa", s.handleUserReset2FA)
	admin("POST /admin/users/{id}/name", s.handleUserRename)
	admin("POST /admin/users/{id}/role", s.handleUserRole)
	auth("POST /account/name", s.handleAccountName)

	auth("GET /account", s.handleAccount)
	auth("POST /account/password", s.handlePasswordChange)
	auth("POST /account/2fa/enable", s.handleTOTPEnable)
	auth("POST /account/2fa/disable", s.handleTOTPDisable)
	auth("POST /account/2fa/recovery", s.handleRecoveryRegenerate)
	auth("POST /account/sessions/revoke", s.handleRevokeSessions)

	admin("GET /admin/settings", s.handleSettings)
	admin("POST /admin/settings", s.handleSettingsSave)
	admin("POST /admin/settings/purge", s.handlePurgeNow)
	admin("GET /admin/auth", s.handleAuthSettings)
	admin("POST /admin/auth", s.handleAuthSettingsSave)
	admin("POST /admin/auth/ldap-test", s.handleLDAPTest)
	admin("GET /admin/email", s.handleEmailSettings)
	admin("POST /admin/email", s.handleEmailSettingsSave)
	admin("POST /admin/email/test", s.handleEmailTest)
	auth("POST /account/email", s.handleAccountEmail)
	admin("GET /admin/branding", s.handleBrandingForm)
	mux.Handle("POST /admin/branding", s.limitBody(3*MaxUploadBytes+64<<10, "/admin/branding", s.requireSession(false, s.requireAdmin(s.handleBrandingSave))))
	mux.Handle("POST /admin/branding/reset-logo", s.limitBody(3*MaxUploadBytes+64<<10, "/admin/branding", s.requireSession(false, s.requireAdmin(s.handleLogoReset))))
	admin("GET /admin/geo", s.handleGeoSettings)
	admin("POST /admin/geo", s.handleGeoSettingsSave)
	admin("POST /admin/geo/update", s.handleGeoUpdate)
	admin("POST /admin/geo/lookup", s.handleGeoLookup)

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

// requireAdmin lets only administrators through; it runs after
// requireSession, which has put the user in the request.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r).IsAdmin() {
			next(w, r)
			return
		}
		if r.Method != http.MethodGet || strings.HasPrefix(r.URL.Path, "/api/") {
			http.Error(w, "Administrators only: your account is read-only.", http.StatusForbidden)
			return
		}
		s.renderStatus(w, r, http.StatusForbidden, "forbidden", &page{Title: "Administrators only"})
	}
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

// noListing answers 404 for directories, so the file server never lists them.
func noListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// crossSite reports whether the browser says r comes from another site
// (Sec-Fetch-Site, or an Origin whose host is not the request host).
func crossSite(r *http.Request) bool {
	if v := r.Header.Get("Sec-Fetch-Site"); v != "" {
		return v == "cross-site"
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		return err != nil || !strings.EqualFold(u.Host, r.Host)
	}
	return false
}

// secureCookie reports whether cookies set in reply to r get the Secure flag.
// Unless MONITORIQ_COOKIE_SECURE forces it, a cookie is Secure when the
// browser reached MonitorIQ over HTTPS: directly, through a trusted proxy
// that says so in X-Forwarded-Proto, or on the host of an https:// base URL.
// Plain HTTP access (for example http://<ip>:8080 on the local network)
// gets ordinary cookies, so signing in there keeps working.
func (s *Server) secureCookie(r *http.Request) bool {
	if s.Cfg.CookieSecure != nil {
		return *s.Cfg.CookieSecure
	}
	if r.TLS != nil {
		return true
	}
	if s.fromTrustedProxy(r) {
		proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
		if strings.EqualFold(proto, "https") {
			return true
		}
	}
	if u, err := url.Parse(s.Cfg.BaseURL); err == nil && u.Scheme == "https" && u.Host != "" {
		return strings.EqualFold(stripPort(r.Host), stripPort(u.Host))
	}
	return false
}

func stripPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.Trim(h, "[]")
}

// fromTrustedProxy reports whether the request came through a proxy whose
// X-Forwarded-* headers can be believed.
func (s *Server) fromTrustedProxy(r *http.Request) bool {
	if !s.Cfg.TrustProxy {
		return false
	}
	if len(s.Cfg.TrustedProxies) == 0 {
		return true
	}
	return s.isTrustedAddr(remoteHost(r))
}

func (s *Server) isTrustedAddr(host string) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range s.Cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) clientIP(r *http.Request) string {
	if s.fromTrustedProxy(r) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if len(s.Cfg.TrustedProxies) == 0 {
				return strings.TrimSpace(parts[0])
			}
			// With known proxies, the client is the last address that is not
			// one of them: entries further left can be forged by the client.
			for i := len(parts) - 1; i >= 0; i-- {
				if p := strings.TrimSpace(parts[i]); p != "" && !s.isTrustedAddr(p) {
					return p
				}
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	return remoteHost(r)
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
	Brand   Branding
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
	p.Brand = s.branding(r.Context())
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
	// Pages are never kept by browsers or proxies: after an update they
	// always show the new version (and link the new static files).
	w.Header().Set("Cache-Control", "no-store")
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
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secureCookie(r), MaxAge: 60})
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
