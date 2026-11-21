package checks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// UserAgent is sent with every HTTP check.
var UserAgent = "MonitorIQ/1.0 (+https://github.com/brainslad/monitoriq)"

// statusMatches reports whether code matches a spec like "200-399" or "200,301,302".
func statusMatches(spec string, code int) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		spec = "200-399"
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			l, err1 := strconv.Atoi(strings.TrimSpace(lo))
			h, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 == nil && err2 == nil && code >= l && code <= h {
				return true
			}
			continue
		}
		if n, err := strconv.Atoi(part); err == nil && n == code {
			return true
		}
	}
	return false
}

func checkHTTP(ctx context.Context, m Monitor) Result {
	url := strings.TrimSpace(m.Target)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	method := strings.ToUpper(m.Config.Method)
	if method == "" {
		method = http.MethodGet
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: m.Timeout}).DialContext,
		TLSHandshakeTimeout:   m.Timeout,
		ResponseHeaderTimeout: m.Timeout,
		DisableKeepAlives:     true,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: m.Config.IgnoreTLSErrors}, //nolint:gosec // user opt-in
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: m.Timeout}
	if m.Config.NoFollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}

	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return down("invalid URL: %v", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return Result{Status: Down, LatencyMs: ms(time.Since(start)), Message: "Invalid TLS certificate: " + certErr.Err.Error()}
		}
		return Result{Status: Down, LatencyMs: ms(time.Since(start)), Message: "Request failed: " + err.Error()}
	}
	defer resp.Body.Close()

	needBody := m.Config.Keyword != ""
	var body []byte
	if needBody {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	}
	latency := time.Since(start)

	details := map[string]any{
		"status_code": resp.StatusCode,
		"final_url":   resp.Request.URL.String(),
		"proto":       resp.Proto,
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		leaf := resp.TLS.PeerCertificates[0]
		details["cert_expires_at"] = leaf.NotAfter.UTC().Format(time.RFC3339)
		details["cert_days_left"] = int(time.Until(leaf.NotAfter).Hours() / 24)
	}

	res := Result{Status: Up, LatencyMs: ms(latency), Details: details}
	if !statusMatches(m.Config.ExpectedStatus, resp.StatusCode) {
		res.Status = Down
		res.Message = fmt.Sprintf("HTTP %d (expected %s)", resp.StatusCode, orDefault(m.Config.ExpectedStatus, "200-399"))
		return res
	}
	if needBody {
		found := strings.Contains(string(body), m.Config.Keyword)
		if m.Config.KeywordAbsent && found {
			res.Status = Down
			res.Message = fmt.Sprintf("HTTP %d, forbidden keyword %q found", resp.StatusCode, m.Config.Keyword)
			return res
		}
		if !m.Config.KeywordAbsent && !found {
			res.Status = Down
			res.Message = fmt.Sprintf("HTTP %d, keyword %q not found", resp.StatusCode, m.Config.Keyword)
			return res
		}
	}
	res.Message = fmt.Sprintf("HTTP %d in %.0f ms", resp.StatusCode, res.LatencyMs)
	if m.Config.DegradedMs > 0 && res.LatencyMs > float64(m.Config.DegradedMs) {
		res.Status = Degraded
		res.Message += fmt.Sprintf(" (slow: threshold %d ms)", m.Config.DegradedMs)
	}
	return res
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
