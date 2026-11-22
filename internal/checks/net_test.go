// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package checks

import "testing"

func TestTargetHost(t *testing.T) {
	cases := []struct {
		m    Monitor
		want string
	}{
		{Monitor{Type: TypeHTTP, Target: "https://shop.example.com:8443/login?x=1"}, "shop.example.com"},
		{Monitor{Type: TypeTCP, Target: "db.internal:5432"}, "db.internal"},
		{Monitor{Type: TypeTCP, Target: "[2001:db8::5]:22"}, "2001:db8::5"},
		{Monitor{Type: TypePing, Target: "10.0.0.1"}, "10.0.0.1"},
		{Monitor{Type: TypeDNS, Target: "example.com", Config: Config{DNSServer: "1.1.1.1:53"}}, "1.1.1.1"},
		{Monitor{Type: TypeDNS, Target: "example.com"}, "example.com"},
	}
	for _, c := range cases {
		if got := TargetHost(c.m); got != c.want {
			t.Errorf("TargetHost(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}
