package sqliteretry

import (
	"database/sql"
	"log/slog"
	"math/rand"
	"strings"
	"time"
)

const maxRetries = 5

// IsBusy returns true if the error is a SQLite BUSY/locked error.
func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}

// Exec wraps db.Exec with retry-on-busy logic (exponential backoff).
func Exec(db *sql.DB, query string, args ...interface{}) (sql.Result, error) {
	baseDelay := 5 * time.Millisecond

	for attempt := 0; ; attempt++ {
		result, err := db.Exec(query, args...)
		if err == nil || !IsBusy(err) || attempt >= maxRetries {
			return result, err
		}
		delay := backoff(baseDelay, attempt)
		slog.Warn("sqlite busy, retrying write", "attempt", attempt+1, "delay", delay)
		time.Sleep(delay)
	}
}

// BeginTx wraps db.Begin with retry-on-busy logic.
func BeginTx(db *sql.DB) (*sql.Tx, error) {
	baseDelay := 5 * time.Millisecond

	for attempt := 0; ; attempt++ {
		tx, err := db.Begin()
		if err == nil || !IsBusy(err) || attempt >= maxRetries {
			return tx, err
		}
		delay := backoff(baseDelay, attempt)
		slog.Warn("sqlite busy on BEGIN, retrying", "attempt", attempt+1, "delay", delay)
		time.Sleep(delay)
	}
}

// Query wraps db.Query with retry-on-busy logic (exponential backoff).
func Query(db *sql.DB, query string, args ...interface{}) (*sql.Rows, error) {
	baseDelay := 5 * time.Millisecond

	for attempt := 0; ; attempt++ {
		rows, err := db.Query(query, args...)
		if err == nil || !IsBusy(err) || attempt >= maxRetries {
			return rows, err
		}
		delay := backoff(baseDelay, attempt)
		slog.Warn("sqlite busy, retrying read", "attempt", attempt+1, "delay", delay)
		time.Sleep(delay)
	}
}

func backoff(base time.Duration, attempt int) time.Duration {
	delay := base * (1 << attempt) // 5ms, 10ms, 20ms, 40ms, 80ms
	jitter := time.Duration(rand.Int63n(int64(delay/2) + 1))
	return delay + jitter
}
