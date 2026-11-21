package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/store"
)

const statusDays = 90

type publicMonitor struct {
	ID     int64
	Name   string
	Status checks.Status
	Days   []store.DayStat
	Uptime float64
}

type publicView struct {
	Page     *store.StatusPage
	Monitors []publicMonitor
	Overall  checks.Status
	Headline string
	Events   []store.Event
	Updated  time.Time
}

func (s *Server) loadPublic(r *http.Request) (*publicView, error) {
	ctx := r.Context()
	p, err := s.Store.GetStatusPageBySlug(ctx, r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	// Unpublished pages are visible to logged-in users only (preview).
	if !p.Published {
		c, cerr := r.Cookie(sessionCookie)
		if cerr != nil {
			return nil, store.ErrNotFound
		}
		if _, _, err := s.Store.SessionUser(ctx, c.Value); err != nil {
			return nil, store.ErrNotFound
		}
	}
	ids := make([]int64, 0, len(p.Monitors))
	for _, m := range p.Monitors {
		ids = append(ids, m.MonitorID)
	}
	list, err := s.Store.ListMonitorsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := map[int64]store.Monitor{}
	for _, m := range list {
		byID[m.ID] = m
	}
	days, err := s.Store.DailyStats(ctx, ids, statusDays)
	if err != nil {
		return nil, err
	}
	v := &publicView{Page: p, Overall: checks.Up, Updated: time.Now()}
	down, degraded, active := 0, 0, 0
	var eventIDs []int64
	for _, pm := range p.Monitors {
		m, ok := byID[pm.MonitorID]
		if !ok {
			continue
		}
		name := pm.DisplayName
		if name == "" {
			name = m.Name
		}
		st := m.DisplayStatus()
		total, okCount := 0, 0
		for _, d := range days[m.ID] {
			total += d.Total
			okCount += d.Up + d.Degraded
		}
		up := -1.0
		if total > 0 {
			up = float64(okCount) * 100 / float64(total)
		}
		v.Monitors = append(v.Monitors, publicMonitor{ID: m.ID, Name: name, Status: st, Days: days[m.ID], Uptime: up})
		if m.Enabled {
			active++
			eventIDs = append(eventIDs, m.ID)
		}
		switch st {
		case checks.Down:
			down++
		case checks.Degraded:
			degraded++
		}
	}
	switch {
	case active == 0:
		v.Overall, v.Headline = checks.Unknown, "Sem serviços monitorizados"
	case down > 0 && down == active:
		v.Overall, v.Headline = checks.Down, "Interrupção grave de serviço"
	case down > 0:
		v.Overall, v.Headline = checks.Down, "Interrupção parcial de serviço"
	case degraded > 0:
		v.Overall, v.Headline = checks.Degraded, "Alguns serviços com desempenho degradado"
	default:
		v.Overall, v.Headline = checks.Up, "Todos os sistemas operacionais"
	}
	if len(eventIDs) > 0 {
		evs, err := s.Store.ListEvents(ctx, eventIDs, time.Now().AddDate(0, 0, -14), 50)
		if err != nil {
			return nil, err
		}
		names := map[int64]string{}
		for _, pm := range v.Monitors {
			names[pm.ID] = pm.Name
		}
		// Public pages list incidents only (transitions to degraded/down).
		for _, e := range evs {
			if e.To == checks.Up {
				continue
			}
			e.MonitorName = names[e.MonitorID]
			v.Events = append(v.Events, e)
		}
	}
	return v, nil
}

func (s *Server) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadPublic(r)
	if errors.Is(err, store.ErrNotFound) {
		s.renderStatus(w, r, http.StatusNotFound, "notfound", &page{Title: "Página não encontrada"})
		return
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	s.renderStatus(w, r, http.StatusOK, "status_public", &page{Title: v.Page.Title, Refresh: 60, Data: map[string]any{"V": v}})
}

func (s *Server) handlePublicStatusJSON(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadPublic(r)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	type day struct {
		Date   string   `json:"date"`
		Uptime *float64 `json:"uptime"`
	}
	type mon struct {
		Name      string   `json:"name"`
		Status    string   `json:"status"`
		Uptime90d *float64 `json:"uptime_90d"`
		Days      []day    `json:"days"`
	}
	type ev struct {
		Service string    `json:"service"`
		At      time.Time `json:"at"`
		From    string    `json:"from"`
		To      string    `json:"to"`
	}
	out := struct {
		Title    string    `json:"title"`
		Status   string    `json:"status"`
		Headline string    `json:"headline"`
		Updated  time.Time `json:"updated_at"`
		Services []mon     `json:"services"`
		Events   []ev      `json:"events"`
	}{Title: v.Page.Title, Status: v.Overall.String(), Headline: v.Headline, Updated: v.Updated, Services: []mon{}, Events: []ev{}}
	for _, m := range v.Monitors {
		x := mon{Name: m.Name, Status: m.Status.String(), Days: []day{}}
		if m.Uptime >= 0 {
			u := m.Uptime
			x.Uptime90d = &u
		}
		for _, d := range m.Days {
			dd := day{Date: d.Day.Format("2006-01-02")}
			if u := d.Uptime(); u >= 0 {
				dd.Uptime = &u
			}
			x.Days = append(x.Days, dd)
		}
		out.Services = append(out.Services, x)
	}
	for _, e := range v.Events {
		out.Events = append(out.Events, ev{Service: e.MonitorName, At: e.CreatedAt, From: e.From.String(), To: e.To.String()})
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, out)
}
