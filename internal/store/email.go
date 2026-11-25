// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package store

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// NormalizeEmails validates a comma, semicolon or newline separated list of
// e-mail addresses and returns it as "a@x, b@y" without duplicates.
func NormalizeEmails(list string) (string, error) {
	addrs, err := ParseEmails(list)
	return strings.Join(addrs, ", "), err
}

// ParseEmails splits and validates a list of e-mail addresses.
func ParseEmails(list string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		a, err := mail.ParseAddress(p)
		if err != nil || !strings.Contains(a.Address, ".") && !strings.HasSuffix(a.Address, "@localhost") {
			return out, fmt.Errorf("%q is not a valid e-mail address", p)
		}
		k := strings.ToLower(a.Address)
		if !seen[k] {
			seen[k] = true
			out = append(out, a.Address)
		}
	}
	return out, nil
}

// SetEmailPrefs updates a user's e-mail address (local users only) and alert opt-in.
func (s *Store) SetEmailPrefs(ctx context.Context, id int64, email string, setEmail, alerts bool) error {
	if setEmail {
		_, err := s.Pool.Exec(ctx, `UPDATE users SET email=$2, email_alerts=$3 WHERE id=$1`, id, email, alerts)
		return err
	}
	_, err := s.Pool.Exec(ctx, `UPDATE users SET email_alerts=$2 WHERE id=$1`, id, alerts)
	return err
}

// AlertSubscribers returns the e-mail addresses of users who opted in to alerts.
func (s *Store) AlertSubscribers(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT email FROM users WHERE email_alerts AND email <> '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// EmailLog is one sent (or failed) message.
type EmailLog struct {
	ID         int64
	CreatedAt  time.Time
	Kind       string
	Recipients string
	Subject    string
	OK         bool
	Error      string
}

// Notification channels recorded in the log.
const (
	ChannelEmail    = "email"
	ChannelPushover = "pushover"
)

// LogEmail records an e-mail that was sent or failed.
func (s *Store) LogEmail(ctx context.Context, kind string, to []string, subject string, sendErr error) error {
	return s.LogNotification(ctx, ChannelEmail, kind, to, subject, sendErr)
}

// LogNotification records a message sent (or not) on any channel.
func (s *Store) LogNotification(ctx context.Context, channel, kind string, to []string, subject string, sendErr error) error {
	msg := ""
	if sendErr != nil {
		msg = sendErr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO email_log (channel, kind, recipients, subject, ok, error) VALUES ($1,$2,$3,$4,$5,$6)`,
		channel, kind, strings.Join(to, ", "), subject, sendErr == nil, msg)
	return err
}

// RecentEmails returns the latest e-mails, newest first.
func (s *Store) RecentEmails(ctx context.Context, limit int) ([]EmailLog, error) {
	return s.RecentNotifications(ctx, ChannelEmail, limit)
}

// RecentNotifications returns the latest messages of one channel, newest first.
func (s *Store) RecentNotifications(ctx context.Context, channel string, limit int) ([]EmailLog, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, created_at, kind, recipients, subject, ok, error
		FROM email_log WHERE channel=$1 ORDER BY created_at DESC, id DESC LIMIT $2`, channel, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (EmailLog, error) {
		var e EmailLog
		err := r.Scan(&e.ID, &e.CreatedAt, &e.Kind, &e.Recipients, &e.Subject, &e.OK, &e.Error)
		return e, err
	})
}

// PreviousChange returns when the monitor's status changed before t, if known.
func (s *Store) PreviousChange(ctx context.Context, monitorID int64, before time.Time) (*time.Time, error) {
	var at time.Time
	err := s.Pool.QueryRow(ctx, `SELECT created_at FROM events WHERE monitor_id=$1 AND created_at < $2
		ORDER BY created_at DESC LIMIT 1`, monitorID, before).Scan(&at)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &at, nil
}
