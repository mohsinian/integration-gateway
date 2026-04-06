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
	"github.com/mohsinian/integration-gateway/internal/api"
	"github.com/mohsinian/integration-gateway/internal/client"
	"github.com/mohsinian/integration-gateway/internal/logger"
	"github.com/mohsinian/integration-gateway/internal/lookup"
	"github.com/mohsinian/integration-gateway/internal/resilience"
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

	// --- Clients -----------------------------------------------------------
	propertyClient := client.NewPropertyClient(
		envOr("MOCK_PROPERTY_URL", "http://localhost:9001"),
		logs.Server,
	)
	courtClient := client.NewCourtClient(
		envOr("MOCK_COURT_URL", "http://localhost:9002"),
		logs.Server,
	)
	scraClient := client.NewSCRAClient(
		envOr("MOCK_SCRA_URL", "http://localhost:9003"),
		logs.Server,
	)

	// --- Resilience --------------------------------------------------------
	propertyCB := resilience.NewCircuitBreaker("property_records", 5, 30*time.Second)
	courtCB := resilience.NewCircuitBreaker("court_records", 5, 30*time.Second)
	scraCB := resilience.NewCircuitBreaker("scra", 5, 30*time.Second)
	courtLimiter := resilience.NewRateLimiter(2) // 2 req/sec

	// --- Store + Orchestrator ----------------------------------------------
	lookupStore := store.NewLookupStore(pool)
	orchestrator := lookup.NewOrchestrator(
		lookupStore,
		propertyClient, courtClient, scraClient,
		propertyCB, courtCB, scraCB,
		courtLimiter,
		logs.Server, logs.Error,
	)

	// --- HTTP Handlers + Router --------------------------------------------
	mux := http.NewServeMux()
	handler := api.NewHandler(
		orchestrator,
		lookupStore,
		pool,
		[]*resilience.CircuitBreaker{propertyCB, courtCB, scraCB},
		logs,
	)
	handler.RegisterRoutes(mux)

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
