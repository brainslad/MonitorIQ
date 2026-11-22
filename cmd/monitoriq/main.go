// Command monitoriq is a self-hosted uptime and service monitoring server.
//
// Copyright (C) 2026 Brainslad <https://github.com/brainslad>. Licensed under the GNU GPL v3 or later
// with an author attribution term (section 7(b)); see LICENSE and NOTICE.
// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/brainslad/monitoriq/internal/auth"
	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/config"
	"github.com/brainslad/monitoriq/internal/notify"
	"github.com/brainslad/monitoriq/internal/scheduler"
	"github.com/brainslad/monitoriq/internal/store"
	"github.com/brainslad/monitoriq/internal/web"
)

var version = "dev"

const defaultEnvFile = "/etc/monitoriq/monitoriq.env"

func usage() {
	fmt.Fprintf(os.Stderr, `MonitorIQ %s by Brainslad — Know before your users do.

Usage:
  monitoriq serve                         start the web server and the check scheduler
  monitoriq migrate                       apply database migrations
  monitoriq user set <username>           create a user or reset its password
                    [--password X]        (prompted on the terminal without --password)
  monitoriq user list                     list users (with sign-in source and 2FA)
  monitoriq user reset-2fa <username>     turn off two-factor authentication for a user
  monitoriq auth local-login on           show the password form again if SSO-only
                                          sign-in locks you out
  monitoriq check <type> <target> [port]  run a one-off check (diagnostics)
  monitoriq email test <address>          send a test e-mail with the saved settings
  monitoriq email digest                  send the certificate digest now
  monitoriq version                       print the version

Global options:
  --env <file>   configuration file (default: %s)
`, version, defaultEnvFile)
}

func main() {
	args := os.Args[1:]
	envFile := defaultEnvFile
	if v := os.Getenv("MONITORIQ_ENV_FILE"); v != "" {
		envFile = v
	}
	for len(args) >= 2 && args[0] == "--env" {
		envFile, args = args[1], args[2:]
	}
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	if err := config.LoadEnvFile(envFile); err != nil {
		fatal("cannot read %s: %v", envFile, err)
	}
	web.Version = version

	switch args[0] {
	case "serve":
		serve()
	case "migrate":
		st := openStore()
		defer st.Close()
		if err := st.Migrate(context.Background()); err != nil {
			fatal("%v", err)
		}
		fmt.Println("Database is up to date.")
	case "user":
		userCmd(args[1:])
	case "auth":
		authCmd(args[1:])
	case "check":
		checkCmd(args[1:])
	case "email":
		emailCmd(args[1:])
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}

func openStore() *store.Store {
	cfg, err := config.Load()
	if err != nil {
		fatal("%v", err)
	}
	st, err := store.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		fatal("%v", err)
	}
	return st
}

func serve() {
	cfg, err := config.Load()
	if err != nil {
		fatal("%v", err)
	}
	level := slog.LevelInfo
	if os.Getenv("MONITORIQ_DEBUG") == "1" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal("%v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		fatal("migrations: %v", err)
	}
	if n, err := st.CountUsers(ctx); err == nil && n == 0 {
		pw := randomPassword()
		if err := st.SetPasswordByUsername(ctx, "admin", pw); err == nil {
			log.Warn("no users existed: created the initial user", "username", "admin", "password", pw)
		}
	}

	box, keySrc, err := auth.LoadBox(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		fatal("secret key: %v", err)
	}
	log.Info("secret key loaded", "from", keySrc)

	mailer := notify.New(st, box, log, cfg.BaseURL)
	srv, err := web.New(st, cfg, box, log)
	if err != nil {
		fatal("%v", err)
	}
	srv.Notifier = mailer
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	sched := scheduler.New(st, cfg.MaxConcurrency, log)
	sched.OnTransition = mailer.Notify
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()
	mailDone := make(chan struct{})
	go func() { mailer.Run(ctx); close(mailDone) }()

	go func() {
		log.Info("MonitorIQ listening", "address", cfg.Listen, "version", version, "author", "Brainslad (https://github.com/brainslad/MonitorIQ)")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down…")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)
	select {
	case <-done:
	case <-sctx.Done():
	}
	select {
	case <-mailDone:
	case <-sctx.Done():
	}
}

func randomPassword() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return strings.NewReplacer("+", "x", "/", "y", "=", "").Replace(base64.StdEncoding.EncodeToString(b))
}

func userCmd(args []string) {
	if len(args) == 0 {
		fatal("usage: monitoriq user set <username> [--password X] | monitoriq user list | monitoriq user reset-2fa <username>")
	}
	st := openStore()
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		fatal("%v", err)
	}
	switch args[0] {
	case "list":
		users, err := st.ListUsers(ctx)
		if err != nil {
			fatal("%v", err)
		}
		for _, u := range users {
			tfa := "2fa-off"
			if u.TOTPEnabled {
				tfa = "2fa-on"
			}
			fmt.Printf("%-24s %-6s %s\n", u.Username, u.Source, tfa)
		}
	case "reset-2fa":
		if len(args) < 2 {
			fatal("enter the username")
		}
		u, err := st.GetUserByUsername(ctx, args[1])
		if err != nil {
			fatal("user %q not found", args[1])
		}
		if err := st.ResetTOTP(ctx, u.ID); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("Two-factor authentication turned off for %q; the user was signed out.\n", u.Username)
	case "set":
		fs := flag.NewFlagSet("user set", flag.ExitOnError)
		pw := fs.String("password", "", "password")
		if len(args) < 2 {
			fatal("enter the username")
		}
		name := args[1]
		_ = fs.Parse(args[2:])
		if *pw == "" {
			fmt.Print("New password: ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			*pw = strings.TrimRight(line, "\r\n")
		}
		if err := st.SetPasswordByUsername(ctx, name, *pw); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("User %q updated.\n", name)
	default:
		fatal("unknown subcommand: %s", args[0])
	}
}

func authCmd(args []string) {
	if len(args) != 2 || args[0] != "local-login" || (args[1] != "on" && args[1] != "off") {
		fatal("usage: monitoriq auth local-login on|off")
	}
	cfg, err := config.Load()
	if err != nil {
		fatal("%v", err)
	}
	box, _, err := auth.LoadBox(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		fatal("secret key: %v", err)
	}
	st := openStore()
	defer st.Close()
	ctx := context.Background()
	raw, err := st.GetSetting(ctx, auth.SettingsKey)
	if err != nil {
		fatal("%v", err)
	}
	as, err := auth.LoadSettings(raw, box)
	if err != nil {
		fatal("%v", err)
	}
	as.OIDC.HideLocalLogin = args[1] == "off"
	enc, err := as.Encode(box)
	if err == nil {
		err = st.SetSetting(ctx, auth.SettingsKey, enc)
	}
	if err != nil {
		fatal("%v", err)
	}
	fmt.Printf("Username/password form on the sign-in page: %s.\n", args[1])
}

func checkCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cfgJSON := fs.String("config", "{}", "type options as JSON, e.g. '{\"record_type\":\"MX\"}'")
	timeout := fs.Duration("timeout", 10*time.Second, "timeout")
	var pos []string
	for len(args) > 0 {
		_ = fs.Parse(args)
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	if len(pos) < 2 || !checks.ValidType(pos[0]) {
		fatal("usage: monitoriq check <http|ping|traceroute|dns|smtp|tls|tcp> <target> [port] [--config JSON]")
	}
	m := checks.Monitor{Type: pos[0], Target: pos[1], Timeout: *timeout}
	if len(pos) > 2 {
		_, _ = fmt.Sscan(pos[2], &m.Port)
	}
	if err := json.Unmarshal([]byte(*cfgJSON), &m.Config); err != nil {
		fatal("invalid --config: %v", err)
	}
	res := checks.Run(context.Background(), m)
	out, _ := json.MarshalIndent(map[string]any{
		"status": res.Status.String(), "latency_ms": res.LatencyMs, "message": res.Message, "details": res.Details,
	}, "", "  ")
	fmt.Println(string(out))
	if res.Status == checks.Down {
		os.Exit(1)
	}
}

func emailCmd(args []string) {
	if len(args) == 0 || (args[0] == "test" && len(args) != 2) || (args[0] != "test" && args[0] != "digest") {
		fatal("usage: monitoriq email test <address> | monitoriq email digest")
	}
	cfg, err := config.Load()
	if err != nil {
		fatal("%v", err)
	}
	st := openStore()
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		fatal("migrations: %v", err)
	}
	box, _, err := auth.LoadBox(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		fatal("secret key: %v", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	n := notify.New(st, box, log, cfg.BaseURL)
	es, err := n.Settings(ctx)
	if err != nil {
		fatal("e-mail settings: %v", err)
	}
	if args[0] == "digest" {
		if !es.Enabled {
			fatal("e-mail is turned off under Settings → Email")
		}
		sent, err := n.SendDigest(ctx, es)
		if err != nil {
			fatal("%v", err)
		}
		fmt.Printf("%d message(s) sent.\n", sent)
		return
	}
	if err := n.SendTest(ctx, es, args[1], "command line"); err != nil {
		fatal("%v", err)
	}
	fmt.Println("Test message sent to " + args[1] + ".")
}
