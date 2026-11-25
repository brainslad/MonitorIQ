// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/brainslad/monitoriq/internal/auth"
	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/store"
)

// PushoverSettingsKey is the key in the settings table.
const PushoverSettingsKey = "pushover"

// Pushover message priorities (https://pushover.net/api#priority).
const (
	PriorityLowest    = -2 // no notification, only listed in the app
	PriorityLow       = -1 // no sound or vibration
	PriorityNormal    = 0
	PriorityHigh      = 1 // bypasses the user's quiet hours
	PriorityEmergency = 2 // repeats until acknowledged
)

// pushoverAPI is the endpoint messages are posted to (replaced in tests).
var pushoverAPI = "https://api.pushover.net/1/messages.json"

// Limits of the Pushover API.
const (
	pushoverMaxTitle   = 250
	pushoverMaxMessage = 1024
	pushoverMaxURL     = 512
)

var (
	pushoverKeyRe    = store.PushoverKeyRe
	pushoverDeviceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,25}$`)
	pushoverSoundRe  = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
)

// PushoverSettings is the configuration edited under Settings → Pushover.
// It is stored as JSON in the settings table, with both keys encrypted.
type PushoverSettings struct {
	Enabled bool `json:"enabled"`
	// Token is the API token of the application registered at pushover.net.
	Token string `json:"token"`
	// UserKey is the default user or group key: it receives the alerts of
	// every monitor that has "Pushover" ticked. Monitors can add their own keys.
	UserKey string `json:"user_key"`
	// Devices limits delivery to these device names (comma separated, empty = all).
	Devices string `json:"devices"`
	Sound   string `json:"sound"`

	OnDown     bool `json:"on_down"`
	OnDegraded bool `json:"on_degraded"`
	OnRecovery bool `json:"on_recovery"`

	DownPriority     int `json:"down_priority"`
	DegradedPriority int `json:"degraded_priority"`
	RecoveryPriority int `json:"recovery_priority"`
	// Retry and Expire (seconds) apply to emergency messages: Pushover repeats
	// the alert every Retry seconds until it is acknowledged or Expire passes.
	Retry  int `json:"retry"`
	Expire int `json:"expire"`

	// NewMonitors is the default of the per-monitor "Pushover" tick for new monitors.
	NewMonitors bool `json:"new_monitors"`

	// CertDigest sends the daily certificate digest (see Settings → Email for
	// the number of days and the hour) as a push message too.
	CertDigest bool `json:"cert_digest"`
}

// DefaultPushoverSettings returns the configuration used before anything is saved.
func DefaultPushoverSettings() PushoverSettings {
	return PushoverSettings{
		OnDown:           true,
		OnRecovery:       true,
		DownPriority:     PriorityHigh,
		DegradedPriority: PriorityNormal,
		RecoveryPriority: PriorityNormal,
		Retry:            300,
		Expire:           3600,
		NewMonitors:      true,
	}
}

// LoadPushoverSettings decodes stored settings and decrypts the keys.
func LoadPushoverSettings(raw string, box *auth.Box) (PushoverSettings, error) {
	s := DefaultPushoverSettings()
	if raw == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, err
	}
	var err error
	if s.Token, err = box.Open(s.Token, "pushover-token"); err != nil {
		return s, err
	}
	s.UserKey, err = box.Open(s.UserKey, "pushover-user")
	return s, err
}

// Encode encrypts the keys and returns the JSON to store.
func (s PushoverSettings) Encode(box *auth.Box) (string, error) {
	var err error
	if s.Token, err = box.Seal(s.Token, "pushover-token"); err != nil {
		return "", err
	}
	if s.UserKey, err = box.Seal(s.UserKey, "pushover-user"); err != nil {
		return "", err
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// Validate normalises the settings and checks them. The keys are only
// required when sending is enabled.
func (s *PushoverSettings) Validate() error {
	s.Token = strings.TrimSpace(s.Token)
	s.UserKey = strings.TrimSpace(s.UserKey)
	s.Sound = strings.TrimSpace(s.Sound)
	var devs []string
	for _, d := range strings.FieldsFunc(s.Devices, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if !pushoverDeviceRe.MatchString(d) {
			return fmt.Errorf("%q is not a valid device name (letters, digits, _ and -, up to 25)", d)
		}
		devs = append(devs, d)
	}
	s.Devices = strings.Join(devs, ",")
	if s.Sound != "" && !pushoverSoundRe.MatchString(s.Sound) {
		return errors.New("the sound must be a Pushover sound name such as siren or pushover")
	}
	for _, p := range []int{s.DownPriority, s.DegradedPriority, s.RecoveryPriority} {
		if p < PriorityLowest || p > PriorityEmergency {
			return errors.New("priorities must be between -2 and 2")
		}
	}
	if s.Retry < 30 {
		return errors.New("emergency alerts can be repeated every 30 seconds at most")
	}
	if s.Expire < s.Retry || s.Expire > 10800 {
		return errors.New("emergency alerts stop repeating after at most 10800 seconds (3 hours), and not before the first repeat")
	}
	if !s.Enabled {
		return nil
	}
	if !pushoverKeyRe.MatchString(s.Token) {
		return errors.New("the application token is 30 letters and digits (create an application at pushover.net)")
	}
	if s.UserKey != "" && !pushoverKeyRe.MatchString(s.UserKey) {
		return errors.New("the user or group key is 30 letters and digits (shown on your pushover.net dashboard)")
	}
	return nil
}

// wanted reports whether a status change should be pushed, and with which priority.
func (s PushoverSettings) wanted(from, to checks.Status) (bool, int) {
	switch to {
	case checks.Down:
		return s.OnDown, s.DownPriority
	case checks.Degraded:
		if from == checks.Down {
			return s.OnRecovery, s.RecoveryPriority
		}
		return s.OnDegraded, s.DegradedPriority
	case checks.Up:
		if from == checks.Down {
			return s.OnRecovery, s.RecoveryPriority
		}
		if from == checks.Degraded {
			return s.OnDegraded, s.DegradedPriority
		}
	}
	return false, 0
}

// PushMessage is one Pushover notification.
type PushMessage struct {
	// To are the user or group keys; empty means the default key.
	To        []string
	Title     string
	Message   string
	Priority  int
	URL       string
	URLTitle  string
	Timestamp time.Time
}

// PushoverError is an error reported by the Pushover API.
type PushoverError struct {
	Status int
	Errors []string
}

func (e *PushoverError) Error() string {
	if len(e.Errors) > 0 {
		return "pushover: " + strings.Join(e.Errors, "; ")
	}
	return fmt.Sprintf("pushover: HTTP %d", e.Status)
}

var pushoverClient = &http.Client{Timeout: 15 * time.Second}

// SendPushover posts one message to the Pushover API.
func SendPushover(ctx context.Context, st PushoverSettings, m PushMessage) error {
	to := m.To
	if len(to) == 0 && st.UserKey != "" {
		to = []string{st.UserKey}
	}
	if st.Token == "" || len(to) == 0 {
		return errors.New("enter the application token and a user or group key")
	}
	v := url.Values{}
	v.Set("token", st.Token)
	v.Set("user", strings.Join(to, ",")) // the API accepts up to 50 comma-separated keys
	v.Set("title", truncate(m.Title, pushoverMaxTitle))
	v.Set("message", truncate(m.Message, pushoverMaxMessage))
	v.Set("priority", strconv.Itoa(m.Priority))
	if m.Priority == PriorityEmergency {
		v.Set("retry", strconv.Itoa(st.Retry))
		v.Set("expire", strconv.Itoa(st.Expire))
	}
	if st.Devices != "" {
		v.Set("device", st.Devices)
	}
	if st.Sound != "" {
		v.Set("sound", st.Sound)
	}
	if m.URL != "" && len(m.URL) <= pushoverMaxURL {
		v.Set("url", m.URL)
		if m.URLTitle != "" {
			v.Set("url_title", truncate(m.URLTitle, 100))
		}
	}
	if !m.Timestamp.IsZero() {
		v.Set("timestamp", strconv.FormatInt(m.Timestamp.Unix(), 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushoverAPI, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "MonitorIQ")
	resp, err := pushoverClient.Do(req)
	if err != nil {
		// Do not leak the token, which is in the request body only, but the
		// URL error may still be long: keep the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("pushover: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var r struct {
		Status int      `json:"status"`
		Errors []string `json:"errors"`
	}
	_ = json.Unmarshal(body, &r)
	if resp.StatusCode != http.StatusOK || r.Status != 1 {
		return &PushoverError{Status: resp.StatusCode, Errors: r.Errors}
	}
	return nil
}

// truncate shortens s to at most n runes, ending with an ellipsis when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// PushAlert builds the push message for one or more status changes.
// The priority is the highest one of the changes.
func PushAlert(items []Item, prios []int, base string) PushMessage {
	m := PushMessage{Priority: PriorityLowest}
	for _, p := range prios {
		if p > m.Priority {
			m.Priority = p
		}
	}
	subject := strings.TrimPrefix(AlertMessage(items, base).Subject, "[MonitorIQ] ")
	m.Title = "MonitorIQ: " + subject
	var b strings.Builder
	if len(items) == 1 {
		it := items[0]
		fmt.Fprintf(&b, "%s %s\n", it.Name, headline(it))
		fmt.Fprintf(&b, "Target: %s (%s)\n", it.Target, strings.ToUpper(it.Type))
		if it.Lasted != "" {
			fmt.Fprintf(&b, "Was %s for %s\n", strings.ToLower(word(it.From)), it.Lasted)
		}
		if it.Message != "" {
			fmt.Fprintf(&b, "Result: %s\n", it.Message)
		}
		m.URL, m.URLTitle, m.Timestamp = it.URL, "Open monitor", it.At
	} else {
		for _, it := range items {
			line := it.Name + " " + headline(it)
			if it.Host != "" {
				line += " (" + it.Host + ")"
			}
			b.WriteString(line + "\n")
		}
		if base != "" {
			m.URL, m.URLTitle = base+"/", "Open dashboard"
		}
		m.Timestamp = items[len(items)-1].At
	}
	m.Message = strings.TrimSpace(b.String())
	return m
}

// PushDigest builds the push message for the certificate digest.
func PushDigest(items []CertItem, days int, base string) PushMessage {
	m := PushMessage{Priority: PriorityNormal}
	m.Title = fmt.Sprintf("MonitorIQ: %d certificate(s) expire within %d days", len(items), days)
	var b strings.Builder
	for _, it := range items {
		switch {
		case it.DaysLeft < 0:
			fmt.Fprintf(&b, "%s: EXPIRED %s\n", it.Name, it.NotAfter.Local().Format("2006-01-02"))
		default:
			fmt.Fprintf(&b, "%s: %d d (%s)\n", it.Name, it.DaysLeft, it.NotAfter.Local().Format("2006-01-02"))
		}
	}
	m.Message = strings.TrimSpace(b.String())
	if base != "" {
		m.URL, m.URLTitle = base+"/", "Open dashboard"
	}
	return m
}

// PushTest builds the test message.
func PushTest(by, base string) PushMessage {
	m := PushMessage{Priority: PriorityNormal, Title: "MonitorIQ test message",
		Message: "Push notifications from MonitorIQ work.\nSent by " + by + "."}
	if base != "" {
		m.URL, m.URLTitle = base+"/", "Open dashboard"
	}
	return m
}
