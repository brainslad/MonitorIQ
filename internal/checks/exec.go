package checks

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Binaries used by the ping and traceroute checks. They come from the
// iputils and traceroute packages on Rocky Linux.
var (
	PingBinary       = "ping"
	TracerouteBinary = "traceroute"
)

var validHost = regexp.MustCompile(`^[A-Za-z0-9._:\-]+$`)

var (
	rePingStats = regexp.MustCompile(`(\d+) packets transmitted, (\d+) (?:packets )?received`)
	rePingRtt   = regexp.MustCompile(`= ([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+) ms`)
)

// PingStats is the parsed summary of a ping run.
type PingStats struct {
	Sent, Received        int
	Min, Avg, Max, Jitter float64
}

// ParsePing parses the summary output of iputils ping.
func ParsePing(out string) (PingStats, bool) {
	var s PingStats
	m := rePingStats.FindStringSubmatch(out)
	if m == nil {
		return s, false
	}
	s.Sent, _ = strconv.Atoi(m[1])
	s.Received, _ = strconv.Atoi(m[2])
	if r := rePingRtt.FindStringSubmatch(out); r != nil {
		s.Min, _ = strconv.ParseFloat(r[1], 64)
		s.Avg, _ = strconv.ParseFloat(r[2], 64)
		s.Max, _ = strconv.ParseFloat(r[3], 64)
		s.Jitter, _ = strconv.ParseFloat(r[4], 64)
	}
	return s, true
}

func checkPing(ctx context.Context, m Monitor) Result {
	host := bareHost(m.Target)
	if !validHost.MatchString(host) || strings.HasPrefix(host, "-") {
		return down("endereço inválido: %q", host)
	}
	count := m.Config.PingCount
	if count <= 0 {
		count = 3
	}
	if count > 20 {
		count = 20
	}
	wait := int(m.Timeout.Seconds())
	if wait < 1 {
		wait = 1
	}
	ctx, cancel := context.WithTimeout(ctx, m.Timeout+time.Duration(count)*time.Second+2*time.Second)
	defer cancel()
	args := []string{"-n", "-q", "-c", strconv.Itoa(count), "-i", "0.5", "-W", strconv.Itoa(wait)}
	if strings.Contains(host, ":") {
		args = append([]string{"-6"}, args...)
	}
	args = append(args, host)
	out, err := exec.CommandContext(ctx, PingBinary, args...).CombinedOutput()
	st, ok := ParsePing(string(out))
	if !ok {
		msg := strings.TrimSpace(string(out))
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return down("ping falhou: %s", truncate(msg, 200))
	}
	loss := 100.0
	if st.Sent > 0 {
		loss = float64(st.Sent-st.Received) * 100 / float64(st.Sent)
	}
	details := map[string]any{"sent": st.Sent, "received": st.Received, "loss_pct": loss,
		"rtt_min": st.Min, "rtt_avg": st.Avg, "rtt_max": st.Max, "rtt_mdev": st.Jitter}
	switch {
	case st.Received == 0:
		return Result{Status: Down, Message: fmt.Sprintf("Sem resposta (%d pacotes perdidos)", st.Sent), Details: details}
	case st.Received < st.Sent:
		return Result{Status: Degraded, LatencyMs: st.Avg, Message: fmt.Sprintf("%.0f%% de perda, média %.1f ms", loss, st.Avg), Details: details}
	default:
		return Result{Status: Up, LatencyMs: st.Avg, Message: fmt.Sprintf("0%% de perda, média %.1f ms", st.Avg), Details: details}
	}
}

// Hop is one traceroute hop.
type Hop struct {
	TTL     int       `json:"ttl"`
	Address string    `json:"address"`
	Name    string    `json:"name,omitempty"`
	RTTs    []float64 `json:"rtts"`
}

var (
	reHopLine = regexp.MustCompile(`^\s*(\d+)\s+(.*)$`)
	reRtt     = regexp.MustCompile(`([\d.]+) ms`)
	reAddr    = regexp.MustCompile(`^([^\s()]+)(?:\s+\(([^)]+)\))?`)
)

// ParseTraceroute parses `traceroute -n`-style output (names optional).
func ParseTraceroute(out string) []Hop {
	var hops []Hop
	for _, line := range strings.Split(out, "\n") {
		mm := reHopLine.FindStringSubmatch(line)
		if mm == nil {
			continue
		}
		ttl, _ := strconv.Atoi(mm[1])
		rest := strings.TrimSpace(mm[2])
		h := Hop{TTL: ttl}
		if !strings.HasPrefix(rest, "*") {
			if a := reAddr.FindStringSubmatch(rest); a != nil {
				if a[2] != "" {
					h.Name, h.Address = a[1], a[2]
				} else {
					h.Address = a[1]
				}
			}
		}
		for _, r := range reRtt.FindAllStringSubmatch(rest, -1) {
			v, _ := strconv.ParseFloat(r[1], 64)
			h.RTTs = append(h.RTTs, v)
		}
		if h.RTTs == nil {
			h.RTTs = []float64{}
		}
		hops = append(hops, h)
	}
	return hops
}

func checkTraceroute(ctx context.Context, m Monitor) Result {
	host := bareHost(m.Target)
	if !validHost.MatchString(host) || strings.HasPrefix(host, "-") {
		return down("endereço inválido: %q", host)
	}
	maxHops := m.Config.MaxHops
	if maxHops <= 0 || maxHops > 64 {
		maxHops = 30
	}
	wait := int(m.Timeout.Seconds())
	if wait < 1 {
		wait = 1
	}
	if wait > 5 {
		wait = 5
	}
	args := []string{"-n", "-q", "1", "-w", strconv.Itoa(wait), "-m", strconv.Itoa(maxHops)}
	if m.Config.TraceICMP {
		args = append(args, "-I")
	}
	if strings.Contains(host, ":") {
		args = append(args, "-6")
	}
	args = append(args, host)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(maxHops*wait+10)*time.Second)
	defer cancel()
	start := time.Now()
	out, err := exec.CommandContext(ctx, TracerouteBinary, args...).CombinedOutput()
	elapsed := time.Since(start)
	hops := ParseTraceroute(string(out))
	if len(hops) == 0 {
		msg := strings.TrimSpace(string(out))
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return down("traceroute falhou: %s", truncate(msg, 200))
	}
	details := map[string]any{"hops": hops, "duration_ms": ms(elapsed)}
	last := hops[len(hops)-1]
	// Destination address as printed in the header line: "traceroute to x (1.2.3.4)".
	dest := ""
	if mm := regexp.MustCompile(`traceroute to \S+ \(([^)]+)\)`).FindStringSubmatch(string(out)); mm != nil {
		dest = mm[1]
	}
	reached := last.Address != "" && (dest == "" || last.Address == dest)
	if !reached {
		return Result{Status: Down, Message: fmt.Sprintf("Destino não alcançado após %d saltos", len(hops)), Details: details}
	}
	lat := 0.0
	if len(last.RTTs) > 0 {
		lat = last.RTTs[0]
	}
	return Result{Status: Up, LatencyMs: lat, Message: fmt.Sprintf("Destino alcançado em %d saltos (%.1f ms)", len(hops), lat), Details: details}
}
