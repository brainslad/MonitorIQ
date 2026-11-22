// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/brainslad/monitoriq/internal/export"
	"github.com/brainslad/monitoriq/internal/store"
)

// maxExportChecks caps the raw check rows of one export (about three full
// Excel sheets).
const maxExportChecks = 3_000_000

// exportSlots limits how many exports run at the same time.
var exportSlots = make(chan struct{}, 2)

// exportForm is the state of the export page.
type exportForm struct {
	From, To  string
	GroupID   int64
	Monitors  []int64
	Daily     bool
	Hourly    bool
	Checks    bool
	Incidents bool
}

func defaultExportForm(now time.Time) exportForm {
	return exportForm{
		From: now.AddDate(0, 0, -29).Format("2006-01-02"), To: now.Format("2006-01-02"),
		Daily: true, Incidents: true,
	}
}

// exportFormFrom reads the form from a query string. Without any field set it
// returns the defaults (used when opening the page).
func exportFormFrom(q url.Values, now time.Time) exportForm {
	f := defaultExportForm(now)
	if v := strings.TrimSpace(q.Get("from")); v != "" {
		f.From = v
	}
	if v := strings.TrimSpace(q.Get("to")); v != "" {
		f.To = v
	}
	f.GroupID, _ = strconv.ParseInt(q.Get("group"), 10, 64)
	for _, v := range q["monitor"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			f.Monitors = append(f.Monitors, id)
		}
	}
	// The sheet boxes are only read from a submitted form ("go" is set),
	// so that links such as /export?monitor=3 keep the defaults.
	if q.Get("go") != "" {
		f.Daily = q.Get("daily") != ""
		f.Hourly = q.Get("hourly") != ""
		f.Checks = q.Get("checks") != ""
		f.Incidents = q.Get("incidents") != ""
	}
	return f
}

func (s *Server) renderExport(w http.ResponseWriter, r *http.Request, code int, f exportForm, flash string) {
	ctx := r.Context()
	mons, err := s.Store.ListMonitors(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	groups, err := s.Store.ListGroups(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	ret, _ := s.Store.GetRetention(ctx)
	oldest, _ := s.Store.OldestResult(ctx)
	now := time.Now()
	zone, off := now.Zone()
	p := &page{Title: "Export to Excel", Active: "export", Flash: flash, Data: map[string]any{
		"F": f, "Monitors": mons, "Groups": groups, "Retention": ret, "OldestResult": oldest,
		"Zone": fmt.Sprintf("%s, UTC%s", zone, fmtZoneOffset(off)), "Today": now.Format("2006-01-02"),
		"MaxChecks": maxExportChecks,
	}}
	s.renderStatus(w, r, code, "export", p)
}

func fmtZoneOffset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}
	return fmt.Sprintf("%s%02d:%02d", sign, sec/3600, sec%3600/60)
}

func (s *Server) handleExportForm(w http.ResponseWriter, r *http.Request) {
	s.renderExport(w, r, http.StatusOK, exportFormFrom(r.URL.Query(), time.Now()), "")
}

// exportMonitors resolves the selection: ticked monitors win over the group,
// and with neither every monitor is exported.
func (s *Server) exportMonitors(ctx context.Context, f exportForm) ([]store.Monitor, string, error) {
	all, err := s.Store.ListMonitors(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(f.Monitors) > 0 {
		want := map[int64]bool{}
		for _, id := range f.Monitors {
			want[id] = true
		}
		var out []store.Monitor
		for _, m := range all {
			if want[m.ID] {
				out = append(out, m)
			}
		}
		return out, "Selected monitors", nil
	}
	if f.GroupID > 0 {
		g, err := s.Store.GetGroup(ctx, f.GroupID)
		if err != nil {
			return nil, "", err
		}
		var out []store.Monitor
		for _, m := range all {
			if m.GroupID == g.ID {
				out = append(out, m)
			}
		}
		return out, "Group " + g.Name, nil
	}
	return all, "All monitors", nil
}

func (s *Server) handleExportDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	f := exportFormFrom(r.URL.Query(), now)
	fail := func(msg string) { s.renderExport(w, r, http.StatusBadRequest, f, msg) }

	first, err := export.ParseDay(f.From)
	if err != nil {
		fail("Start date: " + err.Error() + ".")
		return
	}
	last, err := export.ParseDay(f.To)
	if err != nil {
		fail("End date: " + err.Error() + ".")
		return
	}
	from, to, err := export.Range(first, last, time.Local)
	if err != nil {
		fail(capitalize(err.Error()) + ".")
		return
	}
	if from.After(now) {
		fail("The period starts in the future: there is no data yet.")
		return
	}
	mons, scope, err := s.exportMonitors(ctx, f)
	if errors.Is(err, store.ErrNotFound) {
		fail("That group no longer exists.")
		return
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	if len(mons) == 0 {
		fail("There are no monitors in that selection.")
		return
	}
	ids := make([]int64, len(mons))
	for i, m := range mons {
		ids[i] = m.ID
	}
	if f.Checks {
		n, err := s.Store.CountResults(ctx, ids, from, to)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if n > maxExportChecks {
			fail(fmt.Sprintf("That period has %s individual checks, more than the %s an export can hold. "+
				"Choose a shorter period or fewer monitors, or untick “Every check”.", thousands(n), thousands(maxExportChecks)))
			return
		}
	}

	select {
	case exportSlots <- struct{}{}:
		defer func() { <-exportSlots }()
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		fail("Other exports are running. Please try again in a moment.")
		return
	}

	// Build the file first, so that an error can still be shown as a page.
	tmp, err := os.CreateTemp("", "monitoriq-export-*.xlsx")
	if err != nil {
		s.serverError(w, err)
		return
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	by := ""
	if u := userFrom(r); u != nil {
		by = u.Label()
	}
	opt := export.Options{
		From: from, To: to, Loc: time.Local, Monitors: mons, Scope: scope, By: by, Version: Version,
		Daily: f.Daily, Hourly: f.Hourly, Checks: f.Checks, Incidents: f.Incidents,
		MaxCheckRows: maxExportChecks,
	}
	start := time.Now()
	if err := export.Write(ctx, s.Store, tmp, opt); err != nil {
		if ctx.Err() != nil {
			return
		}
		s.serverError(w, err)
		return
	}
	st, err := tmp.Stat()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		s.serverError(w, err)
		return
	}
	s.Log.Info("export", "user", by, "from", first.String(), "to", last.String(), "monitors", len(mons),
		"bytes", st.Size(), "took", time.Since(start).Round(time.Millisecond))
	name := export.Filename(first, last)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, now, tmp)
}

// thousands writes n with comma separators: 1,234,567.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
