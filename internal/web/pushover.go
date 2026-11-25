// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/brainslad/monitoriq/internal/notify"
	"github.com/brainslad/monitoriq/internal/store"
)

// ---------------------------------------------------------------- Pushover settings

func (s *Server) pushoverSettings(ctx context.Context) notify.PushoverSettings {
	ps, err := s.Notifier.PushoverSettings(ctx)
	if err != nil {
		s.Log.Error("pushover settings", "err", err)
	}
	return ps
}

// pushoverPriorities are the choices offered for each kind of change.
var pushoverPriorities = []struct {
	Value int
	Label string
}{
	{notify.PriorityLowest, "Lowest: no notification, only in the app"},
	{notify.PriorityLow, "Low: no sound or vibration"},
	{notify.PriorityNormal, "Normal"},
	{notify.PriorityHigh, "High: also during quiet hours"},
	{notify.PriorityEmergency, "Emergency: repeat until acknowledged"},
}

func (s *Server) pushoverFormData(r *http.Request, ps notify.PushoverSettings, testMsg string, testOK bool) map[string]any {
	// Keys typed in a form that is shown again are carried along sealed.
	tokEnc, _ := s.Box.Seal(ps.Token, "form:pushover-token")
	userEnc, _ := s.Box.Seal(ps.UserKey, "form:pushover-user")
	recent, err := s.Store.RecentNotifications(r.Context(), store.ChannelPushover, 15)
	if err != nil {
		s.Log.Error("pushover log", "err", err)
	}
	es := s.emailSettings(r.Context())
	def, extra, total, err := s.Store.PushoverCounts(r.Context())
	if err != nil {
		s.Log.Error("pushover counts", "err", err)
	}
	return map[string]any{
		"S":          ps,
		"HasToken":   ps.Token != "",
		"HasUser":    ps.UserKey != "",
		"TokenEnc":   tokEnc,
		"UserEnc":    userEnc,
		"Priorities": pushoverPriorities,
		"CertDays":   es.CertDays,
		"DigestHour": es.DigestHour,
		"Recent":     recent,
		"Ticked":     def,
		"WithKeys":   extra,
		"Monitors":   total,
		"TestMsg":    testMsg,
		"TestOK":     testOK,
	}
}

func (s *Server) handlePushoverSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "pushover_settings", "Pushover", "settings", s.pushoverFormData(r, s.pushoverSettings(r.Context()), "", false))
}

// pushoverFromForm builds settings from the form. An empty key field keeps
// the stored key unless "remove" is ticked.
func (s *Server) pushoverFromForm(r *http.Request, old notify.PushoverSettings) notify.PushoverSettings {
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	ps := notify.PushoverSettings{
		Enabled:          formBool(r, "enabled"),
		Token:            f("token"),
		UserKey:          f("user_key"),
		Devices:          f("devices"),
		Sound:            f("sound"),
		OnDown:           formBool(r, "on_down"),
		OnDegraded:       formBool(r, "on_degraded"),
		OnRecovery:       formBool(r, "on_recovery"),
		DownPriority:     formInt(r, "down_priority", notify.PriorityHigh),
		DegradedPriority: formInt(r, "degraded_priority", notify.PriorityNormal),
		RecoveryPriority: formInt(r, "recovery_priority", notify.PriorityNormal),
		Retry:            formInt(r, "retry", 300),
		Expire:           formInt(r, "expire", 3600),
		CertDigest:       formBool(r, "cert_digest"),
		NewMonitors:      formBool(r, "new_monitors"),
	}
	keep := func(typed *string, stored, field, purpose string) {
		if *typed != "" || formBool(r, "clear_keys") {
			return
		}
		*typed = stored
		if v, err := s.Box.Open(r.PostFormValue(field), purpose); err == nil && v != "" {
			*typed = v
		}
	}
	keep(&ps.Token, old.Token, "token_enc", "form:pushover-token")
	keep(&ps.UserKey, old.UserKey, "user_enc", "form:pushover-user")
	return ps
}

func (s *Server) handlePushoverSettingsSave(w http.ResponseWriter, r *http.Request) {
	ps := s.pushoverFromForm(r, s.pushoverSettings(r.Context()))
	if err := ps.Validate(); err != nil {
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "pushover_settings", &page{Title: "Pushover", Active: "settings",
			Flash: err.Error(), Data: s.pushoverFormData(r, ps, "", false)})
		return
	}
	enc, err := ps.Encode(s.Box)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetSetting(r.Context(), notify.PushoverSettingsKey, enc); err != nil {
		s.serverError(w, err)
		return
	}
	s.Log.Info("pushover settings changed", "by", userFrom(r).Username, "enabled", ps.Enabled)
	msg := "Pushover settings saved."
	if !ps.Enabled {
		msg += " Sending is turned off."
	}
	s.flash(w, r, "/admin/pushover", true, msg)
}

// handlePushoverTest sends a test message with the values in the form, without saving them.
func (s *Server) handlePushoverTest(w http.ResponseWriter, r *http.Request) {
	ps := s.pushoverFromForm(r, s.pushoverSettings(r.Context()))
	enabled := ps.Enabled
	ps.Enabled = true // check the keys even if sending is still off
	err := ps.Validate()
	ps.Enabled = enabled
	if err == nil && ps.UserKey == "" { // Validate trimmed it
		err = errors.New("enter the default user or group key to send a test message to")
	}
	if err == nil {
		err = s.Notifier.SendPushoverTest(r.Context(), ps, userFrom(r).Username)
	}
	msg, ok := "Test message sent. It should appear on your devices within a few seconds.", true
	if err != nil {
		msg, ok = "Test failed: "+err.Error(), false
	}
	s.render(w, r, "pushover_settings", "Pushover", "settings", s.pushoverFormData(r, ps, msg, ok))
}

// handleMonitorsPushover turns sending to the default Pushover key on or off
// for the ticked monitors.
func (s *Server) handleMonitorsPushover(w http.ResponseWriter, r *http.Request) {
	back := safeNext(r.PostFormValue("back"))
	if !strings.HasPrefix(back, "/admin/monitors") {
		back = "/admin/monitors"
	}
	if err := r.ParseForm(); err != nil {
		s.flash(w, r, back, false, "Could not read the form.")
		return
	}
	var ids []int64
	for _, v := range r.PostForm["monitor"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		s.flash(w, r, back, false, "Tick at least one monitor first.")
		return
	}
	on := r.PostFormValue("pushover") == "on"
	n, err := s.Store.SetMonitorsPushover(r.Context(), ids, on)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Log.Info("pushover changed on monitors", "by", userFrom(r).Username, "on", on, "changed", n)
	state := "no longer send"
	if on {
		state = "now send"
	}
	msg := fmt.Sprintf("%d of %d selected monitors %s alerts to the default Pushover key.", n, len(ids), state)
	if n == 0 {
		msg = "Nothing to change: the selected monitors were already set that way."
	}
	s.flash(w, r, back, true, msg)
}
