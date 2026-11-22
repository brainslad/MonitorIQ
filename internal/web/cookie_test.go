// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"

	"github.com/brainslad/monitoriq/internal/config"
)

func TestSecureCookie(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name  string
		cfg   config.Config
		host  string
		proto string
		tls   bool
		want  bool
	}{
		{"plain http, no base URL", config.Config{}, "172.16.101.103:8080", "", false, false},
		{"direct TLS", config.Config{}, "mon.example", "", true, true},
		{"public name of an https base URL", config.Config{BaseURL: "https://uptime.example"}, "uptime.example", "", false, true},
		{"public name with a port", config.Config{BaseURL: "https://uptime.example:8443"}, "UPTIME.example:8443", "", false, true},
		{"IP address with an https base URL", config.Config{BaseURL: "https://uptime.example"}, "172.16.101.103:8080", "", false, false},
		{"trusted proxy says https", config.Config{TrustProxy: true}, "10.0.0.5:8080", "https", false, true},
		{"untrusted proxy header is ignored", config.Config{}, "10.0.0.5:8080", "https", false, false},
		{"trusted proxy says http", config.Config{TrustProxy: true, BaseURL: "https://uptime.example"}, "172.16.101.103:8080", "http", false, false},
		{"forced on", config.Config{CookieSecure: &yes}, "172.16.101.103:8080", "", false, true},
		{"forced off", config.Config{CookieSecure: &no, BaseURL: "https://uptime.example"}, "uptime.example", "https", true, false},
	}
	for _, c := range cases {
		cfg := c.cfg
		s := &Server{Cfg: &cfg}
		r := httptest.NewRequest("GET", "/login", nil)
		r.Host = c.host
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := s.secureCookie(r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
