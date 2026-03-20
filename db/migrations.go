/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package db

import (
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func (db *DB) migrate() error {
	// Ensure migrations table exists (bootstrap)
	_, err := db.writer.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}

	// Read all migration ho_files
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("reading migrations dir: %w", err)
	}

	// Sort by filename
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		name := entry.Name()

		// Check if already applied
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM migrations WHERE name = ?", name).Scan(&count)
		if err != nil {
			return fmt.Errorf("checking migration %s: %w", name, err)
		}
		if count > 0 {
			continue
		}

		// Read and execute
		content, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}

		// Split by semicolons and execute each statement
		statements := strings.Split(string(content), ";")
		for _, stmt := range statements {
			stmt = strings.TrimSpace(stmt)
			// Strip leading comment lines
			for strings.HasPrefix(stmt, "--") {
				if idx := strings.Index(stmt, "\n"); idx >= 0 {
					stmt = strings.TrimSpace(stmt[idx+1:])
				} else {
					stmt = ""
					break
				}
			}
			if stmt == "" {
				continue
			}
			if _, err := db.writer.Exec(stmt); err != nil {
				return fmt.Errorf("executing migration %s: %w\nStatement: %s", name, err, stmt)
			}
		}

		// Record migration
		_, err = db.writer.Exec("INSERT INTO migrations (name) VALUES (?)", name)
		if err != nil {
			return fmt.Errorf("recording migration %s: %w", name, err)
		}

		slog.Info("applied migration", "name", name)
	}

	return nil
}
