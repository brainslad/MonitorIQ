// Package store is the PostgreSQL persistence layer of MonitorIQ.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// MaxRetentionDays is the hard upper bound for any retention setting (2 years).
const MaxRetentionDays = 730

var ErrNotFound = errors.New("não encontrado")

// dummyHash is compared against when a username does not exist.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("monitoriq-dummy-password"), 12)

type Store struct {
	Pool *pgxpool.Pool
}

// Open connects to PostgreSQL and verifies the connection.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("MONITORIQ_DATABASE_URL inválido: %w", err)
	}
	if cfg.MaxConns < 10 {
		cfg.MaxConns = 20
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("não foi possível ligar à base de dados: %w", err)
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() { s.Pool.Close() }

// Migrate applies all pending embedded SQL migrations in order.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var exists bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migração %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ---------------------------------------------------------------- users

type User struct {
	ID          int64
	Username    string
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

func hashPassword(pw string) (string, error) {
	if len(pw) < 8 {
		return "", errors.New("a palavra-passe deve ter pelo menos 8 caracteres")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(h), err
}

func (s *Store) CreateUser(ctx context.Context, username, password string) (int64, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return 0, errors.New("nome de utilizador obrigatório")
	}
	h, err := hashPassword(password)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.Pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1,$2) RETURNING id`, username, h).Scan(&id)
	if err != nil && strings.Contains(err.Error(), "users_username_key") {
		return 0, errors.New("já existe um utilizador com esse nome")
	}
	return id, err
}

// SetPasswordByUsername sets (or creates) a user with the given password.
func (s *Store) SetPasswordByUsername(ctx context.Context, username, password string) error {
	h, err := hashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO users (username, password_hash) VALUES ($1,$2)
		ON CONFLICT (username) DO UPDATE SET password_hash = EXCLUDED.password_hash`, username, h)
	if err == nil {
		_, err = s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username=$1)`, username)
	}
	return err
}

func (s *Store) SetPassword(ctx context.Context, id int64, password string) error {
	h, err := hashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1`, id, h)
	return err
}

// Authenticate checks credentials and returns the user on success.
func (s *Store) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var u User
	var hash string
	err := s.Pool.QueryRow(ctx, `SELECT id, username, created_at, last_login_at, password_hash FROM users WHERE username=$1`,
		strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.CreatedAt, &u.LastLoginAt, &hash)
	if err != nil {
		// Spend comparable time to avoid user enumeration by timing.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrNotFound
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrNotFound
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE users SET last_login_at=now() WHERE id=$1`, u.ID)
	return &u, nil
}

func (s *Store) CheckPassword(ctx context.Context, id int64, password string) bool {
	var hash string
	if err := s.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, id).Scan(&hash); err != nil {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, username, created_at, last_login_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		var u User
		err := r.Scan(&u.ID, &u.Username, &u.CreatedAt, &u.LastLoginAt)
		return u, err
	})
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n <= 1 {
		return errors.New("não é possível apagar o último utilizador")
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
	return err
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
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

// CreateSession creates a login session and returns its token.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	token := randomToken(32)
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, csrf_token, expires_at) VALUES ($1,$2,$3,$4)`,
		tokenHash(token), userID, randomToken(24), time.Now().Add(ttl))
	return token, err
}

// SessionUser resolves a session token to its user and CSRF token.
func (s *Store) SessionUser(ctx context.Context, token string) (*User, string, error) {
	var u User
	var csrf string
	err := s.Pool.QueryRow(ctx, `SELECT u.id, u.username, u.created_at, u.last_login_at, s.csrf_token
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash=$1 AND s.expires_at > now()`, tokenHash(token)).
		Scan(&u.ID, &u.Username, &u.CreatedAt, &u.LastLoginAt, &csrf)
	if err != nil {
		return nil, "", notFound(err)
	}
	return &u, csrf, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, tokenHash(token))
	return err
}

// ---------------------------------------------------------------- settings

type Retention struct {
	RawDays       int
	AggregateDays int
	EventsDays    int
}

func (s *Store) GetRetention(ctx context.Context) (Retention, error) {
	r := Retention{RawDays: 90, AggregateDays: 730, EventsDays: 730}
	rows, err := s.Pool.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return r, err
		}
		var n int
		if _, err := fmt.Sscan(v, &n); err != nil {
			continue
		}
		switch k {
		case "raw_retention_days":
			r.RawDays = n
		case "aggregate_retention_days":
			r.AggregateDays = n
		case "events_retention_days":
			r.EventsDays = n
		}
	}
	r.clamp()
	return r, rows.Err()
}

func clampDays(n int) int {
	if n < 1 {
		return 1
	}
	if n > MaxRetentionDays {
		return MaxRetentionDays
	}
	return n
}

func (r *Retention) clamp() {
	r.RawDays = clampDays(r.RawDays)
	r.AggregateDays = clampDays(r.AggregateDays)
	r.EventsDays = clampDays(r.EventsDays)
}

func (s *Store) SaveRetention(ctx context.Context, r Retention) error {
	r.clamp()
	_, err := s.Pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES
		('raw_retention_days', $1), ('aggregate_retention_days', $2), ('events_retention_days', $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		fmt.Sprint(r.RawDays), fmt.Sprint(r.AggregateDays), fmt.Sprint(r.EventsDays))
	return err
}

// PurgeStats reports how many rows a purge removed.
type PurgeStats struct {
	Results, Hourly, Events, Sessions int64
}

func (s *Store) deleteInBatches(ctx context.Context, query string, cutoff time.Time) (int64, error) {
	var total int64
	for {
		tag, err := s.Pool.Exec(ctx, query, cutoff)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < 5000 {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Purge deletes data older than the configured retention periods.
func (s *Store) Purge(ctx context.Context) (PurgeStats, error) {
	var st PurgeStats
	r, err := s.GetRetention(ctx)
	if err != nil {
		return st, err
	}
	now := time.Now()
	day := 24 * time.Hour
	if st.Results, err = s.deleteInBatches(ctx,
		`DELETE FROM check_results WHERE id IN (SELECT id FROM check_results WHERE checked_at < $1 LIMIT 5000)`,
		now.Add(-time.Duration(r.RawDays)*day)); err != nil {
		return st, err
	}
	if st.Hourly, err = s.deleteInBatches(ctx,
		`DELETE FROM check_results_hourly WHERE ctid IN (SELECT ctid FROM check_results_hourly WHERE bucket < $1 LIMIT 5000)`,
		now.Add(-time.Duration(r.AggregateDays)*day)); err != nil {
		return st, err
	}
	if st.Events, err = s.deleteInBatches(ctx,
		`DELETE FROM events WHERE id IN (SELECT id FROM events WHERE created_at < $1 LIMIT 5000)`,
		now.Add(-time.Duration(r.EventsDays)*day)); err != nil {
		return st, err
	}
	tag, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	if err == nil {
		st.Sessions = tag.RowsAffected()
	}
	return st, err
}

// DBStats is shown on the settings page.
type DBStats struct {
	SizeBytes    int64
	Results      int64
	Hourly       int64
	Events       int64
	OldestResult *time.Time
}

func (s *Store) Stats(ctx context.Context) (DBStats, error) {
	var st DBStats
	err := s.Pool.QueryRow(ctx, `SELECT pg_database_size(current_database()),
		(SELECT count(*) FROM check_results),
		(SELECT count(*) FROM check_results_hourly),
		(SELECT count(*) FROM events),
		(SELECT min(checked_at) FROM check_results)`).
		Scan(&st.SizeBytes, &st.Results, &st.Hourly, &st.Events, &st.OldestResult)
	return st, err
}
