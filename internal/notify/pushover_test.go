// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/store"
)

const (
	testToken = "azGDORePK8gMaC0QOYAMyEEuzJnyUi"
	testUser  = "uQiRzpo4DXghDmr9QzzfQu27cmVRsG"
)

// fakePushover records the posted forms and answers like the real API.
func fakePushover(t *testing.T, status int, body string) chan url.Values {
	got := make(chan url.Values, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		_ = r.ParseForm()
		got <- r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	old := pushoverAPI
	pushoverAPI = srv.URL
	t.Cleanup(func() { pushoverAPI = old; srv.Close() })
	return got
}

func validPushover() PushoverSettings {
	s := DefaultPushoverSettings()
	s.Enabled, s.Token, s.UserKey = true, testToken, testUser
	return s
}

func TestPushoverValidate(t *testing.T) {
	s := validPushover()
	s.Devices = " iphone, watch ;"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Devices != "iphone,watch" {
		t.Errorf("devices %q", s.Devices)
	}
	bad := []func(*PushoverSettings){
		func(s *PushoverSettings) { s.Token = "short" },
		func(s *PushoverSettings) { s.UserKey = "not-a-key" },
		func(s *PushoverSettings) { s.DownPriority = 3 },
		func(s *PushoverSettings) { s.Retry = 10 },
		func(s *PushoverSettings) { s.Expire = 20000 },
		func(s *PushoverSettings) { s.Devices = "bad/device" },
		func(s *PushoverSettings) { s.Sound = "Not A Sound" },
	}
	for i, f := range bad {
		s := validPushover()
		f(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("case %d: expected an error", i)
		}
	}
	// The default key is optional: monitors may have their own keys.
	nodef := validPushover()
	nodef.UserKey = ""
	if err := nodef.Validate(); err != nil {
		t.Errorf("no default key: %v", err)
	}
	// Keys are not required while sending is off.
	off := DefaultPushoverSettings()
	if err := off.Validate(); err != nil {
		t.Errorf("disabled: %v", err)
	}
}

func TestPushoverWanted(t *testing.T) {
	s := DefaultPushoverSettings()
	s.DownPriority = PriorityEmergency
	cases := []struct {
		from, to checks.Status
		ok       bool
		prio     int
	}{
		{checks.Up, checks.Down, true, PriorityEmergency},
		{checks.Down, checks.Up, true, PriorityNormal},
		{checks.Down, checks.Degraded, true, PriorityNormal},
		{checks.Up, checks.Degraded, false, PriorityNormal},
		{checks.Degraded, checks.Up, false, PriorityNormal},
	}
	for _, c := range cases {
		ok, prio := s.wanted(c.from, c.to)
		if ok != c.ok || (ok && prio != c.prio) {
			t.Errorf("%v→%v: got %v/%d", c.from, c.to, ok, prio)
		}
	}
}

func TestSendPushover(t *testing.T) {
	got := fakePushover(t, 200, `{"status":1,"request":"x"}`)
	s := validPushover()
	s.Devices, s.Sound = "iphone", "siren"
	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	items := []Item{{Name: "Web", Host: "edge", Target: "https://example.com", Type: "http",
		From: checks.Up, To: checks.Down, Message: "HTTP 502", At: at, URL: "http://mon/monitors/1"}}
	m := PushAlert(items, []int{PriorityEmergency}, "http://mon")
	if err := SendPushover(context.Background(), s, m); err != nil {
		t.Fatal(err)
	}
	v := <-got
	want := map[string]string{"token": testToken, "user": testUser, "priority": "2", "retry": "300", "expire": "3600",
		"device": "iphone", "sound": "siren", "url": "http://mon/monitors/1", "timestamp": "1795608000"}
	for k, w := range want {
		if v.Get(k) != w {
			t.Errorf("%s = %q, want %q", k, v.Get(k), w)
		}
	}
	if !strings.Contains(v.Get("title"), "DOWN: Web (edge)") || !strings.Contains(v.Get("message"), "HTTP 502") {
		t.Errorf("title %q message %q", v.Get("title"), v.Get("message"))
	}
}

func TestSendPushoverError(t *testing.T) {
	fakePushover(t, 400, `{"user":"invalid","errors":["user identifier is invalid"],"status":0}`)
	err := SendPushover(context.Background(), validPushover(), PushTest("me", ""))
	if err == nil || !strings.Contains(err.Error(), "user identifier is invalid") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("the error leaks the token")
	}
}

func TestPushAlertGroupsAndTruncates(t *testing.T) {
	var items []Item
	var prios []int
	for i := 0; i < 60; i++ {
		items = append(items, Item{Name: strings.Repeat("monitor", 3), Host: "h", From: checks.Up, To: checks.Down, At: time.Now()})
		prios = append(prios, PriorityNormal)
	}
	prios[7] = PriorityHigh
	m := PushAlert(items, prios, "http://mon")
	if m.Priority != PriorityHigh {
		t.Errorf("priority %d", m.Priority)
	}
	if !strings.HasPrefix(m.Title, "MonitorIQ: 60 status changes") {
		t.Errorf("title %q", m.Title)
	}
	if got := truncate(m.Message, pushoverMaxMessage); len([]rune(got)) != pushoverMaxMessage {
		t.Errorf("truncated to %d runes", len([]rune(got)))
	}
	if m.URL != "http://mon/" {
		t.Errorf("url %q", m.URL)
	}
}

func TestPushoverRecipients(t *testing.T) {
	const other = "gznej3rKEVAvPUxu9vvNnqpmZpokzF"
	ps := validPushover()
	cases := []struct {
		m    store.Monitor
		want string
	}{
		{store.Monitor{Pushover: true}, testUser},
		{store.Monitor{Pushover: false}, ""},
		{store.Monitor{Pushover: false, PushoverKeys: other}, other},
		{store.Monitor{Pushover: true, PushoverKeys: other + ", " + testUser}, testUser + "," + other},
		{store.Monitor{Pushover: false, PushoverKeys: testUser}, testUser},
	}
	for i, c := range cases {
		if got := strings.Join(pushoverRecipients(ps, &c.m), ","); got != c.want {
			t.Errorf("case %d: got %q, want %q", i, got, c.want)
		}
	}
	ps.UserKey = ""
	if got := pushoverRecipients(ps, &store.Monitor{Pushover: true}); len(got) != 0 {
		t.Errorf("no default key: got %v", got)
	}
}

func TestSendPushoverSeveralKeys(t *testing.T) {
	const other = "gznej3rKEVAvPUxu9vvNnqpmZpokzF"
	got := fakePushover(t, 200, `{"status":1}`)
	m := PushTest("me", "")
	m.To = []string{testUser, other}
	if err := SendPushover(context.Background(), validPushover(), m); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v.Get("user") != testUser+","+other {
		t.Errorf("user = %q", v.Get("user"))
	}
	s := validPushover()
	s.UserKey = ""
	if err := SendPushover(context.Background(), s, PushTest("me", "")); err == nil {
		t.Error("expected an error without any key")
	}
}
