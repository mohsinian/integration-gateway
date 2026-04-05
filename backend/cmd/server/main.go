package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mohsinian/integration-gateway/internal/logger"
	"github.com/mohsinian/integration-gateway/internal/store"
	"github.com/mohsinian/integration-gateway/seed"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// --- Logging -----------------------------------------------------------
	logDir := envOr("LOG_DIR", "logs")
	logs, err := logger.Init(logDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialise logging: %v\n", err)
		os.Exit(1)
	}
	defer logs.Close()

	log := logs.App // convenience alias for application-level logging

	// --- Database ----------------------------------------------------------
	dbURL := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		envOr("DB_USER", "gateway"),
		envOr("DB_PASSWORD", "gateway"),
		envOr("DB_HOST", "localhost"),
		envOr("DB_PORT", "5432"),
		envOr("DB_NAME", "integration_gateway"),
	)

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		logs.Error.Error("unable to connect to database", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		logs.Error.Error("unable to ping database", slog.String("error", err.Error()))
		os.Exit(1)
	}
	log.Info("connected to PostgreSQL")

	// --- Migrations --------------------------------------------------------
	migrationsDir := envOr("MIGRATIONS_DIR", "migrations")
	if err := store.RunMigrations(ctx, pool, migrationsDir, log); err != nil {
		logs.Error.Error("migration error", slog.String("error", err.Error()))
		os.Exit(1)
	}
	log.Info("migrations complete")

	// --- Seed ---------------------------------------------------------------
	casesFile := envOr("CASES_FILE", "seed/cases.json")
	if err := seed.Cases(ctx, pool, casesFile, log); err != nil {
		logs.Error.Error("seed error", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// --- HTTP Server -------------------------------------------------------
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			logs.Server.Error("health check failed — db down",
				slog.String("remote", r.RemoteAddr),
				slog.String("error", err.Error()),
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"status":"unhealthy","db":"down"}`)
			return
		}
		logs.Server.Info("health check", slog.String("remote", r.RemoteAddr))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"healthy","db":"up"}`)
	})

	addr := ":" + envOr("PORT", "8080")
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("server starting", slog.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logs.Error.Error("server error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)

	log.Info("server stopped")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
