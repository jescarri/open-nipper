// Package sqlite provides the SQLite-backed implementation of the datastore.Repository interface.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed migrations
var migrationsFS embed.FS

// Store is the SQLite-backed repository.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path, runs pending migrations, and returns a Store.
func Open(path string, walMode bool, busyTimeoutMS int) (*Store, error) {
	// Build DSN with pragmas.
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=%d&_foreign_keys=ON", path, busyTimeoutMS)
	if !walMode {
		dsn = fmt.Sprintf("%s?_busy_timeout=%d&_foreign_keys=ON", path, busyTimeoutMS)
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}

	// Single writer connection to avoid "database is locked" with WAL.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.runMigrations(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: migrations: %w", err)
	}

	return s, nil
}

// Ping verifies the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// runMigrations reads SQL files from the embedded migrations FS and applies any unapplied ones in order.
func (s *Store) runMigrations(ctx context.Context) error {
	// Ensure the schema_migrations table exists before doing anything else.
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	// Read applied versions.
	rows, err := s.db.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("query migrations: %w", err)
	}
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Collect migration files from the embedded migrations/ directory.
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	type migration struct {
		version int
		name    string
	}
	var migrations []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var v int
		fmt.Sscanf(e.Name(), "%d", &v)
		if v == 0 {
			continue
		}
		migrations = append(migrations, migration{version: v, name: e.Name()})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}

		sqlBytes, err := migrationsFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.name, err)
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			m.version, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
	}

	return nil
}

// now returns the current UTC time formatted for SQLite TEXT storage.
func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
