// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package export

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/store"
)

type fakeSource struct {
	hours  []store.HourRow
	raw    []store.RawRow
	events []store.Event
}

func (f *fakeSource) EachHour(_ context.Context, ids []int64, from, to time.Time, fn func(store.HourRow) error) error {
	for _, h := range f.hours {
		if !h.Bucket.Before(from) && h.Bucket.Before(to) {
			if err := fn(h); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeSource) EachResult(_ context.Context, ids []int64, from, to time.Time, fn func(store.RawRow) error) error {
	for _, r := range f.raw {
		if !r.CheckedAt.Before(from) && r.CheckedAt.Before(to) {
			if err := fn(r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeSource) StatusChanges(context.Context, []int64, time.Time, time.Time) ([]store.Event, error) {
	return f.events, nil
}

func TestRange(t *testing.T) {
	a, _ := ParseDay("2026-11-01")
	b, _ := ParseDay("2026-11-22")
	from, to, err := Range(a, b, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !from.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("range %v – %v", from, to)
	}
	if _, _, err := Range(b, a, time.UTC); err == nil {
		t.Error("reversed range accepted")
	}
	if _, err := ParseDay("22/11/2026"); err == nil {
		t.Error("bad date accepted")
	}
	if Filename(a, b) != "monitoriq-2026-11-01_2026-11-22.xlsx" || Filename(a, a) != "monitoriq-2026-11-01.xlsx" {
		t.Error("filename")
	}
}

func ts(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestTimelines(t *testing.T) {
	from, to := ts("2026-11-10 00:00"), ts("2026-11-11 00:00")
	now := ts("2026-11-30 00:00")
	evs := []store.Event{
		// monitor 1: down since before the period, up at 01:00, degraded 05:00, down 05:30, up 06:00
		{MonitorID: 1, CreatedAt: ts("2026-11-09 23:00"), From: checks.Up, To: checks.Down, Message: "timeout"},
		{MonitorID: 1, CreatedAt: ts("2026-11-10 01:00"), From: checks.Down, To: checks.Up},
		{MonitorID: 1, CreatedAt: ts("2026-11-10 05:00"), From: checks.Up, To: checks.Degraded, Message: "slow"},
		{MonitorID: 1, CreatedAt: ts("2026-11-10 05:30"), From: checks.Degraded, To: checks.Down, Message: "refused"},
		{MonitorID: 1, CreatedAt: ts("2026-11-10 06:00"), From: checks.Down, To: checks.Up},
		// monitor 2: an incident that ended before the period, then one still going
		{MonitorID: 2, CreatedAt: ts("2026-11-10 23:00"), From: checks.Up, To: checks.Down},
	}
	tl := buildTimelines(evs, from, to, now)
	m1 := tl[1]
	if m1.down != 90*time.Minute || m1.degraded != 30*time.Minute {
		t.Errorf("monitor 1: down %v degraded %v", m1.down, m1.degraded)
	}
	if len(m1.incidents) != 2 {
		t.Fatalf("monitor 1 incidents: %+v", m1.incidents)
	}
	if !m1.incidents[0].start.Equal(ts("2026-11-09 23:00")) || !m1.incidents[0].end.Equal(ts("2026-11-10 01:00")) {
		t.Errorf("first incident %+v", m1.incidents[0])
	}
	if in := m1.incidents[1]; in.worst != checks.Down || in.message != "refused" || !in.end.Equal(ts("2026-11-10 06:00")) {
		t.Errorf("second incident %+v", in)
	}
	m2 := tl[2]
	if m2.down != time.Hour || len(m2.incidents) != 1 || m2.incidents[0].end != nil {
		t.Errorf("monitor 2: %+v", m2)
	}

	// An incident that was over before the period is left out.
	old := buildTimelines([]store.Event{
		{MonitorID: 3, CreatedAt: ts("2026-11-09 23:00"), To: checks.Up},
	}, from, to, now)
	if len(old[3].incidents) != 0 || old[3].down != 0 {
		t.Errorf("monitor 3: %+v", old[3])
	}
}

func f64(v float64) *float64 { return &v }

func TestWrite(t *testing.T) {
	loc := time.UTC
	from, to := ts("2026-11-10 00:00"), ts("2026-11-12 00:00")
	mons := []store.Monitor{
		{ID: 1, Name: "Web shop", HostName: "shop", HostAddress: "shop.example.com", Type: "http", Enabled: true, GroupName: "Customers"},
		{ID: 2, Name: "DNS", HostName: "ns1", HostAddress: "10.0.0.53", Type: "dns", Port: 53, Enabled: false},
	}
	src := &fakeSource{
		hours: []store.HourRow{
			{MonitorID: 1, Bucket: ts("2026-11-10 10:00"), Total: 60, Up: 58, Degraded: 1, Down: 1, LatencySum: 6000, LatencyCount: 59, LatencyMin: f64(50), LatencyMax: f64(400)},
			{MonitorID: 1, Bucket: ts("2026-11-11 10:00"), Total: 60, Up: 60, LatencySum: 3000, LatencyCount: 60, LatencyMin: f64(40), LatencyMax: f64(90)},
		},
		raw: []store.RawRow{
			{MonitorID: 1, CheckedAt: ts("2026-11-10 10:00"), Status: checks.Up, LatencyMs: f64(51)},
			{MonitorID: 1, CheckedAt: ts("2026-11-10 10:01"), Status: checks.Down, Message: "connection refused <&>"},
		},
		events: []store.Event{
			{MonitorID: 1, CreatedAt: ts("2026-11-10 10:01"), From: checks.Up, To: checks.Down, Message: "connection refused"},
			{MonitorID: 1, CreatedAt: ts("2026-11-10 10:02"), From: checks.Down, To: checks.Up},
		},
	}
	var buf bytes.Buffer
	err := Write(context.Background(), src, &buf, Options{From: from, To: to, Loc: loc, Monitors: mons,
		Daily: true, Hourly: true, Checks: true, Incidents: true, Scope: "All monitors", By: "admin", Version: "v0.5.0"})
	if err != nil {
		t.Fatal(err)
	}
	if p := os.Getenv("MONITORIQ_EXPORT_SAMPLE"); p != "" {
		_ = os.WriteFile(p, buf.Bytes(), 0o644)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		for _, f := range zr.File {
			if f.Name == name {
				rc, _ := f.Open()
				b, _ := io.ReadAll(rc)
				rc.Close()
				return string(b)
			}
		}
		t.Fatalf("missing %s", name)
		return ""
	}
	wbx := read("xl/workbook.xml")
	for _, n := range []string{"Summary", "Daily", "Hourly", "Incidents", "Checks"} {
		if !strings.Contains(wbx, `name="`+n+`"`) {
			t.Errorf("no %s sheet", n)
		}
	}
	sum := read("xl/worksheets/sheet1.xml")
	for _, want := range []string{"Web shop", "Customers", "shop.example.com", "10.0.0.53:53", ">120<", "All monitors (2 monitors)", "by admin with MonitorIQ v0.5.0"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary lacks %q", want)
		}
	}
	// Uptime of monitor 1: 119/120.
	if !strings.Contains(sum, "99.1667") {
		t.Errorf("summary uptime missing: %s", sum)
	}
	daily := read("xl/worksheets/sheet2.xml")
	// 2 monitors × 2 days + header = 5 rows.
	if n := strings.Count(daily, "<row "); n != 5 {
		t.Errorf("daily has %d rows", n)
	}
	checksSheet := read("xl/worksheets/sheet5.xml")
	if !strings.Contains(checksSheet, "connection refused &lt;&amp;&gt;") {
		t.Error("checks sheet lacks the escaped message")
	}
}
