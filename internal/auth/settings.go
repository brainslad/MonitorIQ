package auth

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// Settings is the authentication configuration edited under
// Settings → Authentication. It is stored as JSON in the settings table,
// with secrets encrypted by a Box.
type Settings struct {
	// Require2FA forces local and LDAP users to set up an authenticator app.
	// SSO users are expected to use the identity provider's MFA.
	Require2FA bool       `json:"require_2fa"`
	LDAP       LDAPConfig `json:"ldap"`
	OIDC       OIDCConfig `json:"oidc"`
}

// SettingsKey is the key in the settings table.
const SettingsKey = "auth"

// DefaultSettings returns the configuration used before anything is saved.
func DefaultSettings() Settings {
	return Settings{
		LDAP: LDAPConfig{
			URL:          "ldaps://dc01.example.com:636",
			UserFilter:   ADUserFilter,
			UsernameAttr: "sAMAccountName",
			NameAttr:     "displayName",
			EmailAttr:    "mail",
			AutoCreate:   true,
		},
		OIDC: OIDCConfig{
			ButtonLabel:   "Sign in with Microsoft",
			Scopes:        "openid profile email",
			UsernameClaim: "preferred_username",
			GroupsClaim:   "groups",
			AutoCreate:    true,
		},
	}
}

// LoadSettings decodes stored settings and decrypts their secrets.
func LoadSettings(raw string, box *Box) (Settings, error) {
	s := DefaultSettings()
	if raw == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, err
	}
	var err error
	if s.LDAP.BindPassword, err = box.Open(s.LDAP.BindPassword, "ldap-bind"); err != nil {
		return s, err
	}
	if s.OIDC.ClientSecret, err = box.Open(s.OIDC.ClientSecret, "oidc-client"); err != nil {
		return s, err
	}
	return s, nil
}

// Encode encrypts the secrets and returns the JSON to store.
func (s Settings) Encode(box *Box) (string, error) {
	var err error
	if s.LDAP.BindPassword, err = box.Seal(s.LDAP.BindPassword, "ldap-bind"); err != nil {
		return "", err
	}
	if s.OIDC.ClientSecret, err = box.Seal(s.OIDC.ClientSecret, "oidc-client"); err != nil {
		return "", err
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// Validate checks the enabled providers.
func (s *Settings) Validate() error {
	if s.LDAP.Enabled {
		if err := s.LDAP.Validate(); err != nil {
			return errors.New("LDAP: " + err.Error())
		}
	}
	if s.OIDC.Enabled {
		if err := s.OIDC.Validate(); err != nil {
			return errors.New("SSO: " + err.Error())
		}
	}
	return nil
}

// SplitList splits a comma, semicolon or newline separated list.
func SplitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validURL(raw string, schemes ...string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	for _, s := range schemes {
		if u.Scheme == s {
			return true
		}
	}
	return false
}

// Identity is a user authenticated by an external provider.
type Identity struct {
	Source      string // "ldap" or "oidc"
	ExternalID  string // LDAP DN or "<issuer>|<sub>"
	Username    string
	DisplayName string
	Email       string
}

// ErrDenied is returned when credentials are valid but the user is not
// allowed in (group or domain restrictions).
var ErrDenied = errors.New("your account is not allowed to sign in to MonitorIQ")
