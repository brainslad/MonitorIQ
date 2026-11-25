// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"strings"
	"testing"
)

func TestCleanFooter(t *testing.T) {
	if v, err := cleanFooter("  Contoso   NOC ·  24/7 "); err != nil || v != "Contoso NOC · 24/7" {
		t.Errorf("got %q %v", v, err)
	}
	if _, err := cleanFooter(strings.Repeat("é", MaxFooterLen+1)); err == nil {
		t.Error("long footer accepted")
	}
	if _, err := cleanFooter("a\x00b"); err == nil {
		t.Error("control character accepted")
	}
	if (Branding{}).FooterLine() != defaultSlogan || (Branding{Footer: "x"}).FooterLine() != "x" {
		t.Error("FooterLine")
	}
}
