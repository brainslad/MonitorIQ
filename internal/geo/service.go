// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// SettingsKey is the key in the settings table.
const SettingsKey = "geo"

// Public IP detection modes.
const (
	PublicAuto   = "auto"   // ask DetectURL which address the server's traffic leaves from
	PublicManual = "manual" // use PublicIP as typed
	PublicOff    = "off"    // do not show a public address
)

// DefaultDetectURL answers with the caller's public IP address as plain text.
const DefaultDetectURL = "https://api.ipify.org"

// Settings is edited under Settings → Geolocation and stored as JSON.
type Settings struct {
	PublicIPMode string `json:"public_ip_mode"`
	PublicIP     string `json:"public_ip,omitempty"`
	DetectURL    string `json:"detect_url,omitempty"`
	// Label names the monitoring system on maps (defaults to the host name).
	Label string `json:"label,omitempty"`
	// FixedLocation places the monitoring system at Lat/Lon instead of where
	// the database puts its public address (useful behind NAT or a VPN).
	FixedLocation bool    `json:"fixed_location"`
	Lat           float64 `json:"lat,omitempty"`
	Lon           float64 `json:"lon,omitempty"`
	City          string  `json:"city,omitempty"`
	Country       string  `json:"country,omitempty"`
	// ReverseDNS looks up the PTR name of every address shown.
	ReverseDNS bool `json:"reverse_dns"`
	// AutoUpdate downloads the new DB-IP Lite edition every month.
	AutoUpdate bool   `json:"auto_update"`
	Mirror     string `json:"mirror,omitempty"`
}

// DefaultSettings is used before anything is saved.
func DefaultSettings() Settings {
	return Settings{PublicIPMode: PublicAuto, DetectURL: DefaultDetectURL, ReverseDNS: true}
}

// ParseSettings decodes stored settings (empty means defaults).
func ParseSettings(raw string) (Settings, error) {
	st := DefaultSettings()
	if strings.TrimSpace(raw) == "" {
		return st, nil
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return DefaultSettings(), err
	}
	if st.DetectURL == "" {
		st.DetectURL = DefaultDetectURL
	}
	return st, nil
}

// Encode returns the JSON to store.
func (s Settings) Encode() (string, error) {
	b, err := json.Marshal(s)
	return string(b), err
}

// Validate checks the values a person typed.
func (s *Settings) Validate() error {
	s.PublicIP = strings.TrimSpace(s.PublicIP)
	s.DetectURL = strings.TrimSpace(s.DetectURL)
	s.Mirror = strings.TrimRight(strings.TrimSpace(s.Mirror), "/")
	switch s.PublicIPMode {
	case PublicAuto:
		if !strings.HasPrefix(s.DetectURL, "https://") && !strings.HasPrefix(s.DetectURL, "http://") {
			return errors.New("the detection address must start with https://")
		}
	case PublicManual:
		if net.ParseIP(s.PublicIP) == nil {
			return errors.New("enter the public IP address of this server")
		}
	case PublicOff:
	default:
		return errors.New("choose how to find the public IP address")
	}
	if s.FixedLocation {
		if s.Lat < -90 || s.Lat > 90 || s.Lon < -180 || s.Lon > 180 || (s.Lat == 0 && s.Lon == 0) {
			return errors.New("latitude must be between -90 and 90, longitude between -180 and 180")
		}
	}
	if s.Mirror != "" && !strings.HasPrefix(s.Mirror, "https://") && !strings.HasPrefix(s.Mirror, "http://") {
		return errors.New("the download mirror must be an http(s) address")
	}
	return nil
}

// Service combines the databases, DNS and the settings.
type Service struct {
	DB *DB
	// Settings loads the current settings (called on every use).
	Settings func(ctx context.Context) Settings
	Client   *http.Client
	Resolver *net.Resolver

	mu     sync.Mutex
	hosts  map[string]hostEntry
	ptrs   map[string]ptrEntry
	public publicEntry
}

type hostEntry struct {
	ips []net.IP
	err string
	at  time.Time
}

type ptrEntry struct {
	name string
	at   time.Time
}

type publicEntry struct {
	source string // detect URL or "manual"
	ip     net.IP
	err    string
	at     time.Time
}

const (
	hostTTL     = 10 * time.Minute
	ptrTTL      = 6 * time.Hour
	publicTTL   = 6 * time.Hour
	failTTL     = 5 * time.Minute
	maxHostIPs  = 4
	lookupLimit = 2 * time.Second
)

// NewService returns a service with empty caches.
func NewService(db *DB, settings func(context.Context) Settings) *Service {
	return &Service{DB: db, Settings: settings, Client: &http.Client{Timeout: 5 * time.Second},
		Resolver: net.DefaultResolver, hosts: map[string]hostEntry{}, ptrs: map[string]ptrEntry{}}
}

// HostInfo is a resolved target.
type HostInfo struct {
	Host  string
	IsIP  bool
	IPs   []Info
	Error string
}

// Primary is the address used to place the host on a map: the first one
// with coordinates, else the first one.
func (h HostInfo) Primary() *Info {
	for i := range h.IPs {
		if h.IPs[i].Loc.HasCoords {
			return &h.IPs[i]
		}
	}
	if len(h.IPs) > 0 {
		return &h.IPs[0]
	}
	return nil
}

// Host resolves host (a name or an address) and looks up each address.
func (s *Service) Host(ctx context.Context, host string) HostInfo {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	hi := HostInfo{Host: host}
	if host == "" {
		hi.Error = "no host"
		return hi
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		hi.IsIP = true
		ips = []net.IP{ip}
	} else {
		var err string
		ips, err = s.resolve(ctx, host)
		hi.Error = err
	}
	st := s.Settings(ctx)
	hi.IPs = make([]Info, len(ips))
	var wg sync.WaitGroup
	for i, ip := range ips {
		hi.IPs[i] = s.DB.Lookup(ip)
		if st.ReverseDNS {
			wg.Add(1)
			go func(i int, ip net.IP) {
				defer wg.Done()
				hi.IPs[i].PTR = s.ptr(ctx, ip)
			}(i, ip)
		}
	}
	wg.Wait()
	return hi
}

func (s *Service) resolve(ctx context.Context, host string) ([]net.IP, string) {
	key := strings.ToLower(host)
	s.mu.Lock()
	e, ok := s.hosts[key]
	s.mu.Unlock()
	if ok && time.Since(e.at) < hostTTL && (e.err == "" || time.Since(e.at) < failTTL) {
		return e.ips, e.err
	}
	// The lookup continues in the background if the caller gives up, so the
	// next page load finds the answer in the cache.
	done := make(chan hostEntry, 1)
	go func() {
		lctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		addrs, err := s.Resolver.LookupIPAddr(lctx, host)
		ent := hostEntry{at: time.Now()}
		if err != nil {
			ent.err = cleanDNSErr(err)
		}
		var v4, v6 []net.IP
		for _, a := range addrs {
			if a.IP.To4() != nil {
				v4 = append(v4, a.IP)
			} else {
				v6 = append(v6, a.IP)
			}
		}
		sortIPs(v4)
		sortIPs(v6)
		ent.ips = append(v4, v6...)
		if len(ent.ips) > maxHostIPs {
			ent.ips = ent.ips[:maxHostIPs]
		}
		s.mu.Lock()
		s.hosts[key] = ent
		s.mu.Unlock()
		done <- ent
	}()
	wait, cancel := context.WithTimeout(ctx, lookupLimit)
	defer cancel()
	select {
	case ent := <-done:
		return ent.ips, ent.err
	case <-wait.Done():
		if ok {
			return e.ips, e.err // stale but better than nothing
		}
		return nil, "still resolving, reload in a moment"
	}
}

func sortIPs(ips []net.IP) {
	sort.Slice(ips, func(i, j int) bool { return strings.Compare(ips[i].String(), ips[j].String()) < 0 })
}

func cleanDNSErr(err error) string {
	var de *net.DNSError
	if errors.As(err, &de) {
		if de.IsNotFound {
			return "name not found"
		}
		if de.IsTimeout {
			return "DNS lookup timed out"
		}
		return de.Err
	}
	return err.Error()
}

func (s *Service) ptr(ctx context.Context, ip net.IP) string {
	key := ip.String()
	s.mu.Lock()
	e, ok := s.ptrs[key]
	s.mu.Unlock()
	if ok && time.Since(e.at) < ptrTTL {
		return e.name
	}
	lctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	names, err := s.Resolver.LookupAddr(lctx, key)
	name := ""
	if err == nil && len(names) > 0 {
		name = strings.TrimSuffix(names[0], ".")
	}
	if err == nil || !errors.Is(lctx.Err(), context.DeadlineExceeded) {
		s.mu.Lock()
		s.ptrs[key] = ptrEntry{name: name, at: time.Now()}
		s.mu.Unlock()
	}
	return name
}

// Probe describes the system that runs the checks.
type Probe struct {
	Label    string
	Hostname string
	LocalIPs []string
	// Public is the address the server's traffic leaves from (nil if unknown).
	Public      *Info
	PublicError string
	PublicFrom  string // "detected", "manual"
	// Loc is where the server is drawn; LocFrom says why ("database", "fixed", "").
	Loc     Location
	LocFrom string
}

// Located reports whether the probe can be drawn on a map.
func (p Probe) Located() bool { return p.Loc.HasCoords }

// Probe gathers information about this server.
func (s *Service) Probe(ctx context.Context) Probe {
	st := s.Settings(ctx)
	p := Probe{LocalIPs: localIPs()}
	p.Hostname, _ = os.Hostname()
	p.Label = st.Label
	if p.Label == "" {
		p.Label = p.Hostname
	}
	switch st.PublicIPMode {
	case PublicManual:
		if ip := net.ParseIP(st.PublicIP); ip != nil {
			info := s.lookupWithPTR(ctx, ip, st.ReverseDNS)
			p.Public, p.PublicFrom = &info, "manual"
		}
	case PublicAuto:
		ip, err := s.publicIP(ctx, st.DetectURL)
		if ip != nil {
			info := s.lookupWithPTR(ctx, ip, st.ReverseDNS)
			p.Public, p.PublicFrom = &info, "detected"
		} else {
			p.PublicError = err
		}
	}
	if st.FixedLocation {
		p.Loc = Location{City: st.City, Country: st.Country, Lat: st.Lat, Lon: st.Lon, HasCoords: true}
		p.LocFrom = "fixed"
	} else if p.Public != nil && p.Public.Loc.HasCoords {
		p.Loc, p.LocFrom = p.Public.Loc, "database"
	}
	return p
}

func (s *Service) lookupWithPTR(ctx context.Context, ip net.IP, ptr bool) Info {
	info := s.DB.Lookup(ip)
	if ptr {
		info.PTR = s.ptr(ctx, ip)
	}
	return info
}

// RefreshPublicIP forgets the detected public address.
func (s *Service) RefreshPublicIP() {
	s.mu.Lock()
	s.public = publicEntry{}
	s.mu.Unlock()
}

func (s *Service) publicIP(ctx context.Context, url string) (net.IP, string) {
	s.mu.Lock()
	e := s.public
	s.mu.Unlock()
	if e.source == url && !e.at.IsZero() {
		if (e.ip != nil && time.Since(e.at) < publicTTL) || (e.ip == nil && time.Since(e.at) < failTTL) {
			return e.ip, e.err
		}
	}
	done := make(chan publicEntry, 1)
	go func() {
		ent := publicEntry{source: url, at: time.Now()}
		ip, err := s.detect(url)
		if err != nil {
			ent.err = err.Error()
		}
		ent.ip = ip
		s.mu.Lock()
		s.public = ent
		s.mu.Unlock()
		done <- ent
	}()
	wait, cancel := context.WithTimeout(ctx, lookupLimit)
	defer cancel()
	select {
	case ent := <-done:
		return ent.ip, ent.err
	case <-wait.Done():
		if e.ip != nil {
			return e.ip, ""
		}
		return nil, "still detecting, reload in a moment"
	}
}

func (s *Service) detect(url string) (net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MonitorIQ (self-hosted uptime monitor)")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("public IP detection failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("public IP detection failed: HTTP %d", resp.StatusCode)
	}
	ip := net.ParseIP(strings.TrimSpace(string(body)))
	if ip == nil {
		return nil, errors.New("public IP detection returned something that is not an address")
	}
	return ip, nil
}

// localIPs lists the server's own non-loopback addresses, the one used for
// outgoing traffic first.
func localIPs() []string {
	var out []string
	seen := map[string]bool{}
	add := func(ip net.IP) {
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || seen[ip.String()] {
			return
		}
		seen[ip.String()] = true
		out = append(out, ip.String())
	}
	// A UDP "connection" sends nothing; it only picks the outgoing interface.
	for _, probe := range []string{"192.0.2.1:9", "[2001:db8::1]:9"} {
		if c, err := net.Dial("udp", probe); err == nil {
			if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
				add(a.IP)
			}
			_ = c.Close()
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				add(n.IP)
			}
		}
	}
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}
