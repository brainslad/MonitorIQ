// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSafeNext(t *testing.T) {
	keep := []string{"/", "/export", "/monitors/3?range=7d", "/admin/monitors?group=2&by=host"}
	for _, v := range keep {
		if got := safeNext(v); got != v {
			t.Errorf("safeNext(%q) = %q, want it kept", v, got)
		}
	}
	refuse := []string{"", "x", "https://evil.example", "//evil.example", "/\\evil.example",
		"/\t/evil.example", "/\n/evil.example", "/\r/evil.example", "/ /evil.example", "/a\\b", "/\x00", "/\x7f"}
	for _, v := range refuse {
		if got := safeNext(v); got != "/" {
			t.Errorf("safeNext(%q) = %q, want /", v, got)
		}
	}
}

func TestCrossSite(t *testing.T) {
	cases := []struct {
		fetchSite, origin string
		want              bool
	}{
		{"", "", false},
		{"same-origin", "", false},
		{"same-site", "", false},
		{"none", "", false},
		{"cross-site", "", true},
		{"", "https://uptime.example", false},
		{"", "https://evil.example", true},
		{"", "null", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "https://uptime.example/login", nil)
		if c.fetchSite != "" {
			r.Header.Set("Sec-Fetch-Site", c.fetchSite)
		}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := crossSite(r); got != c.want {
			t.Errorf("crossSite(fetch=%q origin=%q) = %v, want %v", c.fetchSite, c.origin, got, c.want)
		}
	}
}

func TestStaticNoListing(t *testing.T) {
	s := &Server{}
	h := s.Handler()
	for path, want := range map[string]int{"/static/": 404, "/static/fonts/": 404, "/static/app.css": 200} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}
