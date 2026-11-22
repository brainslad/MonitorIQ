// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/brainslad/monitoriq/internal/auth"
	"github.com/brainslad/monitoriq/internal/store"
)

// Sign-in: local passwords, LDAP / Active Directory, OpenID Connect single
// sign-on and the TOTP second factor.

var (
	errBadLogin  = errors.New("invalid username or password")
	errNoAccount = errors.New("you do not have a MonitorIQ account yet; ask an administrator to add your user")
)

func (s *Server) authSettings(ctx context.Context) auth.Settings {
	raw, err := s.Store.GetSetting(ctx, auth.SettingsKey)
	if err != nil {
		s.Log.Error("cannot read authentication settings", "err", err)
		return auth.DefaultSettings()
	}
	st, err := auth.LoadSettings(raw, s.Box)
	if err != nil {
		s.Log.Error("cannot decode authentication settings", "err", err)
	}
	return st
}

func (s *Server) oidcRedirectURL(r *http.Request) string {
	return s.baseURL(r) + "/auth/oidc/callback"
}

func totpAD(uid int64) string  { return fmt.Sprintf("totp:%d", uid) }
func setupAD(uid int64) string { return fmt.Sprintf("totp-setup:%d", uid) }

// ---------------------------------------------------------------- login page

func (s *Server) loginData(r *http.Request, st auth.Settings, next, username string) map[string]any {
	sso := st.OIDC.Enabled
	return map[string]any{
		"Next":      next,
		"Username":  username,
		"SSO":       sso,
		"SSOLabel":  st.OIDC.Label(),
		"SSOURL":    "/auth/oidc/login?next=" + url.QueryEscape(next),
		"LDAP":      st.LDAP.Enabled,
		"ShowLocal": !sso || !st.OIDC.HideLocalLogin || r.URL.Query().Get("local") == "1" || r.Method == http.MethodPost,
	}
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	st := s.authSettings(r.Context())
	s.render(w, r, "login", "Sign in", "", s.loginData(r, st, safeNext(r.URL.Query().Get("next")), ""))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)
	next := safeNext(r.PostFormValue("next"))
	username := strings.TrimSpace(r.PostFormValue("username"))
	st := s.authSettings(ctx)
	fail := func(msg string) {
		s.renderStatus(w, r, http.StatusUnauthorized, "login", &page{Title: "Sign in", Flash: msg,
			Data: s.loginData(r, st, next, username)})
	}
	if s.limiter.blocked(ip) {
		fail("Too many failed attempts. Please wait 15 minutes.")
		return
	}
	u, method, err := s.passwordLogin(ctx, st, username, r.PostFormValue("password"))
	if err != nil {
		switch {
		case errors.Is(err, errBadLogin):
			s.limiter.fail(ip)
			s.Log.Warn("failed login", "username", username, "ip", ip)
			fail("Invalid username or password.")
		case errors.Is(err, auth.ErrDenied), errors.Is(err, errNoAccount):
			s.limiter.fail(ip)
			s.Log.Warn("login denied", "username", username, "ip", ip, "reason", err)
			fail(capitalize(err.Error()) + ".")
		case errors.Is(err, store.ErrUsernameTaken):
			fail("The username " + username + " is already used by another account. Ask an administrator.")
		default:
			s.Log.Error("directory login failed", "username", username, "err", err)
			fail("The directory server could not be reached. Try again later or contact an administrator.")
		}
		return
	}
	s.limiter.reset(ip)
	s.startSession(w, r, u, method, next, st)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// passwordLogin checks a username and password. Local accounts are always
// checked first; other names go to LDAP when it is enabled.
func (s *Server) passwordLogin(ctx context.Context, st auth.Settings, username, password string) (*store.User, string, error) {
	existing, err := s.Store.GetUserByUsername(ctx, username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, "", err
	}
	if existing != nil && existing.IsLocal() {
		u, err := s.Store.Authenticate(ctx, existing.Username, password)
		if err != nil {
			return nil, "", errBadLogin
		}
		return u, store.SourceLocal, nil
	}
	if st.LDAP.Enabled && (existing == nil || existing.Source == store.SourceLDAP) {
		id, err := st.LDAP.Authenticate(username, password)
		if errors.Is(err, auth.ErrBadCredentials) {
			return nil, "", errBadLogin
		}
		if err != nil {
			return nil, "", err
		}
		u, err := s.provision(ctx, id, st.LDAP.AutoCreate)
		return u, store.SourceLDAP, err
	}
	_, _ = s.Store.Authenticate(ctx, username, password) // same timing as a wrong password
	return nil, "", errBadLogin
}

// provision maps an external identity to a MonitorIQ user, creating it when
// automatic provisioning is enabled.
func (s *Server) provision(ctx context.Context, id *auth.Identity, autoCreate bool) (*store.User, error) {
	u, err := s.Store.FindExternalUser(ctx, id.Source, id.ExternalID, id.Username)
	if errors.Is(err, store.ErrNotFound) {
		if !autoCreate {
			return nil, errNoAccount
		}
		u, err = s.Store.CreateExternalUser(ctx, id.Source, id.Username, id.ExternalID, id.DisplayName, id.Email)
		if err == nil {
			s.Log.Info("user provisioned", "username", u.Username, "source", u.Source)
		}
		return u, err
	}
	if err != nil {
		return nil, err
	}
	if u.DisplayName != id.DisplayName || u.Email != id.Email {
		if err := s.Store.UpdateProfile(ctx, u.ID, id.DisplayName, id.Email); err == nil {
			u.DisplayName, u.Email = id.DisplayName, id.Email
		}
	}
	return u, nil
}

// startSession creates the session after the first factor and sends the
// browser on, to the 2FA step when needed.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User, method, next string, st auth.Settings) {
	ctx := r.Context()
	mfa := store.MFADone
	if u.TOTPEnabled {
		mfa = store.MFAVerify
	} else if st.Require2FA && method != store.SourceOIDC {
		mfa = store.MFAEnroll
	}
	ttl := sessionTTL
	if mfa != store.MFADone {
		ttl = partialTTL
	}
	if old, err := r.Cookie(sessionCookie); err == nil && old.Value != "" {
		_ = s.Store.DeleteSession(ctx, old.Value)
	}
	token, err := s.Store.CreateSession(ctx, u.ID, ttl, mfa, method)
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.Cfg.CookieSecure, MaxAge: int(sessionTTL.Seconds())})
	if mfa == store.MFADone {
		s.Store.TouchLogin(ctx, u.ID)
		s.Log.Info("login", "username", u.Username, "method", method, "ip", s.clientIP(r))
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login/2fa?next="+url.QueryEscape(next), http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---------------------------------------------------------------- second factor

type totpSetup struct {
	Secret string       // grouped for manual entry
	QR     template.URL // data: URI (trusted: generated here)
	Enc    string       // sealed secret, posted back with the confirmation code
}

func (s *Server) newSetup(u *store.User, secret string) (*totpSetup, error) {
	if secret == "" {
		secret = auth.NewTOTPSecret()
	}
	enc, err := s.Box.Seal(secret, setupAD(u.ID))
	if err != nil {
		return nil, err
	}
	qr, err := auth.QRDataURI(auth.TOTPURI("MonitorIQ", u.Username, secret))
	if err != nil {
		return nil, err
	}
	return &totpSetup{Secret: auth.GroupSecret(secret), QR: template.URL(qr), Enc: enc}, nil //nolint:gosec // PNG data URI built by us
}

// confirmSetup checks the first code typed after scanning the QR code.
func (s *Server) confirmSetup(uid int64, enc, code string) (string, int64, bool) {
	secret, err := s.Box.Open(enc, setupAD(uid))
	if err != nil || secret == "" {
		return "", 0, false
	}
	step, ok := auth.ValidateTOTP(secret, code, time.Now())
	return secret, step, ok
}

func (s *Server) enableTOTP(ctx context.Context, uid int64, secret string, step int64) ([]string, error) {
	sealed, err := s.Box.Seal(secret, totpAD(uid))
	if err != nil {
		return nil, err
	}
	codes := auth.NewRecoveryCodes(10)
	return codes, s.Store.EnableTOTP(ctx, uid, sealed, step, codes)
}

func isTOTPCode(c string) bool {
	c = strings.ReplaceAll(strings.TrimSpace(c), " ", "")
	if len(c) != 6 {
		return false
	}
	for _, r := range c {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// checkSecondFactor verifies a TOTP code or consumes a recovery code.
func (s *Server) checkSecondFactor(ctx context.Context, uid int64, code string) (ok, recovery bool) {
	if !isTOTPCode(code) {
		return s.Store.UseRecoveryCode(ctx, uid, code), true
	}
	enc, last, err := s.Store.TOTPSecret(ctx, uid)
	if err != nil {
		return false, false
	}
	secret, err := s.Box.Open(enc, totpAD(uid))
	if err != nil {
		s.Log.Error("cannot decrypt TOTP secret", "user", uid, "err", err)
		return false, false
	}
	step, valid := auth.ValidateTOTP(secret, code, time.Now())
	if !valid || step <= last {
		return false, false
	}
	return s.Store.UseTOTPStep(ctx, uid, step), false
}

func (s *Server) renderTwoFactor(w http.ResponseWriter, r *http.Request, code int, sess *store.Session, next, flash, secret string) {
	data := map[string]any{"Next": next, "Who": sess.User.Label(), "Mode": sess.MFA}
	if sess.MFA == store.MFAEnroll {
		setup, err := s.newSetup(sess.User, secret)
		if err != nil {
			s.serverError(w, err)
			return
		}
		data["Setup"] = setup
	}
	title := "Two-factor authentication"
	s.renderStatus(w, r, code, "login_2fa", &page{Title: title, Flash: flash, Data: data})
}

func (s *Server) handleTwoFactorForm(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	next := safeNext(r.URL.Query().Get("next"))
	if sess.MFA == store.MFADone {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.renderTwoFactor(w, r, http.StatusOK, sess, next, "", "")
}

func (s *Server) handleTwoFactor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess := sessionFrom(r)
	next := safeNext(r.PostFormValue("next"))
	ip, ukey := s.clientIP(r), fmt.Sprintf("user:%d", sess.User.ID)
	if s.limiter.blocked(ip) || s.limiter.blocked(ukey) {
		s.renderTwoFactor(w, r, http.StatusTooManyRequests, sess, next, "Too many failed attempts. Please wait 15 minutes.", "")
		return
	}
	code := r.PostFormValue("code")
	switch sess.MFA {
	case store.MFAVerify:
		ok, usedRecovery := s.checkSecondFactor(ctx, sess.User.ID, code)
		if !ok {
			s.limiter.fail(ip)
			s.limiter.fail(ukey)
			s.Log.Warn("failed 2FA", "username", sess.User.Username, "ip", ip)
			s.renderTwoFactor(w, r, http.StatusUnauthorized, sess, next, "Invalid code. Try again.", "")
			return
		}
		if err := s.Store.CompleteSession(ctx, tokenFrom(r), sessionTTL); err != nil {
			s.serverError(w, err)
			return
		}
		s.limiter.reset(ukey)
		s.Store.TouchLogin(ctx, sess.User.ID)
		s.Log.Info("login", "username", sess.User.Username, "method", sess.Method+"+totp", "ip", ip)
		if usedRecovery {
			left := s.Store.RecoveryCodesLeft(ctx, sess.User.ID)
			s.flash(w, r, next, false, fmt.Sprintf("You signed in with a recovery code: %d left. You can generate new ones under My account.", left))
			return
		}
		http.Redirect(w, r, next, http.StatusSeeOther)

	case store.MFAEnroll:
		secret, step, ok := s.confirmSetup(sess.User.ID, r.PostFormValue("enc"), code)
		if !ok {
			s.limiter.fail(ukey)
			s.renderTwoFactor(w, r, http.StatusUnauthorized, sess, next, "That code is not valid. Check the time on your phone and try again.", secret)
			return
		}
		codes, err := s.enableTOTP(ctx, sess.User.ID, secret, step)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if err := s.Store.CompleteSession(ctx, tokenFrom(r), sessionTTL); err != nil {
			s.serverError(w, err)
			return
		}
		s.Store.TouchLogin(ctx, sess.User.ID)
		s.Log.Info("2FA enabled", "username", sess.User.Username)
		s.renderStatus(w, r, http.StatusOK, "recovery_codes", &page{Title: "Recovery codes",
			Data: map[string]any{"Codes": codes, "Next": next, "Standalone": true}})
	default:
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}

// ---------------------------------------------------------------- OpenID Connect

func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	st := s.authSettings(r.Context())
	if !st.OIDC.Enabled {
		http.NotFound(w, r)
		return
	}
	next := safeNext(r.URL.Query().Get("next"))
	to, flow, err := s.OIDC.AuthURL(r.Context(), &st.OIDC, s.oidcRedirectURL(r), next)
	if err != nil {
		s.Log.Error("SSO unavailable", "err", err)
		s.flash(w, r, "/login?local=1", false, "Single sign-on is unavailable right now. Contact an administrator.")
		return
	}
	data, _ := json.Marshal(flow)
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: s.Box.SignedValue(data), Path: "/auth/oidc/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Cfg.CookieSecure, MaxAge: 600})
	http.Redirect(w, r, to, http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st := s.authSettings(ctx)
	if !st.OIDC.Enabled {
		http.NotFound(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/auth/oidc/", MaxAge: -1})
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.Log.Warn("SSO error from provider", "error", e, "description", q.Get("error_description"))
		msg := "Sign-in was cancelled or refused by the identity provider."
		if e != "access_denied" {
			msg = "The identity provider returned an error: " + e + "."
		}
		s.flash(w, r, "/login", false, msg)
		return
	}
	var flow auth.Flow
	c, err := r.Cookie(oidcCookie)
	ok := err == nil
	if ok {
		var data []byte
		data, ok = s.Box.VerifySigned(c.Value)
		ok = ok && json.Unmarshal(data, &flow) == nil
	}
	if !ok || flow.State == "" || subtle.ConstantTimeCompare([]byte(flow.State), []byte(q.Get("state"))) != 1 {
		s.flash(w, r, "/login", false, "The sign-in session expired. Please try again.")
		return
	}
	id, err := s.OIDC.Exchange(ctx, &st.OIDC, s.oidcRedirectURL(r), &flow, q.Get("code"))
	if err != nil {
		if errors.Is(err, auth.ErrDenied) {
			s.Log.Warn("SSO login denied", "err", err)
			s.flash(w, r, "/login", false, "Your account is not allowed to sign in to MonitorIQ.")
			return
		}
		s.Log.Error("SSO login failed", "err", err)
		s.flash(w, r, "/login", false, "Single sign-on failed. Contact an administrator.")
		return
	}
	u, err := s.provision(ctx, id, st.OIDC.AutoCreate)
	if err != nil {
		msg := "Single sign-on failed. Contact an administrator."
		switch {
		case errors.Is(err, errNoAccount):
			msg = capitalize(err.Error()) + " (" + id.Username + ")."
		case errors.Is(err, store.ErrUsernameTaken):
			msg = "The username " + id.Username + " is already used by another account. Ask an administrator."
		default:
			s.Log.Error("SSO provisioning failed", "err", err)
		}
		s.flash(w, r, "/login", false, msg)
		return
	}
	s.startSession(w, r, u, store.SourceOIDC, safeNext(flow.Next), st)
}

// ---------------------------------------------------------------- my account

func (s *Server) accountData(r *http.Request, secret string) (map[string]any, error) {
	ctx := r.Context()
	u, err := s.Store.GetUser(ctx, userFrom(r).ID)
	if err != nil {
		return nil, err
	}
	st := s.authSettings(ctx)
	data := map[string]any{
		"U":            u,
		"Method":       sessionFrom(r).Method,
		"RecoveryLeft": s.Store.RecoveryCodesLeft(ctx, u.ID),
		"Mandatory":    st.Require2FA && u.Source != store.SourceOIDC,
	}
	if !u.TOTPEnabled {
		setup, err := s.newSetup(u, secret)
		if err != nil {
			return nil, err
		}
		data["Setup"] = setup
	}
	return data, nil
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	data, err := s.accountData(r, "")
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "account", "My account", "account", data)
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	secret, step, ok := s.confirmSetup(u.ID, r.PostFormValue("enc"), r.PostFormValue("code"))
	if !ok {
		data, err := s.accountData(r, secret)
		if err != nil {
			s.serverError(w, err)
			return
		}
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "account", &page{Title: "My account", Active: "account",
			Flash: "That code is not valid. Check the time on your phone and try again.", Data: data})
		return
	}
	codes, err := s.enableTOTP(r.Context(), u.ID, secret, step)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Log.Info("2FA enabled", "username", u.Username)
	s.render(w, r, "recovery_codes", "Recovery codes", "account", map[string]any{"Codes": codes, "Next": "/account"})
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	st := s.authSettings(r.Context())
	if st.Require2FA && u.Source != store.SourceOIDC {
		s.flash(w, r, "/account", false, "Two-factor authentication is mandatory on this server and cannot be turned off.")
		return
	}
	if ok, _ := s.checkSecondFactor(r.Context(), u.ID, r.PostFormValue("code")); !ok {
		s.flash(w, r, "/account", false, "Invalid code: two-factor authentication is still on.")
		return
	}
	if err := s.Store.DisableTOTP(r.Context(), u.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.Log.Info("2FA disabled", "username", u.Username)
	s.flash(w, r, "/account", true, "Two-factor authentication turned off.")
}

func (s *Server) handleRecoveryRegenerate(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if ok, _ := s.checkSecondFactor(r.Context(), u.ID, r.PostFormValue("code")); !ok {
		s.flash(w, r, "/account", false, "Invalid code: the recovery codes were not changed.")
		return
	}
	codes := auth.NewRecoveryCodes(10)
	if err := s.Store.ReplaceRecoveryCodes(r.Context(), u.ID, codes); err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "recovery_codes", "Recovery codes", "account", map[string]any{"Codes": codes, "Next": "/account"})
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if !u.IsLocal() {
		s.flash(w, r, "/account", false, "Your password is managed by your directory or identity provider.")
		return
	}
	if !s.Store.CheckPassword(r.Context(), u.ID, r.PostFormValue("current")) {
		s.flash(w, r, "/account", false, "The current password is incorrect.")
		return
	}
	if r.PostFormValue("password") != r.PostFormValue("password2") {
		s.flash(w, r, "/account", false, "The new passwords do not match.")
		return
	}
	if err := s.Store.SetPassword(r.Context(), u.ID, r.PostFormValue("password")); err != nil {
		s.flash(w, r, "/account", false, err.Error())
		return
	}
	_ = s.Store.DeleteOtherSessions(r.Context(), u.ID, tokenFrom(r))
	s.flash(w, r, "/account", true, "Password changed. Your other sessions were signed out.")
}

func (s *Server) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteOtherSessions(r.Context(), userFrom(r).ID, tokenFrom(r)); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/account", true, "All your other sessions were signed out.")
}

// ---------------------------------------------------------------- users

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	st := s.authSettings(r.Context())
	s.render(w, r, "users", "Users", "users", map[string]any{"Users": users, "LDAP": st.LDAP.Enabled, "OIDC": st.OIDC.Enabled,
		"Require2FA": st.Require2FA})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	source := r.PostFormValue("source")
	username := r.PostFormValue("username")
	var err error
	if source == "" || source == store.SourceLocal {
		if r.PostFormValue("password") != r.PostFormValue("password2") {
			s.flash(w, r, "/admin/users", false, "Passwords do not match.")
			return
		}
		_, err = s.Store.CreateUser(r.Context(), username, r.PostFormValue("password"))
	} else {
		_, err = s.Store.CreateExternalUser(r.Context(), source, username, "", "", "")
	}
	if err != nil {
		s.flash(w, r, "/admin/users", false, capitalize(err.Error())+".")
		return
	}
	s.flash(w, r, "/admin/users", true, "User created.")
}

func (s *Server) handleUserReset2FA(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if id == userFrom(r).ID {
		s.flash(w, r, "/admin/users", false, "Manage your own two-factor authentication under My account.")
		return
	}
	if err := s.Store.ResetTOTP(r.Context(), id); err != nil {
		s.serverError(w, err)
		return
	}
	s.Log.Info("2FA reset by administrator", "user", id, "by", userFrom(r).Username)
	s.flash(w, r, "/admin/users", true, "Two-factor authentication was reset and the user was signed out.")
}

// ---------------------------------------------------------------- authentication settings

func (s *Server) authFormData(r *http.Request, st auth.Settings, testMsg string, testOK bool) map[string]any {
	// Secrets typed in a form that is shown again (after a test or a
	// validation error) are carried along sealed, never echoed in clear.
	bindEnc, _ := s.Box.Seal(st.LDAP.BindPassword, "form:ldap-bind")
	secretEnc, _ := s.Box.Seal(st.OIDC.ClientSecret, "form:oidc-client")
	return map[string]any{
		"BindEnc":        bindEnc,
		"SecretEnc":      secretEnc,
		"S":              st,
		"HasBind":        st.LDAP.BindPassword != "",
		"HasSecret":      st.OIDC.ClientSecret != "",
		"RedirectURL":    s.oidcRedirectURL(r),
		"ADFilter":       auth.ADUserFilter,
		"OpenLDAPFilter": auth.OpenLDAPUserFilter,
		"TestMsg":        testMsg,
		"TestOK":         testOK,
		"Local":          userFrom(r).IsLocal(),
	}
}

func (s *Server) handleAuthSettings(w http.ResponseWriter, r *http.Request) {
	st := s.authSettings(r.Context())
	s.render(w, r, "auth_settings", "Authentication", "settings", s.authFormData(r, st, "", false))
}

// authFromForm builds settings from the form. Empty secret fields keep the
// stored value unless the matching "clear" box is ticked.
func (s *Server) authFromForm(r *http.Request, old auth.Settings) auth.Settings {
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	st := auth.Settings{
		Require2FA: formBool(r, "require_2fa"),
		LDAP: auth.LDAPConfig{
			Enabled:            formBool(r, "ldap_enabled"),
			URL:                f("ldap_url"),
			StartTLS:           formBool(r, "ldap_start_tls"),
			InsecureSkipVerify: formBool(r, "ldap_insecure"),
			BindDN:             f("ldap_bind_dn"),
			BindPassword:       r.PostFormValue("ldap_bind_password"),
			BaseDN:             f("ldap_base_dn"),
			UserFilter:         f("ldap_user_filter"),
			UsernameAttr:       f("ldap_username_attr"),
			NameAttr:           f("ldap_name_attr"),
			EmailAttr:          f("ldap_email_attr"),
			RequiredGroup:      f("ldap_required_group"),
			NestedGroups:       formBool(r, "ldap_nested"),
			AutoCreate:         formBool(r, "ldap_auto_create"),
		},
		OIDC: auth.OIDCConfig{
			Enabled:        formBool(r, "oidc_enabled"),
			ButtonLabel:    f("oidc_button_label"),
			Issuer:         strings.TrimRight(f("oidc_issuer"), "/"),
			ClientID:       f("oidc_client_id"),
			ClientSecret:   strings.TrimSpace(r.PostFormValue("oidc_client_secret")),
			Scopes:         f("oidc_scopes"),
			UsernameClaim:  f("oidc_username_claim"),
			AllowedDomains: f("oidc_allowed_domains"),
			GroupsClaim:    f("oidc_groups_claim"),
			RequiredGroups: f("oidc_required_groups"),
			AutoCreate:     formBool(r, "oidc_auto_create"),
			HideLocalLogin: formBool(r, "oidc_hide_local"),
		},
	}
	if st.LDAP.BindPassword == "" && !formBool(r, "ldap_clear_password") {
		st.LDAP.BindPassword = old.LDAP.BindPassword
		if v, err := s.Box.Open(r.PostFormValue("ldap_bind_password_enc"), "form:ldap-bind"); err == nil && v != "" {
			st.LDAP.BindPassword = v
		}
	}
	if st.OIDC.ClientSecret == "" && !formBool(r, "oidc_clear_secret") {
		st.OIDC.ClientSecret = old.OIDC.ClientSecret
		if v, err := s.Box.Open(r.PostFormValue("oidc_client_secret_enc"), "form:oidc-client"); err == nil && v != "" {
			st.OIDC.ClientSecret = v
		}
	}
	if tenant := f("oidc_azure_tenant"); tenant != "" && st.OIDC.Issuer == "" {
		st.OIDC.Issuer = auth.AzureIssuer(tenant)
	}
	return st
}

func (s *Server) handleAuthSettingsSave(w http.ResponseWriter, r *http.Request) {
	old := s.authSettings(r.Context())
	st := s.authFromForm(r, old)
	if err := st.Validate(); err != nil {
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "auth_settings", &page{Title: "Authentication", Active: "settings",
			Flash: err.Error(), Data: s.authFormData(r, st, "", false)})
		return
	}
	enc, err := st.Encode(s.Box)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetSetting(r.Context(), auth.SettingsKey, enc); err != nil {
		s.serverError(w, err)
		return
	}
	s.Log.Info("authentication settings changed", "by", userFrom(r).Username,
		"ldap", st.LDAP.Enabled, "oidc", st.OIDC.Enabled, "require_2fa", st.Require2FA)
	msg := "Authentication settings saved."
	if st.Require2FA && !old.Require2FA {
		msg += " Users without 2FA will be asked to set it up at their next sign-in."
	}
	s.flash(w, r, "/admin/auth", true, msg)
}

func (s *Server) handleLDAPTest(w http.ResponseWriter, r *http.Request) {
	st := s.authFromForm(r, s.authSettings(r.Context()))
	msg, err := st.LDAP.Test(r.PostFormValue("ldap_test_username"))
	ok := err == nil
	if err != nil {
		msg = "LDAP test failed: " + err.Error()
	}
	s.render(w, r, "auth_settings", "Authentication", "settings", s.authFormData(r, st, msg, ok))
}
