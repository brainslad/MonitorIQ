// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

// Package config loads MonitorIQ settings from environment variables
// (normally provided by /etc/monitoriq/monitoriq.env through systemd).
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL string
	Listen      string
	BaseURL     string
	SecretKey   string
	DataDir     string
	// GeoIPDir holds the geolocation databases (city.mmdb, asn.mmdb).
	GeoIPDir string
	// CookieSecure forces the Secure flag on cookies (true) or turns it off
	// (false). When nil, it is decided per request: see web.Server.secureCookie.
	CookieSecure   *bool
	MaxConcurrency int
	TrustProxy     bool
}

// LoadEnvFile reads KEY=VALUE lines into the process environment without
// overriding variables that are already set. Missing files are ignored.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
	return sc.Err()
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(env(key, ""))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// envOptBool reads a boolean that may also be left unset or set to "auto"
// (nil).
func envOptBool(key string) *bool {
	switch strings.ToLower(env(key, "")) {
	case "1", "true", "yes", "on":
		t := true
		return &t
	case "0", "false", "no", "off":
		f := false
		return &f
	}
	return nil
}

// Load builds the configuration from the environment.
func Load() (*Config, error) {
	c := &Config{
		DatabaseURL:  env("MONITORIQ_DATABASE_URL", ""),
		Listen:       env("MONITORIQ_LISTEN", "127.0.0.1:8080"),
		BaseURL:      strings.TrimRight(env("MONITORIQ_BASE_URL", ""), "/"),
		SecretKey:    env("MONITORIQ_SECRET_KEY", ""),
		DataDir:      env("MONITORIQ_DATA_DIR", "/var/lib/monitoriq"),
		CookieSecure: envOptBool("MONITORIQ_COOKIE_SECURE"),
		TrustProxy:   envBool("MONITORIQ_TRUST_PROXY", false),
	}
	n, err := strconv.Atoi(env("MONITORIQ_MAX_CONCURRENCY", "50"))
	if err != nil || n < 1 {
		return nil, fmt.Errorf("invalid MONITORIQ_MAX_CONCURRENCY")
	}
	c.MaxConcurrency = n
	c.GeoIPDir = env("MONITORIQ_GEOIP_DIR", filepath.Join(c.DataDir, "geoip"))
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("MONITORIQ_DATABASE_URL is not set")
	}
	return c, nil
}
