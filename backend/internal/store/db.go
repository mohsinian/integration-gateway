package store

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunMigrations reads .sql files from dir and applies any that haven't been
// recorded in schema_version yet. If a previously-run migration has status
// "failed" the function returns an error — manual intervention required.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	// 1. Ensure schema_version table exists.
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		id            SERIAL PRIMARY KEY,
		filename      TEXT NOT NULL UNIQUE,
		status        TEXT NOT NULL,
		error_message TEXT,
		applied_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	// 2. Check for any previously-failed migration.
	var failedCount int
	if err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM schema_version WHERE status = 'failed'",
	).Scan(&failedCount); err != nil {
		return fmt.Errorf("check failed migrations: %w", err)
	}
	if failedCount > 0 {
		rows, _ := pool.Query(ctx,
			"SELECT filename, error_message FROM schema_version WHERE status = 'failed'",
		)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var fn, errMsg string
				rows.Scan(&fn, &errMsg)
				log.Printf("  FAILED: %s — %s", fn, errMsg)
			}
		}
		return fmt.Errorf("%d previously failed migration(s) — fix and retry manually", failedCount)
	}

	// 3. List already-applied migrations.
	applied := make(map[string]bool)
	rows, err := pool.Query(ctx,
		"SELECT filename FROM schema_version WHERE status = 'success'",
	)
	if err != nil {
		return fmt.Errorf("list applied migrations: %w", err)
	}
	for rows.Next() {
		var fn string
		if err := rows.Scan(&fn); err != nil {
			rows.Close()
			return err
		}
		applied[fn] = true
	}
	rows.Close()

	// 4. Read migration files from dir, sorted by name.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	if len(files) == 0 {
		return fmt.Errorf("no .sql files found in %s", dir)
	}

	// 5. Run each pending migration inside a transaction.
	for _, fn := range files {
		if applied[fn] {
			log.Printf("Skipping (already applied): %s", fn)
			continue
		}
		log.Printf("Running migration: %s", fn)

		content, err := os.ReadFile(filepath.Join(dir, fn))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", fn, err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", fn, err)
		}

		if _, err := tx.Exec(ctx, string(content)); err != nil {
			errMsg := err.Error()
			tx.Exec(ctx,
				"INSERT INTO schema_version (filename, status, error_message) VALUES ($1, 'failed', $2) ON CONFLICT (filename) DO UPDATE SET status='failed', error_message=$2, applied_at=NOW()",
				fn, errMsg,
			)
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s failed: %s", fn, errMsg)
		}

		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_version (filename, status) VALUES ($1, 'success') ON CONFLICT (filename) DO UPDATE SET status='success', error_message=NULL, applied_at=NOW()",
			fn,
		); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", fn, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", fn, err)
		}
		log.Printf("  OK: %s", fn)
	}

	return nil
}

// MigrationRecord represents a row in schema_version.
type MigrationRecord struct {
	Filename     string
	Status       string
	ErrorMessage *string
}

// GetMigrationStatus returns all records from schema_version.
func GetMigrationStatus(ctx context.Context, pool *pgxpool.Pool) ([]MigrationRecord, error) {
	rows, err := pool.Query(ctx,
		"SELECT filename, status, error_message FROM schema_version ORDER BY id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []MigrationRecord
	for rows.Next() {
		var r MigrationRecord
		if err := rows.Scan(&r.Filename, &r.Status, &r.ErrorMessage); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}
