package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens a SQLite database at the given path with WAL mode and foreign keys enabled.
func Open(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}

	// Enable foreign key enforcement (off by default in SQLite).
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	return db, nil
}

// Migrate runs all pending SQL migrations in order.
func Migrate(db *sql.DB) error {
	// Ensure the migrations tracking table exists.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (
		filename TEXT PRIMARY KEY,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create _migrations table: %w", err)
	}

	// Read all migration files sorted by name.
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		// Check if already applied.
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM _migrations WHERE filename = ?", entry.Name()).Scan(&count); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if count > 0 {
			continue
		}

		// Read and execute the migration inside a transaction.
		content, err := fs.ReadFile(migrations, "migrations/"+entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin transaction for %s: %w", entry.Name(), err)
		}

		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("execute migration %s: %w", entry.Name(), err)
		}

		if _, err := tx.Exec("INSERT INTO _migrations (filename) VALUES (?)", entry.Name()); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}

		log.Printf("applied migration: %s", entry.Name())
	}

	return nil
}
