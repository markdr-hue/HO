/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package db

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/markdr-hue/HO/internal/sqliteretry"
)

//go:embed site_migrations/*.sql
var siteMigrationFS embed.FS

// SiteDB wraps a per-site SQLite database with a dedicated writer pool.
// The read pool allows concurrent reads via WAL mode.
// The writer pool (single connection) serializes all writes at the connection level,
// eliminating SQLITE_BUSY errors without needing a Go-level mutex.
//
// The reader is unexported to prevent accidental writes through the read pool —
// all writes MUST go through Writer(), ExecWrite(), or BeginWriteTx().
type SiteDB struct {
	reader *sql.DB // read pool (concurrent reads) — use Reader() to access
	writer *sql.DB // write pool (single connection, serializes writes)
	SiteID int
}

// Reader returns the multi-connection read pool.
func (s *SiteDB) Reader() *sql.DB {
	return s.reader
}

// Writer returns the single-connection write pool for direct use.
func (s *SiteDB) Writer() *sql.DB {
	return s.writer
}

// Query delegates to the read pool.
func (s *SiteDB) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return s.reader.Query(query, args...)
}

// QueryRow delegates to the read pool.
func (s *SiteDB) QueryRow(query string, args ...interface{}) *sql.Row {
	return s.reader.QueryRow(query, args...)
}

// Ping pings the read pool.
func (s *SiteDB) Ping() error {
	return s.reader.Ping()
}

// Close closes both the reader and writer pools.
func (s *SiteDB) Close() error {
	wErr := s.writer.Close()
	rErr := s.reader.Close()
	if wErr != nil {
		return wErr
	}
	return rErr
}

// ExecWrite executes a write query through the serialized writer pool with retry.
func (s *SiteDB) ExecWrite(query string, args ...interface{}) (sql.Result, error) {
	return sqliteretry.Exec(s.writer, query, args...)
}

// BeginWriteTx starts a transaction on the writer pool with retry.
func (s *SiteDB) BeginWriteTx() (*sql.Tx, error) {
	return sqliteretry.BeginTx(s.writer)
}

// QueryWriter executes a query on the writer pool (for writes that return rows).
func (s *SiteDB) QueryWriter(query string, args ...interface{}) (*sql.Rows, error) {
	return s.writer.Query(query, args...)
}

// QueryRowWriter executes a query on the writer pool that returns a single row.
func (s *SiteDB) QueryRowWriter(query string, args ...interface{}) *sql.Row {
	return s.writer.QueryRow(query, args...)
}

// SiteDBManager handles the lifecycle of per-site databases.
type SiteDBManager struct {
	mu      sync.RWMutex
	dataDir string
	dbs     map[int]*SiteDB
}

// NewSiteDBManager creates a manager for per-site databases.
func NewSiteDBManager(dataDir string) *SiteDBManager {
	return &SiteDBManager{
		dataDir: dataDir,
		dbs:     make(map[int]*SiteDB),
	}
}

// dbPath returns the filesystem path for a site's database.
func (m *SiteDBManager) dbPath(siteID int) string {
	return filepath.Join(m.dataDir, "sites", fmt.Sprintf("%d", siteID), "site.db")
}

// Open opens (or returns a cached) site database. Runs migrations on first open.
func (m *SiteDBManager) Open(siteID int) (*SiteDB, error) {
	m.mu.RLock()
	if sdb, ok := m.dbs[siteID]; ok {
		m.mu.RUnlock()
		return sdb, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock.
	if sdb, ok := m.dbs[siteID]; ok {
		return sdb, nil
	}

	sdb, err := m.openSiteDB(siteID)
	if err != nil {
		return nil, err
	}

	m.dbs[siteID] = sdb
	return sdb, nil
}

// Create creates a new site database and runs migrations.
func (m *SiteDBManager) Create(siteID int) (*SiteDB, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sdb, ok := m.dbs[siteID]; ok {
		return sdb, nil
	}

	// Ensure the site directory exists.
	dbPath := m.dbPath(siteID)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("creating project db directory: %w", err)
	}

	sdb, err := m.openSiteDB(siteID)
	if err != nil {
		return nil, err
	}

	m.dbs[siteID] = sdb
	slog.Info("Project database created", "site_id", siteID, "path", dbPath)
	return sdb, nil
}

// Close closes a site's database and removes it from the cache.
func (m *SiteDBManager) Close(siteID int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sdb, ok := m.dbs[siteID]; ok {
		sdb.writer.Close()
		sdb.reader.Close()
		delete(m.dbs, siteID)
		slog.Info("project database closed", "site_id", siteID)
	}
}

// Delete closes a site's database and removes the DB file from disk.
func (m *SiteDBManager) Delete(siteID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sdb, ok := m.dbs[siteID]; ok {
		sdb.writer.Close()
		sdb.reader.Close()
		delete(m.dbs, siteID)
	}

	// Remove the entire site directory (includes site.db, assets, files).
	siteDir := filepath.Join(m.dataDir, "sites", fmt.Sprintf("%d", siteID))
	if err := os.RemoveAll(siteDir); err != nil {
		return fmt.Errorf("removing project directory: %w", err)
	}

	slog.Info("project database deleted", "site_id", siteID)
	return nil
}

// Get returns a cached site database or nil if not open.
func (m *SiteDBManager) Get(siteID int) *SiteDB {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dbs[siteID]
}

// CloseAll closes all open site databases. Called on shutdown.
func (m *SiteDBManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, sdb := range m.dbs {
		sdb.writer.Close()
		sdb.reader.Close()
		slog.Info("project database closed", "site_id", id)
	}
	m.dbs = make(map[int]*SiteDB)
}

// OpenSiteIDs returns a list of all currently open site IDs.
func (m *SiteDBManager) OpenSiteIDs() []int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]int, 0, len(m.dbs))
	for id := range m.dbs {
		ids = append(ids, id)
	}
	return ids
}

// openSiteDB opens the SQLite file and runs site migrations. Must be called with m.mu held.
func (m *SiteDBManager) openSiteDB(siteID int) (*SiteDB, error) {
	dbPath := m.dbPath(siteID)

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("creating project db directory: %w", err)
	}

	// modernc.org/sqlite uses _pragma=name(value) syntax (not _name=value which is mattn/go-sqlite3).
	readDSN := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)", dbPath)
	// Writer uses _txlock=immediate so BEGIN acquires the write lock immediately
	// instead of deferring until the first write statement. This prevents
	// SQLITE_BUSY mid-transaction when another connection sneaks in a write.
	writeDSN := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_txlock=immediate", dbPath)

	// Read pool — concurrent reads via WAL mode.
	readDB, err := sql.Open("sqlite", readDSN)
	if err != nil {
		return nil, fmt.Errorf("opening project database %d (read): %w", siteID, err)
	}
	if err := readDB.Ping(); err != nil {
		readDB.Close()
		return nil, fmt.Errorf("pinging project database %d: %w", siteID, err)
	}
	readDB.SetMaxOpenConns(4)

	// Write pool — single connection serializes all writes at the DB level.
	// No Go-level mutex needed; Go's sql.DB queues callers when MaxOpenConns=1.
	writeDB, err := sql.Open("sqlite", writeDSN)
	if err != nil {
		readDB.Close()
		return nil, fmt.Errorf("opening project database %d (write): %w", siteID, err)
	}
	if err := writeDB.Ping(); err != nil {
		readDB.Close()
		writeDB.Close()
		return nil, fmt.Errorf("pinging project database %d (write): %w", siteID, err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)

	sdb := &SiteDB{reader: readDB, writer: writeDB, SiteID: siteID}

	if err := runSiteMigrations(sdb); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("running project migrations for project %d: %w", siteID, err)
	}

	slog.Info("project database initialized", "site_id", siteID, "path", dbPath)
	return sdb, nil
}

// runSiteMigrations applies pending site-level migrations.
func runSiteMigrations(sdb *SiteDB) error {
	_, err := sdb.writer.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return fmt.Errorf("creating project migrations table: %w", err)
	}

	entries, err := siteMigrationFS.ReadDir("site_migrations")
	if err != nil {
		return fmt.Errorf("reading site_migrations dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		name := entry.Name()

		var count int
		if err := sdb.QueryRow("SELECT COUNT(*) FROM migrations WHERE name = ?", name).Scan(&count); err != nil {
			return fmt.Errorf("checking project migration %s: %w", name, err)
		}
		if count > 0 {
			continue
		}

		content, err := siteMigrationFS.ReadFile("site_migrations/" + name)
		if err != nil {
			return fmt.Errorf("reading project migration %s: %w", name, err)
		}

		statements := strings.Split(string(content), ";")
		for _, stmt := range statements {
			stmt = strings.TrimSpace(stmt)
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
			if _, err := sdb.writer.Exec(stmt); err != nil {
				slog.Debug("migration statement failed", "migration", name, "statement", stmt, "error", err)
				return fmt.Errorf("executing project migration %s: %w", name, err)
			}
		}

		if _, err := sdb.writer.Exec("INSERT INTO migrations (name) VALUES (?)", name); err != nil {
			return fmt.Errorf("recording project migration %s: %w", name, err)
		}

		slog.Info("applied project migration", "site_id", sdb.SiteID, "name", name)
	}

	return nil
}
