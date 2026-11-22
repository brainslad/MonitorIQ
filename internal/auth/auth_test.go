// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

func TestTOTPRFC6238(t *testing.T) {
	// RFC 6238 appendix B, SHA1, truncated to 6 digits.
	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range cases {
		got, err := TOTPCode(secret, time.Unix(ts, 0))
		if err != nil || got != want {
			t.Errorf("t=%d: got %s (%v), want %s", ts, got, err, want)
		}
	}
}

func TestValidateTOTPWindow(t *testing.T) {
	secret := NewTOTPSecret()
	now := time.Unix(1_700_000_000, 0)
	for _, d := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, _ := TOTPCode(secret, now.Add(d))
		step, ok := ValidateTOTP(secret, code, now)
		if !ok || step != now.Add(d).Unix()/30 {
			t.Errorf("offset %v: ok=%v step=%d", d, ok, step)
		}
	}
	old, _ := TOTPCode(secret, now.Add(-90*time.Second))
	if _, ok := ValidateTOTP(secret, old, now); ok {
		t.Error("a code 3 steps old must be rejected")
	}
	if _, ok := ValidateTOTP(secret, "12345", now); ok {
		t.Error("short code accepted")
	}
	code, _ := TOTPCode(secret, now)
	if _, ok := ValidateTOTP(secret, code[:3]+" "+code[3:], now); !ok {
		t.Error("code with a space should be accepted")
	}
}

func TestTOTPURI(t *testing.T) {
	u := TOTPURI("MonitorIQ", "jane@example.com", "ABCDEF")
	if !strings.HasPrefix(u, "otpauth://totp/MonitorIQ:jane@example.com?") || !strings.Contains(u, "secret=ABCDEF") {
		t.Fatal(u)
	}
	if qr, err := QRDataURI(u); err != nil || !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Fatal(err)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes := NewRecoveryCodes(10)
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' || seen[c] {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
	}
}

func TestBox(t *testing.T) {
	b, err := NewBox([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := b.Seal("hello", "totp:1")
	if !strings.HasPrefix(enc, "v1:") || strings.Contains(enc, "hello") {
		t.Fatal(enc)
	}
	if got, err := b.Open(enc, "totp:1"); err != nil || got != "hello" {
		t.Fatal(got, err)
	}
	if _, err := b.Open(enc, "totp:2"); err == nil {
		t.Fatal("ciphertext must be bound to its associated data")
	}
	other, _ := NewBox([]byte("another key that is long enough"))
	if _, err := other.Open(enc, "totp:1"); err == nil {
		t.Fatal("decrypted with the wrong key")
	}
	v := b.SignedValue([]byte(`{"s":"x"}`))
	if d, ok := b.VerifySigned(v); !ok || string(d) != `{"s":"x"}` {
		t.Fatal("signature roundtrip")
	}
	if _, ok := b.VerifySigned(strings.Replace(v, "eyJz", "eyJt", 1)); ok {
		t.Fatal("tampered value accepted")
	}
}

func TestLoadBoxCreatesKeyFile(t *testing.T) {
	dir := t.TempDir()
	b1, src, err := LoadBox("", dir)
	if err != nil || !strings.HasSuffix(src, "secret.key") {
		t.Fatal(src, err)
	}
	enc, _ := b1.Seal("x", "")
	b2, _, err := LoadBox("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := b2.Open(enc, ""); err != nil || v != "x" {
		t.Fatal("the key file must be reused")
	}
}

func TestSettingsSecretsEncrypted(t *testing.T) {
	b, _ := NewBox([]byte("0123456789abcdef0123456789abcdef"))
	s := DefaultSettings()
	s.LDAP.BindPassword = "bind-secret"
	s.OIDC.ClientSecret = "client-secret"
	raw, err := s.Encode(b)
	if err != nil || strings.Contains(raw, "bind-secret") || strings.Contains(raw, "client-secret") {
		t.Fatal("secrets stored in clear", raw)
	}
	back, err := LoadSettings(raw, b)
	if err != nil || back.LDAP.BindPassword != "bind-secret" || back.OIDC.ClientSecret != "client-secret" {
		t.Fatal(back, err)
	}
}

func TestLDAPValidate(t *testing.T) {
	c := DefaultSettings().LDAP
	c.BaseDN = "DC=example,DC=com"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.UserFilter = "(uid=x)"
	if c.Validate() == nil {
		t.Fatal("filter without {username} accepted")
	}
	c.UserFilter = "(uid={username}"
	if c.Validate() == nil {
		t.Fatal("unbalanced filter accepted")
	}
	c = DefaultSettings().LDAP
	c.BaseDN, c.URL = "dc=x", "http://dc01"
	if c.Validate() == nil {
		t.Fatal("http URL accepted")
	}
}

func TestLDAPRejectsEmptyPassword(t *testing.T) {
	c := DefaultSettings().LDAP
	if _, err := c.Authenticate("jdoe", ""); err != ErrBadCredentials {
		t.Fatal("empty password must never reach the server", err)
	}
}

func TestClaims(t *testing.T) {
	c := DefaultSettings().OIDC
	claims := map[string]any{"preferred_username": "jane@example.com", "name": "Jane", "groups": []any{"g-ops", "g-dev"}}
	id, err := c.identityFromClaims("https://idp", "sub1", claims)
	if err != nil || id.Username != "jane@example.com" || id.Email != "jane@example.com" || id.ExternalID != "https://idp|sub1" {
		t.Fatal(id, err)
	}
	c.AllowedDomains = "other.com, @example.com"
	if _, err := c.identityFromClaims("i", "s", claims); err != nil {
		t.Fatal(err)
	}
	c.AllowedDomains = "other.com"
	if _, err := c.identityFromClaims("i", "s", claims); err != ErrDenied {
		t.Fatal("domain not enforced")
	}
	c.AllowedDomains = ""
	c.RequiredGroups = "G-OPS"
	if _, err := c.identityFromClaims("i", "s", claims); err != nil {
		t.Fatal(err)
	}
	c.RequiredGroups = "g-admins"
	if _, err := c.identityFromClaims("i", "s", claims); err != ErrDenied {
		t.Fatal("groups not enforced")
	}
	c.UsernameClaim = "upn"
	if _, err := c.identityFromClaims("i", "s", claims); err == nil {
		t.Fatal("missing username claim accepted")
	}
}
