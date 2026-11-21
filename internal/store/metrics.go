package store

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brainslad/monitoriq/internal/checks"
)

// UptimeSince returns availability (up+degraded / total, 0..100) per monitor.
// Monitors without data are absent from the map.
func (s *Store) UptimeSince(ctx context.Context, ids []int64, since time.Time) (map[int64]float64, error) {
	out := map[int64]float64{}
	rows, err := s.Pool.Query(ctx, `SELECT monitor_id, sum(up + degraded)::float8, sum(total)::float8
		FROM check_results_hourly WHERE monitor_id = ANY($1) AND bucket >= date_trunc('hour', $2::timestamptz)
		GROUP BY monitor_id`, ids, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var ok, total float64
		if err := rows.Scan(&id, &ok, &total); err != nil {
			return nil, err
		}
		if total > 0 {
			out[id] = ok * 100 / total
		}
	}
	return out, rows.Err()
}

// Point is one point of a latency/availability chart.
type Point struct {
	T      time.Time `json:"t"`
	Avg    *float64  `json:"avg"`
	Min    *float64  `json:"min"`
	Max    *float64  `json:"max"`
	Uptime *float64  `json:"uptime"`
	Status int16     `json:"status,omitempty"`
}

// Series returns chart data. Short ranges use raw results, medium ranges
// hourly roll-ups and long ranges daily roll-ups.
func (s *Store) Series(ctx context.Context, id int64, since time.Time) ([]Point, error) {
	span := time.Since(since)
	var rows pgx.Rows
	var err error
	switch {
	case span <= 48*time.Hour:
		rows, err = s.Pool.Query(ctx, `SELECT checked_at, latency_ms, latency_ms, latency_ms,
			CASE WHEN status = 3 THEN 0.0 ELSE 100.0 END, status
			FROM check_results WHERE monitor_id=$1 AND checked_at >= $2 ORDER BY checked_at`, id, since)
	case span <= 120*24*time.Hour:
		rows, err = s.Pool.Query(ctx, `SELECT bucket,
			CASE WHEN latency_count > 0 THEN latency_sum / latency_count END, latency_min, latency_max,
			CASE WHEN total > 0 THEN (up + degraded) * 100.0 / total END, 0::smallint
			FROM check_results_hourly WHERE monitor_id=$1 AND bucket >= $2 ORDER BY bucket`, id, since)
	default:
		rows, err = s.Pool.Query(ctx, `SELECT date_trunc('day', bucket) AS d,
			CASE WHEN sum(latency_count) > 0 THEN sum(latency_sum) / sum(latency_count) END, min(latency_min), max(latency_max),
			CASE WHEN sum(total) > 0 THEN sum(up + degraded) * 100.0 / sum(total) END, 0::smallint
			FROM check_results_hourly WHERE monitor_id=$1 AND bucket >= $2 GROUP BY d ORDER BY d`, id, since)
	}
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Point, error) {
		var p Point
		err := r.Scan(&p.T, &p.Avg, &p.Min, &p.Max, &p.Uptime, &p.Status)
		return p, err
	})
}

// Sparklines returns the hourly average latency of the last 24 hours per monitor.
func (s *Store) Sparklines(ctx context.Context, ids []int64) (map[int64][]float64, error) {
	out := map[int64][]float64{}
	rows, err := s.Pool.Query(ctx, `SELECT monitor_id, latency_sum / latency_count FROM check_results_hourly
		WHERE monitor_id = ANY($1) AND bucket >= now() - interval '24 hours' AND latency_count > 0
		ORDER BY monitor_id, bucket`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var v float64
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = append(out[id], v)
	}
	return out, rows.Err()
}

// DayStat is the availability of one monitor on one day.
type DayStat struct {
	Day      time.Time
	Total    int
	Up       int
	Degraded int
	Down     int
}

// Uptime returns the availability percentage of the day, or -1 without data.
func (d DayStat) Uptime() float64 {
	if d.Total == 0 {
		return -1
	}
	return float64(d.Up+d.Degraded) * 100 / float64(d.Total)
}

// Level classifies the day for the status bars.
func (d DayStat) Level() string {
	switch {
	case d.Total == 0:
		return "none"
	case d.Down == 0 && d.Degraded == 0:
		return "up"
	case d.Uptime() >= 99:
		return "minor"
	case d.Uptime() >= 95:
		return "degraded"
	default:
		return "down"
	}
}

// DailyStats returns `days` consecutive day buckets (oldest first) per monitor,
// using the server's local time zone for day boundaries.
func (s *Store) DailyStats(ctx context.Context, ids []int64, days int) (map[int64][]DayStat, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
	out := map[int64][]DayStat{}
	for _, id := range ids {
		list := make([]DayStat, days)
		for i := range list {
			list[i].Day = start.AddDate(0, 0, i)
		}
		out[id] = list
	}
	rows, err := s.Pool.Query(ctx, `SELECT monitor_id, bucket, total, up, degraded, down FROM check_results_hourly
		WHERE monitor_id = ANY($1) AND bucket >= $2`, ids, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var b time.Time
		var t, u, dg, dn int
		if err := rows.Scan(&id, &b, &t, &u, &dg, &dn); err != nil {
			return nil, err
		}
		b = b.In(now.Location())
		day := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, now.Location())
		idx := int(day.Sub(start).Hours()/24 + 0.5)
		if list, ok := out[id]; ok && idx >= 0 && idx < len(list) {
			list[idx].Total += t
			list[idx].Up += u
			list[idx].Degraded += dg
			list[idx].Down += dn
		}
	}
	return out, rows.Err()
}

// Result is a stored check result.
type Result struct {
	CheckedAt time.Time
	Status    checks.Status
	LatencyMs *float64
	Message   string
	Details   map[string]any
}

func (s *Store) RecentResults(ctx context.Context, id int64, limit int) ([]Result, error) {
	rows, err := s.Pool.Query(ctx, `SELECT checked_at, status, latency_ms, message, details FROM check_results
		WHERE monitor_id=$1 ORDER BY checked_at DESC LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Result, error) {
		var res Result
		var details []byte
		err := r.Scan(&res.CheckedAt, &res.Status, &res.LatencyMs, &res.Message, &details)
		if len(details) > 0 {
			_ = json.Unmarshal(details, &res.Details)
		}
		return res, err
	})
}

// Event is a status transition.
type Event struct {
	ID          int64
	MonitorID   int64
	MonitorName string
	CreatedAt   time.Time
	From, To    checks.Status
	Message     string
	// Duration until the next event of the same monitor (nil while ongoing).
	EndedAt *time.Time
}

func (s *Store) ListEvents(ctx context.Context, ids []int64, since time.Time, limit int) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `SELECT e.id, e.monitor_id, m.name, e.created_at, e.from_status, e.to_status, e.message,
		(SELECT min(n.created_at) FROM events n WHERE n.monitor_id = e.monitor_id AND n.created_at > e.created_at)
		FROM events e JOIN monitors m ON m.id = e.monitor_id
		WHERE ($1::bigint[] IS NULL OR e.monitor_id = ANY($1)) AND e.created_at >= $2
		ORDER BY e.created_at DESC LIMIT $3`, ids, since, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		err := r.Scan(&e.ID, &e.MonitorID, &e.MonitorName, &e.CreatedAt, &e.From, &e.To, &e.Message, &e.EndedAt)
		return e, err
	})
}

// ---------------------------------------------------------------- status pages

type StatusPageMonitor struct {
	MonitorID   int64
	DisplayName string
	Position    int
}

type StatusPage struct {
	ID          int64
	Slug        string
	Title       string
	Description string
	Published   bool
	CreatedAt   time.Time
	Monitors    []StatusPageMonitor
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (s *Store) ListStatusPages(ctx context.Context) ([]StatusPage, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, slug, title, description, published, created_at FROM status_pages ORDER BY title`)
	if err != nil {
		return nil, err
	}
	pages, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (StatusPage, error) {
		var p StatusPage
		err := r.Scan(&p.ID, &p.Slug, &p.Title, &p.Description, &p.Published, &p.CreatedAt)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	for i := range pages {
		if pages[i].Monitors, err = s.statusPageMonitors(ctx, pages[i].ID); err != nil {
			return nil, err
		}
	}
	return pages, nil
}

func (s *Store) statusPageMonitors(ctx context.Context, pageID int64) ([]StatusPageMonitor, error) {
	rows, err := s.Pool.Query(ctx, `SELECT monitor_id, display_name, position FROM status_page_monitors
		WHERE status_page_id=$1 ORDER BY position, monitor_id`, pageID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (StatusPageMonitor, error) {
		var m StatusPageMonitor
		err := r.Scan(&m.MonitorID, &m.DisplayName, &m.Position)
		return m, err
	})
}

func (s *Store) getStatusPage(ctx context.Context, where string, arg any) (*StatusPage, error) {
	var p StatusPage
	err := s.Pool.QueryRow(ctx, `SELECT id, slug, title, description, published, created_at FROM status_pages WHERE `+where, arg).
		Scan(&p.ID, &p.Slug, &p.Title, &p.Description, &p.Published, &p.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	p.Monitors, err = s.statusPageMonitors(ctx, p.ID)
	return &p, err
}

func (s *Store) GetStatusPage(ctx context.Context, id int64) (*StatusPage, error) {
	return s.getStatusPage(ctx, "id=$1", id)
}

func (s *Store) GetStatusPageBySlug(ctx context.Context, slug string) (*StatusPage, error) {
	return s.getStatusPage(ctx, "slug=$1", slug)
}

func (s *Store) SaveStatusPage(ctx context.Context, p *StatusPage) error {
	p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" {
		return errors.New("o título é obrigatório")
	}
	if !slugRe.MatchString(p.Slug) {
		return errors.New("o endereço (slug) só pode ter letras minúsculas, números e hífens")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if p.ID == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO status_pages (slug, title, description, published) VALUES ($1,$2,$3,$4) RETURNING id`,
			p.Slug, p.Title, p.Description, p.Published).Scan(&p.ID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE status_pages SET slug=$2, title=$3, description=$4, published=$5 WHERE id=$1`,
			p.ID, p.Slug, p.Title, p.Description, p.Published)
	}
	if err != nil {
		if strings.Contains(err.Error(), "status_pages_slug_key") {
			return errors.New("já existe uma status page com esse endereço")
		}
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM status_page_monitors WHERE status_page_id=$1`, p.ID); err != nil {
		return err
	}
	for i, m := range p.Monitors {
		if _, err := tx.Exec(ctx, `INSERT INTO status_page_monitors (status_page_id, monitor_id, position, display_name)
			VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, p.ID, m.MonitorID, i, strings.TrimSpace(m.DisplayName)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteStatusPage(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM status_pages WHERE id=$1`, id)
	return err
}
