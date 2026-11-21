package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
)

func funcMap() template.FuncMap {
	return template.FuncMap{
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
				return humanDuration(time.Since(a)) + " (em curso)"
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
		"add":   func(a, b int) int { return a + b },
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
		return "nunca"
	}
	return "há " + humanDuration(time.Since(*t))
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
		return fmt.Sprintf("%d dias", int(d.Hours()/24))
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
