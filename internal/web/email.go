// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/brainslad/monitoriq/internal/notify"
	"github.com/brainslad/monitoriq/internal/store"
)

// ---------------------------------------------------------------- e-mail settings

func (s *Server) emailSettings(ctx context.Context) notify.Settings {
	st, err := s.Notifier.Settings(ctx)
	if err != nil {
		s.Log.Error("e-mail settings", "err", err)
	}
	return st
}

func (s *Server) emailFormData(r *http.Request, st notify.Settings, testMsg string, testOK bool) map[string]any {
	ctx := r.Context()
	// A password typed in a form that is shown again is carried along sealed.
	pwEnc, _ := s.Box.Seal(st.Password, "form:smtp-password")
	recent, err := s.Store.RecentEmails(ctx, 15)
	if err != nil {
		s.Log.Error("e-mail log", "err", err)
	}
	subs, _ := s.Store.AlertSubscribers(ctx)
	testTo := r.PostFormValue("test_to")
	if testTo == "" {
		testTo = userFrom(r).Email
	}
	return map[string]any{
		"S":           st,
		"HasPassword": st.Password != "",
		"PasswordEnc": pwEnc,
		"Recent":      recent,
		"Subscribers": subs,
		"TestTo":      testTo,
		"TestMsg":     testMsg,
		"TestOK":      testOK,
		"BaseURL":     s.baseURL(r),
		"Hours":       hoursOfDay(),
	}
}

func hoursOfDay() []int {
	h := make([]int, 24)
	for i := range h {
		h[i] = i
	}
	return h
}

func (s *Server) handleEmailSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "email_settings", "Email", "settings", s.emailFormData(r, s.emailSettings(r.Context()), "", false))
}

// emailFromForm builds settings from the form. An empty password keeps the
// stored one unless "remove" is ticked.
func (s *Server) emailFromForm(r *http.Request, old notify.Settings) notify.Settings {
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	st := notify.Settings{
		Enabled:            formBool(r, "enabled"),
		Host:               f("host"),
		Port:               formInt(r, "port", 0),
		Security:           f("security"),
		InsecureSkipVerify: formBool(r, "insecure"),
		Username:           f("username"),
		Password:           r.PostFormValue("password"),
		FromAddress:        f("from_address"),
		FromName:           f("from_name"),
		Recipients:         r.PostFormValue("recipients"),
		DashboardURL:       f("dashboard_url"),
		OnDown:             formBool(r, "on_down"),
		OnDegraded:         formBool(r, "on_degraded"),
		OnRecovery:         formBool(r, "on_recovery"),
		CertDigest:         formBool(r, "cert_digest"),
		CertDays:           formInt(r, "cert_days", 21),
		DigestHour:         formInt(r, "digest_hour", 8),
	}
	if st.Password == "" && !formBool(r, "clear_password") {
		st.Password = old.Password
		if v, err := s.Box.Open(r.PostFormValue("password_enc"), "form:smtp-password"); err == nil && v != "" {
			st.Password = v
		}
	}
	if st.Username == "" {
		st.Password = ""
	}
	return st
}

func (s *Server) handleEmailSettingsSave(w http.ResponseWriter, r *http.Request) {
	st := s.emailFromForm(r, s.emailSettings(r.Context()))
	if err := st.Validate(); err != nil {
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "email_settings", &page{Title: "Email", Active: "settings",
			Flash: err.Error(), Data: s.emailFormData(r, st, "", false)})
		return
	}
	enc, err := st.Encode(s.Box)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetSetting(r.Context(), notify.SettingsKey, enc); err != nil {
		s.serverError(w, err)
		return
	}
	s.Log.Info("e-mail settings changed", "by", userFrom(r).Username, "enabled", st.Enabled, "host", st.Host)
	msg := "E-mail settings saved."
	if !st.Enabled {
		msg += " Sending is turned off."
	}
	s.flash(w, r, "/admin/email", true, msg)
}

// handleEmailTest sends a test message with the values in the form, without saving them.
func (s *Server) handleEmailTest(w http.ResponseWriter, r *http.Request) {
	st := s.emailFromForm(r, s.emailSettings(r.Context()))
	enabled := st.Enabled
	st.Enabled = true // validate the server fields even if sending is still off
	err := st.Validate()
	st.Enabled = enabled
	if err == nil {
		err = s.Notifier.SendTest(r.Context(), st, r.PostFormValue("test_to"), userFrom(r).Username)
	}
	msg, ok := "Test message sent to "+strings.TrimSpace(r.PostFormValue("test_to"))+". Check the inbox (and the spam folder).", true
	if err != nil {
		msg, ok = "Test failed: "+err.Error(), false
	}
	s.render(w, r, "email_settings", "Email", "settings", s.emailFormData(r, st, msg, ok))
}

// ---------------------------------------------------------------- my account

func (s *Server) handleAccountEmail(w http.ResponseWriter, r *http.Request) {
	u, err := s.Store.GetUser(r.Context(), userFrom(r).ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	alerts := formBool(r, "email_alerts")
	email := u.Email
	if u.IsLocal() {
		addrs, err := store.ParseEmails(r.PostFormValue("email"))
		if err != nil || len(addrs) > 1 {
			s.flash(w, r, "/account", false, "Enter one valid e-mail address.")
			return
		}
		email = ""
		if len(addrs) == 1 {
			email = addrs[0]
		}
	}
	if alerts && email == "" {
		s.flash(w, r, "/account", false, "Add an e-mail address to receive alerts.")
		return
	}
	if err := s.Store.SetEmailPrefs(r.Context(), u.ID, email, u.IsLocal(), alerts); err != nil {
		s.serverError(w, err)
		return
	}
	msg := "E-mail preferences saved."
	if alerts {
		if st := s.emailSettings(r.Context()); !st.Enabled {
			msg += " Note: sending e-mail is not turned on yet under Settings → Email."
		}
	}
	s.flash(w, r, "/account", true, msg)
}
