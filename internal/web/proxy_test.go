// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"net/http/httptest"
	"testing"

	"github.com/brainslad/monitoriq/internal/config"
)

func TestClientIPAndTrustedProxies(t *testing.T) {
	_, proxies, _ := config.ParseTrustProxy("10.61.1.101")
	cases := []struct {
		name   string
		cfg    config.Config
		remote string
		xff    string
		proto  string
		ip     string
		secure bool
	}{
		{"no proxy trust", config.Config{}, "10.61.1.101:5000", "203.0.113.7", "https", "10.61.1.101", false},
		{"trust any proxy (legacy true)", config.Config{TrustProxy: true}, "172.16.5.5:5000", "203.0.113.7", "https", "203.0.113.7", true},
		{"listed proxy", config.Config{TrustProxy: true, TrustedProxies: proxies}, "10.61.1.101:5000", "203.0.113.7", "https", "203.0.113.7", true},
		{"direct client forging headers", config.Config{TrustProxy: true, TrustedProxies: proxies}, "172.16.5.5:5000", "198.51.100.1", "https", "172.16.5.5", false},
		{"forged entry left of the real client", config.Config{TrustProxy: true, TrustedProxies: proxies}, "10.61.1.101:5000", "198.51.100.1, 203.0.113.7", "", "203.0.113.7", false},
		{"chain of trusted proxies", config.Config{TrustProxy: true, TrustedProxies: proxies}, "10.61.1.101:5000", "203.0.113.7, 10.61.1.101", "", "203.0.113.7", false},
		{"IPv4-mapped proxy address", config.Config{TrustProxy: true, TrustedProxies: proxies}, "[::ffff:10.61.1.101]:5000", "203.0.113.7", "", "203.0.113.7", false},
	}
	for _, c := range cases {
		cfg := c.cfg
		s := &Server{Cfg: &cfg}
		r := httptest.NewRequest("GET", "/login", nil)
		r.RemoteAddr = c.remote
		r.Host = "172.16.101.103:8080"
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if got := s.clientIP(r); got != c.ip {
			t.Errorf("%s: clientIP = %s, want %s", c.name, got, c.ip)
		}
		if got := s.secureCookie(r); got != c.secure {
			t.Errorf("%s: secureCookie = %v, want %v", c.name, got, c.secure)
		}
	}
}
