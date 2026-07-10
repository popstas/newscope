package repository

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // pure Go SQLite driver
)

//go:embed schema.sql
var schemaFS embed.FS

// Config represents database configuration
type Config struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Repositories contains all repository instances
type Repositories struct {
	Feed           *FeedRepository
	Item           *ItemRepository
	Classification *ClassificationRepository
	Setting        *SettingRepository
	DB             *sqlx.DB
}

// NewRepositories creates all repositories with a shared database connection
func NewRepositories(ctx context.Context, cfg Config) (*Repositories, error) {
	if cfg.DSN == "" {
		cfg.DSN = "file:newscope.db?cache=shared&mode=rwc&_txlock=immediate"
	}

	db, err := sqlx.Open("sqlite", withConnPragmas(cfg.DSN))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// configure connection pool
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	// journal_mode is database-level: WAL is recorded in the file header and
	// subsequent connections inherit it, so a single exec is enough. all other
	// pragmas are per-connection and are applied via the dsn (withConnPragmas).
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		return nil, fmt.Errorf("enable WAL: %w", err)
	}

	// initialize schema
	if err := initSchema(ctx, db); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}

	// create repositories
	repos := &Repositories{
		Feed:           NewFeedRepository(db),
		Item:           NewItemRepository(db),
		Classification: NewClassificationRepository(db),
		Setting:        NewSettingRepository(db),
		DB:             db,
	}

	return repos, nil
}

// withConnPragmas appends modernc.org/sqlite `_pragma=` query parameters that
// must take effect on every pooled connection. PRAGMAs like foreign_keys are
// per-connection: running `PRAGMA foreign_keys = ON` once via *sqlx.DB only
// configures whichever pooled connection answered that exec, leaving every
// other connection with FKs disabled — and cascade deletes silently no-op on
// those connections. The driver re-runs `_pragma=` params on every connect.
func withConnPragmas(dsn string) string {
	pragmas := []string{
		"_pragma=busy_timeout(5000)",
		"_pragma=cache_size(-64000)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=temp_store(MEMORY)",
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + strings.Join(pragmas, "&")
}

// Close closes the database connection
func (r *Repositories) Close() error {
	return r.DB.Close()
}

// Ping verifies the database connection
func (r *Repositories) Ping(ctx context.Context) error {
	return r.DB.PingContext(ctx)
}

// initSchema creates tables if they don't exist
func initSchema(ctx context.Context, db *sqlx.DB) error {
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}

	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		return fmt.Errorf("execute schema: %w", err)
	}

	return nil
}

// criticalError wraps an error to signal repeater to stop retrying
type criticalError struct {
	err error
}

func (e *criticalError) Error() string {
	return e.err.Error()
}

// isLockError checks if an error is a SQLite lock/busy error
func isLockError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "SQLITE_BUSY") ||
		strings.Contains(errStr, "database is locked") ||
		strings.Contains(errStr, "database table is locked")
}
