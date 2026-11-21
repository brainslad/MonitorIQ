// Command monitoriq is a self-hosted uptime and service monitoring server.
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

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/config"
	"github.com/brainslad/monitoriq/internal/scheduler"
	"github.com/brainslad/monitoriq/internal/store"
	"github.com/brainslad/monitoriq/internal/web"
)

var version = "dev"

const defaultEnvFile = "/etc/monitoriq/monitoriq.env"

func usage() {
	fmt.Fprintf(os.Stderr, `MonitorIQ %s

Utilização:
  monitoriq serve                         inicia o servidor web e o agendador de verificações
  monitoriq migrate                       aplica as migrações da base de dados
  monitoriq user set <utilizador>         cria um utilizador ou redefine a palavra-passe
                    [--password X]        (sem --password é pedida no terminal)
  monitoriq user list                     lista os utilizadores
  monitoriq check <tipo> <alvo> [porta]   executa uma verificação pontual (diagnóstico)
  monitoriq version                       mostra a versão

Opções globais:
  --env <ficheiro>   ficheiro de configuração (predefinido: %s)
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
		fatal("erro a ler %s: %v", envFile, err)
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
		fmt.Println("Base de dados atualizada.")
	case "user":
		userCmd(args[1:])
	case "check":
		checkCmd(args[1:])
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
	fmt.Fprintf(os.Stderr, "erro: "+format+"\n", a...)
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
		fatal("migrações: %v", err)
	}
	if n, err := st.CountUsers(ctx); err == nil && n == 0 {
		pw := randomPassword()
		if err := st.SetPasswordByUsername(ctx, "admin", pw); err == nil {
			log.Warn("nenhum utilizador existia: foi criado o utilizador inicial", "utilizador", "admin", "palavra-passe", pw)
		}
	}

	srv, err := web.New(st, cfg, log)
	if err != nil {
		fatal("%v", err)
	}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	sched := scheduler.New(st, cfg.MaxConcurrency, log)
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	go func() {
		log.Info("MonitorIQ a escutar", "endereço", cfg.Listen, "versão", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("servidor HTTP", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("a terminar…")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)
	select {
	case <-done:
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
		fatal("utilize: monitoriq user set <utilizador> [--password X] | monitoriq user list")
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
			fmt.Println(u.Username)
		}
	case "set":
		fs := flag.NewFlagSet("user set", flag.ExitOnError)
		pw := fs.String("password", "", "palavra-passe")
		if len(args) < 2 {
			fatal("indique o nome do utilizador")
		}
		name := args[1]
		_ = fs.Parse(args[2:])
		if *pw == "" {
			fmt.Print("Nova palavra-passe: ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			*pw = strings.TrimRight(line, "\r\n")
		}
		if err := st.SetPasswordByUsername(ctx, name, *pw); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("Utilizador %q atualizado.\n", name)
	default:
		fatal("subcomando desconhecido: %s", args[0])
	}
}

func checkCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cfgJSON := fs.String("config", "{}", "opções em JSON, ex.: '{\"record_type\":\"MX\"}'")
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
		fatal("utilize: monitoriq check <http|ping|traceroute|dns|smtp|tls|tcp> <alvo> [porta] [--config JSON]")
	}
	m := checks.Monitor{Type: pos[0], Target: pos[1], Timeout: *timeout}
	if len(pos) > 2 {
		_, _ = fmt.Sscan(pos[2], &m.Port)
	}
	if err := json.Unmarshal([]byte(*cfgJSON), &m.Config); err != nil {
		fatal("--config inválido: %v", err)
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
