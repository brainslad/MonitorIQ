// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

// Package notify sends e-mail: alerts when a monitor changes status, a daily
// digest of certificates that are about to expire, and test messages.
package notify

import (
	"encoding/json"
	"errors"
	"net/mail"
	"strings"

	"github.com/brainslad/monitoriq/internal/auth"
	"github.com/brainslad/monitoriq/internal/store"
)

// SettingsKey is the key in the settings table.
const SettingsKey = "email"

// Connection security for the SMTP server.
const (
	SecurityStartTLS = "starttls" // port 587, upgrade with STARTTLS (required)
	SecurityTLS      = "tls"      // port 465, implicit TLS
	SecurityNone     = "none"     // port 25, plain (local relays only)
)

// Settings is the e-mail configuration edited under Settings → Email.
// It is stored as JSON in the settings table, with the password encrypted.
type Settings struct {
	Enabled            bool   `json:"enabled"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Security           string `json:"security"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	Username           string `json:"username"`
	Password           string `json:"password"`
	FromAddress        string `json:"from_address"`
	FromName           string `json:"from_name"`
	// Recipients receive every alert (comma separated).
	Recipients string `json:"recipients"`
	// DashboardURL is used for links in messages (defaults to MONITORIQ_BASE_URL).
	DashboardURL string `json:"dashboard_url"`

	OnDown     bool `json:"on_down"`
	OnDegraded bool `json:"on_degraded"`
	OnRecovery bool `json:"on_recovery"`
	// CertDigest sends one message a day listing certificates that expire
	// within CertDays days, at DigestHour (server time).
	CertDigest bool `json:"cert_digest"`
	CertDays   int  `json:"cert_days"`
	DigestHour int  `json:"digest_hour"`
}

// DefaultSettings returns the configuration used before anything is saved.
func DefaultSettings() Settings {
	return Settings{
		Port:       587,
		Security:   SecurityStartTLS,
		FromName:   "MonitorIQ",
		OnDown:     true,
		OnRecovery: true,
		CertDigest: true,
		CertDays:   21,
		DigestHour: 8,
	}
}

// LoadSettings decodes stored settings and decrypts the password.
func LoadSettings(raw string, box *auth.Box) (Settings, error) {
	s := DefaultSettings()
	if raw == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, err
	}
	var err error
	s.Password, err = box.Open(s.Password, "smtp-password")
	return s, err
}

// Encode encrypts the password and returns the JSON to store.
func (s Settings) Encode(box *auth.Box) (string, error) {
	var err error
	if s.Password, err = box.Seal(s.Password, "smtp-password"); err != nil {
		return "", err
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// Validate normalises the settings and checks them. The server fields are
// only required when sending is enabled.
func (s *Settings) Validate() error {
	s.Host = strings.TrimSpace(s.Host)
	s.Username = strings.TrimSpace(s.Username)
	s.FromAddress = strings.TrimSpace(s.FromAddress)
	s.FromName = strings.TrimSpace(s.FromName)
	s.DashboardURL = strings.TrimRight(strings.TrimSpace(s.DashboardURL), "/")
	switch s.Security {
	case SecurityStartTLS, SecurityTLS, SecurityNone:
	default:
		return errors.New("choose how to connect to the SMTP server")
	}
	if s.Port == 0 {
		s.Port = map[string]int{SecurityStartTLS: 587, SecurityTLS: 465, SecurityNone: 25}[s.Security]
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("invalid SMTP port (1-65535)")
	}
	if s.CertDays < 1 || s.CertDays > 90 {
		return errors.New("the certificate warning must be between 1 and 90 days")
	}
	if s.DigestHour < 0 || s.DigestHour > 23 {
		return errors.New("the digest hour must be between 0 and 23")
	}
	rec, err := store.NormalizeEmails(s.Recipients)
	if err != nil {
		return errors.New("recipients: " + err.Error())
	}
	s.Recipients = rec
	if s.DashboardURL != "" && !strings.HasPrefix(s.DashboardURL, "http://") && !strings.HasPrefix(s.DashboardURL, "https://") {
		return errors.New("the dashboard URL must start with http:// or https://")
	}
	if s.FromAddress != "" {
		a, err := mail.ParseAddress(s.FromAddress)
		if err != nil {
			return errors.New("the sender address is not a valid e-mail address")
		}
		s.FromAddress = a.Address
	}
	if !s.Enabled {
		return nil
	}
	if s.Host == "" {
		return errors.New("enter the SMTP server")
	}
	if s.FromAddress == "" {
		return errors.New("enter the sender address")
	}
	if s.Username != "" && s.Security == SecurityNone && !isLocal(s.Host) {
		return errors.New("a password can only be sent over an encrypted connection: use STARTTLS or TLS")
	}
	return nil
}

func isLocal(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
