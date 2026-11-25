// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/brainslad/monitoriq/internal/store"
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

func TestPickHeat(t *testing.T) {
	var list []store.Monitor
	for i := int64(1); i <= 25; i++ {
		list = append(list, store.Monitor{ID: i, Name: "m", Enabled: i != 4})
	}
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)
	got := pickHeat(list, []int64{7, 4, 99, 3, 7}, 1, now)
	if len(got) != store.MaxHeatMonitors {
		t.Fatalf("len = %d", len(got))
	}
	// Pinned first and in order; paused (4), unknown (99) and repeated ids are skipped.
	if got[0].ID != 7 || !got[0].pinned || got[1].ID != 3 || !got[1].pinned || got[2].pinned {
		t.Fatalf("pinned rows: %+v", got[:3])
	}
	seen := map[int64]bool{}
	for _, h := range got {
		if seen[h.ID] || h.ID == 4 {
			t.Fatalf("duplicate or paused monitor %d", h.ID)
		}
		seen[h.ID] = true
	}
	// The random part is stable during a day and differs between users.
	again := pickHeat(list, []int64{7, 3}, 1, now.Add(2*time.Hour))
	for i := range got {
		if got[i].ID != again[i].ID {
			t.Fatal("random fill changed within the same day")
		}
	}
	other := pickHeat(list, nil, 2, now)
	same := true
	for i := range other {
		same = same && other[i].ID == pickHeat(list, nil, 1, now)[i].ID
	}
	if same {
		t.Error("two users got the same random fill")
	}
	if n := len(pickHeat(list[:4], nil, 1, now)); n != 3 {
		t.Errorf("small fleet: %d rows, want 3", n)
	}
}
