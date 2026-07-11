package db

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // Support SQLite database
)

//go:embed migrations/*.sql
var migrations embed.FS

// pragmaDSN carries the connection pragmas and transaction mode in the DSN so
// modernc applies them to every pooled connection. foreign_keys and busy_timeout
// are per-connection settings that SQLite does not persist: a one-off
// db.Exec("PRAGMA …") configures only whichever single connection served it,
// leaving the rest of the pool with foreign_keys=0 (ON DELETE CASCADE silently
// skipped → orphaned junction rows) and busy_timeout=0 (immediate SQLITE_BUSY on
// write contention). Encoding them in the DSN guarantees they hold on each
// connection. journal_mode/synchronous are listed too for consistency (WAL is
// persisted in the file header regardless). _txlock=immediate makes every BeginTx
// issue BEGIN IMMEDIATE, acquiring the write lock upfront — matching the
// single-writer design and avoiding deferred-to-immediate promotion conflicts.
const pragmaDSN = "_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=foreign_keys(1)" +
	"&_txlock=immediate"

const (
	// maxOpenConns bounds the connection pool. Each modernc/sqlite connection is a
	// distinct handle on the DB + WAL, so an uncapped pool (database/sql's default)
	// grows with read concurrency until it exhausts file descriptors/memory on the
	// low-spec target hosts. Reads run concurrently on WAL and writes are already
	// serialized by WriteGuard + busy_timeout, so a modest cap costs nothing but
	// keeps the pool bounded.
	maxOpenConns = 8
	// connMaxIdleTime releases connections that have sat idle so a burst of reads
	// does not pin handles/memory for the whole process lifetime.
	connMaxIdleTime = 5 * time.Minute
)

func Open(log *slog.Logger, dataDir string) (*sql.DB, error) {
	dbPath := filepath.Join(dataDir, "folio.db")

	db, err := sql.Open("sqlite", dbPath+"?"+pragmaDSN)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	db.SetConnMaxIdleTime(connMaxIdleTime)

	if err := migrate(log, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return db, nil
}

func migrate(log *slog.Logger, db *sql.DB) error {
	goose.SetBaseFS(migrations)
	goose.SetLogger(newSlogger(log, os.Exit))

	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}
