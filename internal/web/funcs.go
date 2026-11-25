// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
)

func funcMap() template.FuncMap {
	return template.FuncMap{
		"asset":       assetURL,
		"statusClass": func(s checks.Status) string { return s.String() },
		"statusLabel": func(s checks.Status) string { return s.Label() },
		"typeLabel":   checks.TypeLabel,
		"types":       func() []checks.TypeInfo { return checks.Types },
		"fmtTime": func(t any) string {
			switch v := t.(type) {
			case time.Time:
				return v.Local().Format("2006-01-02 15:04:05")
			case *time.Time:
				if v == nil {
					return "—"
				}
				return v.Local().Format("2006-01-02 15:04:05")
			}
			return "—"
		},
		"fmtDate": func(t time.Time) string { return t.Local().Format("2006-01-02") },
		"ago":     ago,
		"since":   func(t time.Time) string { return humanDuration(time.Since(t)) },
		"between": func(a time.Time, b *time.Time) string {
			if b == nil {
				return humanDuration(time.Since(a)) + " (ongoing)"
			}
			return humanDuration(b.Sub(a))
		},
		"fmtMs": func(v any) string {
			switch x := v.(type) {
			case *float64:
				if x == nil {
					return "—"
				}
				return fmtMs(*x)
			case float64:
				return fmtMs(x)
			}
			return "—"
		},
		"pct": func(m map[int64]float64, id int64) string {
			v, ok := m[id]
			if !ok {
				return "—"
			}
			return fmtPct(v)
		},
		"pctVal": fmtPct,
		"pctClass": func(m map[int64]float64, id int64) string {
			v, ok := m[id]
			switch {
			case !ok:
				return ""
			case v >= 99.9:
				return "good"
			case v >= 99:
				return "ok"
			case v >= 95:
				return "warn"
			default:
				return "bad"
			}
		},
		"sparkline": sparkline,
		"json": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		// has reports whether a list of form values holds the id.
		"has": func(list []string, id int64) bool {
			for _, v := range list {
				if v == strconv.FormatInt(id, 10) {
					return true
				}
			}
			return false
		},
		"add2": func(a, b float64) float64 { return a + b },
		"mul":  func(a, b float64) float64 { return a * b },
		"km": func(v float64) string {
			if v < 10 {
				return fmt.Sprintf("%.1f km", v)
			}
			return groupThousands(int64(v+0.5)) + " km"
		},
		"mb":    func(n int64) string { return fmt.Sprintf("%.0f MB", float64(n)/(1<<20)) },
		"split": strings.Fields,
		"upper": strings.ToUpper,
		"pctf": func(v float64) string {
			if v < 0 {
				return "—"
			}
			if v >= 100 {
				return "100"
			}
			return fmt.Sprintf("%.2f", math.Floor(v*100)/100)
		},
		"pctLevel": func(v float64) string {
			switch {
			case v < 0:
				return ""
			case v >= 99.9:
				return "good"
			case v >= 99:
				return "ok"
			case v >= 95:
				return "warn"
			}
			return "bad"
		},
		"msNum": func(v float64) string {
			if v < 10 {
				return fmt.Sprintf("%.1f", v)
			}
			return fmt.Sprintf("%.0f", v)
		},
		"clock":     func(t time.Time) string { return t.Local().Format("15:04:05") },
		"hourLabel": func(t time.Time) string { return t.Local().Format("15:04") },
		"eq64":      func(a, b int64) bool { return a == b },
		"initials": func(name string) string {
			var out []rune
			for _, f := range strings.FieldsFunc(name, func(r rune) bool { return r == ' ' || r == '.' || r == '_' || r == '-' || r == '@' }) {
				if len(out) == 2 {
					break
				}
				out = append(out, []rune(strings.ToUpper(f))[0])
			}
			if len(out) == 0 {
				return "?"
			}
			return string(out)
		},
		"thousands": func(n int) string {
			s := fmt.Sprint(n)
			for i := len(s) - 3; i > 0; i -= 3 {
				s = s[:i] + "," + s[i:]
			}
			return s
		},
		"shortWhen": func(t time.Time) string {
			l := t.Local()
			if y, m, d := l.Date(); y == time.Now().Year() && m == time.Now().Month() && d == time.Now().Day() {
				return l.Format("15:04")
			}
			return l.Format("2 Jan")
		},
		"longDate": func(t time.Time) string { return t.Local().Format("Monday 2 January") },
		"greeting": func(t time.Time) string {
			switch h := t.Local().Hour(); {
			case h < 5:
				return "Good evening"
			case h < 12:
				return "Good morning"
			case h < 18:
				return "Good afternoon"
			}
			return "Good evening"
		},
		"firstName": func(name string) string {
			name = strings.TrimSpace(name)
			if i := strings.IndexAny(name, " @"); i > 0 {
				name = name[:i]
			}
			if name == "" {
				return "there"
			}
			return strings.ToUpper(name[:1]) + name[1:]
		},
		"clamp": func(v float64) float64 { return math.Max(0, math.Min(100, v)) },
		"neg":   func(n int) int { return -n },
		"section": func(active string) string {
			switch active {
			case "users", "settings":
				return "Administration"
			case "account":
				return "Account"
			}
			return "Monitoring"
		},
		"contains": func(list []int64, id int64) bool {
			for _, x := range list {
				if x == id {
					return true
				}
			}
			return false
		},
		"detail": func(d map[string]any, key string) any {
			if d == nil {
				return nil
			}
			return d[key]
		},
		"join": func(v any) string {
			switch x := v.(type) {
			case []any:
				parts := make([]string, len(x))
				for i, p := range x {
					parts[i] = fmt.Sprint(p)
				}
				return strings.Join(parts, ", ")
			case []string:
				return strings.Join(x, ", ")
			}
			return fmt.Sprint(v)
		},
		"hops": func(d map[string]any) []map[string]any {
			raw, ok := d["hops"].([]any)
			if !ok {
				return nil
			}
			out := make([]map[string]any, 0, len(raw))
			for _, h := range raw {
				if m, ok := h.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out
		},
		"rtts": func(v any) string {
			list, ok := v.([]any)
			if !ok || len(list) == 0 {
				return "*"
			}
			parts := make([]string, len(list))
			for i, x := range list {
				if f, ok := x.(float64); ok {
					parts[i] = fmtMs(f)
				}
			}
			return strings.Join(parts, " / ")
		},
	}
}

func fmtMs(v float64) string {
	switch {
	case v >= 1000:
		return fmt.Sprintf("%.2f s", v/1000)
	case v >= 100:
		return fmt.Sprintf("%.0f ms", v)
	default:
		return fmt.Sprintf("%.1f ms", v)
	}
}

func fmtPct(v float64) string {
	if v < 0 {
		return "—"
	}
	if v >= 100 {
		return "100%"
	}
	return fmt.Sprintf("%.2f%%", math.Floor(v*100)/100)
}

func ago(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return humanDuration(time.Since(*t)) + " ago"
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dmin", h, m)
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// sparkline renders a tiny inline SVG line chart.
func sparkline(values []float64) template.HTML {
	const w, h = 120.0, 28.0
	if len(values) < 2 {
		return template.HTML(`<svg class="spark" viewBox="0 0 120 28" aria-hidden="true"></svg>`)
	}
	lo, hi := values[0], values[0]
	for _, v := range values {
		lo = math.Min(lo, v)
		hi = math.Max(hi, v)
	}
	if hi-lo < 1e-9 {
		hi = lo + 1
	}
	var b strings.Builder
	for i, v := range values {
		x := float64(i) * w / float64(len(values)-1)
		y := h - 2 - (v-lo)/(hi-lo)*(h-4)
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
	}
	return template.HTML(fmt.Sprintf(`<svg class="spark" viewBox="0 0 120 28" preserveAspectRatio="none" aria-hidden="true"><polyline points="%s"/></svg>`, b.String()))
}

// groupThousands writes 12345 as "12,345".
func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
