// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Authentication sources.
const (
	SourceLocal = "local"
	SourceLDAP  = "ldap"
	SourceOIDC  = "oidc"
)

// Session MFA states.
const (
	MFADone   = ""
	MFAVerify = "verify"
	MFAEnroll = "enroll"
)

// ErrUsernameTaken is returned when an external identity would reuse the
// username of a different account.
var ErrUsernameTaken = errors.New("a user with that name already exists")

type User struct {
	ID          int64
	Username    string
	CreatedAt   time.Time
	LastLoginAt *time.Time
	Source      string
	ExternalID  string
	DisplayName string
	Email       string
	TOTPEnabled bool
	EmailAlerts bool
	// CustomName is set when the display name was chosen in MonitorIQ.
	CustomName bool
}

// IsLocal reports whether the user signs in with a password stored here.
func (u *User) IsLocal() bool { return u.Source == SourceLocal }

// Greeting is the name the dashboard greets the user with: the display name
// chosen in MonitorIQ as typed, else the first word of the directory name
// or of the username ("jane.doe" → "Jane").
func (u *User) Greeting() string {
	if u.CustomName && strings.TrimSpace(u.DisplayName) != "" {
		return strings.TrimSpace(u.DisplayName)
	}
	name := strings.TrimSpace(u.DisplayName)
	if name == "" {
		name = strings.TrimSpace(u.Username)
	}
	if i := strings.IndexAny(name, " @._-"); i > 0 {
		name = name[:i]
	}
	if name == "" {
		return ""
	}
	r := []rune(name)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// Label is the display name, falling back to the username.
func (u *User) Label() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

const userCols = `u.id, u.username, u.created_at, u.last_login_at, u.auth_source, u.external_id, u.display_name, u.email, u.totp_enabled, u.email_alerts, u.display_name_custom`

func scanUser(r pgx.Row, extra ...any) (*User, error) {
	var u User
	dest := append([]any{&u.ID, &u.Username, &u.CreatedAt, &u.LastLoginAt, &u.Source, &u.ExternalID,
		&u.DisplayName, &u.Email, &u.TOTPEnabled, &u.EmailAlerts, &u.CustomName}, extra...)
	if err := r.Scan(dest...); err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func hashPassword(pw string) (string, error) {
	if len(pw) < 8 {
		return "", errors.New("the password must be at least 8 characters long")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(h), err
}

func uniqueErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "users_username_key") {
		return ErrUsernameTaken
	}
	return err
}

// CreateUser creates a local user with a password.
func (s *Store) CreateUser(ctx context.Context, username, password string) (int64, error) {
	return s.CreateUserNamed(ctx, username, "", password)
}

// CreateUserNamed creates a local user with a password and a display name.
func (s *Store) CreateUserNamed(ctx context.Context, username, displayName, password string) (int64, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return 0, errors.New("username is required")
	}
	h, err := hashPassword(password)
	if err != nil {
		return 0, err
	}
	displayName, err = CleanDisplayName(displayName)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.Pool.QueryRow(ctx, `INSERT INTO users (username, password_hash, display_name, display_name_custom)
		VALUES ($1,$2,$3,$3 <> '') RETURNING id`, username, h, displayName).Scan(&id)
	return id, uniqueErr(err)
}

// CreateExternalUser pre-registers (externalID empty) or provisions a user
// that signs in through LDAP or OpenID Connect.
func (s *Store) CreateExternalUser(ctx context.Context, source, username, externalID, displayName, email string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username is required")
	}
	if source != SourceLDAP && source != SourceOIDC {
		return nil, errors.New("invalid authentication source")
	}
	u, err := scanUser(s.Pool.QueryRow(ctx, `INSERT INTO users AS u (username, password_hash, auth_source, external_id, display_name, email)
		VALUES ($1,'',$2,$3,$4,$5) RETURNING `+userCols, username, source, externalID, displayName, email))
	return u, uniqueErr(err)
}

// FindExternalUser resolves an external identity to a user. It matches the
// stable external id first and then a pre-registered user (same source and
// username, no external id yet), which it links to the identity.
func (s *Store) FindExternalUser(ctx context.Context, source, externalID, username string) (*User, error) {
	u, err := scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE auth_source=$1 AND external_id=$2`, source, externalID))
	if err == nil || !errors.Is(err, ErrNotFound) {
		return u, err
	}
	return scanUser(s.Pool.QueryRow(ctx, `UPDATE users u SET external_id=$3
		WHERE auth_source=$1 AND lower(username)=lower($2) AND external_id='' RETURNING `+userCols, source, username, externalID))
}

// UpdateProfile refreshes the name and e-mail reported by the directory.
// A display name set in MonitorIQ (SetDisplayName) is kept.
func (s *Store) UpdateProfile(ctx context.Context, id int64, displayName, email string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET display_name = CASE WHEN display_name_custom THEN display_name ELSE $2 END,
		email=$3 WHERE id=$1`, id, displayName, email)
	return err
}

// MaxDisplayName is the longest display name accepted, in characters.
const MaxDisplayName = 80

// CleanDisplayName trims a display name and checks its length and characters.
func CleanDisplayName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > MaxDisplayName {
		return "", errors.New("the display name can be at most 80 characters long")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("the display name contains invalid characters")
		}
	}
	return name, nil
}

// SetDisplayName sets the name shown in the interface and the dashboard
// greeting. An empty name goes back to the username (or, for LDAP and SSO
// users, to the name the directory reports at the next sign-in).
func (s *Store) SetDisplayName(ctx context.Context, id int64, name string) error {
	name, err := CleanDisplayName(name)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE users SET display_name=$2, display_name_custom=($2 <> '') WHERE id=$1`, id, name)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE id=$1`, id))
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE lower(username)=lower($1)
		ORDER BY (username=$1) DESC LIMIT 1`, strings.TrimSpace(username)))
}

// SetPasswordByUsername sets (or creates) a local user with the given password.
func (s *Store) SetPasswordByUsername(ctx context.Context, username, password string) error {
	h, err := hashPassword(password)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO users (username, password_hash) VALUES ($1,$2)
		ON CONFLICT (username) DO UPDATE SET password_hash = EXCLUDED.password_hash WHERE users.auth_source='local'`, username, h)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("this user signs in through LDAP or SSO; its password is managed there")
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username=$1)`, username)
	return err
}

func (s *Store) SetPassword(ctx context.Context, id int64, password string) error {
	h, err := hashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1 AND auth_source='local'`, id, h)
	return err
}

// Authenticate checks local credentials and returns the user on success.
func (s *Store) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var hash string
	u, err := scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+`, u.password_hash FROM users u
		WHERE username=$1 AND auth_source='local'`, strings.TrimSpace(username)), &hash)
	if err != nil {
		// Spend comparable time to avoid user enumeration by timing.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrNotFound
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrNotFound
	}
	return u, nil
}

// TouchLogin records a successful sign-in.
func (s *Store) TouchLogin(ctx context.Context, id int64) {
	_, _ = s.Pool.Exec(ctx, `UPDATE users SET last_login_at=now() WHERE id=$1`, id)
}

func (s *Store) CheckPassword(ctx context.Context, id int64, password string) bool {
	var hash string
	if err := s.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1 AND auth_source='local'`, id).Scan(&hash); err != nil || hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+userCols+` FROM users u ORDER BY username`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		u, err := scanUser(r)
		if err != nil {
			return User{}, err
		}
		return *u, nil
	})
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n <= 1 {
		return errors.New("the last user cannot be deleted")
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
	return err
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------- two-factor

// TOTPSecret returns the stored (encrypted) TOTP secret and the last
// accepted time step.
func (s *Store) TOTPSecret(ctx context.Context, id int64) (secret string, lastStep int64, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT totp_secret, totp_last_step FROM users WHERE id=$1 AND totp_enabled`, id).Scan(&secret, &lastStep)
	return secret, lastStep, notFound(err)
}

// UseTOTPStep records step as used. It fails when the step (or a later one)
// was already used, which prevents a code from being replayed.
func (s *Store) UseTOTPStep(ctx context.Context, id, step int64) bool {
	tag, err := s.Pool.Exec(ctx, `UPDATE users SET totp_last_step=$2 WHERE id=$1 AND totp_last_step < $2`, id, step)
	return err == nil && tag.RowsAffected() == 1
}

// EnableTOTP stores the encrypted secret, enables 2FA and replaces the
// recovery codes.
func (s *Store) EnableTOTP(ctx context.Context, id int64, encSecret string, step int64, recovery []string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE users SET totp_secret=$2, totp_enabled=true, totp_last_step=$3 WHERE id=$1`, id, encSecret, step); err != nil {
		return err
	}
	if err := replaceRecovery(ctx, tx, id, recovery); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DisableTOTP turns 2FA off and deletes the recovery codes.
func (s *Store) DisableTOTP(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `WITH d AS (DELETE FROM recovery_codes WHERE user_id=$1)
		UPDATE users SET totp_secret='', totp_enabled=false, totp_last_step=0 WHERE id=$1`, id)
	return err
}

// ResetTOTP is used by administrators: it disables 2FA and signs the user out.
func (s *Store) ResetTOTP(ctx context.Context, id int64) error {
	if err := s.DisableTOTP(ctx, id); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id)
	return err
}

func (s *Store) ReplaceRecoveryCodes(ctx context.Context, id int64, codes []string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := replaceRecovery(ctx, tx, id, codes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func normalizeCode(c string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
}

func codeHash(c string) []byte {
	h := sha256.Sum256([]byte("monitoriq-recovery:" + normalizeCode(c)))
	return h[:]
}

func replaceRecovery(ctx context.Context, tx pgx.Tx, id int64, codes []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id=$1`, id); err != nil {
		return err
	}
	for _, c := range codes {
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1,$2)`, id, codeHash(c)); err != nil {
			return err
		}
	}
	return nil
}

// UseRecoveryCode consumes a recovery code. It returns false when the code
// is unknown or was already used.
func (s *Store) UseRecoveryCode(ctx context.Context, id int64, code string) bool {
	if len(normalizeCode(code)) < 8 {
		return false
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE recovery_codes SET used_at=now() WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL`,
		id, codeHash(code))
	return err == nil && tag.RowsAffected() == 1
}

// RecoveryCodesLeft counts unused recovery codes.
func (s *Store) RecoveryCodesLeft(ctx context.Context, id int64) int {
	var n int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id=$1 AND used_at IS NULL`, id).Scan(&n)
	return n
}

// ---------------------------------------------------------------- sessions

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// Session is an authenticated (or partially authenticated) browser session.
type Session struct {
	User   *User
	CSRF   string
	MFA    string // MFADone, MFAVerify or MFAEnroll
	Method string // how the first factor was verified: local, ldap, oidc
}

// CreateSession creates a login session and returns its token.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration, mfa, method string) (string, error) {
	token := randomToken(32)
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, csrf_token, expires_at, mfa, method) VALUES ($1,$2,$3,$4,$5,$6)`,
		tokenHash(token), userID, randomToken(24), time.Now().Add(ttl), mfa, method)
	return token, err
}

// GetSession resolves a session token.
func (s *Store) GetSession(ctx context.Context, token string) (*Session, error) {
	var sess Session
	u, err := scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+`, s.csrf_token, s.mfa, s.method
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash=$1 AND s.expires_at > now()`, tokenHash(token)), &sess.CSRF, &sess.MFA, &sess.Method)
	if err != nil {
		return nil, err
	}
	sess.User = u
	return &sess, nil
}

// CompleteSession marks a session as fully authenticated (2FA passed) and
// extends it to ttl.
func (s *Store) CompleteSession(ctx context.Context, token string, ttl time.Duration) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET mfa='', expires_at=$2 WHERE token_hash=$1`, tokenHash(token), time.Now().Add(ttl))
	return err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, tokenHash(token))
	return err
}

// DeleteOtherSessions signs a user out everywhere except the given session.
func (s *Store) DeleteOtherSessions(ctx context.Context, userID int64, keepToken string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2`, userID, tokenHash(keepToken))
	return err
}

// ---------------------------------------------------------------- key/value settings

// GetSetting returns a raw setting value ("" when unset).
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1,$2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}
