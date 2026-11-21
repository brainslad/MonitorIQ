package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

func hostPort(target string, port, defPort int) string {
	target = strings.TrimSpace(target)
	// Strip a scheme and path if the user pasted a URL.
	if i := strings.Index(target, "://"); i >= 0 {
		target = target[i+3:]
	}
	if i := strings.IndexAny(target, "/?#"); i >= 0 {
		target = target[:i]
	}
	if h, p, err := net.SplitHostPort(target); err == nil {
		if port == 0 {
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}
		}
		target = h
	}
	if port == 0 {
		port = defPort
	}
	target = strings.Trim(target, "[]")
	return net.JoinHostPort(target, strconv.Itoa(port))
}

// bareHost returns only the hostname part of a target.
func bareHost(target string) string {
	h, _, _ := net.SplitHostPort(hostPort(target, 0, 1))
	return h
}

func checkTCP(ctx context.Context, m Monitor) Result {
	if m.Port <= 0 {
		if _, p, err := net.SplitHostPort(strings.TrimSpace(m.Target)); err != nil || p == "" {
			return down("no port defined")
		}
	}
	addr := hostPort(m.Target, m.Port, 0)
	d := net.Dialer{Timeout: m.Timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	lat := time.Since(start)
	if err != nil {
		return Result{Status: Down, LatencyMs: ms(lat), Message: "Connection to " + addr + " failed: " + cleanErr(err)}
	}
	_ = conn.Close()
	return Result{Status: Up, LatencyMs: ms(lat), Message: fmt.Sprintf("Port %s open (%.0f ms)", addr, ms(lat))}
}

func checkTLS(ctx context.Context, m Monitor) Result {
	addr := hostPort(m.Target, m.Port, 443)
	host := bareHost(m.Target)
	warn := m.Config.WarnDays
	if warn <= 0 {
		warn = 14
	}
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: m.Timeout},
		Config:    &tls.Config{ServerName: host, InsecureSkipVerify: true}, //nolint:gosec // verified manually below
	}
	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", addr)
	lat := time.Since(start)
	if err != nil {
		return Result{Status: Down, LatencyMs: ms(lat), Message: "TLS handshake failed: " + cleanErr(err)}
	}
	defer c.Close()
	state := c.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return down("server presented no certificate")
	}
	return evaluateCert(state.PeerCertificates, host, warn, lat, time.Now())
}

// evaluateCert verifies the chain and the expiry of the leaf certificate.
func evaluateCert(chain []*x509.Certificate, host string, warnDays int, lat time.Duration, now time.Time) Result {
	leaf := chain[0]
	daysLeft := int(leaf.NotAfter.Sub(now).Hours() / 24)
	details := map[string]any{
		"subject":    leaf.Subject.CommonName,
		"issuer":     leaf.Issuer.CommonName,
		"not_before": leaf.NotBefore.UTC().Format(time.RFC3339),
		"not_after":  leaf.NotAfter.UTC().Format(time.RFC3339),
		"days_left":  daysLeft,
		"dns_names":  leaf.DNSNames,
	}
	res := Result{Status: Up, LatencyMs: ms(lat), Details: details}

	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter, CurrentTime: now})

	exp := leaf.NotAfter.Local().Format("2006-01-02")
	switch {
	case now.After(leaf.NotAfter):
		res.Status = Down
		res.Message = fmt.Sprintf("Certificate expired on %s", exp)
	case verr != nil:
		res.Status = Down
		res.Message = "Invalid certificate: " + verr.Error()
	case daysLeft < warnDays:
		res.Status = Degraded
		res.Message = fmt.Sprintf("Certificate expires in %d days (%s)", daysLeft, exp)
	default:
		res.Message = fmt.Sprintf("Certificate valid, expires in %d days (%s)", daysLeft, exp)
	}
	return res
}

func checkSMTP(ctx context.Context, m Monitor) Result {
	mode := m.Config.SMTPTLS
	defPort := 25
	if mode == "implicit" {
		defPort = 465
	}
	addr := hostPort(m.Target, m.Port, defPort)
	host := bareHost(m.Target)
	deadline := time.Now().Add(m.Timeout)
	start := time.Now()

	var conn net.Conn
	var err error
	nd := &net.Dialer{Timeout: m.Timeout, Deadline: deadline}
	if mode == "implicit" {
		conn, err = (&tls.Dialer{NetDialer: nd, Config: &tls.Config{ServerName: host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = nd.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return Result{Status: Down, LatencyMs: ms(time.Since(start)), Message: "SMTP connection failed: " + cleanErr(err)}
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return Result{Status: Down, LatencyMs: ms(time.Since(start)), Message: "Invalid SMTP banner: " + cleanErr(err)}
	}
	defer c.Close()
	helo, _ := os.Hostname()
	if helo == "" {
		helo = "monitoriq.local"
	}
	if err := c.Hello(helo); err != nil {
		return Result{Status: Down, LatencyMs: ms(time.Since(start)), Message: "EHLO failed: " + cleanErr(err)}
	}
	details := map[string]any{"address": addr, "tls": mode}
	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return Result{Status: Down, LatencyMs: ms(time.Since(start)), Message: "Server does not advertise STARTTLS", Details: details}
		}
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return Result{Status: Down, LatencyMs: ms(time.Since(start)), Message: "STARTTLS failed: " + cleanErr(err), Details: details}
		}
	}
	if st, ok := c.TLSConnectionState(); ok && len(st.PeerCertificates) > 0 {
		details["cert_days_left"] = int(time.Until(st.PeerCertificates[0].NotAfter).Hours() / 24)
	}
	_ = c.Quit()
	lat := time.Since(start)
	return Result{Status: Up, LatencyMs: ms(lat), Message: fmt.Sprintf("SMTP answered in %.0f ms", ms(lat)), Details: details}
}

func checkDNS(ctx context.Context, m Monitor) Result {
	name := strings.TrimSuffix(bareHost(m.Target), ".")
	rtype := strings.ToUpper(m.Config.RecordType)
	if rtype == "" {
		rtype = "A"
	}
	r := net.DefaultResolver
	server := strings.TrimSpace(m.Config.DNSServer)
	if server != "" {
		port := m.Port
		if port == 0 {
			port = 53
		}
		saddr := hostPort(server, port, 53)
		r = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: m.Timeout}
				return d.DialContext(ctx, network, saddr)
			},
		}
	}
	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()

	start := time.Now()
	var answers []string
	var err error
	switch rtype {
	case "A", "AAAA":
		var ips []net.IP
		network := "ip4"
		if rtype == "AAAA" {
			network = "ip6"
		}
		ips, err = r.LookupIP(ctx, network, name)
		for _, ip := range ips {
			answers = append(answers, ip.String())
		}
	case "CNAME":
		var c string
		c, err = r.LookupCNAME(ctx, name)
		if c != "" {
			answers = append(answers, strings.TrimSuffix(c, "."))
		}
	case "MX":
		var mx []*net.MX
		mx, err = r.LookupMX(ctx, name)
		for _, x := range mx {
			answers = append(answers, fmt.Sprintf("%d %s", x.Pref, strings.TrimSuffix(x.Host, ".")))
		}
	case "TXT":
		answers, err = r.LookupTXT(ctx, name)
	case "NS":
		var ns []*net.NS
		ns, err = r.LookupNS(ctx, name)
		for _, x := range ns {
			answers = append(answers, strings.TrimSuffix(x.Host, "."))
		}
	default:
		return down("unsupported DNS record type: %s", rtype)
	}
	lat := time.Since(start)
	sort.Strings(answers)
	details := map[string]any{"record_type": rtype, "answers": answers}
	if server != "" {
		details["server"] = server
	}
	if err != nil {
		return Result{Status: Down, LatencyMs: ms(lat), Message: fmt.Sprintf("%s lookup of %s failed: %s", rtype, name, cleanErr(err)), Details: details}
	}
	if len(answers) == 0 {
		return Result{Status: Down, LatencyMs: ms(lat), Message: fmt.Sprintf("No %s records for %s", rtype, name), Details: details}
	}
	if exp := strings.TrimSpace(m.Config.Expected); exp != "" {
		match := false
		for _, a := range answers {
			if strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(exp, ".")) || strings.Contains(strings.ToLower(a), strings.ToLower(exp)) {
				match = true
				break
			}
		}
		if !match {
			return Result{Status: Down, LatencyMs: ms(lat), Message: fmt.Sprintf("%s answer does not contain %q: %s", rtype, exp, strings.Join(answers, ", ")), Details: details}
		}
	}
	return Result{Status: Up, LatencyMs: ms(lat), Message: fmt.Sprintf("%s %s → %s", rtype, name, truncate(strings.Join(answers, ", "), 200)), Details: details}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func cleanErr(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "name not found (" + dnsErr.Name + ")"
		}
		if dnsErr.IsTimeout {
			return "DNS timeout"
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return err.Error()
}
