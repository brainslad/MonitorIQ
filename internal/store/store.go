// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

// Package store is the PostgreSQL persistence layer of MonitorIQ.
package store

import (
	"context"
	"embed"
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

var ErrNotFound = errors.New("not found")

// dummyHash is compared against when a username does not exist.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("monitoriq-dummy-password"), 12)

type Store struct {
	Pool *pgxpool.Pool
}

// Open connects to PostgreSQL and verifies the connection.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("invalid MONITORIQ_DATABASE_URL: %w", err)
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
		return nil, fmt.Errorf("cannot connect to the database: %w", err)
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
			return fmt.Errorf("migration %s: %w", name, err)
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
