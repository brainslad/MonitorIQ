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

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "login", "Entrar", "", map[string]any{"Next": safeNext(r.URL.Query().Get("next"))})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	next := safeNext(r.PostFormValue("next"))
	fail := func(msg string) {
		s.renderStatus(w, r, http.StatusUnauthorized, "login", &page{Title: "Entrar", Flash: msg,
			Data: map[string]any{"Next": next, "Username": r.PostFormValue("username")}})
	}
	if s.limiter.blocked(ip) {
		fail("Demasiadas tentativas falhadas. Aguarde 15 minutos.")
		return
	}
	u, err := s.Store.Authenticate(r.Context(), r.PostFormValue("username"), r.PostFormValue("password"))
	if err != nil {
		s.limiter.fail(ip)
		s.Log.Warn("login falhado", "username", r.PostFormValue("username"), "ip", ip)
		fail("Utilizador ou palavra-passe incorretos.")
		return
	}
	s.limiter.reset(ip)
	token, err := s.Store.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.Cfg.CookieSecure, MaxAge: int(sessionTTL.Seconds())})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
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

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.Store.ListMonitors(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
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
	sparks, err := s.Store.Sparklines(ctx, ids)
	if err != nil {
		s.serverError(w, err)
		return
	}
	events, err := s.Store.ListEvents(ctx, nil, now.AddDate(0, 0, -7), 15)
	if err != nil {
		s.serverError(w, err)
		return
	}
	counts := map[string]int{"up": 0, "degraded": 0, "down": 0, "unknown": 0, "paused": 0}
	for _, m := range list {
		if !m.Enabled {
			counts["paused"]++
			continue
		}
		counts[m.Status.String()]++
	}
	hosts, err := s.Store.ListHosts(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	p := &page{Title: "Dashboard", Active: "dashboard", Refresh: 30, Data: map[string]any{
		"Groups": groupByHost(list), "Up24": up24, "Up30": up30, "Sparks": sparks,
		"Counts": counts, "Total": len(list), "Events": events, "HostCount": len(hosts),
	}}
	s.renderStatus(w, r, http.StatusOK, "dashboard", p)
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
	s.flash(w, r, "/monitors/"+strconv.FormatInt(id, 10), true, "Verificação agendada. Os resultados aparecem em alguns segundos.")
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
			"Host criado. Adicione agora os serviços a monitorizar.")
		return
	}
	s.flash(w, r, "/admin/hosts", true, "Host guardado.")
}

func (s *Server) handleHostDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteHost(r.Context(), pathID(r)); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/hosts", true, "Host e respetivos serviços apagados.")
}

// ---------------------------------------------------------------- monitors

func (s *Server) handleMonitors(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListMonitors(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "monitors", "Serviços", "monitors", map[string]any{"Groups": groupByHost(list), "Total": len(list)})
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
		s.flash(w, r, "/admin/hosts/new", false, "Crie primeiro um host para lhe associar serviços.")
		return
	}
	s.render(w, r, "monitor_form", "Serviço", "monitors", data)
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
		s.renderStatus(w, r, http.StatusBadRequest, "monitor_form", &page{Title: "Serviço", Active: "monitors", Flash: err.Error(), Data: data})
		return
	}
	s.flash(w, r, "/monitors/"+strconv.FormatInt(m.ID, 10), true, "Serviço guardado. A primeira verificação corre de imediato.")
}

func (s *Server) handleMonitorDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteMonitor(r.Context(), pathID(r)); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/monitors", true, "Serviço apagado, incluindo o seu histórico.")
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
	msg := "Serviço pausado."
	if !m.Enabled {
		msg = "Serviço retomado."
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
		http.Error(w, "formulário inválido", http.StatusBadRequest)
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
	s.flash(w, r, "/admin/status-pages", true, "Status page guardada.")
}

func (s *Server) handleStatusPageDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteStatusPage(r.Context(), pathID(r)); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/status-pages", true, "Status page apagada.")
}

// ---------------------------------------------------------------- users

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "users", "Utilizadores", "users", map[string]any{"Users": users})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	if r.PostFormValue("password") != r.PostFormValue("password2") {
		s.flash(w, r, "/admin/users", false, "As palavras-passe não coincidem.")
		return
	}
	if _, err := s.Store.CreateUser(r.Context(), r.PostFormValue("username"), r.PostFormValue("password")); err != nil {
		s.flash(w, r, "/admin/users", false, err.Error())
		return
	}
	s.flash(w, r, "/admin/users", true, "Utilizador criado.")
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if id == userFrom(r).ID {
		s.flash(w, r, "/admin/users", false, "Não pode apagar o seu próprio utilizador.")
		return
	}
	if err := s.Store.DeleteUser(r.Context(), id); err != nil {
		s.flash(w, r, "/admin/users", false, err.Error())
		return
	}
	s.flash(w, r, "/admin/users", true, "Utilizador apagado.")
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if !s.Store.CheckPassword(r.Context(), u.ID, r.PostFormValue("current")) {
		s.flash(w, r, "/admin/users", false, "A palavra-passe atual está incorreta.")
		return
	}
	if r.PostFormValue("password") != r.PostFormValue("password2") {
		s.flash(w, r, "/admin/users", false, "As palavras-passe novas não coincidem.")
		return
	}
	if err := s.Store.SetPassword(r.Context(), u.ID, r.PostFormValue("password")); err != nil {
		s.flash(w, r, "/admin/users", false, err.Error())
		return
	}
	s.flash(w, r, "/admin/users", true, "Palavra-passe alterada.")
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
	s.render(w, r, "settings", "Definições", "settings", map[string]any{
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
			s.flash(w, r, "/admin/settings", false, "Os períodos de retenção devem estar entre 1 e 730 dias (2 anos).")
			return
		}
	}
	if err := s.Store.SaveRetention(r.Context(), ret); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/settings", true, "Retenção guardada. A limpeza automática corre de hora a hora.")
}

func (s *Server) handlePurgeNow(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Purge(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/settings", true, "Limpeza concluída: "+strconv.FormatInt(st.Results, 10)+" resultados, "+
		strconv.FormatInt(st.Hourly, 10)+" agregados e "+strconv.FormatInt(st.Events, 10)+" eventos removidos.")
}
