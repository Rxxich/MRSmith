// Command mrsmith runs the rule-provider panel.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mrsmith/internal/audit"
	"mrsmith/internal/auth"
	"mrsmith/internal/config"
	"mrsmith/internal/downloads"
	"mrsmith/internal/fetch"
	"mrsmith/internal/notify"
	"mrsmith/internal/rules"
	"mrsmith/internal/rulesets"
	"mrsmith/internal/server"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
	"mrsmith/internal/version"
	"mrsmith/internal/webui"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "healthcheck":
		err = healthcheck()
	case "reset-password":
		name := ""
		if len(os.Args) > 2 {
			name = os.Args[2]
		}
		err = resetPassword(name)
	case "version", "--version", "-v":
		fmt.Println(version.Version)
	default:
		err = fmt.Errorf("неизвестная команда %q; доступны: serve, healthcheck, reset-password, version", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mrsmith:", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

func openDB(cfg *config.Config) (*store.DB, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("каталог данных %s: %w", cfg.DataDir, err)
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "mrsmith.db"))
	if err != nil {
		return nil, fmt.Errorf("база данных: %w (если панель запущена, остановите её)", err)
	}
	return db, nil
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	authSvc := auth.New(db, cfg.SessionTTL, cfg.SessionIdle)
	if _, _, err := authSvc.Bootstrap(os.Stdout); err != nil {
		return err
	}
	for _, k := range []string{"MRSMITH_ADMIN_USER", "MRSMITH_ADMIN_PASSWORD"} {
		if os.Getenv(k) != "" {
			log.Warn("переменная больше не используется: логин и пароль создаются при первом запуске, уберите её из .env", "var", k)
		}
	}
	st, err := storage.New(db, cfg.DataDir, cfg.MaxUploadBytes)
	if err != nil {
		return err
	}
	fetcher := fetch.New(cfg.FetchTimeout, cfg.MaxFetchBytes, cfg.AllowPrivateFetch, cfg.FetchAllow, version.Version,
		fetch.WithGitHubProxy(cfg.GitHubProxy))
	if cfg.GitHubProxy != nil {
		log.Info("GitHub загружается через прокси", "proxy", cfg.GitHubProxy.String())
	}
	mgr, err := rulesets.NewManager(db, st, fetcher, cfg.DataDir, cfg.MaxFetchBytes, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var tg *notify.Telegram
	if cfg.Telegram.Enabled() {
		tg = notify.NewTelegram(cfg.Telegram.Token, cfg.Telegram.Chats, cfg.Telegram.Proxy, log)
		mgr.OnChange(func(rs *rulesets.Ruleset, ev *rulesets.Event) {
			files := make([]string, 0, len(rs.Formats))
			for _, f := range rs.Formats {
				if o, ok := rs.State.Outputs[f]; ok && o.Error == "" {
					files = append(files, rs.Slug+"."+string(f))
				}
			}
			if len(files) == 0 {
				files = append(files, rs.Slug+"."+string(rules.OutMRS))
			}
			tg.Enqueue(notify.ChangeMessage(notify.Change{
				Name: rs.Name, Files: files, Trigger: ev.Trigger,
				Added: ev.Added, Removed: ev.Removed, Total: ev.Total,
				SampleAdded: ev.SampleAdded, SampleRemoved: ev.SampleRemoved, Warning: ev.Message,
			}))
		})
		go tg.Run(ctx)
		proxy := "нет"
		if cfg.Telegram.Proxy != nil {
			proxy = cfg.Telegram.Proxy.String()
		}
		log.Info("уведомления в Telegram включены", "recipients", len(cfg.Telegram.Chats), "proxy", proxy)
	}
	dl := downloads.New(db, mgr, st, log)
	go dl.Run(ctx)

	srv, err := server.New(server.Deps{
		Config: cfg, Auth: authSvc, Rulesets: mgr, Storage: st, Fetch: fetcher,
		Audit:     audit.New(mgr, st, cfg.MaxFetchBytes),
		Downloads: dl, Telegram: tg,
		Log: log, Version: version.Version, UI: webui.FS(), Ctx: ctx,
	})
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	go mgr.Run(ctx)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				authSvc.Cleanup()
			}
		}
	}()

	errc := make(chan error, 1)
	go func() {
		log.Info("MRSmith запущен", "version", version.Version, "listen", cfg.Listen, "panel", cfg.PanelPath, "data", cfg.DataDir)
		errc <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("остановка…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		// Save counts of requests that finished during shutdown.
		dl.Flush()
	}
	return nil
}

func healthcheck() error {
	listen := os.Getenv("MRSMITH_LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func resetPassword(username string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	return auth.New(db, cfg.SessionTTL, cfg.SessionIdle).ResetPassword(username, os.Stdout)
}
