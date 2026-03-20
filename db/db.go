/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/markdr-hue/HO/internal/sqliteretry"
	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB         // read pool (concurrent reads)
	writer  *sql.DB // write pool (single connection, serializes writes)
}

func Open(path string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	// modernc.org/sqlite uses _pragma=name(value) syntax (not _name=value which is mattn/go-sqlite3).
	readDSN := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)", path)
	writeDSN := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_txlock=immediate", path)

	// Read pool — concurrent reads via WAL mode.
	readDB, err := sql.Open("sqlite", readDSN)
	if err != nil {
		return nil, fmt.Errorf("opening database (read): %w", err)
	}
	readDB.SetMaxOpenConns(4)
	if err := readDB.Ping(); err != nil {
		readDB.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	// Write pool — single connection serializes all writes at the DB level.
	writeDB, err := sql.Open("sqlite", writeDSN)
	if err != nil {
		readDB.Close()
		return nil, fmt.Errorf("opening database (write): %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	if err := writeDB.Ping(); err != nil {
		readDB.Close()
		writeDB.Close()
		return nil, fmt.Errorf("pinging database (write): %w", err)
	}

	db := &DB{DB: readDB, writer: writeDB}

	// Run migrations
	if err := db.migrate(); err != nil {
		readDB.Close()
		writeDB.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	slog.Info("database initialized", "path", path)
	return db, nil
}

// Close closes both the read and write pools.
func (db *DB) Close() error {
	db.writer.Close()
	return db.DB.Close()
}

// Writer returns the single-connection write pool for direct use.
func (db *DB) Writer() *sql.DB {
	return db.writer
}

// ExecWrite executes a write query through the serialized writer pool with retry.
func (db *DB) ExecWrite(query string, args ...interface{}) (sql.Result, error) {
	return sqliteretry.Exec(db.writer, query, args...)
}

// BeginWriteTx starts a transaction on the writer pool with retry.
func (db *DB) BeginWriteTx() (*sql.Tx, error) {
	return sqliteretry.BeginTx(db.writer)
}

// GetSetting retrieves a setting value by key
func (db *DB) GetSetting(key string) (string, error) {
	var value string
	err := db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	return value, err
}

// SetSetting sets a setting value
func (db *DB) SetSetting(key, value string) error {
	_, err := db.ExecWrite(
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value,
	)
	return err
}
