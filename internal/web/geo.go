// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/geo"
	"github.com/brainslad/monitoriq/internal/store"
)

// GeoSettings loads the geolocation settings (defaults on error).
func (s *Server) GeoSettings(ctx context.Context) geo.Settings {
	raw, err := s.Store.GetSetting(ctx, geo.SettingsKey)
	if err != nil {
		s.Log.Error("geo settings", "err", err)
	}
	st, err := geo.ParseSettings(raw)
	if err != nil {
		s.Log.Error("geo settings", "err", err)
	}
	return st
}

// monitorStatusClass is the CSS status class of a monitor.
func monitorStatusClass(m *store.Monitor) string {
	if !m.Enabled {
		return "paused"
	}
	return m.Status.String()
}

// ---------------------------------------------------------------- monitor page

type geoHop struct {
	TTL  int
	Info geo.Info
}

type monitorGeo struct {
	Probe   geo.Probe
	Target  geo.HostInfo
	Primary *geo.Info
	// Local is true when the target is on a private network: it is drawn at
	// the monitoring system's location.
	Local       bool
	Map         *geo.Map
	DistanceKm  float64
	MinRTTms    float64
	HasDistance bool
	Hops        []geoHop
	DB          geo.Status
	AttrText    string
	AttrURL     string
}

func (s *Server) monitorGeo(ctx context.Context, m *store.Monitor, latest *store.Result) *monitorGeo {
	if s.Geo == nil {
		return nil
	}
	g := &monitorGeo{DB: s.Geo.DB.Status(), Map: &geo.Map{}}
	g.AttrText, g.AttrURL = g.DB.Attribution()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); g.Probe = s.Geo.Probe(ctx) }()
	go func() { defer wg.Done(); g.Target = s.Geo.Host(ctx, checks.TargetHost(m.CheckSpec())) }()
	var hops []geoHop
	if latest != nil && m.Type == checks.TypeTraceroute {
		wg.Add(1)
		go func() { defer wg.Done(); hops = s.traceHops(latest.Details) }()
	}
	wg.Wait()
	g.Hops = hops
	g.Primary = g.Target.Primary()

	p := g.Probe
	status := monitorStatusClass(m)
	if p.Located() {
		g.Map.AddMarker(geo.KindProbe, p.Loc.Lat, p.Loc.Lon, "", "Monitoring system · "+p.Label, p.Label)
	}
	var tLat, tLon float64
	placed := false
	if g.Primary != nil && g.Primary.Loc.HasCoords {
		tLat, tLon, placed = g.Primary.Loc.Lat, g.Primary.Loc.Lon, true
	} else if g.Primary != nil && !g.Primary.Public() && p.Located() {
		tLat, tLon, placed, g.Local = p.Loc.Lat, p.Loc.Lon, true, true
	}
	// Traceroute hops in between (only those with a known place).
	prevLat, prevLon, havePrev := p.Loc.Lat, p.Loc.Lon, p.Located()
	for _, h := range hops {
		if !h.Info.Loc.HasCoords {
			continue
		}
		g.Map.AddMarker(geo.KindHop, h.Info.Loc.Lat, h.Info.Loc.Lon, "", fmt.Sprintf("Hop %d · %s · %s", h.TTL, h.Info.IP, h.Info.Loc.Place()), "")
		if havePrev {
			g.Map.Connect("hop", "", prevLat, prevLon, h.Info.Loc.Lat, h.Info.Loc.Lon, 0.12)
		}
		prevLat, prevLon, havePrev = h.Info.Loc.Lat, h.Info.Loc.Lon, true
	}
	if placed {
		label := g.Target.Host
		if g.Local {
			// Same place as the probe: one label for both.
			label = ""
			if len(g.Map.Markers) > 0 && g.Map.Markers[0].Kind == geo.KindProbe {
				g.Map.Markers[0].Label = p.Label + " → " + g.Target.Host
			}
		}
		g.Map.AddMarker(geo.KindTarget, tLat, tLon, status, "Target · "+g.Target.Host, label)
		if len(hops) > 0 && havePrev {
			g.Map.Connect("hop", "", prevLat, prevLon, tLat, tLon, 0.12)
		}
		if p.Located() {
			g.Map.Connect("path", status, p.Loc.Lat, p.Loc.Lon, tLat, tLon, 0.22)
			if !g.Local {
				g.DistanceKm = geo.DistanceKm(p.Loc.Lat, p.Loc.Lon, tLat, tLon)
				g.MinRTTms = geo.MinRTTms(g.DistanceKm)
				g.HasDistance = true
			}
		}
	}
	g.Map.FitMarkers()
	return g
}

// traceHops looks up the addresses of the hops of a traceroute result.
func (s *Server) traceHops(details map[string]any) []geoHop {
	raw, _ := details["hops"].([]any)
	var out []geoHop
	for _, h := range raw {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		addr, _ := hm["address"].(string)
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		ttl := 0
		switch v := hm["ttl"].(type) {
		case float64:
			ttl = int(v)
		case int:
			ttl = v
		}
		out = append(out, geoHop{TTL: ttl, Info: s.Geo.DB.Lookup(ip)})
	}
	return out
}

// ---------------------------------------------------------------- dashboard

type geoCountry struct {
	Flag, Name string
	Count      int
	Status     string
}

type geoTarget struct {
	ID           int64
	Name, Status string
	Flag, Place  string
	IP           string
	Km           float64
}

type dashGeo struct {
	Targets   []geoTarget
	Probe     geo.Probe
	Map       *geo.Map
	Countries []geoCountry
	Located   int
	Local     int
	Unknown   int
	Farthest  string
	FarKm     float64
	DB        geo.Status
	AttrText  string
	AttrURL   string
}

// dashboardGeo places every monitor's target on the world map.
func (s *Server) dashboardGeo(ctx context.Context, list []store.Monitor) *dashGeo {
	if s.Geo == nil {
		return nil
	}
	d := &dashGeo{DB: s.Geo.DB.Status(), Map: &geo.Map{}}
	d.AttrText, d.AttrURL = d.DB.Attribution()
	hosts := make([]geo.HostInfo, len(list))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); d.Probe = s.Geo.Probe(ctx) }()
	sem := make(chan struct{}, 8)
	for i := range list {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			hosts[i] = s.Geo.Host(ctx, checks.TargetHost(list[i].CheckSpec()))
		}(i)
	}
	wg.Wait()

	p := d.Probe
	if p.Located() {
		d.Map.AddMarker(geo.KindProbe, p.Loc.Lat, p.Loc.Lon, "", "Monitoring system · "+p.Label, p.Label)
	}
	countries := map[string]*geoCountry{}
	for i := range list {
		m := &list[i]
		status := monitorStatusClass(m)
		pr := hosts[i].Primary()
		switch {
		case pr != nil && pr.Loc.HasCoords:
			d.Located++
			title := fmt.Sprintf("%s · %s · %s", m.Name, hosts[i].Host, pr.Loc.Place())
			d.Map.AddMarker(geo.KindTarget, pr.Loc.Lat, pr.Loc.Lon, status, title, "")
			t := geoTarget{ID: m.ID, Name: m.Name, Status: status, Flag: pr.Loc.Flag(), Place: pr.Loc.Place(), IP: pr.IP, Km: -1}
			if p.Located() {
				d.Map.Connect("path", status, p.Loc.Lat, p.Loc.Lon, pr.Loc.Lat, pr.Loc.Lon, 0.2)
				t.Km = geo.DistanceKm(p.Loc.Lat, p.Loc.Lon, pr.Loc.Lat, pr.Loc.Lon)
				if t.Km > d.FarKm {
					d.FarKm, d.Farthest = t.Km, m.Name
				}
			}
			d.Targets = append(d.Targets, t)
			key := pr.Loc.CountryCode
			c, ok := countries[key]
			if !ok {
				c = &geoCountry{Flag: pr.Loc.Flag(), Name: pr.Loc.Country}
				if c.Name == "" {
					c.Name = "Unknown country"
				}
				countries[key] = c
			}
			c.Count++
			c.Status = worseStatus(c.Status, status)
		case pr != nil && !pr.Public():
			d.Local++
		default:
			d.Unknown++
		}
	}
	for _, c := range countries {
		d.Countries = append(d.Countries, *c)
	}
	sort.Slice(d.Countries, func(i, j int) bool {
		if d.Countries[i].Count != d.Countries[j].Count {
			return d.Countries[i].Count > d.Countries[j].Count
		}
		return d.Countries[i].Name < d.Countries[j].Name
	})
	if len(d.Countries) > 6 {
		d.Countries = d.Countries[:6]
	}
	// Problems first, then the farthest.
	sort.SliceStable(d.Targets, func(i, j int) bool {
		a, b := statusOrder[d.Targets[i].Status], statusOrder[d.Targets[j].Status]
		if a != b {
			return a > b
		}
		return d.Targets[i].Km > d.Targets[j].Km
	})
	if len(d.Targets) > 8 {
		d.Targets = d.Targets[:8]
	}
	d.Map.FitMarkers()
	return d
}

var statusOrder = map[string]int{"": 0, "paused": 1, "unknown": 1, "up": 2, "degraded": 3, "down": 4}

func worseStatus(a, b string) string {
	if statusOrder[b] > statusOrder[a] {
		return b
	}
	return a
}

// ---------------------------------------------------------------- settings page

// geoUpdate tracks a database download started from the settings page.
type geoUpdate struct {
	mu      sync.Mutex
	running bool
	started time.Time
	last    string
	lastOK  bool
}

func (u *geoUpdate) snapshot() (running bool, msg string, ok bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running, u.last, u.lastOK
}

func (s *Server) geoFormData(r *http.Request, st geo.Settings, lookup *geo.HostInfo, lookupQ string) map[string]any {
	ctx := r.Context()
	status := s.Geo.DB.Status()
	attr, attrURL := status.Attribution()
	running, msg, ok := s.geoUpd.snapshot()
	return map[string]any{
		"S": st, "DB": status, "AttrText": attr, "AttrURL": attrURL,
		"Probe": s.Geo.Probe(ctx), "Updating": running, "UpdateMsg": msg, "UpdateOK": ok,
		"Lookup": lookup, "LookupQ": lookupQ, "DefaultURL": geo.DefaultDetectURL, "DefaultMirror": geo.DefaultMirror,
	}
}

func (s *Server) handleGeoSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "geo_settings", "Geolocation", "settings", s.geoFormData(r, s.GeoSettings(r.Context()), nil, ""))
}

func parseCoord(v string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(v, ",", ".")), 64)
	return f
}

func (s *Server) handleGeoSettingsSave(w http.ResponseWriter, r *http.Request) {
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	st := geo.Settings{
		PublicIPMode:  f("public_ip_mode"),
		PublicIP:      f("public_ip"),
		DetectURL:     f("detect_url"),
		Label:         f("label"),
		FixedLocation: formBool(r, "fixed_location"),
		Lat:           parseCoord(r.PostFormValue("lat")),
		Lon:           parseCoord(r.PostFormValue("lon")),
		City:          f("city"),
		Country:       f("country"),
		ReverseDNS:    formBool(r, "reverse_dns"),
		AutoUpdate:    formBool(r, "auto_update"),
		Mirror:        f("mirror"),
	}
	if st.DetectURL == "" {
		st.DetectURL = geo.DefaultDetectURL
	}
	if err := st.Validate(); err != nil {
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "geo_settings", &page{Title: "Geolocation", Active: "settings",
			Flash: err.Error(), Data: s.geoFormData(r, st, nil, "")})
		return
	}
	enc, err := st.Encode()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetSetting(r.Context(), geo.SettingsKey, enc); err != nil {
		s.serverError(w, err)
		return
	}
	s.Geo.RefreshPublicIP()
	s.Log.Info("geolocation settings changed", "by", userFrom(r).Username, "public_ip", st.PublicIPMode, "fixed", st.FixedLocation)
	s.flash(w, r, "/admin/geo", true, "Geolocation settings saved.")
}

// handleGeoUpdate starts a database download in the background.
func (s *Server) handleGeoUpdate(w http.ResponseWriter, r *http.Request) {
	u := s.geoUpd
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		s.flash(w, r, "/admin/geo", false, "A download is already running.")
		return
	}
	u.running, u.started = true, time.Now()
	u.mu.Unlock()
	mirror := s.GeoSettings(r.Context()).Mirror
	by := userFrom(r).Username
	go func() {
		msg, ok := s.UpdateGeoDB(context.Background(), mirror)
		s.Log.Info("geolocation database update", "by", by, "ok", ok, "result", msg)
		u.mu.Lock()
		u.running, u.last, u.lastOK = false, msg, ok
		u.mu.Unlock()
	}()
	s.flash(w, r, "/admin/geo", true, "Download started. It takes a minute or two; reload this page to see the result.")
}

// UpdateGeoDB downloads the DB-IP Lite databases and describes the outcome.
func (s *Server) UpdateGeoDB(ctx context.Context, mirror string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	res, err := s.Geo.DB.Update(ctx, nil, mirror, time.Now().UTC())
	if err != nil {
		return "Download failed: " + err.Error(), false
	}
	var parts []string
	for _, r := range res {
		state := "downloaded"
		if r.Skipped {
			state = "already current"
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s edition)", r.File, state, r.Built.Format("January 2006")))
	}
	return "Databases updated: " + strings.Join(parts, ", ") + ".", true
}

// handleGeoLookup shows what is known about an address or host name.
func (s *Server) handleGeoLookup(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.PostFormValue("q"))
	q = checks.TargetHost(checks.Monitor{Target: q})
	var hi *geo.HostInfo
	if q != "" {
		h := s.Geo.Host(r.Context(), q)
		hi = &h
	}
	s.render(w, r, "geo_settings", "Geolocation", "settings", s.geoFormData(r, s.GeoSettings(r.Context()), hi, q))
}

// RunGeoAutoUpdate downloads a new database edition once a month when the
// setting is on. It returns when ctx ends.
func (s *Server) RunGeoAutoUpdate(ctx context.Context) {
	if s.Geo == nil {
		return
	}
	var lastTry time.Time
	check := func() {
		if changed, err := s.Geo.DB.ReloadIfChanged(); changed {
			s.Log.Info("geolocation database reloaded", "err", err)
		}
		st := s.GeoSettings(ctx)
		if !st.AutoUpdate {
			return
		}
		status := s.Geo.DB.Status()
		now := time.Now().UTC()
		built := status.City.Built.UTC()
		// DB-IP publishes at the start of each month; try from the 3rd.
		if status.City.Loaded && built.Year() == now.Year() && built.Month() == now.Month() {
			return
		}
		if now.Day() < 3 && status.City.Loaded {
			return
		}
		if time.Since(lastTry) < 12*time.Hour {
			return
		}
		lastTry = time.Now()
		msg, ok := s.UpdateGeoDB(ctx, st.Mirror)
		s.Log.Info("geolocation database auto-update", "ok", ok, "result", msg)
		s.geoUpd.mu.Lock()
		s.geoUpd.last, s.geoUpd.lastOK = msg, ok
		s.geoUpd.mu.Unlock()
	}
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			check()
			timer.Reset(time.Hour)
		}
	}
}
