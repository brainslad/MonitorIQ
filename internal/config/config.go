// Package config loads MonitorIQ settings from environment variables
// (normally provided by /etc/monitoriq/monitoriq.env through systemd).
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL    string
	Listen         string
	BaseURL        string
	SecretKey      string
	CookieSecure   bool
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
	case "1", "true", "yes", "on", "sim":
		return true
	case "0", "false", "no", "off", "nao", "não":
		return false
	}
	return def
}

// Load builds the configuration from the environment.
func Load() (*Config, error) {
	c := &Config{
		DatabaseURL:  env("MONITORIQ_DATABASE_URL", ""),
		Listen:       env("MONITORIQ_LISTEN", "127.0.0.1:8080"),
		BaseURL:      strings.TrimRight(env("MONITORIQ_BASE_URL", ""), "/"),
		SecretKey:    env("MONITORIQ_SECRET_KEY", ""),
		CookieSecure: envBool("MONITORIQ_COOKIE_SECURE", strings.HasPrefix(env("MONITORIQ_BASE_URL", ""), "https://")),
		TrustProxy:   envBool("MONITORIQ_TRUST_PROXY", false),
	}
	n, err := strconv.Atoi(env("MONITORIQ_MAX_CONCURRENCY", "50"))
	if err != nil || n < 1 {
		return nil, fmt.Errorf("MONITORIQ_MAX_CONCURRENCY inválido")
	}
	c.MaxConcurrency = n
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("MONITORIQ_DATABASE_URL não definido")
	}
	return c, nil
}
