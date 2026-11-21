// Package checks implements the individual service probes supported by
// MonitorIQ: HTTP(S), ping, traceroute, DNS, SMTP, TLS certificate expiry and
// generic TCP ports.
package checks

import (
	"context"
	"fmt"
	"time"
)

// Status is the outcome of a check.
type Status int16

const (
	Unknown  Status = 0
	Up       Status = 1
	Degraded Status = 2
	Down     Status = 3
)

func (s Status) String() string {
	switch s {
	case Up:
		return "up"
	case Degraded:
		return "degraded"
	case Down:
		return "down"
	default:
		return "unknown"
	}
}

// Label returns a human label for the status.
func (s Status) Label() string {
	switch s {
	case Up:
		return "Operational"
	case Degraded:
		return "Degraded"
	case Down:
		return "Down"
	default:
		return "Unknown"
	}
}

// Monitor types.
const (
	TypeHTTP       = "http"
	TypePing       = "ping"
	TypeTraceroute = "traceroute"
	TypeDNS        = "dns"
	TypeSMTP       = "smtp"
	TypeTLS        = "tls"
	TypeTCP        = "tcp"
)

// TypeInfo describes a monitor type for the UI.
type TypeInfo struct {
	Key         string
	Label       string
	DefaultPort int
	TargetHint  string
}

// Types lists all supported monitor types in display order.
var Types = []TypeInfo{
	{TypeHTTP, "Website HTTP/HTTPS", 0, "https://example.com/"},
	{TypePing, "Ping (ICMP)", 0, "example.com or 192.0.2.10"},
	{TypeTraceroute, "Traceroute", 0, "example.com"},
	{TypeDNS, "DNS", 53, "name to resolve, e.g. example.com"},
	{TypeSMTP, "SMTP", 25, "mail.example.com"},
	{TypeTLS, "TLS certificate expiry", 443, "example.com"},
	{TypeTCP, "Custom TCP port", 0, "example.com"},
}

// TypeLabel returns the display label for a type key.
func TypeLabel(key string) string {
	for _, t := range Types {
		if t.Key == key {
			return t.Label
		}
	}
	return key
}

// ValidType reports whether key is a supported monitor type.
func ValidType(key string) bool {
	for _, t := range Types {
		if t.Key == key {
			return true
		}
	}
	return false
}

// Config holds type-specific options. It is stored as JSON in the database.
type Config struct {
	// HTTP
	Method            string `json:"method,omitempty"`
	ExpectedStatus    string `json:"expected_status,omitempty"` // e.g. "200-399" or "200,204"
	Keyword           string `json:"keyword,omitempty"`
	KeywordAbsent     bool   `json:"keyword_absent,omitempty"`
	IgnoreTLSErrors   bool   `json:"ignore_tls_errors,omitempty"`
	NoFollowRedirects bool   `json:"no_follow_redirects,omitempty"`
	DegradedMs        int    `json:"degraded_ms,omitempty"`

	// Ping
	PingCount int `json:"ping_count,omitempty"`

	// Traceroute
	MaxHops   int  `json:"max_hops,omitempty"`
	TraceICMP bool `json:"trace_icmp,omitempty"`

	// DNS
	RecordType string `json:"record_type,omitempty"`
	DNSServer  string `json:"dns_server,omitempty"`
	Expected   string `json:"expected,omitempty"`

	// SMTP: "none", "starttls" or "implicit"
	SMTPTLS string `json:"smtp_tls,omitempty"`

	// TLS certificate
	WarnDays int `json:"warn_days,omitempty"`
}

// Monitor is the minimal information a check needs.
type Monitor struct {
	Type    string
	Target  string
	Port    int
	Timeout time.Duration
	Config  Config
}

// Result is the outcome of running a check.
type Result struct {
	Status    Status         `json:"status"`
	LatencyMs float64        `json:"latency_ms"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
}

func down(format string, a ...any) Result {
	return Result{Status: Down, Message: fmt.Sprintf(format, a...)}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

// Run executes the check described by m.
func Run(ctx context.Context, m Monitor) (res Result) {
	if m.Timeout <= 0 {
		m.Timeout = 10 * time.Second
	}
	defer func() {
		if r := recover(); r != nil {
			res = down("internal error: %v", r)
		}
	}()
	switch m.Type {
	case TypeHTTP:
		return checkHTTP(ctx, m)
	case TypePing:
		return checkPing(ctx, m)
	case TypeTraceroute:
		return checkTraceroute(ctx, m)
	case TypeDNS:
		return checkDNS(ctx, m)
	case TypeSMTP:
		return checkSMTP(ctx, m)
	case TypeTLS:
		return checkTLS(ctx, m)
	case TypeTCP:
		return checkTCP(ctx, m)
	default:
		return down("unknown monitor type: %s", m.Type)
	}
}
