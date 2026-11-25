package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/cmspeedau/gamedepot/internal/auth"
	"github.com/cmspeedau/gamedepot/internal/config"
	"github.com/cmspeedau/gamedepot/internal/database"
	db "github.com/cmspeedau/gamedepot/internal/database/db"
	"github.com/cmspeedau/gamedepot/internal/maintenance"
	"github.com/cmspeedau/gamedepot/internal/server"
	"github.com/cmspeedau/gamedepot/internal/version"
	gamedepot "github.com/cmspeedau/gamedepot/sql"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(server.NewContextHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))))

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	pool, err := database.NewPool(ctx, cfg)
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		db := stdlib.OpenDB(*pool.Config().ConnConfig)
		fsys, err := fs.Sub(gamedepot.Migrations, "migrations")
		if err != nil {
			slog.Error("migration fs", "err", err)
			os.Exit(1)
		}
		goose.SetBaseFS(fsys)
		if err := goose.SetDialect("postgres"); err != nil {
			slog.Error("goose dialect", "err", err)
			os.Exit(1)
		}
		if err := goose.Up(db, "."); err != nil {
			slog.Error("migrate", "err", err)
			os.Exit(1)
		}
		db.Close()
		slog.Info("migrations applied")
		return
	}

	if cfg.AutoMigrate {
		db := stdlib.OpenDB(*pool.Config().ConnConfig)
		fsys, err := fs.Sub(gamedepot.Migrations, "migrations")
		if err != nil {
			slog.Error("migration fs", "err", err)
			os.Exit(1)
		}
		goose.SetBaseFS(fsys)
		if err := goose.SetDialect("postgres"); err != nil {
			slog.Error("goose dialect", "err", err)
			os.Exit(1)
		}
		if err := goose.Up(db, "."); err != nil {
			slog.Error("migrate", "err", err)
			os.Exit(1)
		}
		db.Close()
		slog.Info("migrations applied")
	}

	if token, err := auth.EnsureSetupToken(ctx, pool, cfg.ExternalURL); err != nil {
		slog.Error("setup token", "err", err)
	} else if token != "" {
		slog.Info("setup required", "url", cfg.ExternalURL+"/setup?token="+token)
	}

	srv := server.New(cfg, pool)

	if cfg.IntegrityInterval > 0 || cfg.CleanupInterval > 0 {
		mroot := filepath.Join(cfg.DataDir, "depot")
		loop := &maintenance.Loop{
			Job:       maintenance.NewJob(db.New(pool), mroot),
			Integrity: cfg.IntegrityInterval,
			Cleanup:   cfg.CleanupInterval,
		}
		loop.Start(ctx)
		slog.Info("maintenance loop started",
			"integrity_interval", cfg.IntegrityInterval.String(),
			"cleanup_interval", cfg.CleanupInterval.String(),
			"depot_root", mroot)
	}

	if cfg.StoreEnabled && cfg.StoreUpdateInterval > 0 {
		go func() {
			ticker := time.NewTicker(cfg.StoreUpdateInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					report, err := srv.RunStoreUpdateCheck(ctx)
					if err != nil {
						slog.ErrorContext(ctx, "store update check", "err", err)
						continue
					}
					if len(report.Outdated) > 0 {
						slog.InfoContext(ctx, "store update check: updates available", "count", len(report.Outdated))
					}
				}
			}
		}()
		slog.Info("store update check started", "interval", cfg.StoreUpdateInterval.String())
	}

	httpServer := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("listening", "port", cfg.Port, "external", cfg.ExternalURL, "version", version.Version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("shutting down")
	cancelCtx()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
	pool.Close()
}
