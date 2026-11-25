// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/jackc/pgx/v5"
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

// DrillMonitor is one monitor's share of a dashboard chart bucket.
type DrillMonitor struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Type     string   `json:"type"`
	Up       int      `json:"up"`
	Degraded int      `json:"degraded"`
	Down     int      `json:"down"`
	AvgMs    *float64 `json:"avg_ms"`
	MaxMs    *float64 `json:"max_ms"`
}

// DrillEvent is something recorded during a dashboard chart bucket: a
// status change of a monitor, or a check that failed or was degraded.
type DrillEvent struct {
	Kind        string        `json:"kind"` // "change" or "check"
	At          time.Time     `json:"t"`
	MonitorID   int64         `json:"monitor_id"`
	MonitorName string        `json:"monitor"`
	From        checks.Status `json:"from"`
	Status      checks.Status `json:"status"`
	LatencyMs   *float64      `json:"latency_ms"`
	Message     string        `json:"message"`
}

// Drill is what happened between From and To, for the drill-down of a bar
// of the dashboard chart.
type Drill struct {
	Monitors []DrillMonitor `json:"monitors"`
	Events   []DrillEvent   `json:"events"`
	// Truncated is set when there were more events than the limit.
	Truncated bool `json:"truncated"`
}

// DrillDown returns, for the enabled monitors, the checks by result of each
// monitor (from the hourly roll-ups, problems first), and the status changes
// and failed or degraded checks recorded in [from, to), newest first.
func (s *Store) DrillDown(ctx context.Context, from, to time.Time, limit int) (*Drill, error) {
	d := &Drill{Monitors: []DrillMonitor{}, Events: []DrillEvent{}}
	rows, err := s.Pool.Query(ctx, `SELECT m.id, m.name, hs.name, m.type,
		sum(h.up)::int, sum(h.degraded)::int, sum(h.down)::int,
		CASE WHEN sum(h.latency_count) > 0 THEN sum(h.latency_sum) / sum(h.latency_count) END, max(h.latency_max)
		FROM check_results_hourly h JOIN monitors m ON m.id = h.monitor_id JOIN hosts hs ON hs.id = m.host_id
		WHERE m.enabled AND h.bucket >= $1 AND h.bucket < $2
		GROUP BY m.id, m.name, hs.name, m.type
		ORDER BY sum(h.down) DESC, sum(h.degraded) DESC, lower(m.name)`, from, to)
	if err != nil {
		return nil, err
	}
	d.Monitors, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (DrillMonitor, error) {
		var m DrillMonitor
		err := r.Scan(&m.ID, &m.Name, &m.Host, &m.Type, &m.Up, &m.Degraded, &m.Down, &m.AvgMs, &m.MaxMs)
		return m, err
	})
	if err != nil {
		return nil, err
	}
	if d.Monitors == nil {
		d.Monitors = []DrillMonitor{}
	}
	rows, err = s.Pool.Query(ctx, `SELECT * FROM (
		SELECT 'change' AS kind, e.created_at, e.monitor_id, m.name, e.from_status, e.to_status, NULL::double precision, e.message
		FROM events e JOIN monitors m ON m.id = e.monitor_id
		WHERE m.enabled AND e.created_at >= $1 AND e.created_at < $2
		UNION ALL
		SELECT 'check', r.checked_at, r.monitor_id, m.name, r.status, r.status, r.latency_ms, r.message
		FROM check_results r JOIN monitors m ON m.id = r.monitor_id
		WHERE m.enabled AND r.status IN (2,3) AND r.checked_at >= $1 AND r.checked_at < $2
	) x ORDER BY 2 DESC LIMIT $3`, from, to, limit+1)
	if err != nil {
		return nil, err
	}
	d.Events, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (DrillEvent, error) {
		var e DrillEvent
		err := r.Scan(&e.Kind, &e.At, &e.MonitorID, &e.MonitorName, &e.From, &e.Status, &e.LatencyMs, &e.Message)
		return e, err
	})
	if err != nil {
		return nil, err
	}
	if d.Events == nil {
		d.Events = []DrillEvent{}
	}
	if len(d.Events) > limit {
		d.Events, d.Truncated = d.Events[:limit], true
	}
	return d, nil
}
