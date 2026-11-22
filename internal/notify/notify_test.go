// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/store"
)

// fakeSMTP is a minimal SMTP server for tests.
type fakeSMTP struct {
	ln       net.Listener
	tlsCfg   *tls.Config
	implicit bool
	got      chan received
}

type received struct {
	from, auth string
	to         []string
	data       string
	tls        bool
}

func selfSigned(t *testing.T) tls.Certificate {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
}

func startFake(t *testing.T, withTLS, implicit bool) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, got: make(chan received, 4), implicit: implicit}
	if withTLS {
		f.tlsCfg = &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}}
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	var r received
	if f.implicit {
		tc := tls.Server(c, f.tlsCfg)
		if tc.Handshake() != nil {
			return
		}
		c, r.tls = tc, true
	}
	br := bufio.NewReader(c)
	w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			w("250-fake")
			if f.tlsCfg != nil && !r.tls {
				w("250-STARTTLS")
			}
			w("250 AUTH PLAIN LOGIN")
		case cmd == "STARTTLS":
			w("220 go ahead")
			tc := tls.Server(c, f.tlsCfg)
			if tc.Handshake() != nil {
				return
			}
			c, r.tls = tc, true
			br = bufio.NewReader(c)
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			b, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			r.auth = strings.ReplaceAll(string(b), "\x00", "|")
			w("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			r.from = strings.Trim(line[10:], "<> ")
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			r.to = append(r.to, strings.Trim(line[8:], "<> "))
			w("250 ok")
		case cmd == "DATA":
			w("354 send")
			var b strings.Builder
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			r.data = b.String()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			f.got <- r
			return
		default:
			w("502 unknown")
		}
	}
}

func (f *fakeSMTP) wait(t *testing.T) received {
	select {
	case r := <-f.got:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no message received")
	}
	return received{}
}

func baseSettings(port int, sec string) Settings {
	st := DefaultSettings()
	st.Enabled, st.Host, st.Port, st.Security = true, "127.0.0.1", port, sec
	st.FromAddress, st.InsecureSkipVerify = "monitoriq@example.com", true
	return st
}

func TestSendStartTLSWithAuth(t *testing.T) {
	f := startFake(t, true, false)
	st := baseSettings(f.port(), SecurityStartTLS)
	st.Username, st.Password = "user", "secret"
	msg := Message{To: []string{"ops@example.com", "dev@example.com"}, Subject: "Olá: test ✓", Text: "line 1\nline 2", HTML: "<p>hi</p>"}
	if err := Send(context.Background(), st, msg); err != nil {
		t.Fatal(err)
	}
	r := f.wait(t)
	if !r.tls || r.auth != "|user|secret" || r.from != "monitoriq@example.com" || len(r.to) != 2 {
		t.Fatalf("unexpected session: %+v", r)
	}
	m, err := mail.ReadMessage(strings.NewReader(r.data))
	if err != nil {
		t.Fatal(err)
	}
	subj, _ := new(mimeDecoder).decode(m.Header.Get("Subject"))
	if subj != "Olá: test ✓" || m.Header.Get("Auto-Submitted") != "auto-generated" ||
		!strings.HasPrefix(m.Header.Get("Content-Type"), "multipart/alternative") {
		t.Fatalf("bad headers: %v", m.Header)
	}
}

func TestSendImplicitTLS(t *testing.T) {
	f := startFake(t, true, true)
	if err := Send(context.Background(), baseSettings(f.port(), SecurityTLS), Message{To: []string{"a@example.com"}, Subject: "x", Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if r := f.wait(t); !r.tls {
		t.Fatal("expected TLS")
	}
}

func TestStartTLSRequired(t *testing.T) {
	f := startFake(t, false, false)
	err := Send(context.Background(), baseSettings(f.port(), SecurityStartTLS), Message{To: []string{"a@example.com"}, Subject: "x", Text: "y"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("expected STARTTLS error, got %v", err)
	}
}

func TestPlainLocalRelay(t *testing.T) {
	f := startFake(t, false, false)
	if err := Send(context.Background(), baseSettings(f.port(), SecurityNone), Message{To: []string{"a@example.com"}, Subject: "x", Text: "y"}); err != nil {
		t.Fatal(err)
	}
	f.wait(t)
}

func TestHeaderInjection(t *testing.T) {
	st := baseSettings(25, SecurityNone)
	if _, err := buildMessage(st, Message{To: []string{"a@example.com"}, Subject: "x\r\nBcc: evil@example.com"}, time.Now()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestValidate(t *testing.T) {
	st := DefaultSettings()
	st.Enabled = true
	if st.Validate() == nil {
		t.Fatal("host should be required")
	}
	st.Host, st.FromAddress = "smtp.example.com", "MonitorIQ <noc@example.com>"
	st.Recipients = "ops@example.com; ops@example.com,dev@example.com"
	if err := st.Validate(); err != nil {
		t.Fatal(err)
	}
	if st.FromAddress != "noc@example.com" || st.Recipients != "ops@example.com, dev@example.com" {
		t.Fatalf("not normalised: %q %q", st.FromAddress, st.Recipients)
	}
	st.Security, st.Username = SecurityNone, "u"
	if st.Validate() == nil {
		t.Fatal("password over plain text must be refused")
	}
}

func TestWanted(t *testing.T) {
	st := DefaultSettings() // down + recovery, no degraded
	cases := []struct {
		from, to checks.Status
		want     bool
	}{
		{checks.Up, checks.Down, true},
		{checks.Down, checks.Up, true},
		{checks.Up, checks.Degraded, false},
		{checks.Degraded, checks.Up, false},
		{checks.Down, checks.Degraded, true},
	}
	for _, c := range cases {
		if got := st.wanted(store.Transition{From: c.from, To: c.to}); got != c.want {
			t.Errorf("%v→%v: got %v", c.from, c.to, got)
		}
	}
}

func TestRecipientsAndMessages(t *testing.T) {
	got := recipients("ops@example.com", []string{"OPS@example.com", "me@example.com"}, "x@example.com")
	if strings.Join(got, ",") != "ops@example.com,me@example.com,x@example.com" {
		t.Fatalf("recipients: %v", got)
	}
	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	items := []Item{
		{MonitorID: 1, Name: "Shop", Host: "web01", Target: "https://shop", Type: "http", From: checks.Up, To: checks.Down, Message: "HTTP 503", At: at, URL: "https://miq/monitors/1"},
		{MonitorID: 2, Name: "API", Host: "web01", Target: "api:443", Type: "tcp", From: checks.Down, To: checks.Up, At: at, Lasted: "12 min"},
	}
	m := AlertMessage(items[:1], "https://miq")
	if m.Subject != "[MonitorIQ] DOWN: Shop (web01)" || !strings.Contains(m.HTML, "View monitor") || !strings.Contains(m.Text, "HTTP 503") {
		t.Fatalf("single alert: %q", m.Subject)
	}
	m = AlertMessage(items, "")
	if m.Subject != "[MonitorIQ] 2 status changes: 1 down, 1 recovered" || !strings.Contains(m.Text, "Was down for 12 min") {
		t.Fatalf("grouped alert: %q\n%s", m.Subject, m.Text)
	}
	d := DigestMessage([]CertItem{{Name: "Shop TLS", Target: "shop:443", DaysLeft: 5, NotAfter: at}}, 21, "")
	if !strings.Contains(d.Subject, "expires in 5 days") || d.HTML == "" {
		t.Fatalf("digest: %q", d.Subject)
	}
	if Duration(90*time.Minute) != "1 h 30 min" || Duration(50*time.Hour) != "2 d 2 h" {
		t.Fatal("duration format")
	}
}
