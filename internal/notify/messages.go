// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package notify

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
)

// CertItem is one certificate in the daily digest.
type CertItem struct {
	Name     string
	Target   string
	Issuer   string
	NotAfter time.Time
	DaysLeft int
	URL      string
}

const timeFmt = "2006-01-02 15:04 MST"

func word(s checks.Status) string {
	switch s {
	case checks.Up:
		return "UP"
	case checks.Degraded:
		return "DEGRADED"
	case checks.Down:
		return "DOWN"
	}
	return "UNKNOWN"
}

// headline is the short description of a change, e.g. "is DOWN" or "is back UP".
func headline(it Item) string {
	switch {
	case it.To == checks.Up && it.From == checks.Down:
		return "is back UP"
	case it.To == checks.Up:
		return "is operational again"
	case it.To == checks.Degraded && it.From == checks.Down:
		return "is reachable again but DEGRADED"
	}
	return "is " + word(it.To)
}

func color(s checks.Status) string {
	switch s {
	case checks.Up:
		return "#1f8f74"
	case checks.Degraded:
		return "#c07a12"
	case checks.Down:
		return "#d63c33"
	}
	return "#67737a"
}

// AlertMessage builds the e-mail for one or more status changes.
func AlertMessage(items []Item, base string) Message {
	var subject string
	if len(items) == 1 {
		it := items[0]
		subject = fmt.Sprintf("[MonitorIQ] %s: %s", word(it.To), it.Name)
		if it.To == checks.Up && it.From == checks.Down {
			subject = fmt.Sprintf("[MonitorIQ] RECOVERED: %s", it.Name)
		}
		if it.Host != "" {
			subject += " (" + it.Host + ")"
		}
	} else {
		counts := map[string]int{}
		var order []string
		for _, it := range items {
			k := strings.ToLower(word(it.To))
			if it.To == checks.Up {
				k = "recovered"
			}
			if counts[k] == 0 {
				order = append(order, k)
			}
			counts[k]++
		}
		parts := make([]string, 0, len(order))
		for _, k := range order {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
		}
		subject = fmt.Sprintf("[MonitorIQ] %d status changes: %s", len(items), strings.Join(parts, ", "))
	}

	var t strings.Builder
	for _, it := range items {
		fmt.Fprintf(&t, "%s %s\n", it.Name, headline(it))
		fmt.Fprintf(&t, "  Host:    %s\n  Target:  %s (%s)\n", it.Host, it.Target, strings.ToUpper(it.Type))
		fmt.Fprintf(&t, "  Since:   %s\n", it.At.Local().Format(timeFmt))
		if it.Lasted != "" {
			fmt.Fprintf(&t, "  Was %s for %s\n", strings.ToLower(word(it.From)), it.Lasted)
		}
		if it.Message != "" {
			fmt.Fprintf(&t, "  Result:  %s\n", it.Message)
		}
		if it.URL != "" {
			fmt.Fprintf(&t, "  Details: %s\n", it.URL)
		}
		t.WriteString("\n")
	}
	if base != "" {
		fmt.Fprintf(&t, "Dashboard: %s/\n", base)
	}
	t.WriteString(footerText)

	type row struct {
		Item
		Headline, Color, FromWord string
		When                      string
	}
	rows := make([]row, len(items))
	for i, it := range items {
		rows[i] = row{Item: it, Headline: headline(it), Color: color(it.To), FromWord: strings.ToLower(word(it.From)),
			When: it.At.Local().Format(timeFmt)}
	}
	return Message{Subject: subject, Text: t.String(), HTML: render(alertTpl, map[string]any{"Rows": rows, "Base": base, "Title": subject})}
}

// DigestMessage lists certificates that expire soon.
func DigestMessage(items []CertItem, days int, base string) Message {
	soonest := items[0]
	for _, c := range items {
		if c.DaysLeft < soonest.DaysLeft {
			soonest = c
		}
	}
	subject := fmt.Sprintf("[MonitorIQ] %d certificate(s) expire within %d days", len(items), days)
	if len(items) == 1 {
		subject = fmt.Sprintf("[MonitorIQ] Certificate for %s %s", soonest.Name, leftText(soonest.DaysLeft))
	}
	var t strings.Builder
	fmt.Fprintf(&t, "These certificates expire within %d days:\n\n", days)
	for _, c := range items {
		fmt.Fprintf(&t, "%s (%s)\n  %s, on %s", c.Name, c.Target, leftText(c.DaysLeft), c.NotAfter.Local().Format("2006-01-02"))
		if c.Issuer != "" {
			fmt.Fprintf(&t, ", issued by %s", c.Issuer)
		}
		t.WriteString("\n")
		if c.URL != "" {
			fmt.Fprintf(&t, "  %s\n", c.URL)
		}
		t.WriteString("\n")
	}
	t.WriteString("Renew them before they expire to avoid browser and client errors.\n")
	t.WriteString(footerText)
	type row struct {
		CertItem
		Left, Date, Color string
	}
	rows := make([]row, len(items))
	for i, c := range items {
		col := "#c07a12"
		if c.DaysLeft < 7 {
			col = "#d63c33"
		}
		rows[i] = row{CertItem: c, Left: leftText(c.DaysLeft), Date: c.NotAfter.Local().Format("2006-01-02"), Color: col}
	}
	return Message{Subject: subject, Text: t.String(), HTML: render(digestTpl, map[string]any{"Rows": rows, "Days": days, "Base": base, "Title": subject})}
}

func leftText(d int) string {
	switch {
	case d < 0:
		return fmt.Sprintf("expired %d days ago", -d)
	case d == 0:
		return "expires today"
	case d == 1:
		return "expires tomorrow"
	}
	return fmt.Sprintf("expires in %d days", d)
}

// TestMessage confirms that the SMTP settings work.
func TestMessage(st Settings, by, base string) Message {
	subject := "[MonitorIQ] Test message"
	sec := map[string]string{SecurityStartTLS: "STARTTLS", SecurityTLS: "TLS", SecurityNone: "no encryption"}[st.Security]
	server := fmt.Sprintf("%s:%d (%s)", st.Host, st.Port, sec)
	text := fmt.Sprintf("E-mail from MonitorIQ works.\n\nServer: %s\nSent by: %s\nTime: %s\n\nAlerts will be sent from %s.\n",
		server, by, time.Now().Format(timeFmt), st.FromAddress)
	if base != "" {
		text += "Dashboard: " + base + "/\n"
	}
	text += footerText
	return Message{Subject: subject, Text: text, HTML: render(testTpl, map[string]any{
		"Server": server, "By": by, "When": time.Now().Format(timeFmt), "From": st.FromAddress, "Base": base, "Title": subject})}
}

const footerText = "\n--\nMonitorIQ by Brainslad · Know before your users do.\nhttps://github.com/brainslad/MonitorIQ\n"

func render(t *template.Template, data any) string {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "page", data); err != nil {
		return ""
	}
	return b.String()
}

const layout = `{{define "page"}}<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>{{.Title}}</title></head>
<body style="margin:0;padding:0;background:#f6f3ef;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#13232a">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f6f3ef;padding:24px 12px"><tr><td align="center">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px">
<tr><td style="padding:0 4px 14px;font-size:18px;font-weight:700;letter-spacing:-.3px">Monitor<span style="color:#1b6a80">IQ</span></td></tr>
<tr><td style="background:#ffffff;border:1px solid #ece6e0;border-radius:16px;padding:22px 24px">{{template "body" .}}</td></tr>
<tr><td style="padding:14px 4px;font-size:12px;color:#67737a">
{{if .Base}}<a href="{{.Base}}/" style="color:#1b6a80">Open the dashboard</a> · {{end}}<a href="https://github.com/brainslad/MonitorIQ" style="color:#67737a">MonitorIQ</a> by <a href="https://github.com/brainslad" style="color:#67737a">Brainslad</a> · Know before your users do.
</td></tr></table></td></tr></table></body></html>{{end}}`

var alertTpl = template.Must(template.New("").Parse(layout + `{{define "body"}}
{{range $i, $r := .Rows}}{{if $i}}<hr style="border:0;border-top:1px solid #ece6e0;margin:18px 0">{{end}}
<div style="font-size:12px;font-weight:700;letter-spacing:.08em;color:{{$r.Color}}">● {{if and (eq $r.To 1) (eq $r.From 3)}}RECOVERED{{else if eq $r.To 1}}OPERATIONAL{{else if eq $r.To 2}}DEGRADED{{else}}DOWN{{end}}</div>
<div style="font-size:20px;font-weight:700;margin:4px 0 10px">{{$r.Name}} <span style="font-weight:500;color:#67737a">{{$r.Headline}}</span></div>
<table role="presentation" cellpadding="0" cellspacing="0" style="font-size:14px;line-height:1.6">
<tr><td style="color:#67737a;padding-right:14px">Host</td><td>{{$r.Host}}</td></tr>
<tr><td style="color:#67737a;padding-right:14px">Target</td><td style="font-family:Menlo,Consolas,monospace;font-size:13px">{{$r.Target}} <span style="color:#67737a">({{$r.Type}})</span></td></tr>
<tr><td style="color:#67737a;padding-right:14px">Since</td><td>{{$r.When}}</td></tr>
{{if $r.Lasted}}<tr><td style="color:#67737a;padding-right:14px">Was {{$r.FromWord}}</td><td>for {{$r.Lasted}}</td></tr>{{end}}
{{if $r.Message}}<tr><td style="color:#67737a;padding-right:14px;vertical-align:top">Result</td><td>{{$r.Message}}</td></tr>{{end}}
</table>
{{if $r.URL}}<p style="margin:14px 0 0"><a href="{{$r.URL}}" style="display:inline-block;background:#1b6a80;color:#fff;text-decoration:none;font-weight:600;font-size:14px;padding:9px 16px;border-radius:10px">View monitor</a></p>{{end}}
{{end}}{{end}}`))

var digestTpl = template.Must(template.New("").Parse(layout + `{{define "body"}}
<div style="font-size:12px;font-weight:700;letter-spacing:.08em;color:#c07a12">● CERTIFICATE RENEWAL</div>
<div style="font-size:20px;font-weight:700;margin:4px 0 6px">{{len .Rows}} certificate{{if gt (len .Rows) 1}}s{{end}} expire within {{.Days}} days</div>
<p style="margin:0 0 14px;color:#67737a;font-size:14px">Renew them before they expire to avoid browser and client errors.</p>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="font-size:14px">
{{range .Rows}}<tr><td style="padding:10px 0;border-top:1px solid #ece6e0">{{if .URL}}<a href="{{.URL}}" style="color:#13232a;font-weight:600;text-decoration:none">{{.Name}}</a>{{else}}<strong>{{.Name}}</strong>{{end}}<br><span style="color:#67737a;font-size:13px">{{.Target}}{{if .Issuer}} · {{.Issuer}}{{end}}</span></td>
<td align="right" style="padding:10px 0;border-top:1px solid #ece6e0;white-space:nowrap"><strong style="color:{{.Color}}">{{.Left}}</strong><br><span style="color:#67737a;font-size:13px">{{.Date}}</span></td></tr>{{end}}
</table>{{end}}`))

var testTpl = template.Must(template.New("").Parse(layout + `{{define "body"}}
<div style="font-size:12px;font-weight:700;letter-spacing:.08em;color:#1f8f74">● TEST MESSAGE</div>
<div style="font-size:20px;font-weight:700;margin:4px 0 10px">E-mail from MonitorIQ works</div>
<table role="presentation" cellpadding="0" cellspacing="0" style="font-size:14px;line-height:1.6">
<tr><td style="color:#67737a;padding-right:14px">Server</td><td>{{.Server}}</td></tr>
<tr><td style="color:#67737a;padding-right:14px">Sent by</td><td>{{.By}}</td></tr>
<tr><td style="color:#67737a;padding-right:14px">Time</td><td>{{.When}}</td></tr>
</table>
<p style="margin:14px 0 0;font-size:14px">Alerts will be sent from <strong>{{.From}}</strong>.</p>{{end}}`))
