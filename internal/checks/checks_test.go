package checks

import (
	"bufio"
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStatusMatches(t *testing.T) {
	cases := []struct {
		spec string
		code int
		want bool
	}{
		{"", 200, true}, {"", 302, true}, {"", 404, false},
		{"200", 200, true}, {"200,204", 204, true}, {"200-299", 301, false},
		{"200-299, 401", 401, true}, {"abc", 200, false},
	}
	for _, c := range cases {
		if got := statusMatches(c.spec, c.code); got != c.want {
			t.Errorf("statusMatches(%q,%d)=%v want %v", c.spec, c.code, got, c.want)
		}
	}
}

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, "hello world")
		case "/slow":
			time.Sleep(60 * time.Millisecond)
			fmt.Fprint(w, "slow")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	run := func(path string, c Config) Result {
		return Run(ctx, Monitor{Type: TypeHTTP, Target: srv.URL + path, Timeout: 2 * time.Second, Config: c})
	}
	if r := run("/ok", Config{}); r.Status != Up {
		t.Fatalf("ok: %+v", r)
	}
	if r := run("/missing", Config{}); r.Status != Down {
		t.Fatalf("404 should be down: %+v", r)
	}
	if r := run("/missing", Config{ExpectedStatus: "404"}); r.Status != Up {
		t.Fatalf("404 expected should be up: %+v", r)
	}
	if r := run("/ok", Config{Keyword: "world"}); r.Status != Up {
		t.Fatalf("keyword: %+v", r)
	}
	if r := run("/ok", Config{Keyword: "nope"}); r.Status != Down {
		t.Fatalf("missing keyword: %+v", r)
	}
	if r := run("/ok", Config{Keyword: "hello", KeywordAbsent: true}); r.Status != Down {
		t.Fatalf("forbidden keyword: %+v", r)
	}
	if r := run("/slow", Config{DegradedMs: 10}); r.Status != Degraded {
		t.Fatalf("slow should be degraded: %+v", r)
	}
}

func TestHTTPSAndTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()
	// Self-signed: verification fails unless ignored.
	r := Run(context.Background(), Monitor{Type: TypeHTTP, Target: srv.URL, Timeout: 2 * time.Second})
	if r.Status != Down || !strings.Contains(r.Message, "certificate") {
		t.Fatalf("expected cert failure: %+v", r)
	}
	r = Run(context.Background(), Monitor{Type: TypeHTTP, Target: srv.URL, Timeout: 2 * time.Second, Config: Config{IgnoreTLSErrors: true}})
	if r.Status != Up || r.Details["cert_days_left"] == nil {
		t.Fatalf("expected up with cert details: %+v", r)
	}
	addr := strings.TrimPrefix(srv.URL, "https://")
	r = Run(context.Background(), Monitor{Type: TypeTLS, Target: addr, Timeout: 2 * time.Second})
	if r.Status != Down || r.Details["days_left"] == nil {
		t.Fatalf("self-signed TLS should be down with details: %+v", r)
	}
}

func TestEvaluateCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	leaf := srv.Certificate()
	now := leaf.NotAfter.Add(-5 * 24 * time.Hour)
	r := evaluateCert([]*x509.Certificate{leaf}, "example.com", 14, time.Millisecond, now)
	// The httptest certificate is not issued by a trusted CA, so it is invalid.
	if r.Status != Down {
		t.Fatalf("untrusted cert should be down: %+v", r)
	}
	r = evaluateCert([]*x509.Certificate{leaf}, "example.com", 14, time.Millisecond, leaf.NotAfter.Add(time.Hour))
	if r.Status != Down || !strings.Contains(r.Message, "expired") {
		t.Fatalf("expired: %+v", r)
	}
}

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	r := Run(context.Background(), Monitor{Type: TypeTCP, Target: "127.0.0.1", Port: port, Timeout: time.Second})
	if r.Status != Up {
		t.Fatalf("open port: %+v", r)
	}
	ln.Close()
	r = Run(context.Background(), Monitor{Type: TypeTCP, Target: "127.0.0.1", Port: port, Timeout: time.Second})
	if r.Status != Down {
		t.Fatalf("closed port: %+v", r)
	}
	if r := Run(context.Background(), Monitor{Type: TypeTCP, Target: "127.0.0.1", Timeout: time.Second}); r.Status != Down {
		t.Fatalf("missing port should be down")
	}
}

// fakeSMTP is a minimal SMTP server that accepts EHLO and QUIT.
func fakeSMTP(t *testing.T, starttls bool) (string, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
				w := func(s string) { rw.WriteString(s + "\r\n"); rw.Flush() }
				w("220 test ESMTP")
				for {
					line, err := rw.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"):
						if starttls {
							w("250-test\r\n250 STARTTLS")
						} else {
							w("250-test\r\n250 SIZE 1000")
						}
					case cmd == "QUIT":
						w("221 bye")
						return
					default:
						w("502 no")
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestSMTP(t *testing.T) {
	addr, stop := fakeSMTP(t, false)
	defer stop()
	r := Run(context.Background(), Monitor{Type: TypeSMTP, Target: addr, Timeout: 2 * time.Second, Config: Config{SMTPTLS: "none"}})
	if r.Status != Up {
		t.Fatalf("smtp plain: %+v", r)
	}
	r = Run(context.Background(), Monitor{Type: TypeSMTP, Target: addr, Timeout: 2 * time.Second, Config: Config{SMTPTLS: "starttls"}})
	if r.Status != Down || !strings.Contains(r.Message, "STARTTLS") {
		t.Fatalf("smtp missing starttls should be down: %+v", r)
	}
}

func TestParsePing(t *testing.T) {
	out := `PING 1.1.1.1 (1.1.1.1) 56(84) bytes of data.

--- 1.1.1.1 ping statistics ---
3 packets transmitted, 2 received, 33.3333% packet loss, time 1003ms
rtt min/avg/max/mdev = 10.123/11.456/12.789/1.088 ms
`
	s, ok := ParsePing(out)
	if !ok || s.Sent != 3 || s.Received != 2 || s.Avg != 11.456 || s.Max != 12.789 {
		t.Fatalf("parse: %+v %v", s, ok)
	}
	s, ok = ParsePing("--- x ping statistics ---\n3 packets transmitted, 0 received, +3 errors, 100% packet loss, time 2ms\n")
	if !ok || s.Received != 0 {
		t.Fatalf("parse loss: %+v", s)
	}
}

func TestParseTraceroute(t *testing.T) {
	out := `traceroute to 1.1.1.1 (1.1.1.1), 30 hops max, 60 byte packets
 1  192.168.1.1  0.512 ms
 2  *
 3  gw.isp.example (10.0.0.1)  5.1 ms
 4  1.1.1.1  10.204 ms
`
	hops := ParseTraceroute(out)
	if len(hops) != 4 {
		t.Fatalf("hops: %+v", hops)
	}
	if hops[0].Address != "192.168.1.1" || hops[0].RTTs[0] != 0.512 {
		t.Fatalf("hop1: %+v", hops[0])
	}
	if hops[1].Address != "" || len(hops[1].RTTs) != 0 {
		t.Fatalf("hop2: %+v", hops[1])
	}
	if hops[2].Address != "10.0.0.1" || hops[2].Name != "gw.isp.example" {
		t.Fatalf("hop3: %+v", hops[2])
	}
}

func TestHostPort(t *testing.T) {
	cases := map[string]string{
		"example.com":              "example.com:443",
		"https://example.com/path": "example.com:443",
		"example.com:8443":         "example.com:8443",
		"[2001:db8::1]:25":         "[2001:db8::1]:25",
		"2001:db8::1":              "[2001:db8::1]:443",
	}
	for in, want := range cases {
		if got := hostPort(in, 0, 443); got != want {
			t.Errorf("hostPort(%q)=%q want %q", in, got, want)
		}
	}
	if got := hostPort("example.com:8443", 9000, 443); got != "example.com:9000" {
		t.Errorf("explicit port should win: %s", got)
	}
}

func TestPingRejectsOptionInjection(t *testing.T) {
	r := Run(context.Background(), Monitor{Type: TypePing, Target: "-f", Timeout: time.Second})
	if r.Status != Down || !strings.Contains(r.Message, "invalid") {
		t.Fatalf("expected rejection: %+v", r)
	}
}
