// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogoType(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	if ct, err := logoType(png); err != nil || ct != "image/png" {
		t.Errorf("png: %q %v", ct, err)
	}
	svg := []byte(`<?xml version="1.0"?><!-- logo --><svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`)
	if ct, err := logoType(svg); err != nil || ct != "image/svg+xml" {
		t.Errorf("svg: %q %v", ct, err)
	}
	for _, bad := range [][]byte{nil, []byte("<html><body>hi</body></html>"), []byte("GIF? no"), []byte("<svg")} {
		if _, err := logoType(bad); err == nil {
			t.Errorf("logoType(%q) accepted", bad)
		}
	}
	if _, err := logoType(append(png, bytes.Repeat([]byte{0}, MaxLogoBytes)...)); err == nil {
		t.Error("oversized logo accepted")
	}
}

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
