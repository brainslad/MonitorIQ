// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"
)

// Message is an e-mail with a plain-text and an HTML body.
type Message struct {
	To      []string
	Subject string
	Text    string
	HTML    string
}

const sendTimeout = 60 * time.Second

// Send delivers a message through the configured SMTP server.
func Send(ctx context.Context, st Settings, msg Message) error {
	if st.Host == "" || st.FromAddress == "" {
		return errors.New("e-mail is not configured: enter the SMTP server and the sender address")
	}
	if len(msg.To) == 0 {
		return errors.New("no recipients")
	}
	raw, err := buildMessage(st, msg, time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	addr := net.JoinHostPort(st.Host, strconv.Itoa(st.Port))
	tlsCfg := &tls.Config{ServerName: st.Host, InsecureSkipVerify: st.InsecureSkipVerify, MinVersion: tls.VersionTLS12} //nolint:gosec // opt-in for test labs
	d := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot connect to %s: %w", addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if st.Security == SecurityTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS handshake with %s failed: %w", addr, err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, st.Host)
	if err != nil {
		return fmt.Errorf("SMTP greeting from %s: %w", addr, err)
	}
	defer c.Close()
	if err := c.Hello(heloName()); err != nil {
		return fmt.Errorf("EHLO: %w", err)
	}
	if st.Security == SecurityStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS: choose TLS (port 465) or no encryption (local relays only)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	if st.Username != "" {
		ok, mechs := c.Extension("AUTH")
		if !ok {
			return errors.New("the server does not accept authentication: clear the username or use another port")
		}
		var a smtp.Auth
		switch {
		case strings.Contains(strings.ToUpper(mechs), "PLAIN"):
			a = smtp.PlainAuth("", st.Username, st.Password, st.Host)
		case strings.Contains(strings.ToUpper(mechs), "LOGIN"):
			a = &loginAuth{user: st.Username, pass: st.Password, host: st.Host}
		default:
			return fmt.Errorf("no supported authentication method (server offers %s)", mechs)
		}
		if err := c.Auth(a); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
	}
	if err := c.Mail(st.FromAddress); err != nil {
		return fmt.Errorf("sender %s rejected: %w", st.FromAddress, err)
	}
	for _, to := range msg.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("recipient %s rejected: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("sending the message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the server did not accept the message: %w", err)
	}
	_ = c.Quit()
	return nil
}

func heloName() string {
	h, err := os.Hostname()
	if err != nil || h == "" || strings.ContainsAny(h, " \t\r\n") {
		return "localhost"
	}
	return h
}

// loginAuth implements the LOGIN mechanism (used by some Exchange servers),
// only over TLS or to localhost, like smtp.PlainAuth.
type loginAuth struct{ user, pass, host string }

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !isLocal(server.Name) {
		return "", nil, errors.New("unencrypted connection")
	}
	if server.Name != a.host {
		return "", nil, errors.New("wrong host name")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:", "user name", "username":
		return []byte(a.user), nil
	case "password:", "password":
		return []byte(a.pass), nil
	}
	return nil, fmt.Errorf("unexpected server challenge %q", fromServer)
}

func buildMessage(st Settings, msg Message, now time.Time) ([]byte, error) {
	for _, s := range append([]string{msg.Subject, st.FromName}, msg.To...) {
		if strings.ContainsAny(s, "\r\n") {
			return nil, errors.New("invalid header value")
		}
	}
	from := (&mail.Address{Name: st.FromName, Address: st.FromAddress}).String()
	domain := "monitoriq.local"
	if i := strings.LastIndex(st.FromAddress, "@"); i >= 0 {
		domain = st.FromAddress[i+1:]
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)

	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from)
	h("To", strings.Join(msg.To, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	h("Date", now.Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")
	h("X-Auto-Response-Suppress", "All")
	h("X-Mailer", "MonitorIQ")

	mw := multipart.NewWriter(&b)
	h("Content-Type", `multipart/alternative; boundary="`+mw.Boundary()+`"`)
	b.WriteString("\r\n")
	part := func(ctype, body string) error {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {ctype + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return err
		}
		qw := quotedprintable.NewWriter(w)
		if _, err := io.WriteString(qw, strings.ReplaceAll(body, "\n", "\r\n")); err != nil {
			return err
		}
		return qw.Close()
	}
	if err := part("text/plain", msg.Text); err != nil {
		return nil, err
	}
	if msg.HTML != "" {
		if err := part("text/html", msg.HTML); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
