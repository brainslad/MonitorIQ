// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package store

import (
	"context"
	"encoding/json"
	"time"
)

// NavCounts feeds the badges of the sidebar.
type NavCounts struct {
	Monitors    int
	Down        int
	Hosts       int
	Groups      int
	StatusPages int
	Users       int
}

func (s *Store) NavCounts(ctx context.Context) (NavCounts, error) {
	var n NavCounts
	err := s.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM monitors),
		(SELECT count(*) FROM monitors WHERE enabled AND status = 3),
		(SELECT count(*) FROM hosts),
		(SELECT count(*) FROM monitor_groups),
		(SELECT count(*) FROM status_pages),
		(SELECT count(*) FROM users)`).Scan(&n.Monitors, &n.Down, &n.Hosts, &n.Groups, &n.StatusPages, &n.Users)
	return n, err
}

// StatusBucket is one bar of the dashboard chart: checks by result and the
// average response time in the bucket.
type StatusBucket struct {
	T        time.Time `json:"t"`
	Up       int       `json:"up"`
	Degraded int       `json:"degraded"`
	Down     int       `json:"down"`
	AvgMs    *float64  `json:"avg_ms"`
}

// StatusSeries aggregates the hourly roll-ups of enabled monitors into
// buckets of `hours` hours starting at `start` (which must be aligned).
func (s *Store) StatusSeries(ctx context.Context, start time.Time, hours, count int) ([]StatusBucket, error) {
	out := make([]StatusBucket, count)
	step := time.Duration(hours) * time.Hour
	for i := range out {
		out[i].T = start.Add(time.Duration(i) * step)
	}
	rows, err := s.Pool.Query(ctx, `SELECT floor(extract(epoch FROM h.bucket - $1::timestamptz) / ($2 * 3600))::int AS i,
		sum(h.up), sum(h.degraded), sum(h.down), sum(h.latency_sum), sum(h.latency_count)
		FROM check_results_hourly h JOIN monitors m ON m.id = h.monitor_id
		WHERE m.enabled AND h.bucket >= $1
		GROUP BY i`, start, hours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i, up, dg, dn, lc int
		var ls float64
		if err := rows.Scan(&i, &up, &dg, &dn, &ls, &lc); err != nil {
			return nil, err
		}
		if i < 0 || i >= count {
			continue
		}
		b := &out[i]
		b.Up, b.Degraded, b.Down = up, dg, dn
		if lc > 0 {
			v := ls / float64(lc)
			b.AvgMs = &v
		}
	}
	return out, rows.Err()
}

// CountIncidents counts transitions to degraded or down since `since`.
func (s *Store) CountIncidents(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE to_status IN (2,3) AND created_at >= $1`, since).Scan(&n)
	return n, err
}

// CertInfo is the certificate seen by the last check of a TLS or HTTPS monitor.
type CertInfo struct {
	MonitorID int64
	Name      string
	Target    string
	NotBefore *time.Time
	NotAfter  time.Time
	Issuer    string
}

// DaysLeft is the number of whole days until expiry (negative when expired).
func (c CertInfo) DaysLeft() int {
	return int(time.Until(c.NotAfter).Hours() / 24)
}

// Certificates returns the certificates reported by enabled TLS and HTTPS
// monitors in the last two days, soonest expiry first.
func (s *Store) Certificates(ctx context.Context) ([]CertInfo, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT ON (r.monitor_id) r.monitor_id, m.name,
		CASE WHEN m.target <> '' THEN m.target ELSE h.address END, r.details
		FROM check_results r JOIN monitors m ON m.id = r.monitor_id JOIN hosts h ON h.id = m.host_id
		WHERE m.enabled AND m.type IN ('tls','http') AND r.checked_at > now() - interval '2 days'
		  AND (r.details ? 'not_after' OR r.details ? 'cert_expires_at')
		ORDER BY r.monitor_id, r.checked_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CertInfo
	for rows.Next() {
		var c CertInfo
		var raw []byte
		if err := rows.Scan(&c.MonitorID, &c.Name, &c.Target, &raw); err != nil {
			return nil, err
		}
		var d map[string]any
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		str := func(keys ...string) string {
			for _, k := range keys {
				if v, ok := d[k].(string); ok && v != "" {
					return v
				}
			}
			return ""
		}
		na, err := time.Parse(time.RFC3339, str("not_after", "cert_expires_at"))
		if err != nil {
			continue
		}
		c.NotAfter = na
		if nb, err := time.Parse(time.RFC3339, str("not_before", "cert_issued_at")); err == nil {
			c.NotBefore = &nb
		}
		c.Issuer = str("issuer", "cert_issuer")
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := 1; i < len(out); i++ { // insertion sort: soonest expiry first
		for j := i; j > 0 && out[j].NotAfter.Before(out[j-1].NotAfter); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// ChecksSince counts the checks run since `since`.
func (s *Store) ChecksSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT coalesce(sum(total), 0) FROM check_results_hourly WHERE bucket >= date_trunc('hour', $1::timestamptz)`, since).Scan(&n)
	return n, err
}
