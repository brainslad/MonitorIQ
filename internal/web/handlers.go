package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/store"
)

// ---------------------------------------------------------------- auth

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

// ---------------------------------------------------------------- dashboard

type hostGroup struct {
	HostID   int64
	HostName string
	Address  string
	Monitors []store.Monitor
}

func groupByHost(list []store.Monitor) []hostGroup {
	var groups []hostGroup
	idx := map[int64]int{}
	for _, m := range list {
		i, ok := idx[m.HostID]
		if !ok {
			i = len(groups)
			idx[m.HostID] = i
			groups = append(groups, hostGroup{HostID: m.HostID, HostName: m.HostName, Address: m.HostAddress})
		}
		groups[i].Monitors = append(groups[i].Monitors, m)
	}
	return groups
}

func monitorIDs(list []store.Monitor) []int64 {
	ids := make([]int64, len(list))
	for i, m := range list {
		ids[i] = m.ID
	}
	return ids
}

type dashKPI struct {
	Total, Up, Degraded, Down, Unknown, Paused int
	Uptime24, Uptime30                         float64 // -1 without data
	RespAvg                                    float64 // -1 without data
	SlowestName                                string
	SlowestMs                                  float64
	OpenIncidents, Incidents24h                int
	BarUp, BarDegraded, BarDown, BarPaused     float64 // stacked bar widths in %
}

func avg(m map[int64]float64, ids []int64) float64 {
	sum, n := 0.0, 0
	for _, id := range ids {
		if v, ok := m[id]; ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return -1
	}
	return sum / float64(n)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.Store.ListMonitors(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// Problems first, then by host and name.
	rank := func(m store.Monitor) int {
		if !m.Enabled {
			return 4
		}
		switch m.Status {
		case checks.Down:
			return 0
		case checks.Degraded:
			return 1
		case checks.Unknown:
			return 2
		}
		return 3
	}
	sort.SliceStable(list, func(i, j int) bool { return rank(list[i]) < rank(list[j]) })

	ids := monitorIDs(list)
	now := time.Now()
	up24, err := s.Store.UptimeSince(ctx, ids, now.Add(-24*time.Hour))
	if err != nil {
		s.serverError(w, err)
		return
	}
	up30, err := s.Store.UptimeSince(ctx, ids, now.AddDate(0, 0, -30))
	if err != nil {
		s.serverError(w, err)
		return
	}
	bars, err := s.Store.HourlyBars(ctx, ids, 24)
	if err != nil {
		s.serverError(w, err)
		return
	}
	incidents, err := s.Store.Incidents(ctx, now.AddDate(0, 0, -7), 20)
	if err != nil {
		s.serverError(w, err)
		return
	}

	k := dashKPI{Total: len(list), SlowestMs: -1}
	var active []int64
	types := map[string]bool{}
	latSum, latN := 0.0, 0
	for _, m := range list {
		types[m.Type] = true
		if !m.Enabled {
			k.Paused++
			continue
		}
		active = append(active, m.ID)
		switch m.Status {
		case checks.Up:
			k.Up++
		case checks.Degraded:
			k.Degraded++
			k.OpenIncidents++
		case checks.Down:
			k.Down++
			k.OpenIncidents++
		default:
			k.Unknown++
		}
		if m.Status != checks.Down && m.LastLatencyMs != nil {
			latSum += *m.LastLatencyMs
			latN++
			if *m.LastLatencyMs > k.SlowestMs {
				k.SlowestMs, k.SlowestName = *m.LastLatencyMs, m.Name
			}
		}
	}
	k.Uptime24, k.Uptime30, k.RespAvg = avg(up24, active), avg(up30, active), -1
	if latN > 0 {
		k.RespAvg = latSum / float64(latN)
	}
	for _, e := range incidents {
		if e.CreatedAt.After(now.Add(-24 * time.Hour)) {
			k.Incidents24h++
		}
	}
	if k.Total > 0 {
		t := float64(k.Total)
		k.BarUp, k.BarDegraded, k.BarDown = float64(k.Up)*100/t, float64(k.Degraded)*100/t, float64(k.Down)*100/t
		k.BarPaused = float64(k.Paused+k.Unknown) * 100 / t
	}
	var typeList []string
	for _, t := range checks.Types {
		if types[t.Key] {
			typeList = append(typeList, t.Key)
		}
	}
	p := &page{Title: "Dashboard", Active: "dashboard", Data: map[string]any{
		"Monitors": list, "Up24": up24, "Up30": up30, "Bars": bars, "K": k,
		"Incidents": incidents, "Types": typeList, "Now": now,
	}}
	s.renderStatus(w, r, http.StatusOK, "dashboard", p)
}

func (s *Server) handleDashboardSeries(w http.ResponseWriter, r *http.Request) {
	pts, err := s.Store.ResponseSeries(r.Context(), time.Now().Add(-24*time.Hour), 600)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if pts == nil {
		pts = []store.Point{}
	}
	writeJSON(w, map[string]any{"points": pts})
}

func (s *Server) handleMonitorDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, err := s.Store.GetMonitor(ctx, pathID(r))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	now := time.Now()
	ids := []int64{m.ID}
	uptimes := map[string]string{}
	for _, p := range []struct {
		key  string
		from time.Time
	}{{"24h", now.Add(-24 * time.Hour)}, {"7d", now.AddDate(0, 0, -7)}, {"30d", now.AddDate(0, 0, -30)}, {"365d", now.AddDate(-1, 0, 0)}} {
		u, err := s.Store.UptimeSince(ctx, ids, p.from)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if v, ok := u[m.ID]; ok {
			uptimes[p.key] = fmtPct(v)
		} else {
			uptimes[p.key] = "—"
		}
	}
	results, err := s.Store.RecentResults(ctx, m.ID, 50)
	if err != nil {
		s.serverError(w, err)
		return
	}
	events, err := s.Store.ListEvents(ctx, ids, now.AddDate(-2, 0, 0), 30)
	if err != nil {
		s.serverError(w, err)
		return
	}
	var latest *store.Result
	if len(results) > 0 {
		latest = &results[0]
	}
	p := &page{Title: m.Name, Active: "dashboard", Data: map[string]any{
		"M": m, "Uptimes": uptimes, "Results": results, "Events": events, "Latest": latest,
	}}
	s.renderStatus(w, r, http.StatusOK, "monitor", p)
}

func (s *Server) handleMonitorCheckNow(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if err := s.Store.RequestCheck(r.Context(), id); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/monitors/"+strconv.FormatInt(id, 10), true, "Check scheduled. Results will appear in a few seconds.")
}

var seriesRanges = map[string]time.Duration{
	"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "48h": 48 * time.Hour,
	"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour,
	"1y": 365 * 24 * time.Hour, "2y": 730 * 24 * time.Hour,
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	d, ok := seriesRanges[r.URL.Query().Get("range")]
	if !ok {
		d = 24 * time.Hour
	}
	pts, err := s.Store.Series(r.Context(), pathID(r), time.Now().Add(-d))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if pts == nil {
		pts = []store.Point{}
	}
	writeJSON(w, map[string]any{"points": pts})
}

// ---------------------------------------------------------------- hosts

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.Store.ListHosts(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "hosts", "Hosts", "hosts", map[string]any{"Hosts": hosts})
}

func (s *Server) handleHostForm(w http.ResponseWriter, r *http.Request) {
	h := &store.Host{}
	if id := pathID(r); id != 0 {
		var err error
		if h, err = s.Store.GetHost(r.Context(), id); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	s.render(w, r, "host_form", "Host", "hosts", map[string]any{"H": h})
}

func (s *Server) handleHostSave(w http.ResponseWriter, r *http.Request) {
	h := &store.Host{ID: pathID(r), Name: r.PostFormValue("name"), Address: r.PostFormValue("address"),
		Description: r.PostFormValue("description")}
	isNew := h.ID == 0
	if err := s.Store.SaveHost(r.Context(), h); err != nil {
		s.renderStatus(w, r, http.StatusBadRequest, "host_form", &page{Title: "Host", Active: "hosts", Flash: err.Error(),
			Data: map[string]any{"H": h}})
		return
	}
	if isNew {
		s.flash(w, r, "/admin/monitors/new?host="+strconv.FormatInt(h.ID, 10), true,
			"Host created. Now add the monitors for it.")
		return
	}
	s.flash(w, r, "/admin/hosts", true, "Host saved.")
}

func (s *Server) handleHostDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteHost(r.Context(), pathID(r)); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/hosts", true, "Host and its monitors deleted.")
}

// ---------------------------------------------------------------- monitors

func (s *Server) handleMonitors(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListMonitors(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "monitors", "Monitors", "monitors", map[string]any{"Groups": groupByHost(list), "Total": len(list)})
}

func defaultMonitor() *store.Monitor {
	return &store.Monitor{Type: checks.TypeHTTP, IntervalSeconds: 60, TimeoutSeconds: 10, Retries: 1, Enabled: true,
		Config: checks.Config{ExpectedStatus: "200-399", PingCount: 3, MaxHops: 30, RecordType: "A", WarnDays: 14, SMTPTLS: "starttls"}}
}

func (s *Server) monitorFormData(r *http.Request, m *store.Monitor) (map[string]any, error) {
	hosts, err := s.Store.ListHosts(r.Context())
	if err != nil {
		return nil, err
	}
	return map[string]any{"M": m, "Hosts": hosts}, nil
}

func (s *Server) handleMonitorForm(w http.ResponseWriter, r *http.Request) {
	m := defaultMonitor()
	if id := pathID(r); id != 0 {
		var err error
		if m, err = s.Store.GetMonitor(r.Context(), id); err != nil {
			http.NotFound(w, r)
			return
		}
	} else {
		m.HostID, _ = strconv.ParseInt(r.URL.Query().Get("host"), 10, 64)
		if t := r.URL.Query().Get("type"); checks.ValidType(t) {
			m.Type = t
		}
	}
	data, err := s.monitorFormData(r, m)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if len(data["Hosts"].([]store.Host)) == 0 {
		s.flash(w, r, "/admin/hosts/new", false, "Create a host first to attach monitors to it.")
		return
	}
	s.render(w, r, "monitor_form", "Monitor", "monitors", data)
}

func monitorFromForm(r *http.Request, id int64) *store.Monitor {
	hostID, _ := strconv.ParseInt(r.PostFormValue("host_id"), 10, 64)
	m := &store.Monitor{
		ID:              id,
		HostID:          hostID,
		Name:            r.PostFormValue("name"),
		Type:            r.PostFormValue("type"),
		Target:          r.PostFormValue("target"),
		Port:            formInt(r, "port", 0),
		IntervalSeconds: formInt(r, "interval_seconds", 60),
		TimeoutSeconds:  formInt(r, "timeout_seconds", 10),
		Retries:         formInt(r, "retries", 1),
		Enabled:         formBool(r, "enabled"),
	}
	c := checks.Config{}
	switch m.Type {
	case checks.TypeHTTP:
		c.Method = strings.ToUpper(strings.TrimSpace(r.PostFormValue("method")))
		c.ExpectedStatus = strings.TrimSpace(r.PostFormValue("expected_status"))
		c.Keyword = r.PostFormValue("keyword")
		c.KeywordAbsent = formBool(r, "keyword_absent")
		c.IgnoreTLSErrors = formBool(r, "ignore_tls_errors")
		c.NoFollowRedirects = formBool(r, "no_follow_redirects")
		c.DegradedMs = formInt(r, "degraded_ms", 0)
	case checks.TypePing:
		c.PingCount = formInt(r, "ping_count", 3)
	case checks.TypeTraceroute:
		c.MaxHops = formInt(r, "max_hops", 30)
		c.TraceICMP = formBool(r, "trace_icmp")
	case checks.TypeDNS:
		c.RecordType = strings.ToUpper(r.PostFormValue("record_type"))
		c.DNSServer = strings.TrimSpace(r.PostFormValue("dns_server"))
		c.Expected = strings.TrimSpace(r.PostFormValue("expected"))
	case checks.TypeSMTP:
		c.SMTPTLS = r.PostFormValue("smtp_tls")
	case checks.TypeTLS:
		c.WarnDays = formInt(r, "warn_days", 14)
	}
	m.Config = c
	return m
}

func (s *Server) handleMonitorSave(w http.ResponseWriter, r *http.Request) {
	m := monitorFromForm(r, pathID(r))
	if err := s.Store.SaveMonitor(r.Context(), m); err != nil {
		data, derr := s.monitorFormData(r, m)
		if derr != nil {
			s.serverError(w, derr)
			return
		}
		s.renderStatus(w, r, http.StatusBadRequest, "monitor_form", &page{Title: "Monitor", Active: "monitors", Flash: err.Error(), Data: data})
		return
	}
	s.flash(w, r, "/monitors/"+strconv.FormatInt(m.ID, 10), true, "Monitor saved. The first check runs right away.")
}

func (s *Server) handleMonitorDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteMonitor(r.Context(), pathID(r)); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/monitors", true, "Monitor deleted, including its history.")
}

func (s *Server) handleMonitorToggle(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	m, err := s.Store.GetMonitor(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.SetMonitorEnabled(r.Context(), id, !m.Enabled); err != nil {
		s.serverError(w, err)
		return
	}
	msg := "Monitor paused."
	if !m.Enabled {
		msg = "Monitor resumed."
	}
	back := safeNext(r.PostFormValue("back"))
	s.flash(w, r, back, true, msg)
}

// ---------------------------------------------------------------- status pages (admin)

func (s *Server) handleStatusPages(w http.ResponseWriter, r *http.Request) {
	pages, err := s.Store.ListStatusPages(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "status_pages", "Status pages", "status", map[string]any{"Pages": pages, "BaseURL": s.baseURL(r)})
}

func (s *Server) baseURL(r *http.Request) string {
	if s.Cfg.BaseURL != "" {
		return s.Cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil || (s.Cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) statusPageFormData(r *http.Request, p *store.StatusPage) (map[string]any, error) {
	list, err := s.Store.ListMonitors(r.Context())
	if err != nil {
		return nil, err
	}
	selected := map[int64]store.StatusPageMonitor{}
	for _, m := range p.Monitors {
		selected[m.MonitorID] = m
	}
	type option struct {
		M           store.Monitor
		Selected    bool
		DisplayName string
		Position    int
	}
	opts := make([]option, 0, len(list))
	for _, m := range list {
		sel, ok := selected[m.ID]
		pos := 1000
		if ok {
			pos = sel.Position
		}
		opts = append(opts, option{M: m, Selected: ok, DisplayName: sel.DisplayName, Position: pos})
	}
	sort.SliceStable(opts, func(i, j int) bool { return opts[i].Position < opts[j].Position })
	return map[string]any{"P": p, "Options": opts, "BaseURL": s.baseURL(r)}, nil
}

func (s *Server) handleStatusPageForm(w http.ResponseWriter, r *http.Request) {
	p := &store.StatusPage{Published: true}
	if id := pathID(r); id != 0 {
		var err error
		if p, err = s.Store.GetStatusPage(r.Context(), id); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	data, err := s.statusPageFormData(r, p)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "status_page_form", "Status page", "status", data)
}

func (s *Server) handleStatusPageSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	p := &store.StatusPage{ID: pathID(r), Slug: r.PostFormValue("slug"), Title: r.PostFormValue("title"),
		Description: strings.TrimSpace(r.PostFormValue("description")), Published: formBool(r, "published")}
	type sel struct {
		id   int64
		pos  int
		name string
	}
	var sels []sel
	for _, v := range r.PostForm["monitor"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		key := strconv.FormatInt(id, 10)
		sels = append(sels, sel{id: id, pos: formInt(r, "pos_"+key, 1000), name: r.PostFormValue("name_" + key)})
	}
	sort.SliceStable(sels, func(i, j int) bool { return sels[i].pos < sels[j].pos })
	for _, x := range sels {
		p.Monitors = append(p.Monitors, store.StatusPageMonitor{MonitorID: x.id, DisplayName: x.name})
	}
	if err := s.Store.SaveStatusPage(r.Context(), p); err != nil {
		data, derr := s.statusPageFormData(r, p)
		if derr != nil {
			s.serverError(w, derr)
			return
		}
		s.renderStatus(w, r, http.StatusBadRequest, "status_page_form", &page{Title: "Status page", Active: "status", Flash: err.Error(), Data: data})
		return
	}
	s.flash(w, r, "/admin/status-pages", true, "Status page saved.")
}

func (s *Server) handleStatusPageDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteStatusPage(r.Context(), pathID(r)); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/status-pages", true, "Status page deleted.")
}

// ---------------------------------------------------------------- users

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if id == userFrom(r).ID {
		s.flash(w, r, "/admin/users", false, "You cannot delete your own user.")
		return
	}
	if err := s.Store.DeleteUser(r.Context(), id); err != nil {
		s.flash(w, r, "/admin/users", false, err.Error())
		return
	}
	s.flash(w, r, "/admin/users", true, "User deleted.")
}

// ---------------------------------------------------------------- settings

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	ret, err := s.Store.GetRetention(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	stats, err := s.Store.Stats(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "settings", "Settings", "settings", map[string]any{
		"R": ret, "Stats": stats, "Max": store.MaxRetentionDays,
		"SizeMB": float64(stats.SizeBytes) / (1 << 20),
	})
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	ret := store.Retention{
		RawDays:       formInt(r, "raw_retention_days", 90),
		AggregateDays: formInt(r, "aggregate_retention_days", 730),
		EventsDays:    formInt(r, "events_retention_days", 730),
	}
	for _, v := range []int{ret.RawDays, ret.AggregateDays, ret.EventsDays} {
		if v < 1 || v > store.MaxRetentionDays {
			s.flash(w, r, "/admin/settings", false, "Retention periods must be between 1 and 730 days (2 years).")
			return
		}
	}
	if err := s.Store.SaveRetention(r.Context(), ret); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/settings", true, "Retention saved. Automatic cleanup runs every hour.")
}

func (s *Server) handlePurgeNow(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Purge(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/settings", true, "Cleanup finished: "+strconv.FormatInt(st.Results, 10)+" results, "+
		strconv.FormatInt(st.Hourly, 10)+" aggregates and "+strconv.FormatInt(st.Events, 10)+" events removed.")
}
