// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
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
	if _, err := logoType(append(png, bytes.Repeat([]byte{0}, MaxUploadBytes)...)); err == nil {
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
	if (Branding{}).FooterLine() != "" || (Branding{Footer: "x"}).FooterLine() != "x" {
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

func TestFitLogo(t *testing.T) {
	for _, c := range []struct{ w, h, ww, wh int }{
		{2000, 500, 600, 150}, {100, 40, 100, 40}, {1024, 1024, 150, 150}, {3000, 100, 600, 20}, {600, 150, 600, 150},
	} {
		if w, h := fitBox(c.w, c.h, logoMaxW, logoMaxH); w != c.ww || h != c.wh {
			t.Errorf("fitBox(%d×%d) = %d×%d, want %d×%d", c.w, c.h, w, h, c.ww, c.wh)
		}
	}
	// A large opaque red PNG with a transparent right half shrinks to the box.
	src := image.NewNRGBA(image.Rect(0, 0, 1200, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 600; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	out, ct, err := fitLogo(buf.Bytes(), "image/png", logoMaxW, logoMaxH)
	if err != nil || ct != "image/png" {
		t.Fatalf("fitLogo: %q %v", ct, err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 600 || b.Dy() != 150 {
		t.Fatalf("size %v", b)
	}
	if r, _, _, a := img.At(10, 10).RGBA(); r>>8 != 255 || a>>8 != 255 {
		t.Errorf("left pixel %v", img.At(10, 10))
	}
	if _, _, _, a := img.At(590, 10).RGBA(); a != 0 {
		t.Errorf("right pixel not transparent: %v", img.At(590, 10))
	}
	// Small images and SVG are kept as they are.
	small := []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")
	if out, ct, _ := fitLogo(small, "image/svg+xml", logoMaxW, logoMaxH); !bytes.Equal(out, small) || ct != "image/svg+xml" {
		t.Error("svg changed")
	}
}
