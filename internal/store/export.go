// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package store

import (
	"context"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
)

// HourRow is one hourly roll-up of a monitor's checks.
type HourRow struct {
	MonitorID    int64
	Bucket       time.Time
	Total        int
	Up           int
	Degraded     int
	Down         int
	LatencySum   float64
	LatencyCount int
	LatencyMin   *float64
	LatencyMax   *float64
}

// EachHour calls fn for every hourly roll-up of the monitors in [from, to),
// ordered by monitor and hour. It streams, so it suits long ranges.
func (s *Store) EachHour(ctx context.Context, ids []int64, from, to time.Time, fn func(HourRow) error) error {
	rows, err := s.Pool.Query(ctx, `SELECT monitor_id, bucket, total, up, degraded, down,
		latency_sum, latency_count, latency_min, latency_max
		FROM check_results_hourly WHERE monitor_id = ANY($1) AND bucket >= $2 AND bucket < $3
		ORDER BY monitor_id, bucket`, ids, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h HourRow
		if err := rows.Scan(&h.MonitorID, &h.Bucket, &h.Total, &h.Up, &h.Degraded, &h.Down,
			&h.LatencySum, &h.LatencyCount, &h.LatencyMin, &h.LatencyMax); err != nil {
			return err
		}
		if err := fn(h); err != nil {
			return err
		}
	}
	return rows.Err()
}

// RawRow is one stored check result.
type RawRow struct {
	MonitorID int64
	CheckedAt time.Time
	Status    checks.Status
	LatencyMs *float64
	Message   string
}

// EachResult calls fn for every raw check result of the monitors in
// [from, to), ordered by monitor and time.
func (s *Store) EachResult(ctx context.Context, ids []int64, from, to time.Time, fn func(RawRow) error) error {
	rows, err := s.Pool.Query(ctx, `SELECT monitor_id, checked_at, status, latency_ms, message
		FROM check_results WHERE monitor_id = ANY($1) AND checked_at >= $2 AND checked_at < $3
		ORDER BY monitor_id, checked_at`, ids, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r RawRow
		if err := rows.Scan(&r.MonitorID, &r.CheckedAt, &r.Status, &r.LatencyMs, &r.Message); err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// CountResults returns how many raw check results the monitors have in [from, to).
func (s *Store) CountResults(ctx context.Context, ids []int64, from, to time.Time) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM check_results
		WHERE monitor_id = ANY($1) AND checked_at >= $2 AND checked_at < $3`, ids, from, to).Scan(&n)
	return n, err
}

// OldestResult returns the time of the oldest raw check result kept, or nil.
func (s *Store) OldestResult(ctx context.Context) (*time.Time, error) {
	var t *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT min(checked_at) FROM check_results`).Scan(&t)
	return t, err
}

// StatusChanges returns the status transitions of the monitors in [from, to)
// plus, per monitor, the last transition before from (the status the range
// starts in). They are ordered by monitor and time.
func (s *Store) StatusChanges(ctx context.Context, ids []int64, from, to time.Time) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, monitor_id, created_at, from_status, to_status, message FROM (
			SELECT id, monitor_id, created_at, from_status, to_status, message FROM events
			WHERE monitor_id = ANY($1) AND created_at >= $2 AND created_at < $3
			UNION ALL
			SELECT * FROM (SELECT DISTINCT ON (monitor_id) id, monitor_id, created_at, from_status, to_status, message
				FROM events WHERE monitor_id = ANY($1) AND created_at < $2
				ORDER BY monitor_id, created_at DESC, id DESC) before
		) e ORDER BY monitor_id, created_at, id`, ids, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.MonitorID, &e.CreatedAt, &e.From, &e.To, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
