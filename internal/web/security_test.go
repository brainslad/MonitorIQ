// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestStaticVersioned(t *testing.T) {
	h := (&Server{}).Handler()
	u := assetURL("app.css")
	if !strings.HasPrefix(u, "/static/app.css?v=") || len(assetVersion()) != 12 {
		t.Fatalf("assetURL = %q", u)
	}
	for path, want := range map[string]string{
		u:                     "public, max-age=31536000, immutable",
		"/static/app.css":     "no-cache",
		"/static/app.css?v=x": "no-cache",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != want {
			t.Errorf("GET %s = %d %q, want %q", path, rec.Code, rec.Header().Get("Cache-Control"), want)
		}
	}
}

func TestCheckSVG(t *testing.T) {
	ok := []string{
		`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="#123"/></svg>`,
		`<?xml version="1.0" encoding="UTF-8"?><svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><defs><linearGradient id="g"/></defs><use xlink:href="#g"/><image href="data:image/png;base64,AAAA"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><style>.a{fill:red}</style><text class="a">Logo</text></svg>`,
	}
	for _, v := range ok {
		if _, err := logoType([]byte(v)); err != nil {
			t.Errorf("logoType(%q) = %v, want it accepted", v, err)
		}
	}
	bad := []string{
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><rect ONCLICK="x()"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div xmlns="http://www.w3.org/1999/xhtml">x</div></foreignObject></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><text>x</text></a></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="https://evil.example/x.png"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="https://evil.example/s.svg#a"/></svg>`,
		`<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg xmlns="http://www.w3.org/2000/svg">&x;</svg>`,
		`<?xml-stylesheet href="https://evil.example/a.css"?><svg xmlns="http://www.w3.org/2000/svg"/>`,
	}
	for _, v := range bad {
		if _, err := logoType([]byte(v)); err == nil {
			t.Errorf("logoType(%q) accepted, want it refused", v)
		}
	}
}
