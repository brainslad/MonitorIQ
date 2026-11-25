// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package geo

import (
	"fmt"
	"math"
	"strings"
)

// The world map is an equirectangular projection on a 1000 × 500 canvas
// (static/world.svg uses the same projection).
const (
	MapW = 1000.0
	MapH = 500.0
)

// Project converts coordinates to map units.
func Project(lat, lon float64) (x, y float64) {
	return (lon + 180) / 360 * MapW, (90 - lat) / 180 * MapH
}

// Marker kinds.
const (
	KindProbe  = "probe"
	KindTarget = "target"
	KindHop    = "hop"
)

// Marker is a point drawn on a map.
type Marker struct {
	X, Y, R float64
	Kind    string
	Status  string // CSS status class for targets ("up", "down", ...)
	Title   string // tooltip
	Label   string // text drawn next to the marker (may be empty)
	LX, LY  float64
	Anchor  string // text-anchor of the label
	Count   int    // markers merged into this one
	lat     float64
	lon     float64
}

// Line joins two markers.
type Line struct {
	D      string
	Kind   string // "path" (probe → target) or "hop" (traceroute)
	Status string
}

// Map is a ready-to-draw SVG map.
type Map struct {
	VX, VY, VW, VH float64
	Markers        []Marker
	Lines          []Line
	FontSize       float64
}

// ViewBox is the SVG viewBox attribute.
func (m *Map) ViewBox() string {
	return fmt.Sprintf("%.1f %.1f %.1f %.1f", m.VX, m.VY, m.VW, m.VH)
}

// Empty reports whether nothing could be placed.
func (m *Map) Empty() bool { return len(m.Markers) == 0 }

// Grid is the graticule (every 30°) as a path.
func (m *Map) Grid() string {
	var b strings.Builder
	for lon := -150.0; lon <= 150; lon += 30 {
		x, _ := Project(0, lon)
		fmt.Fprintf(&b, "M%.0f 0V%.0f", x, MapH)
	}
	for lat := -60.0; lat <= 60; lat += 30 {
		_, y := Project(lat, 0)
		fmt.Fprintf(&b, "M0 %.0fH%.0f", y, MapW)
	}
	return b.String()
}

// AddMarker places a marker; markers of the same kind at the same place are merged.
func (m *Map) AddMarker(kind string, lat, lon float64, status, title, label string) *Marker {
	x, y := Project(lat, lon)
	for i := range m.Markers {
		mk := &m.Markers[i]
		if mk.Kind == kind && math.Abs(mk.X-x) < 0.6 && math.Abs(mk.Y-y) < 0.6 {
			mk.Count++
			mk.Status = worse(mk.Status, status)
			if title != "" {
				mk.Title += "\n" + title
			}
			if mk.Label != label {
				mk.Label = ""
			}
			return mk
		}
	}
	m.Markers = append(m.Markers, Marker{X: x, Y: y, Kind: kind, Status: status, Title: title, Label: label, Count: 1, lat: lat, lon: lon})
	return &m.Markers[len(m.Markers)-1]
}

// statusRank orders status classes from best to worst.
var statusRank = map[string]int{"": 0, "paused": 1, "unknown": 1, "up": 2, "degraded": 3, "down": 4}

func worse(a, b string) string {
	if statusRank[b] > statusRank[a] {
		return b
	}
	return a
}

// Connect draws a curved line from the first marker of kind `from` at
// (lat1, lon1) to (lat2, lon2).
func (m *Map) Connect(kind, status string, lat1, lon1, lat2, lon2 float64, curve float64) {
	x1, y1 := Project(lat1, lon1)
	x2, y2 := Project(lat2, lon2)
	if math.Hypot(x2-x1, y2-y1) < 0.8 {
		return
	}
	mx, my := (x1+x2)/2, (y1+y2)/2
	dx, dy := x2-x1, y2-y1
	// Bend the curve "upwards" (towards the north) so arcs look like routes.
	nx, ny := -dy*curve, dx*curve
	if ny > 0 {
		nx, ny = -nx, -ny
	}
	m.Lines = append(m.Lines, Line{Kind: kind, Status: status,
		D: fmt.Sprintf("M%.1f %.1fQ%.1f %.1f %.1f %.1f", x1, y1, mx+nx, my+ny, x2, y2)})
}

// FitWorld shows the inhabited world.
func (m *Map) FitWorld() {
	m.VX, m.VY, m.VW, m.VH = 0, 22, MapW, 390
	m.finish()
}

// minFitW is the narrowest view FitMarkers zooms to (about 11° of longitude).
const minFitW = 30.0

// FitMarkers zooms to the markers, keeping the frame of the world view.
func (m *Map) FitMarkers() {
	if len(m.Markers) == 0 {
		m.FitWorld()
		return
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, mk := range m.Markers {
		minX, maxX = math.Min(minX, mk.X), math.Max(maxX, mk.X)
		minY, maxY = math.Min(minY, mk.Y), math.Max(maxY, mk.Y)
	}
	w, h := maxX-minX, maxY-minY
	// A small margin, so nearby places (e.g. two cities in one country)
	// end up far enough apart on screen to tell them apart.
	pad := math.Max(8, 0.25*math.Max(w, 2*h))
	w, h = w+2*pad, h+2*pad
	const aspect = MapW / 390
	if w/h < aspect {
		w = h * aspect
	} else {
		h = w / aspect
	}
	if w < minFitW {
		w, h = minFitW, minFitW/aspect
	}
	if w > MapW {
		m.FitWorld()
		return
	}
	cx, cy := (minX+maxX)/2, (minY+maxY)/2
	m.VX = clamp(cx-w/2, 0, MapW-w)
	m.VY = clamp(cy-h/2, 0, MapH-h)
	m.VW, m.VH = w, h
	m.finish()
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// finish sizes markers and labels for the zoom level.
func (m *Map) finish() {
	s := m.VW / MapW
	m.FontSize = 15 * s
	for i := range m.Markers {
		mk := &m.Markers[i]
		switch mk.Kind {
		case KindProbe:
			mk.R = 8.5 * s
		case KindHop:
			mk.R = 4.5 * s
		default:
			mk.R = (6.5 + math.Min(5, 1.5*float64(mk.Count-1))) * s
		}
		// Labels go to the side with more room.
		mk.LY = mk.Y + m.FontSize*0.35
		if mk.X > m.VX+m.VW*0.62 {
			mk.LX, mk.Anchor = mk.X-mk.R-5*s, "end"
		} else {
			mk.LX, mk.Anchor = mk.X+mk.R+5*s, "start"
		}
	}
}
