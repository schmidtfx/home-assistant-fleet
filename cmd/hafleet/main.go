// Command hafleet is a fleet manager for Home Assistant instances.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
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

	"golang.org/x/term"

	"github.com/schmidtfx/home-assistant-fleet/internal/actions"
	"github.com/schmidtfx/home-assistant-fleet/internal/ai"
	"github.com/schmidtfx/home-assistant-fleet/internal/api"
	"github.com/schmidtfx/home-assistant-fleet/internal/auth"
	"github.com/schmidtfx/home-assistant-fleet/internal/config"
	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/rollout"
	"github.com/schmidtfx/home-assistant-fleet/internal/secrets"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
	"github.com/schmidtfx/home-assistant-fleet/web"
)

const usage = `usage: hafleet <command> [flags]

commands:
  serve                     run the server
  admin create <username> [-role viewer|operator|admin]
                            create a user (password from prompt or HAFLEET_ADMIN_PASSWORD; default role admin)
  admin passwd <username>   change a user's password and revoke their sessions
  admin role <username> <viewer|operator|admin>
                            change a user's role and revoke their sessions
  genkey                    print a new random secret key

Run 'hafleet serve -h' for server flags.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "admin":
		err = admin(os.Args[2:])
	case "genkey":
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		fmt.Println(hex.EncodeToString(b))
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	box, err := secrets.New(cfg.SecretKey)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if n, err := st.CountUsers(ctx); err == nil && n == 0 {
		log.Warn("no users exist yet; create one with: hafleet admin create <username>")
	}

	fm := fleet.New(st, box, fleet.Options{
		ResyncInterval: cfg.ResyncInterval, BackupMaxAge: cfg.BackupMaxAge, Logger: log,
		Health: tasks.HealthOptions{DiskFreeMinPercent: cfg.DiskFreeMin, BatteryMinPercent: cfg.BatteryMin},
	})
	if err := fm.Start(ctx); err != nil {
		return err
	}
	am := auth.NewManager(st, cfg.SecureCookies, cfg.AllowedOrigins)
	go am.CleanupLoop(ctx)

	aiSvc, err := newAIService(cfg, st, fm, log)
	if err != nil {
		return err
	}
	if aiSvc != nil {
		go aiSvc.Run(ctx)
		log.Info("AI features enabled", "provider", aiSvc.Status().Provider, "model", aiSvc.Status().Model)
	}

	runner := actions.NewRunner(fm, st, log)
	ropts := rollout.Options{Logger: log}
	if aiSvc != nil {
		ropts.HealthCheck = func(ctx context.Context, rec actions.Record) (string, []string, error) {
			h, err := aiSvc.HealthCheck(ctx, "", rec, true)
			if errors.Is(err, ai.ErrDisabled) {
				return rollout.VerdictSkipped, nil, nil // this instance has not opted in to AI
			}
			if err != nil {
				return "", nil, err
			}
			return h.Verdict, h.Findings, nil
		}
	}
	rollouts := rollout.New(st, fm, runner, ropts)
	if err := rollouts.Start(ctx); err != nil {
		return fmt.Errorf("start rollouts: %w", err)
	}

	srv := &api.Server{
		Store: st, Box: box, Auth: am, Fleet: fm,
		Actions: runner, Rollouts: rollouts,
		AI:     aiSvc,
		Static: web.Dist(), Log: log,
		HSTS: cfg.TLSCert != "", DevMode: cfg.DevMode, MetricsToken: cfg.MetricsToken,
	}
	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // SSE handler clears its own deadline
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()

	log.Info("listening", "addr", cfg.Listen, "tls", cfg.TLSCert != "")
	if cfg.TLSCert != "" {
		err = hs.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		if cfg.SecureCookies {
			log.Info("serving plain HTTP with Secure cookies: put hafleet behind an HTTPS reverse proxy (or use -secure-cookies=false for local dev)")
		}
		err = hs.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// newAIService builds the configured AI provider, or returns nil when AI is disabled.
func newAIService(cfg *config.Config, st *store.Store, fm *fleet.Manager, log *slog.Logger) (*ai.Service, error) {
	var p ai.Provider
	switch cfg.AIProvider {
	case "":
		return nil, nil
	case "anthropic":
		p = ai.NewAnthropic(ai.AnthropicOptions{Model: cfg.AIModel, Fallbacks: cfg.AIFallbacks})
	case "ollama":
		o, err := ai.NewOllama(ai.OllamaOptions{URL: cfg.OllamaURL, Model: cfg.OllamaModel, NumCtx: cfg.OllamaNumCtx})
		if err != nil {
			return nil, err
		}
		p = o
	}
	return ai.NewService(p, st, fm, ai.Options{
		Effort: cfg.AIEffort, ChatLimit: cfg.AIChatLimit, AutoReview: cfg.AIAutoReview, Logger: log,
	}), nil
}

func admin(args []string) error {
	if len(args) < 2 || (args[0] != "create" && args[0] != "passwd" && args[0] != "role") {
		return errors.New("usage: hafleet admin create|passwd|role <username> [...] [-data-dir dir]")
	}
	username := strings.TrimSpace(args[1])
	if username == "" {
		return errors.New("username must not be empty")
	}
	rest := args[2:]
	var newRole auth.Role
	if args[0] == "role" {
		if len(rest) < 1 {
			return errors.New("usage: hafleet admin role <username> <viewer|operator|admin>")
		}
		r, ok := auth.ParseRole(rest[0])
		if !ok {
			return fmt.Errorf("unknown role %q (viewer, operator or admin)", rest[0])
		}
		newRole, rest = r, rest[1:]
	}
	fs := flag.NewFlagSet("admin", flag.ContinueOnError)
	dataDir := fs.String("data-dir", envOr("HAFLEET_DATA_DIR", "./data"), "data directory")
	roleFlag := fs.String("role", string(auth.RoleAdmin), "role for 'create': viewer, operator or admin")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if args[0] == "create" {
		r, ok := auth.ParseRole(*roleFlag)
		if !ok {
			return fmt.Errorf("unknown role %q (viewer, operator or admin)", *roleFlag)
		}
		newRole = r
	}
	st, err := store.Open(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if args[0] == "role" {
		if err := st.SetUserRole(ctx, username, string(newRole)); err != nil {
			return fmt.Errorf("set role: %w", err)
		}
		if err := st.DeleteUserSessions(ctx, username); err != nil {
			return err
		}
		fmt.Printf("role of %s set to %s, sessions revoked\n", username, newRole)
		return nil
	}
	pw, err := readPassword()
	if err != nil {
		return err
	}
	if len(pw) < 12 {
		return errors.New("password must be at least 12 characters")
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	if args[0] == "create" {
		if err := st.CreateUser(ctx, username, hash); err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		if err := st.SetUserRole(ctx, username, string(newRole)); err != nil {
			return fmt.Errorf("set role: %w", err)
		}
		fmt.Printf("user created: %s (%s)\n", username, newRole)
		return nil
	}
	if err := st.SetUserPassword(ctx, username, hash); err != nil {
		return err
	}
	if err := st.DeleteUserSessions(ctx, username); err != nil {
		return err
	}
	fmt.Println("password changed and sessions revoked for", username)
	return nil
}

func readPassword() (string, error) {
	if pw := os.Getenv("HAFLEET_ADMIN_PASSWORD"); pw != "" {
		return pw, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	a, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("passwords do not match")
	}
	return string(a), nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
