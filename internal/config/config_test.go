// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package config

import "testing"

func TestParseTrustProxy(t *testing.T) {
	for _, v := range []string{"", "false", "OFF", "0"} {
		if on, list, err := ParseTrustProxy(v); on || list != nil || err != nil {
			t.Errorf("%q: %v %v %v", v, on, list, err)
		}
	}
	if on, list, err := ParseTrustProxy("true"); !on || list != nil || err != nil {
		t.Errorf("true: %v %v %v", on, list, err)
	}
	on, list, err := ParseTrustProxy("10.61.1.101, 192.168.0.0/16 ::ffff:10.0.0.1\t2001:db8::/32")
	if err != nil || !on || len(list) != 4 {
		t.Fatalf("list: %v %v %v", on, list, err)
	}
	if list[0].String() != "10.61.1.101/32" || list[2].String() != "10.0.0.1/32" || list[3].String() != "2001:db8::/32" {
		t.Errorf("parsed as %v", list)
	}
	if _, _, err := ParseTrustProxy("10.61.1.101, proxy.local"); err == nil {
		t.Error("a host name must be rejected")
	}
}
