// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package geo

import (
	"bytes"
	"compress/gzip"
	"context"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScope(t *testing.T) {
	cases := map[string]string{
		"8.8.8.8":       ScopePublic,
		"10.1.2.3":      ScopePrivate,
		"192.168.1.1":   ScopePrivate,
		"172.20.0.1":    ScopePrivate,
		"127.0.0.1":     ScopeLoopback,
		"::1":           ScopeLoopback,
		"169.254.10.1":  ScopeLinkLocal,
		"fe80::1":       ScopeLinkLocal,
		"100.72.0.1":    ScopeSharedCGNAT,
		"192.0.2.7":     ScopeReserved,
		"203.0.113.9":   ScopeReserved,
		"2001:db8::1":   ScopeReserved,
		"fd00::1":       ScopePrivate,
		"2a00:1450::1":  ScopePublic,
		"0.0.0.0":       ScopeReserved,
		"239.255.0.1":   ScopeReserved,
		"81.2.69.142":   ScopePublic,
		"100.128.0.1":   ScopePublic, // just outside 100.64/10
		"198.19.255.1":  ScopeReserved,
		"240.0.0.1":     ScopeReserved,
		"2.125.160.216": ScopePublic,
	}
	for ip, want := range cases {
		if got := Scope(net.ParseIP(ip)); got != want {
			t.Errorf("Scope(%s) = %q, want %q", ip, got, want)
		}
	}
}

func TestDistanceAndRTT(t *testing.T) {
	// Lisbon → London is about 1,585 km.
	d := DistanceKm(38.7223, -9.1393, 51.5072, -0.1276)
	if math.Abs(d-1585) > 15 {
		t.Fatalf("Lisbon-London = %.0f km", d)
	}
	if DistanceKm(10, 20, 10, 20) != 0 {
		t.Fatal("same point should be 0 km")
	}
	if got := MinRTTms(1000); math.Abs(got-10) > 1e-9 {
		t.Fatalf("MinRTT(1000 km) = %v ms, want 10", got)
	}
}

func TestLocationFormatting(t *testing.T) {
	l := Location{City: "Lisbon", Country: "Portugal", CountryCode: "pt", Lat: 38.72, Lon: -9.14, HasCoords: true}
	if l.Place() != "Lisbon, Portugal" {
		t.Errorf("Place = %q", l.Place())
	}
	if l.Coords() != "38.72° N, 9.14° W" {
		t.Errorf("Coords = %q", l.Coords())
	}
	if l.Flag() != "🇵🇹" {
		t.Errorf("Flag = %q", l.Flag())
	}
	if (Location{CountryCode: "XYZ"}).Flag() != "" || (Location{}).Coords() != "" {
		t.Error("bad codes and missing coordinates must give empty strings")
	}
	if (Location{Region: "Bavaria", CountryCode: "DE"}).Place() != "Bavaria, DE" {
		t.Error("Place should fall back to the region and the code")
	}
	i := Info{ASN: 3215, Org: "Orange"}
	if i.ASNLabel() != "AS3215 · Orange" {
		t.Errorf("ASNLabel = %q", i.ASNLabel())
	}
}

func TestOpenEmptyDir(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.Status().Ready() {
		t.Fatal("an empty folder must not be ready")
	}
	info := db.Lookup(net.ParseIP("81.2.69.142"))
	if info.Found || info.Scope != ScopePublic || info.Version != 4 {
		t.Fatalf("unexpected %+v", info)
	}
	if changed, err := db.ReloadIfChanged(); changed || err != nil {
		t.Fatalf("nothing changed, got %v %v", changed, err)
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, CityFile), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := Open(dir)
	if err == nil || !strings.Contains(err.Error(), CityFile) {
		t.Fatalf("expected an error naming %s, got %v", CityFile, err)
	}
	if db.Status().Ready() {
		t.Fatal("a corrupt file must not be loaded")
	}
}

func TestUpdateFallsBackAndValidates(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.Contains(r.URL.Path, "2026-11") {
			http.NotFound(w, r) // this month is not published yet
			return
		}
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write([]byte("garbage, not an mmdb file"))
		_ = zw.Close()
		_, _ = w.Write(b.Bytes())
	}))
	defer srv.Close()
	dir := t.TempDir()
	db, _ := Open(dir)
	_, err := db.Update(context.Background(), srv.Client(), srv.URL, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "not a valid database") {
		t.Fatalf("expected a validation error, got %v", err)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[0], "dbip-city-lite-2026-11.mmdb.gz") || !strings.HasSuffix(paths[1], "dbip-city-lite-2026-10.mmdb.gz") {
		t.Fatalf("unexpected requests %v", paths)
	}
	if _, err := os.Stat(filepath.Join(dir, CityFile)); !os.IsNotExist(err) {
		t.Fatal("an invalid download must not replace the database")
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".download-*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

func TestSettings(t *testing.T) {
	st, err := ParseSettings("")
	if err != nil || st.PublicIPMode != PublicAuto || !st.ReverseDNS || st.DetectURL != DefaultDetectURL {
		t.Fatalf("defaults: %+v %v", st, err)
	}
	enc, _ := Settings{PublicIPMode: PublicManual, PublicIP: "203.0.113.4", FixedLocation: true, Lat: 38.7, Lon: -9.1}.Encode()
	back, err := ParseSettings(enc)
	if err != nil || back.PublicIP != "203.0.113.4" || !back.FixedLocation || back.DetectURL != DefaultDetectURL {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	bad := []Settings{
		{PublicIPMode: PublicManual, PublicIP: "not-an-ip"},
		{PublicIPMode: PublicAuto, DetectURL: "ftp://x"},
		{PublicIPMode: "sometimes"},
		{PublicIPMode: PublicOff, FixedLocation: true, Lat: 91, Lon: 0},
		{PublicIPMode: PublicOff, FixedLocation: true},
		{PublicIPMode: PublicOff, Mirror: "file:///etc"},
	}
	for i, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("case %d should be rejected: %+v", i, s)
		}
	}
	good := Settings{PublicIPMode: PublicAuto, DetectURL: " https://api.ipify.org ", FixedLocation: true, Lat: 38.72, Lon: -9.14, Mirror: "https://mirror.example/geo/"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if good.Mirror != "https://mirror.example/geo" || good.DetectURL != "https://api.ipify.org" {
		t.Fatalf("not normalised: %+v", good)
	}
}

func TestServiceHostWithAddress(t *testing.T) {
	db, _ := Open(t.TempDir())
	svc := NewService(db, func(context.Context) Settings { return Settings{} })
	h := svc.Host(context.Background(), "[10.0.0.5]")
	if !h.IsIP || len(h.IPs) != 1 || h.IPs[0].Scope != ScopePrivate || h.Error != "" {
		t.Fatalf("unexpected %+v", h)
	}
	if p := h.Primary(); p == nil || p.IP != "10.0.0.5" {
		t.Fatalf("primary = %+v", p)
	}
	if svc.Host(context.Background(), " ").Error == "" {
		t.Fatal("an empty host must report an error")
	}
}

func TestServiceDetectsPublicIP(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte("198.51.100.23\n"))
	}))
	defer srv.Close()
	db, _ := Open(t.TempDir())
	svc := NewService(db, func(context.Context) Settings {
		return Settings{PublicIPMode: PublicAuto, DetectURL: srv.URL, FixedLocation: true, Lat: 38.72, Lon: -9.14, City: "Lisbon"}
	})
	p := svc.Probe(context.Background())
	if p.Public == nil || p.Public.IP != "198.51.100.23" || p.PublicFrom != "detected" {
		t.Fatalf("public = %+v (%s)", p.Public, p.PublicError)
	}
	if !p.Located() || p.LocFrom != "fixed" || p.Loc.City != "Lisbon" {
		t.Fatalf("location = %+v from %q", p.Loc, p.LocFrom)
	}
	svc.Probe(context.Background())
	if calls != 1 {
		t.Fatalf("the public address must be cached, got %d requests", calls)
	}
}

func TestMapFitting(t *testing.T) {
	x, y := Project(0, 0)
	if x != 500 || y != 250 {
		t.Fatalf("Project(0,0) = %v,%v", x, y)
	}
	m := &Map{}
	m.AddMarker(KindProbe, 38.72, -9.14, "", "probe", "probe")
	m.AddMarker(KindTarget, 51.51, -0.13, "up", "a", "a")
	m.AddMarker(KindTarget, 51.51, -0.13, "down", "b", "b") // merged
	m.Connect("path", "up", 38.72, -9.14, 51.51, -0.13, 0.2)
	m.Connect("path", "up", 1, 1, 1, 1, 0.2) // zero length: skipped
	m.FitMarkers()
	if len(m.Markers) != 2 || m.Markers[1].Count != 2 || m.Markers[1].Status != "down" || m.Markers[1].Label != "" {
		t.Fatalf("merge failed: %+v", m.Markers)
	}
	if len(m.Lines) != 1 || !strings.HasPrefix(m.Lines[0].D, "M") {
		t.Fatalf("lines: %+v", m.Lines)
	}
	if m.VW >= MapW || m.VW < 150 || math.Abs(m.VW/m.VH-MapW/390) > 1e-9 {
		t.Fatalf("zoom: %v", m.ViewBox())
	}
	for _, mk := range m.Markers {
		if mk.X < m.VX || mk.X > m.VX+m.VW || mk.Y < m.VY || mk.Y > m.VY+m.VH {
			t.Fatalf("marker %+v outside %s", mk, m.ViewBox())
		}
	}
	w := &Map{}
	w.AddMarker(KindTarget, 47, -122, "up", "", "")
	w.AddMarker(KindTarget, -33, 151, "up", "", "")
	w.FitMarkers()
	if w.VW != MapW {
		t.Fatalf("far apart markers should show the world, got %s", w.ViewBox())
	}
	if (&Map{}).Empty() != true || w.Grid() == "" {
		t.Fatal("Empty/Grid")
	}
}
