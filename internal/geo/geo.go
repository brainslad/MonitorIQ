// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

// Package geo finds where IP addresses are: city, country, coordinates and
// network operator (ASN), from local MaxMind-format databases (DB-IP Lite or
// MaxMind GeoLite2). Lookups never leave the server; only the optional
// database download and public-IP detection make outbound requests.
package geo

import (
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// File names inside the database directory.
const (
	CityFile = "city.mmdb"
	ASNFile  = "asn.mmdb"
)

// Location is where an address is, as far as the database knows.
type Location struct {
	City        string
	Region      string
	Country     string
	CountryCode string
	Continent   string
	TimeZone    string
	Lat, Lon    float64
	HasCoords   bool
	// AccuracyKm is the radius the database gives for the coordinates (0 if unknown).
	AccuracyKm int
}

// Place is a short human description, e.g. "Lisbon, Portugal".
func (l Location) Place() string {
	var parts []string
	if l.City != "" {
		parts = append(parts, l.City)
	} else if l.Region != "" {
		parts = append(parts, l.Region)
	}
	if l.Country != "" {
		parts = append(parts, l.Country)
	} else if l.CountryCode != "" {
		parts = append(parts, l.CountryCode)
	}
	return strings.Join(parts, ", ")
}

// Coords formats the coordinates, e.g. "38.72° N, 9.14° W".
func (l Location) Coords() string {
	if !l.HasCoords {
		return ""
	}
	ns, ew := "N", "E"
	if l.Lat < 0 {
		ns = "S"
	}
	if l.Lon < 0 {
		ew = "W"
	}
	return fmt.Sprintf("%.2f° %s, %.2f° %s", math.Abs(l.Lat), ns, math.Abs(l.Lon), ew)
}

// Flag returns the flag emoji for the country code.
func (l Location) Flag() string {
	cc := strings.ToUpper(l.CountryCode)
	if len(cc) != 2 || cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z' {
		return ""
	}
	return string([]rune{0x1F1E6 + rune(cc[0]-'A'), 0x1F1E6 + rune(cc[1]-'A')})
}

// Address scopes.
const (
	ScopePublic      = "public"
	ScopePrivate     = "private"
	ScopeLoopback    = "loopback"
	ScopeLinkLocal   = "link-local"
	ScopeSharedCGNAT = "shared (CGNAT)"
	ScopeReserved    = "reserved"
)

// Info is everything known about one IP address.
type Info struct {
	IP      string
	Version int // 4 or 6
	Scope   string
	PTR     string // reverse DNS name, if looked up and found
	ASN     uint
	Org     string
	Network string // the database network that matched, e.g. "81.2.69.0/24"
	Loc     Location
	// Found is true when the city database had a record for the address.
	Found bool
}

// Public reports whether the address is routable on the Internet.
func (i Info) Public() bool { return i.Scope == ScopePublic }

// ASNLabel is e.g. "AS3215 · Orange".
func (i Info) ASNLabel() string {
	switch {
	case i.ASN > 0 && i.Org != "":
		return fmt.Sprintf("AS%d · %s", i.ASN, i.Org)
	case i.ASN > 0:
		return fmt.Sprintf("AS%d", i.ASN)
	}
	return i.Org
}

var (
	cgnat     = mustCIDR("100.64.0.0/10")
	reserved4 = []*net.IPNet{mustCIDR("0.0.0.0/8"), mustCIDR("192.0.0.0/24"), mustCIDR("192.0.2.0/24"),
		mustCIDR("198.18.0.0/15"), mustCIDR("198.51.100.0/24"), mustCIDR("203.0.113.0/24"), mustCIDR("240.0.0.0/4")}
	reserved6 = []*net.IPNet{mustCIDR("2001:db8::/32"), mustCIDR("100::/64")}
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// Scope classifies an address.
func Scope(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return ScopeLoopback
	case ip.IsPrivate():
		return ScopePrivate
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return ScopeLinkLocal
	case ip.IsUnspecified(), ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return ScopeReserved
	}
	if ip4 := ip.To4(); ip4 != nil {
		if cgnat.Contains(ip4) {
			return ScopeSharedCGNAT
		}
		for _, n := range reserved4 {
			if n.Contains(ip4) {
				return ScopeReserved
			}
		}
		return ScopePublic
	}
	for _, n := range reserved6 {
		if n.Contains(ip) {
			return ScopeReserved
		}
	}
	return ScopePublic
}

// DB holds the open databases. Missing files are not an error: lookups then
// only classify the address. Safe for concurrent use; Reload swaps files in.
type DB struct {
	dir string

	mu   sync.RWMutex
	city *maxminddb.Reader
	asn  *maxminddb.Reader
	mods [2]time.Time // modification times of the files when loaded
}

// Open opens the databases in dir (files that are missing are skipped).
func Open(dir string) (*DB, error) {
	d := &DB{dir: dir}
	return d, d.Reload()
}

// Dir is the database directory.
func (d *DB) Dir() string { return d.dir }

// Reload re-opens the database files, e.g. after an update.
func (d *DB) Reload() error {
	var errs []error
	city, err := openIfExists(filepath.Join(d.dir, CityFile))
	if err != nil {
		errs = append(errs, err)
	}
	asn, err := openIfExists(filepath.Join(d.dir, ASNFile))
	if err != nil {
		errs = append(errs, err)
	}
	mods := d.fileMods()
	d.mu.Lock()
	oldCity, oldASN := d.city, d.asn
	d.city, d.asn, d.mods = city, asn, mods
	d.mu.Unlock()
	// Readers use memory maps; close the old ones once nothing uses them.
	if oldCity != nil {
		_ = oldCity.Close()
	}
	if oldASN != nil {
		_ = oldASN.Close()
	}
	return errors.Join(errs...)
}

func (d *DB) fileMods() [2]time.Time {
	var m [2]time.Time
	for i, f := range []string{CityFile, ASNFile} {
		if st, err := os.Stat(filepath.Join(d.dir, f)); err == nil {
			m[i] = st.ModTime()
		}
	}
	return m
}

// ReloadIfChanged reloads the files when they changed on disk (for example
// after "monitoriq geoip update" or a copied GeoLite2 file).
func (d *DB) ReloadIfChanged() (bool, error) {
	cur := d.fileMods()
	d.mu.RLock()
	same := cur == d.mods
	d.mu.RUnlock()
	if same {
		return false, nil
	}
	return true, d.Reload()
}

// Close releases the databases.
func (d *DB) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.city != nil {
		_ = d.city.Close()
	}
	if d.asn != nil {
		_ = d.asn.Close()
	}
	d.city, d.asn = nil, nil
}

func openIfExists(path string) (*maxminddb.Reader, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return r, nil
}

// FileStatus describes one database file.
type FileStatus struct {
	Loaded bool
	Type   string // e.g. "DBIP-City-Lite" or "GeoLite2-City"
	Built  time.Time
	Size   int64
}

// Status describes both databases.
type Status struct {
	Dir  string
	City FileStatus
	ASN  FileStatus
}

// Ready reports whether locations can be looked up.
func (s Status) Ready() bool { return s.City.Loaded }

// Attribution is the credit line the database licence asks for.
func (s Status) Attribution() (text, url string) {
	t := strings.ToLower(s.City.Type + " " + s.ASN.Type)
	switch {
	case strings.Contains(t, "dbip") || strings.Contains(t, "db-ip"):
		return "IP Geolocation by DB-IP", "https://db-ip.com"
	case strings.Contains(t, "geolite"):
		return "GeoLite2 data created by MaxMind", "https://www.maxmind.com"
	case strings.Contains(t, "geoip2"):
		return "GeoIP2 data by MaxMind", "https://www.maxmind.com"
	}
	return "", ""
}

// Status reports what is loaded.
func (d *DB) Status() Status {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return Status{Dir: d.dir, City: fileStatus(d.city, filepath.Join(d.dir, CityFile)), ASN: fileStatus(d.asn, filepath.Join(d.dir, ASNFile))}
}

func fileStatus(r *maxminddb.Reader, path string) FileStatus {
	if r == nil {
		return FileStatus{}
	}
	fs := FileStatus{Loaded: true, Type: r.Metadata.DatabaseType, Built: time.Unix(int64(r.Metadata.BuildEpoch), 0)}
	if st, err := os.Stat(path); err == nil {
		fs.Size = st.Size()
	}
	return fs
}

type cityRecord struct {
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Continent struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"continent"`
	Subdivisions []struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	Location struct {
		Latitude  *float64 `maxminddb:"latitude"`
		Longitude *float64 `maxminddb:"longitude"`
		Accuracy  uint16   `maxminddb:"accuracy_radius"`
		TimeZone  string   `maxminddb:"time_zone"`
	} `maxminddb:"location"`
}

type asnRecord struct {
	Number uint   `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

// Lookup finds what the databases know about ip. Reverse DNS is not done here.
func (d *DB) Lookup(ip net.IP) Info {
	info := Info{IP: ip.String(), Version: 6, Scope: Scope(ip)}
	if ip.To4() != nil {
		info.Version = 4
	}
	if info.Scope != ScopePublic {
		return info
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.city != nil {
		var rec cityRecord
		if n, ok, err := d.city.LookupNetwork(ip, &rec); err == nil && ok {
			info.Found = true
			info.Network = n.String()
			l := &info.Loc
			l.City = rec.City.Names["en"]
			l.Country = rec.Country.Names["en"]
			l.CountryCode = rec.Country.ISOCode
			l.Continent = rec.Continent.Code
			l.TimeZone = rec.Location.TimeZone
			l.AccuracyKm = int(rec.Location.Accuracy)
			if len(rec.Subdivisions) > 0 {
				l.Region = rec.Subdivisions[0].Names["en"]
			}
			if rec.Location.Latitude != nil && rec.Location.Longitude != nil {
				l.Lat, l.Lon, l.HasCoords = *rec.Location.Latitude, *rec.Location.Longitude, true
			}
		}
	}
	if d.asn != nil {
		var rec asnRecord
		if n, ok, err := d.asn.LookupNetwork(ip, &rec); err == nil && ok {
			info.ASN, info.Org = rec.Number, rec.Org
			if info.Network == "" {
				info.Network = n.String()
			}
		}
	}
	return info
}

// DistanceKm is the great-circle distance between two points.
func DistanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

// MinRTTms is the round-trip time light needs in optical fibre (about
// 200 000 km/s) over the great-circle distance: no real path can be faster.
func MinRTTms(km float64) float64 { return 2 * km / 200000 * 1000 }
