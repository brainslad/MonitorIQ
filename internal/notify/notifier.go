// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package notify

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brainslad/monitoriq/internal/auth"
	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/store"
)

// Kinds of messages, as recorded in the e-mail log.
const (
	KindAlert  = "alert"
	KindDigest = "certificates"
	KindTest   = "test"
)

// batchDelay groups status changes that happen close together (for example
// a whole host going down) into one message per set of recipients.
const batchDelay = 20 * time.Second

const digestStateKey = "email_digest_last"

// Notifier turns status changes into e-mail alerts and sends the daily
// certificate digest. Settings are read at send time, so changes made in
// the back-office apply immediately.
type Notifier struct {
	Store   *store.Store
	Box     *auth.Box
	Log     *slog.Logger
	BaseURL string // MONITORIQ_BASE_URL, used when no dashboard URL is set

	mu      sync.Mutex
	pending []store.Transition
	timer   *time.Timer
	flushCh chan struct{}
	now     func() time.Time
}

func New(st *store.Store, box *auth.Box, log *slog.Logger, baseURL string) *Notifier {
	return &Notifier{Store: st, Box: box, Log: log, BaseURL: baseURL, flushCh: make(chan struct{}, 1), now: time.Now}
}

// Settings loads the current e-mail settings.
func (n *Notifier) Settings(ctx context.Context) (Settings, error) {
	raw, err := n.Store.GetSetting(ctx, SettingsKey)
	if err != nil {
		return DefaultSettings(), err
	}
	return LoadSettings(raw, n.Box)
}

// Notify queues a status change. It never blocks the caller.
func (n *Notifier) Notify(tr store.Transition) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pending = append(n.pending, tr)
	if n.timer == nil {
		n.timer = time.AfterFunc(batchDelay, func() {
			select {
			case n.flushCh <- struct{}{}:
			default:
			}
		})
	}
}

// Run sends queued alerts and the daily digest until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context) {
	digest := time.NewTicker(5 * time.Minute)
	defer digest.Stop()
	for {
		select {
		case <-ctx.Done():
			// Send what is queued before exiting.
			sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			n.flush(sctx)
			cancel()
			return
		case <-n.flushCh:
			n.flush(ctx)
		case <-digest.C:
			n.maybeDigest(ctx)
		}
	}
}

func (n *Notifier) take() []store.Transition {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := n.pending
	n.pending = nil
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	return out
}

// wanted reports whether a status change should be e-mailed.
func (st Settings) wanted(tr store.Transition) bool {
	switch tr.To {
	case checks.Down:
		return st.OnDown
	case checks.Degraded:
		if tr.From == checks.Down {
			return st.OnRecovery
		}
		return st.OnDegraded
	case checks.Up:
		if tr.From == checks.Down {
			return st.OnRecovery
		}
		if tr.From == checks.Degraded {
			return st.OnDegraded
		}
	}
	return false
}

// Item is one line of an alert.
type Item struct {
	MonitorID int64
	Name      string
	Host      string
	Target    string
	Type      string
	From, To  checks.Status
	Message   string
	At        time.Time
	Lasted    string // how long the previous status lasted, if known
	URL       string
}

func (n *Notifier) flush(ctx context.Context) {
	trs := n.take()
	if len(trs) == 0 {
		return
	}
	st, err := n.Settings(ctx)
	if err != nil {
		n.Log.Error("e-mail settings", "err", err)
		return
	}
	if !st.Enabled {
		return
	}
	subs, err := n.Store.AlertSubscribers(ctx)
	if err != nil {
		n.Log.Error("e-mail subscribers", "err", err)
	}
	base := n.linkBase(st)
	groups := map[string][]Item{}
	rcpts := map[string][]string{}
	for _, tr := range trs {
		if !st.wanted(tr) {
			continue
		}
		m, err := n.Store.GetMonitor(ctx, tr.MonitorID)
		if err != nil || m.AlertsMuted {
			continue
		}
		to := recipients(st.Recipients, subs, m.AlertEmails)
		if len(to) == 0 {
			continue
		}
		it := Item{MonitorID: m.ID, Name: m.Name, Host: m.HostName, Target: m.EffectiveTarget(), Type: m.Type,
			From: tr.From, To: tr.To, Message: tr.Message, At: tr.At}
		if m.Port > 0 {
			it.Target += ":" + fmt.Sprint(m.Port)
		}
		if prev, err := n.Store.PreviousChange(ctx, m.ID, tr.At); err == nil && prev != nil {
			it.Lasted = Duration(tr.At.Sub(*prev))
		}
		if base != "" {
			it.URL = fmt.Sprintf("%s/monitors/%d", base, m.ID)
		}
		key := strings.Join(to, ",")
		groups[key] = append(groups[key], it)
		rcpts[key] = to
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		msg := AlertMessage(groups[k], base)
		msg.To = rcpts[k]
		n.send(ctx, st, KindAlert, msg)
	}
}

func (n *Notifier) linkBase(st Settings) string {
	if st.DashboardURL != "" {
		return st.DashboardURL
	}
	return strings.TrimRight(n.BaseURL, "/")
}

// recipients merges the lists without duplicates (case-insensitive).
func recipients(defaults string, subs []string, extra string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(list []string) {
		for _, a := range list {
			k := strings.ToLower(a)
			if a != "" && !seen[k] {
				seen[k] = true
				out = append(out, a)
			}
		}
	}
	d, _ := store.ParseEmails(defaults)
	add(d)
	add(subs)
	e, _ := store.ParseEmails(extra)
	add(e)
	return out
}

func (n *Notifier) send(ctx context.Context, st Settings, kind string, msg Message) error {
	err := Send(ctx, st, msg)
	if lerr := n.Store.LogEmail(context.WithoutCancel(ctx), kind, msg.To, msg.Subject, err); lerr != nil {
		n.Log.Error("e-mail log", "err", lerr)
	}
	if err != nil {
		n.Log.Error("e-mail not sent", "kind", kind, "to", strings.Join(msg.To, ","), "err", err)
	} else {
		n.Log.Info("e-mail sent", "kind", kind, "to", strings.Join(msg.To, ","), "subject", msg.Subject)
	}
	return err
}

// SendTest sends a test message with the given (possibly unsaved) settings.
func (n *Notifier) SendTest(ctx context.Context, st Settings, to string, by string) error {
	addrs, err := store.ParseEmails(to)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return fmt.Errorf("enter an address to send the test message to")
	}
	msg := TestMessage(st, by, n.linkBase(st))
	msg.To = addrs
	return n.send(ctx, st, KindTest, msg)
}

// maybeDigest sends the certificate digest once a day, after DigestHour.
func (n *Notifier) maybeDigest(ctx context.Context) {
	st, err := n.Settings(ctx)
	if err != nil || !st.Enabled || !st.CertDigest {
		return
	}
	now := n.now()
	if now.Hour() < st.DigestHour {
		return
	}
	today := now.Format("2006-01-02")
	if last, _ := n.Store.GetSetting(ctx, digestStateKey); last == today {
		return
	}
	// Mark the day first so a failing server is not retried every 5 minutes.
	if err := n.Store.SetSetting(ctx, digestStateKey, today); err != nil {
		n.Log.Error("e-mail digest state", "err", err)
		return
	}
	_, _ = n.SendDigest(ctx, st)
}

// SendDigest sends the certificate digest now and returns how many messages were sent.
func (n *Notifier) SendDigest(ctx context.Context, st Settings) (int, error) {
	certs, err := n.Store.Certificates(ctx)
	if err != nil {
		return 0, err
	}
	subs, _ := n.Store.AlertSubscribers(ctx)
	base := n.linkBase(st)
	groups := map[string][]CertItem{}
	rcpts := map[string][]string{}
	for _, c := range certs {
		left := c.DaysLeft()
		if left > st.CertDays {
			continue
		}
		m, err := n.Store.GetMonitor(ctx, c.MonitorID)
		if err != nil || m.AlertsMuted {
			continue
		}
		to := recipients(st.Recipients, subs, m.AlertEmails)
		if len(to) == 0 {
			continue
		}
		it := CertItem{Name: c.Name, Target: c.Target, Issuer: c.Issuer, NotAfter: c.NotAfter, DaysLeft: left}
		if base != "" {
			it.URL = fmt.Sprintf("%s/monitors/%d", base, c.MonitorID)
		}
		key := strings.Join(to, ",")
		groups[key] = append(groups[key], it)
		rcpts[key] = to
	}
	sent := 0
	var lastErr error
	for k, items := range groups {
		msg := DigestMessage(items, st.CertDays, base)
		msg.To = rcpts[k]
		if err := n.send(ctx, st, KindDigest, msg); err != nil {
			lastErr = err
			continue
		}
		sent++
	}
	return sent, lastErr
}

// Duration formats a duration for people: "45 s", "12 min", "3 h 5 min", "2 d 4 h".
func Duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		if m := int(d.Minutes()) % 60; m > 0 {
			return fmt.Sprintf("%d h %d min", h, m)
		}
		return fmt.Sprintf("%d h", h)
	default:
		days := int(d.Hours()) / 24
		if h := int(d.Hours()) % 24; h > 0 {
			return fmt.Sprintf("%d d %d h", days, h)
		}
		return fmt.Sprintf("%d d", days)
	}
}
