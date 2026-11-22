// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/store"
)

// monitorSection is one block of the monitor list: a group, a host, or the
// monitors that are not in a group.
type monitorSection struct {
	Kind        string // "group", "host" or "none"
	ID          int64
	Title       string
	Sub         string
	Color       string
	Monitors    []store.Monitor
	Up, Down    int
	Degraded    int
	Paused      int
	Uptime      float64 // average 24h uptime of the enabled monitors, -1 without data
	Description string
}

// Status is the worst status in the section, as a CSS class.
func (s monitorSection) Status() string {
	switch {
	case s.Down > 0:
		return "down"
	case s.Degraded > 0:
		return "degraded"
	case s.Up > 0:
		return "up"
	}
	return "unknown"
}

// Active is the number of enabled monitors in the section.
func (s monitorSection) Active() int { return len(s.Monitors) - s.Paused }

func (sec *monitorSection) add(m store.Monitor) {
	sec.Monitors = append(sec.Monitors, m)
	if !m.Enabled {
		sec.Paused++
		return
	}
	switch m.Status {
	case checks.Up:
		sec.Up++
	case checks.Degraded:
		sec.Degraded++
	case checks.Down:
		sec.Down++
	}
}

func (sec *monitorSection) setUptime(up24 map[int64]float64) {
	var ids []int64
	for _, m := range sec.Monitors {
		if m.Enabled {
			ids = append(ids, m.ID)
		}
	}
	sec.Uptime = avg(up24, ids)
}

// sectionsByGroup splits monitors by group, in the order of groups, with the
// ungrouped monitors last. Empty groups are kept when keepEmpty is set.
func sectionsByGroup(groups []store.Group, list []store.Monitor, keepEmpty bool) []monitorSection {
	secs := make([]monitorSection, 0, len(groups)+1)
	idx := map[int64]int{}
	for _, g := range groups {
		idx[g.ID] = len(secs)
		secs = append(secs, monitorSection{Kind: "group", ID: g.ID, Title: g.Name, Color: g.Color, Description: g.Description})
	}
	none := monitorSection{Kind: "none", Title: "Not in a group", Color: "none"}
	for _, m := range list {
		if i, ok := idx[m.GroupID]; ok && m.GroupID != 0 {
			secs[i].add(m)
		} else {
			none.add(m)
		}
	}
	out := secs[:0]
	for _, s := range secs {
		if keepEmpty || len(s.Monitors) > 0 {
			out = append(out, s)
		}
	}
	if len(none.Monitors) > 0 {
		out = append(out, none)
	}
	return out
}

// sectionsByHost splits monitors by host (the list is ordered by host).
func sectionsByHost(list []store.Monitor) []monitorSection {
	var secs []monitorSection
	idx := map[int64]int{}
	for _, m := range list {
		i, ok := idx[m.HostID]
		if !ok {
			i = len(secs)
			idx[m.HostID] = i
			secs = append(secs, monitorSection{Kind: "host", ID: m.HostID, Title: m.HostName, Sub: m.HostAddress})
		}
		secs[i].add(m)
	}
	return secs
}

// ---------------------------------------------------------------- monitors list

func (s *Server) handleMonitors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.Store.ListMonitors(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	groups, err := s.Store.ListGroups(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	q := r.URL.Query()
	by := q.Get("by")
	if by != "host" && by != "group" {
		by = "group"
		if len(groups) == 0 {
			by = "host"
		}
	}
	// Optional filter on one group ("none" = monitors without a group).
	filter := q.Get("group")
	var current *store.Group
	if filter != "" {
		var keep []store.Monitor
		gid, _ := strconv.ParseInt(filter, 10, 64)
		for i := range groups {
			if groups[i].ID == gid {
				current = &groups[i]
			}
		}
		if filter != "none" && current == nil {
			filter = ""
		} else {
			for _, m := range list {
				if (filter == "none" && m.GroupID == 0) || (current != nil && m.GroupID == current.ID) {
					keep = append(keep, m)
				}
			}
			list = keep
		}
	}
	var secs []monitorSection
	if by == "host" {
		secs = sectionsByHost(list)
	} else {
		visible := groups
		if current != nil {
			visible = []store.Group{*current}
		} else if filter == "none" {
			visible = nil
		}
		secs = sectionsByGroup(visible, list, filter == "")
	}
	up24, err := s.Store.UptimeSince(ctx, monitorIDs(list), time.Now().Add(-24*time.Hour))
	if err != nil {
		s.serverError(w, err)
		return
	}
	for i := range secs {
		secs[i].setUptime(up24)
	}
	ungrouped := 0
	for _, m := range list {
		if m.GroupID == 0 {
			ungrouped++
		}
	}
	s.render(w, r, "monitors", "Monitors", "monitors", map[string]any{
		"Sections": secs, "Total": len(list), "By": by, "Groups": groups, "Filter": filter, "Current": current,
		"Ungrouped": ungrouped, "Back": r.URL.RequestURI(),
	})
}

// handleMonitorsMove moves the ticked monitors to a group, creating it first
// when a new name is given.
func (s *Server) handleMonitorsMove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	back := safeNext(r.PostFormValue("back"))
	if !strings.HasPrefix(back, "/admin/monitors") {
		back = "/admin/monitors"
	}
	if err := r.ParseForm(); err != nil {
		s.flash(w, r, back, false, "Could not read the form.")
		return
	}
	var ids []int64
	for _, v := range r.PostForm["monitor"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		s.flash(w, r, back, false, "Tick at least one monitor first.")
		return
	}
	target := r.PostFormValue("group_id")
	var gid int64
	name := "no group"
	switch target {
	case "", "none":
	case "new":
		g := &store.Group{Name: r.PostFormValue("new_group"), Color: r.PostFormValue("new_color")}
		if err := s.Store.SaveGroup(ctx, g); err != nil {
			s.flash(w, r, back, false, "Could not create the group: "+err.Error()+".")
			return
		}
		gid, name = g.ID, g.Name
	default:
		gid, _ = strconv.ParseInt(target, 10, 64)
		g, err := s.Store.GetGroup(ctx, gid)
		if err != nil {
			s.flash(w, r, back, false, "That group no longer exists.")
			return
		}
		name = g.Name
	}
	n, err := s.Store.SetMonitorsGroup(ctx, ids, gid)
	if err != nil {
		s.serverError(w, err)
		return
	}
	plural := "monitors"
	if n == 1 {
		plural = "monitor"
	}
	if gid == 0 {
		s.flash(w, r, back, true, fmt.Sprintf("%d %s taken out of their group.", n, plural))
		return
	}
	s.flash(w, r, back, true, fmt.Sprintf("%d %s moved to %s.", n, plural, name))
}

// ---------------------------------------------------------------- groups

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groups, err := s.Store.ListGroups(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	list, err := s.Store.ListMonitors(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	up24, err := s.Store.UptimeSince(ctx, monitorIDs(list), time.Now().Add(-24*time.Hour))
	if err != nil {
		s.serverError(w, err)
		return
	}
	secs := sectionsByGroup(groups, list, true)
	for i := range secs {
		secs[i].setUptime(up24)
	}
	var ungrouped *monitorSection
	if n := len(secs); n > 0 && secs[n-1].Kind == "none" {
		ungrouped = &secs[n-1]
		secs = secs[:n-1]
	}
	s.render(w, r, "groups", "Groups", "groups", map[string]any{"Sections": secs, "Ungrouped": ungrouped})
}

func (s *Server) groupFormData(r *http.Request, g *store.Group) (map[string]any, error) {
	list, err := s.Store.ListMonitors(r.Context())
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { // the group's own monitors first
		return list[i].GroupID == g.ID && g.ID != 0 && list[j].GroupID != g.ID
	})
	return map[string]any{"G": g, "Colors": store.GroupColors, "Monitors": list}, nil
}

func (s *Server) handleGroupForm(w http.ResponseWriter, r *http.Request) {
	g := &store.Group{Color: store.GroupColors[0]}
	if id := pathID(r); id != 0 {
		var err error
		if g, err = s.Store.GetGroup(r.Context(), id); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	data, err := s.groupFormData(r, g)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "group_form", "Group", "groups", data)
}

func (s *Server) handleGroupSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	g := &store.Group{ID: pathID(r), Name: r.PostFormValue("name"), Description: r.PostFormValue("description"),
		Color: r.PostFormValue("color")}
	if g.ID != 0 {
		if _, err := s.Store.GetGroup(ctx, g.ID); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	if err := s.Store.SaveGroup(ctx, g); err != nil {
		data, derr := s.groupFormData(r, g)
		if derr != nil {
			s.serverError(w, derr)
			return
		}
		data["Picked"] = r.PostForm["monitor"]
		s.renderStatus(w, r, http.StatusBadRequest, "group_form", &page{Title: "Group", Active: "groups", Flash: err.Error(), Data: data})
		return
	}
	// The ticked monitors are the members: add the new ones, release the rest.
	picked := map[int64]bool{}
	var add []int64
	for _, v := range r.PostForm["monitor"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			picked[id] = true
			add = append(add, id)
		}
	}
	list, err := s.Store.ListMonitors(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	var remove []int64
	for _, m := range list {
		if m.GroupID == g.ID && !picked[m.ID] {
			remove = append(remove, m.ID)
		}
	}
	if _, err := s.Store.SetMonitorsGroup(ctx, add, g.ID); err != nil {
		s.serverError(w, err)
		return
	}
	if _, err := s.Store.SetMonitorsGroup(ctx, remove, 0); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/monitors?group="+strconv.FormatInt(g.ID, 10), true, "Group "+g.Name+" saved.")
}

func (s *Server) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	g, err := s.Store.GetGroup(r.Context(), pathID(r))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	if err := s.Store.DeleteGroup(r.Context(), g.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/groups", true, "Group "+g.Name+" deleted. Its monitors are kept, without a group.")
}
