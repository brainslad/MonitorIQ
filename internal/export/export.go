// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

// Package export writes monitoring data for a date range to an Excel
// workbook: a summary per monitor, daily and hourly availability and
// response times, incidents and, optionally, every single check.
package export

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/store"
	"github.com/brainslad/monitoriq/internal/xlsx"
)

// Source is the data the export reads (implemented by *store.Store).
type Source interface {
	EachHour(ctx context.Context, ids []int64, from, to time.Time, fn func(store.HourRow) error) error
	EachResult(ctx context.Context, ids []int64, from, to time.Time, fn func(store.RawRow) error) error
	StatusChanges(ctx context.Context, ids []int64, from, to time.Time) ([]store.Event, error)
}

// Options choose what goes into the workbook.
type Options struct {
	// From and To bound the exported period: [From, To).
	From, To time.Time
	// Loc is the time zone of the times in the workbook and of the day
	// boundaries of the daily sheet (nil: time.Local).
	Loc *time.Location
	// Monitors to export, in the order they should appear.
	Monitors []store.Monitor
	Daily    bool
	Hourly   bool
	Checks   bool // every raw check result
	// Incidents adds the list of incidents (transitions to degraded/down).
	Incidents bool
	// Scope describes the selection for the cover lines ("All monitors", a group…).
	Scope string
	// By is the user who asked for the export.
	By string
	// Version of MonitorIQ, for the cover lines.
	Version string
	// MaxCheckRows caps the raw check rows (0: no cap beyond Excel's own
	// per-sheet limit, which makes the export continue on a new sheet).
	MaxCheckRows int
}

// Day is one calendar day; Range turns a day range into [from, to).
type Day struct{ Y, M, D int }

// ParseDay reads a date written as YYYY-MM-DD.
func ParseDay(s string) (Day, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return Day{}, fmt.Errorf("%q is not a date (YYYY-MM-DD)", s)
	}
	return Day{t.Year(), int(t.Month()), t.Day()}, nil
}

func (d Day) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Y, d.M, d.D) }

// Start is midnight at the start of the day in loc.
func (d Day) Start(loc *time.Location) time.Time {
	return time.Date(d.Y, time.Month(d.M), d.D, 0, 0, 0, 0, loc)
}

// MaxDays is the longest period one export may cover.
const MaxDays = 3660

// Range returns [start of first, start of the day after last) in loc and
// checks that the period is sensible.
func Range(first, last Day, loc *time.Location) (time.Time, time.Time, error) {
	from := first.Start(loc)
	to := last.Start(loc).AddDate(0, 0, 1)
	if !to.After(from) {
		return from, to, errors.New("the end date must be on or after the start date")
	}
	if to.Sub(from) > MaxDays*24*time.Hour {
		return from, to, fmt.Errorf("the period may cover at most %d days", MaxDays)
	}
	return from, to, nil
}

// Filename suggests a file name for the export.
func Filename(first, last Day) string {
	if first == last {
		return "monitoriq-" + first.String() + ".xlsx"
	}
	return "monitoriq-" + first.String() + "_" + last.String() + ".xlsx"
}

// agg accumulates check counts and latencies.
type agg struct {
	total, up, degraded, down int
	latSum                    float64
	latCount                  int
	latMin, latMax            *float64
}

func (a *agg) add(h store.HourRow) {
	a.total += h.Total
	a.up += h.Up
	a.degraded += h.Degraded
	a.down += h.Down
	a.latSum += h.LatencySum
	a.latCount += h.LatencyCount
	if h.LatencyMin != nil && (a.latMin == nil || *h.LatencyMin < *a.latMin) {
		v := *h.LatencyMin
		a.latMin = &v
	}
	if h.LatencyMax != nil && (a.latMax == nil || *h.LatencyMax > *a.latMax) {
		v := *h.LatencyMax
		a.latMax = &v
	}
}

// uptime is (up + degraded) / total in percent, nil without checks. It is the
// same definition the dashboard uses.
func (a *agg) uptime() *float64 {
	if a.total == 0 {
		return nil
	}
	v := round(float64(a.up+a.degraded)*100/float64(a.total), 4)
	return &v
}

func (a *agg) avg() *float64 {
	if a.latCount == 0 {
		return nil
	}
	v := round(a.latSum/float64(a.latCount), 2)
	return &v
}

func (a *agg) min() *float64 { return roundp(a.latMin, 2) }
func (a *agg) max() *float64 { return roundp(a.latMax, 2) }

// round keeps n decimals, so the workbook holds the figures people read.
func round(v float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(v*p) / p
}

func roundp(v *float64, n int) *float64 {
	if v == nil {
		return nil
	}
	r := round(*v, n)
	return &r
}

// incident is a stretch of time a monitor spent degraded or down.
type incident struct {
	monitorID int64
	start     time.Time
	end       *time.Time // nil while ongoing
	worst     checks.Status
	message   string
}

// timeline holds what the status changes say about one monitor.
type timeline struct {
	down, degraded time.Duration
	incidents      []incident
}

// buildTimelines turns status changes (ordered by monitor and time, with the
// change in force at `from` first) into time spent down/degraded within
// [from, min(to, now)) and a list of incidents that overlap the period. An
// incident starts when a monitor leaves "up" for degraded/down and ends when
// it is up again.
func buildTimelines(evs []store.Event, from, to, now time.Time) map[int64]*timeline {
	end := to
	if now.Before(end) {
		end = now
	}
	out := map[int64]*timeline{}
	byMon := map[int64][]store.Event{}
	var order []int64
	for _, e := range evs {
		if _, ok := byMon[e.MonitorID]; !ok {
			order = append(order, e.MonitorID)
		}
		byMon[e.MonitorID] = append(byMon[e.MonitorID], e)
	}
	clip := func(a, b time.Time) time.Duration {
		if a.Before(from) {
			a = from
		}
		if b.After(end) {
			b = end
		}
		if b.After(a) {
			return b.Sub(a)
		}
		return 0
	}
	for _, id := range order {
		list := byMon[id]
		tl := &timeline{}
		out[id] = tl
		var cur *incident
		for i, e := range list {
			next := end
			if i+1 < len(list) {
				next = list[i+1].CreatedAt
			}
			switch e.To {
			case checks.Down:
				tl.down += clip(e.CreatedAt, next)
			case checks.Degraded:
				tl.degraded += clip(e.CreatedAt, next)
			}
			bad := e.To == checks.Down || e.To == checks.Degraded
			switch {
			case bad && cur == nil:
				cur = &incident{monitorID: id, start: e.CreatedAt, worst: e.To, message: e.Message}
			case bad:
				if e.To > cur.worst {
					cur.worst, cur.message = e.To, e.Message
				}
			case cur != nil:
				t := e.CreatedAt
				cur.end = &t
				tl.incidents = append(tl.incidents, *cur)
				cur = nil
			}
		}
		if cur != nil {
			tl.incidents = append(tl.incidents, *cur)
		}
		// Keep incidents that overlap the period (the one in force at
		// `from` may have ended before it).
		kept := tl.incidents[:0]
		for _, in := range tl.incidents {
			if in.end != nil && !in.end.After(from) {
				continue
			}
			if !in.start.Before(to) {
				continue
			}
			kept = append(kept, in)
		}
		tl.incidents = kept
	}
	return out
}

func minutes(d time.Duration) float64 { return round(d.Minutes(), 2) }

func groupName(m store.Monitor) string { return m.GroupName }

func target(m store.Monitor) string {
	t := m.EffectiveTarget()
	if m.Port != 0 {
		t += ":" + strconv.Itoa(m.Port)
	}
	return t
}

// Write builds the workbook and writes it to w.
func Write(ctx context.Context, src Source, w io.Writer, opt Options) error {
	loc := opt.Loc
	if loc == nil {
		loc = time.Local
	}
	if len(opt.Monitors) == 0 {
		return errors.New("no monitors to export")
	}
	ids := make([]int64, len(opt.Monitors))
	mons := map[int64]store.Monitor{}
	for i, m := range opt.Monitors {
		ids[i] = m.ID
		mons[m.ID] = m
	}
	now := time.Now()

	// Totals per monitor and per day come from the hourly roll-ups, which are
	// kept much longer than raw results. One pass fills both.
	total := map[int64]*agg{}
	type dayKey struct {
		id  int64
		day time.Time
	}
	days := map[dayKey]*agg{}
	if err := src.EachHour(ctx, ids, opt.From, opt.To, func(h store.HourRow) error {
		a := total[h.MonitorID]
		if a == nil {
			a = &agg{}
			total[h.MonitorID] = a
		}
		a.add(h)
		if opt.Daily {
			b := h.Bucket.In(loc)
			k := dayKey{h.MonitorID, time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, loc)}
			d := days[k]
			if d == nil {
				d = &agg{}
				days[k] = d
			}
			d.add(h)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("reading hourly data: %w", err)
	}

	evs, err := src.StatusChanges(ctx, ids, opt.From, opt.To)
	if err != nil {
		return fmt.Errorf("reading status changes: %w", err)
	}
	tls := buildTimelines(evs, opt.From, opt.To, now)

	wb := xlsx.New(w, loc)
	wb.Creator = "MonitorIQ by Brainslad"
	last := opt.To.Add(-time.Nanosecond).In(loc)
	tzName, offset := opt.From.In(loc).Zone()
	tz := fmt.Sprintf("%s (UTC%s)", loc.String(), fmtOffset(offset))
	if loc.String() == "Local" || loc.String() == tzName {
		tz = fmt.Sprintf("%s (UTC%s)", tzName, fmtOffset(offset))
	}
	period := fmt.Sprintf("%s to %s", opt.From.In(loc).Format("2006-01-02"), last.Format("2006-01-02"))
	cover := [][]any{
		{xlsx.Title("MonitorIQ monitoring report")},
		{xlsx.Note(fmt.Sprintf("Period: %s · times in %s · %s", period, tz, scopeText(opt)))},
		{xlsx.Note(fmt.Sprintf("Generated %s%s with MonitorIQ%s by Brainslad · uptime = (up + degraded) / checks",
			now.In(loc).Format("2006-01-02 15:04"), byText(opt.By), versionText(opt.Version)))},
		{},
	}

	monCols := []xlsx.Column{
		{Header: "Group", Width: 16}, {Header: "Host", Width: 20}, {Header: "Monitor", Width: 24},
	}

	// ---------------------------------------------------------------- Summary
	sum, err := wb.AddSheet("Summary", append(append([]xlsx.Column{}, monCols...),
		xlsx.Column{Header: "Type", Width: 8},
		xlsx.Column{Header: "Target", Width: 30},
		xlsx.Column{Header: "Enabled", Width: 9},
		xlsx.Column{Header: "Checks", Style: xlsx.Integer},
		xlsx.Column{Header: "Up", Style: xlsx.Integer},
		xlsx.Column{Header: "Degraded", Style: xlsx.Integer},
		xlsx.Column{Header: "Down", Style: xlsx.Integer},
		xlsx.Column{Header: "Uptime %", Style: xlsx.Percent3},
		xlsx.Column{Header: "Avg response (ms)", Style: xlsx.Decimal},
		xlsx.Column{Header: "Min response (ms)", Style: xlsx.Decimal},
		xlsx.Column{Header: "Max response (ms)", Style: xlsx.Decimal},
		xlsx.Column{Header: "Incidents", Style: xlsx.Integer},
		xlsx.Column{Header: "Downtime (min)", Style: xlsx.Decimal},
		xlsx.Column{Header: "Degraded time (min)", Style: xlsx.Decimal},
	), cover...)
	if err != nil {
		return err
	}
	for _, m := range opt.Monitors {
		a := total[m.ID]
		if a == nil {
			a = &agg{}
		}
		tl := tls[m.ID]
		if tl == nil {
			tl = &timeline{}
		}
		if err := sum.Row(groupName(m), m.HostName, m.Name, m.Type, target(m), yesNo(m.Enabled),
			a.total, a.up, a.degraded, a.down, a.uptime(), a.avg(), a.min(), a.max(),
			len(tl.incidents), minutes(tl.down), minutes(tl.degraded)); err != nil {
			return err
		}
	}

	// ---------------------------------------------------------------- Daily
	if opt.Daily {
		sh, err := wb.AddSheet("Daily", append(append([]xlsx.Column{}, monCols...),
			xlsx.Column{Header: "Date", Style: xlsx.Date},
			xlsx.Column{Header: "Checks", Style: xlsx.Integer},
			xlsx.Column{Header: "Up", Style: xlsx.Integer},
			xlsx.Column{Header: "Degraded", Style: xlsx.Integer},
			xlsx.Column{Header: "Down", Style: xlsx.Integer},
			xlsx.Column{Header: "Uptime %", Style: xlsx.Percent3},
			xlsx.Column{Header: "Avg response (ms)", Style: xlsx.Decimal},
			xlsx.Column{Header: "Min response (ms)", Style: xlsx.Decimal},
			xlsx.Column{Header: "Max response (ms)", Style: xlsx.Decimal},
		))
		if err != nil {
			return err
		}
		for _, m := range opt.Monitors {
			for d := opt.From.In(loc); d.Before(opt.To); d = d.AddDate(0, 0, 1) {
				day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
				if day.After(now) {
					break
				}
				a := days[dayKey{m.ID, day}]
				if a == nil {
					a = &agg{}
				}
				if err := sh.Row(groupName(m), m.HostName, m.Name, day, a.total, a.up, a.degraded, a.down,
					a.uptime(), a.avg(), a.min(), a.max()); err != nil {
					return err
				}
			}
		}
	}

	// ---------------------------------------------------------------- Hourly
	if opt.Hourly {
		cols := append(append([]xlsx.Column{}, monCols...),
			xlsx.Column{Header: "Hour", Style: xlsx.DateTime},
			xlsx.Column{Header: "Checks", Style: xlsx.Integer},
			xlsx.Column{Header: "Up", Style: xlsx.Integer},
			xlsx.Column{Header: "Degraded", Style: xlsx.Integer},
			xlsx.Column{Header: "Down", Style: xlsx.Integer},
			xlsx.Column{Header: "Uptime %", Style: xlsx.Percent3},
			xlsx.Column{Header: "Avg response (ms)", Style: xlsx.Decimal},
			xlsx.Column{Header: "Min response (ms)", Style: xlsx.Decimal},
			xlsx.Column{Header: "Max response (ms)", Style: xlsx.Decimal},
		)
		sh, err := wb.AddSheet("Hourly", cols)
		if err != nil {
			return err
		}
		if err := src.EachHour(ctx, ids, opt.From, opt.To, func(h store.HourRow) error {
			if sh.Full() {
				if sh, err = wb.AddSheet("Hourly", cols); err != nil {
					return err
				}
			}
			m := mons[h.MonitorID]
			a := &agg{}
			a.add(h)
			return sh.Row(groupName(m), m.HostName, m.Name, h.Bucket, a.total, a.up, a.degraded, a.down,
				a.uptime(), a.avg(), a.min(), a.max())
		}); err != nil {
			return fmt.Errorf("reading hourly data: %w", err)
		}
	}

	// ---------------------------------------------------------------- Incidents
	if opt.Incidents {
		sh, err := wb.AddSheet("Incidents", append(append([]xlsx.Column{}, monCols...),
			xlsx.Column{Header: "Worst status", Width: 13},
			xlsx.Column{Header: "Started", Style: xlsx.DateTime},
			xlsx.Column{Header: "Ended", Style: xlsx.DateTime},
			xlsx.Column{Header: "Duration (min)", Style: xlsx.Decimal},
			xlsx.Column{Header: "Ongoing", Width: 9},
			xlsx.Column{Header: "Message", Width: 60},
		))
		if err != nil {
			return err
		}
		var all []incident
		for _, m := range opt.Monitors {
			if tl := tls[m.ID]; tl != nil {
				all = append(all, tl.incidents...)
			}
		}
		sort.SliceStable(all, func(i, j int) bool { return all[i].start.Before(all[j].start) })
		for _, in := range all {
			m := mons[in.monitorID]
			end := now
			if in.end != nil {
				end = *in.end
			}
			if err := sh.Row(groupName(m), m.HostName, m.Name, in.worst.Label(), in.start, in.end,
				minutes(end.Sub(in.start)), yesNo(in.end == nil), in.message); err != nil {
				return err
			}
		}
	}

	// ---------------------------------------------------------------- Checks
	if opt.Checks {
		cols := append(append([]xlsx.Column{}, monCols...),
			xlsx.Column{Header: "Time", Style: xlsx.DateTime},
			xlsx.Column{Header: "Status", Width: 10},
			xlsx.Column{Header: "Response (ms)", Style: xlsx.Decimal},
			xlsx.Column{Header: "Message", Width: 60},
		)
		sh, err := wb.AddSheet("Checks", cols)
		if err != nil {
			return err
		}
		n := 0
		errCap := errors.New("cap")
		err = src.EachResult(ctx, ids, opt.From, opt.To, func(r store.RawRow) error {
			if opt.MaxCheckRows > 0 && n >= opt.MaxCheckRows {
				return errCap
			}
			if sh.Full() {
				if sh, err = wb.AddSheet("Checks", cols); err != nil {
					return err
				}
			}
			n++
			m := mons[r.MonitorID]
			return sh.Row(groupName(m), m.HostName, m.Name, r.CheckedAt, r.Status.Label(), roundp(r.LatencyMs, 2), r.Message)
		})
		if errors.Is(err, errCap) {
			if sh.Full() {
				if sh, err = wb.AddSheet("Checks", cols); err != nil {
					return err
				}
			}
			err = sh.Row(xlsx.Note(fmt.Sprintf("Stopped after %d checks. Choose a shorter period or fewer monitors for the rest.", n)))
		}
		if err != nil {
			return fmt.Errorf("reading check results: %w", err)
		}
	}

	return wb.Close()
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func fmtOffset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}
	return fmt.Sprintf("%s%02d:%02d", sign, sec/3600, sec%3600/60)
}

func scopeText(opt Options) string {
	n := len(opt.Monitors)
	s := fmt.Sprintf("%d monitor", n)
	if n != 1 {
		s += "s"
	}
	if opt.Scope != "" {
		s = opt.Scope + " (" + s + ")"
	}
	return s
}

func byText(by string) string {
	if by == "" {
		return ""
	}
	return " by " + by
}

func versionText(v string) string {
	if v == "" || v == "dev" {
		return ""
	}
	return " " + v
}
