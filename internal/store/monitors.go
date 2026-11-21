package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brainslad/monitoriq/internal/checks"
)

// ---------------------------------------------------------------- hosts

type Host struct {
	ID           int64
	Name         string
	Address      string
	Description  string
	CreatedAt    time.Time
	MonitorCount int
}

func (s *Store) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.Pool.Query(ctx, `SELECT h.id, h.name, h.address, h.description, h.created_at,
		(SELECT count(*) FROM monitors m WHERE m.host_id = h.id)
		FROM hosts h ORDER BY lower(h.name)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Host, error) {
		var h Host
		err := r.Scan(&h.ID, &h.Name, &h.Address, &h.Description, &h.CreatedAt, &h.MonitorCount)
		return h, err
	})
}

func (s *Store) GetHost(ctx context.Context, id int64) (*Host, error) {
	var h Host
	err := s.Pool.QueryRow(ctx, `SELECT id, name, address, description, created_at FROM hosts WHERE id=$1`, id).
		Scan(&h.ID, &h.Name, &h.Address, &h.Description, &h.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &h, nil
}

func validateHost(h *Host) error {
	h.Name = strings.TrimSpace(h.Name)
	h.Address = strings.TrimSpace(h.Address)
	h.Description = strings.TrimSpace(h.Description)
	if h.Name == "" {
		return errors.New("host name is required")
	}
	if h.Address == "" {
		return errors.New("host address is required")
	}
	return nil
}

func (s *Store) SaveHost(ctx context.Context, h *Host) error {
	if err := validateHost(h); err != nil {
		return err
	}
	if h.ID == 0 {
		return s.Pool.QueryRow(ctx, `INSERT INTO hosts (name, address, description) VALUES ($1,$2,$3) RETURNING id`,
			h.Name, h.Address, h.Description).Scan(&h.ID)
	}
	_, err := s.Pool.Exec(ctx, `UPDATE hosts SET name=$2, address=$3, description=$4 WHERE id=$1`,
		h.ID, h.Name, h.Address, h.Description)
	return err
}

func (s *Store) DeleteHost(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM hosts WHERE id=$1`, id)
	return err
}

// ---------------------------------------------------------------- monitors

type Monitor struct {
	ID                  int64
	HostID              int64
	HostName            string
	HostAddress         string
	Name                string
	Type                string
	Target              string
	Port                int
	IntervalSeconds     int
	TimeoutSeconds      int
	Retries             int
	Enabled             bool
	Config              checks.Config
	Status              checks.Status
	LastCheckedAt       *time.Time
	LastLatencyMs       *float64
	LastMessage         string
	ConsecutiveFailures int
	StatusChangedAt     *time.Time
	CreatedAt           time.Time
}

// EffectiveTarget is the monitor target, falling back to the host address.
func (m *Monitor) EffectiveTarget() string {
	if strings.TrimSpace(m.Target) != "" {
		return strings.TrimSpace(m.Target)
	}
	if m.Type == checks.TypeHTTP && !strings.Contains(m.HostAddress, "://") {
		return "https://" + m.HostAddress
	}
	return m.HostAddress
}

// CheckSpec converts the monitor into the input of a check.
func (m *Monitor) CheckSpec() checks.Monitor {
	return checks.Monitor{
		Type:    m.Type,
		Target:  m.EffectiveTarget(),
		Port:    m.Port,
		Timeout: time.Duration(m.TimeoutSeconds) * time.Second,
		Config:  m.Config,
	}
}

// DisplayStatus is the status shown in the UI (paused monitors show as unknown).
func (m *Monitor) DisplayStatus() checks.Status {
	if !m.Enabled {
		return checks.Unknown
	}
	return m.Status
}

const monitorCols = `m.id, m.host_id, h.name, h.address, m.name, m.type, m.target, m.port, m.interval_seconds,
	m.timeout_seconds, m.retries, m.enabled, m.config, m.status, m.last_checked_at, m.last_latency_ms,
	m.last_message, m.consecutive_failures, m.status_changed_at, m.created_at`

func scanMonitor(r pgx.Row) (Monitor, error) {
	var m Monitor
	var cfg []byte
	err := r.Scan(&m.ID, &m.HostID, &m.HostName, &m.HostAddress, &m.Name, &m.Type, &m.Target, &m.Port,
		&m.IntervalSeconds, &m.TimeoutSeconds, &m.Retries, &m.Enabled, &cfg, &m.Status, &m.LastCheckedAt,
		&m.LastLatencyMs, &m.LastMessage, &m.ConsecutiveFailures, &m.StatusChangedAt, &m.CreatedAt)
	if err == nil && len(cfg) > 0 {
		_ = json.Unmarshal(cfg, &m.Config)
	}
	return m, err
}

func (s *Store) queryMonitors(ctx context.Context, where string, args ...any) ([]Monitor, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+monitorCols+` FROM monitors m JOIN hosts h ON h.id = m.host_id `+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Monitor, error) { return scanMonitor(r) })
}

func (s *Store) ListMonitors(ctx context.Context) ([]Monitor, error) {
	return s.queryMonitors(ctx, `ORDER BY lower(h.name), lower(m.name)`)
}

func (s *Store) ListMonitorsByIDs(ctx context.Context, ids []int64) ([]Monitor, error) {
	return s.queryMonitors(ctx, `WHERE m.id = ANY($1)`, ids)
}

func (s *Store) GetMonitor(ctx context.Context, id int64) (*Monitor, error) {
	m, err := scanMonitor(s.Pool.QueryRow(ctx, `SELECT `+monitorCols+` FROM monitors m JOIN hosts h ON h.id = m.host_id WHERE m.id=$1`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &m, nil
}

// DueMonitors returns enabled monitors whose next check time has passed.
func (s *Store) DueMonitors(ctx context.Context) ([]Monitor, error) {
	return s.queryMonitors(ctx, `WHERE m.enabled AND (m.last_checked_at IS NULL
		OR m.last_checked_at + make_interval(secs => m.interval_seconds) <= now())
		ORDER BY m.last_checked_at NULLS FIRST`)
}

// ValidateMonitor normalises and validates user input.
func ValidateMonitor(m *Monitor) error {
	m.Name = strings.TrimSpace(m.Name)
	m.Target = strings.TrimSpace(m.Target)
	if m.Name == "" {
		return errors.New("monitor name is required")
	}
	if !checks.ValidType(m.Type) {
		return errors.New("invalid monitor type")
	}
	if m.HostID == 0 {
		return errors.New("choose a host")
	}
	if m.Port < 0 || m.Port > 65535 {
		return errors.New("invalid port (0-65535)")
	}
	if m.Type == checks.TypeTCP && m.Port == 0 {
		return errors.New("enter the TCP port to check")
	}
	if m.IntervalSeconds < 10 || m.IntervalSeconds > 86400 {
		return errors.New("interval must be between 10 and 86400 seconds")
	}
	if m.TimeoutSeconds < 1 || m.TimeoutSeconds > 120 {
		return errors.New("timeout must be between 1 and 120 seconds")
	}
	if m.Retries < 0 || m.Retries > 10 {
		return errors.New("failures before alerting must be between 0 and 10")
	}
	if m.Type == checks.TypeDNS {
		switch strings.ToUpper(m.Config.RecordType) {
		case "", "A", "AAAA", "CNAME", "MX", "TXT", "NS":
		default:
			return errors.New("invalid DNS record type")
		}
	}
	return nil
}

func (s *Store) SaveMonitor(ctx context.Context, m *Monitor) error {
	if err := ValidateMonitor(m); err != nil {
		return err
	}
	cfg, _ := json.Marshal(m.Config)
	if m.ID == 0 {
		return s.Pool.QueryRow(ctx, `INSERT INTO monitors (host_id, name, type, target, port, interval_seconds,
			timeout_seconds, retries, enabled, config) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
			m.HostID, m.Name, m.Type, m.Target, m.Port, m.IntervalSeconds, m.TimeoutSeconds, m.Retries, m.Enabled, cfg).Scan(&m.ID)
	}
	// Changing what is being checked resets the state so stale status is not shown.
	_, err := s.Pool.Exec(ctx, `UPDATE monitors SET
		status = CASE WHEN type <> $3 OR target <> $4 OR port <> $5 OR host_id <> $1 THEN 0 ELSE status END,
		consecutive_failures = CASE WHEN type <> $3 OR target <> $4 OR port <> $5 OR host_id <> $1 THEN 0 ELSE consecutive_failures END,
		host_id=$1, name=$2, type=$3, target=$4, port=$5, interval_seconds=$6, timeout_seconds=$7, retries=$8,
		enabled=$9, config=$10, last_checked_at = NULL WHERE id=$11`,
		m.HostID, m.Name, m.Type, m.Target, m.Port, m.IntervalSeconds, m.TimeoutSeconds, m.Retries, m.Enabled, cfg, m.ID)
	return err
}

func (s *Store) DeleteMonitor(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM monitors WHERE id=$1`, id)
	return err
}

func (s *Store) SetMonitorEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := s.Pool.Exec(ctx, `UPDATE monitors SET enabled=$2, last_checked_at = CASE WHEN $2 THEN NULL ELSE last_checked_at END WHERE id=$1`, id, enabled)
	return err
}

// RequestCheck makes the scheduler run the monitor on its next tick.
func (s *Store) RequestCheck(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE monitors SET last_checked_at = NULL WHERE id=$1`, id)
	return err
}

// Transition describes a status change caused by a result.
type Transition struct {
	MonitorID int64
	Name      string
	From, To  checks.Status
	Message   string
}

// NextState applies the retry policy and returns the new status and failure count.
func NextState(prev checks.Status, failures, retries int, r checks.Status) (checks.Status, int) {
	if r != checks.Down {
		return r, 0
	}
	failures++
	if failures > retries {
		return checks.Down, failures
	}
	return prev, failures
}

// RecordResult stores a check result, updates the hourly roll-up and the
// monitor state, and records an event when the status changes.
func (s *Store) RecordResult(ctx context.Context, m *Monitor, res checks.Result, at time.Time) (*Transition, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var prev checks.Status
	var failures int
	if err := tx.QueryRow(ctx, `SELECT status, consecutive_failures FROM monitors WHERE id=$1 FOR UPDATE`, m.ID).
		Scan(&prev, &failures); err != nil {
		return nil, notFound(err)
	}

	var details []byte
	if len(res.Details) > 0 {
		details, _ = json.Marshal(res.Details)
	}
	var latency *float64
	if res.Status != checks.Down || res.LatencyMs > 0 {
		l := res.LatencyMs
		latency = &l
	}
	if _, err := tx.Exec(ctx, `INSERT INTO check_results (monitor_id, checked_at, status, latency_ms, message, details)
		VALUES ($1,$2,$3,$4,$5,$6)`, m.ID, at, res.Status, latency, res.Message, details); err != nil {
		return nil, err
	}

	var up, deg, dn int
	switch res.Status {
	case checks.Up:
		up = 1
	case checks.Degraded:
		deg = 1
	default:
		dn = 1
	}
	latCount := 0
	latVal := 0.0
	if res.Status != checks.Down && latency != nil {
		latCount, latVal = 1, *latency
	}
	var latMinMax *float64
	if latCount == 1 {
		latMinMax = &latVal
	}
	if _, err := tx.Exec(ctx, `INSERT INTO check_results_hourly AS h
		(monitor_id, bucket, total, up, degraded, down, latency_sum, latency_count, latency_min, latency_max)
		VALUES ($1, date_trunc('hour', $2::timestamptz), 1, $3, $4, $5, $6, $7, $8, $8)
		ON CONFLICT (monitor_id, bucket) DO UPDATE SET
			total = h.total + 1, up = h.up + $3, degraded = h.degraded + $4, down = h.down + $5,
			latency_sum = h.latency_sum + $6, latency_count = h.latency_count + $7,
			latency_min = LEAST(h.latency_min, $8), latency_max = GREATEST(h.latency_max, $8)`,
		m.ID, at, up, deg, dn, latVal, latCount, latMinMax); err != nil {
		return nil, err
	}

	next, failures := NextState(prev, failures, m.Retries, res.Status)
	if _, err := tx.Exec(ctx, `UPDATE monitors SET status=$2, consecutive_failures=$3, last_checked_at=$4,
		last_latency_ms=$5, last_message=$6,
		status_changed_at = CASE WHEN status <> $2 THEN $4 ELSE status_changed_at END
		WHERE id=$1`, m.ID, next, failures, at, latency, res.Message); err != nil {
		return nil, err
	}

	var tr *Transition
	if next != prev && !(prev == checks.Unknown && next == checks.Up) {
		tr = &Transition{MonitorID: m.ID, Name: m.Name, From: prev, To: next, Message: res.Message}
		if _, err := tx.Exec(ctx, `INSERT INTO events (monitor_id, created_at, from_status, to_status, message)
			VALUES ($1,$2,$3,$4,$5)`, m.ID, at, prev, next, res.Message); err != nil {
			return nil, err
		}
	}
	return tr, tx.Commit(ctx)
}
