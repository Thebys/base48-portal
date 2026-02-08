// Package migrate runs embedded SQL migrations against a SQLite database.
// It tracks applied migrations in a schema_migrations table and auto-detects
// the current state of existing databases on first run.
package migrate

import (
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strconv"
	"strings"
)

// Run applies all pending migrations from the embedded filesystem.
// Safe to call on every startup — already-applied migrations are skipped.
func Run(db *sql.DB, fsys fs.FS) error {
	// Create tracking table
	created, err := ensureTrackingTable(db)
	if err != nil {
		return fmt.Errorf("migrate: create tracking table: %w", err)
	}

	// On first run against an existing DB, detect what's already applied
	if created {
		if err := detectExisting(db); err != nil {
			return fmt.Errorf("migrate: detect existing state: %w", err)
		}
	}

	// Read and sort migration files
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("migrate: read dir: %w", err)
	}

	var files []fs.DirEntry
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})

	for _, f := range files {
		version := parseVersion(f.Name())
		if version == 0 {
			continue
		}

		// Skip 002 (data import — requires external file, one-time only)
		if version == 2 {
			continue
		}

		if isApplied(db, version) {
			log.Printf("[Migrate] %s — already applied", f.Name())
			continue
		}

		content, err := fs.ReadFile(fsys, f.Name())
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", f.Name(), err)
		}

		if _, err := db.Exec(string(content)); err != nil {
			return fmt.Errorf("migrate: apply %s: %w", f.Name(), err)
		}

		db.Exec("INSERT OR IGNORE INTO schema_migrations (version) VALUES (?)", version)
		log.Printf("[Migrate] %s — applied", f.Name())
	}

	return nil
}

// ensureTrackingTable creates schema_migrations if it doesn't exist.
// Returns true if the table was just created (first run).
func ensureTrackingTable(db *sql.DB) (bool, error) {
	if hasTable(db, "schema_migrations") {
		return false, nil
	}
	_, err := db.Exec(`CREATE TABLE schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	return err == nil, err
}

// detectExisting checks which migrations are already applied by inspecting
// the database schema. Called only on first run (when schema_migrations was
// just created). This handles upgrading existing databases to tracked migrations.
func detectExisting(db *sql.DB) error {
	checks := []struct {
		version int
		check   func(*sql.DB) bool
	}{
		{1, func(db *sql.DB) bool { return hasTable(db, "users") }},
		{3, func(db *sql.DB) bool { return hasTable(db, "system_logs") }},
		{5, func(db *sql.DB) bool { return hasTable(db, "projects") }},
		{6, func(db *sql.DB) bool { return hasColumn(db, "payments", "dismissed_at") }},
		{7, func(db *sql.DB) bool { return hasTable(db, "project_vs") }},
		{8, func(db *sql.DB) bool { return hasTable(db, "email_outbox") }},
		{9, func(db *sql.DB) bool { return hasColumn(db, "users", "locale") }},
		{10, func(db *sql.DB) bool { return hasIndex(db, "idx_payments_identification") }},
	}

	for _, c := range checks {
		if c.check(db) {
			db.Exec("INSERT OR IGNORE INTO schema_migrations (version) VALUES (?)", c.version)
			log.Printf("[Migrate] Detected migration %03d already applied", c.version)
		}
	}
	return nil
}

// parseVersion extracts the numeric prefix from a filename like "007_foo.sql" → 7.
func parseVersion(name string) int {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) == 0 {
		return 0
	}
	v, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	return v
}

func isApplied(db *sql.DB, version int) bool {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&count)
	return count > 0
}

func hasTable(db *sql.DB, name string) bool {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&count)
	return count > 0
}

func hasColumn(db *sql.DB, table, column string) bool {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", table, column).Scan(&count)
	return count > 0
}

func hasIndex(db *sql.DB, name string) bool {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&count)
	return count > 0
}
