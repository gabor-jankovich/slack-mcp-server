package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// schemaVersion is the current migration version.
const schemaVersion = 1

// migrations holds ordered SQL migration scripts.
var migrations = []string{
	// Version 1
	`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY
	);

	CREATE TABLE IF NOT EXISTS conversation_work_items (
		id TEXT PRIMARY KEY,
		source_id TEXT NOT NULL,
		channel_id TEXT NOT NULL,
		thread_ts TEXT NOT NULL,
		newest_message_ts TEXT NOT NULL,
		status TEXT NOT NULL,
		retry_count INTEGER DEFAULT 0,
		version INTEGER DEFAULT 1,
		message_count INTEGER DEFAULT 1,
		agent_id TEXT,
		lease_until DATETIME,
		created_at DATETIME,
		updated_at DATETIME,
		delivered_at DATETIME,
		acked_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS thread_state (
		thread_ts TEXT NOT NULL,
		channel_id TEXT NOT NULL,
		last_processed_message_ts TEXT,
		updated_at DATETIME,
		PRIMARY KEY (thread_ts, channel_id)
	);

	CREATE TABLE IF NOT EXISTS agent_registry (
		agent_id TEXT PRIMARY KEY,
		tmux_session TEXT,
		status TEXT,
		heartbeat DATETIME,
		current_thread TEXT
	);

	CREATE TABLE IF NOT EXISTS leases (
		work_item_id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		lease_until DATETIME NOT NULL,
		FOREIGN KEY (work_item_id) REFERENCES conversation_work_items(id)
	);

	CREATE INDEX IF NOT EXISTS idx_work_items_status ON conversation_work_items(status, created_at);
	CREATE INDEX IF NOT EXISTS idx_work_items_thread ON conversation_work_items(thread_ts, channel_id);
	CREATE INDEX IF NOT EXISTS idx_leases_until ON leases(lease_until);

	INSERT INTO schema_migrations (version) VALUES (1)
		ON CONFLICT(version) DO NOTHING;
	`,
}

// Open opens a SQLite database at the given path and runs migrations.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, fmt.Errorf("creating database directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("pinging sqlite database: %w", err)
	}

	if err := Run(ctx, db); err != nil {
		return nil, err
	}

	return db, nil
}

// Run applies pending migrations.
func Run(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1").Scan(&version); err != nil {
		if err != sql.ErrNoRows && !strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("reading schema version: %w", err)
		}
		version = 0
	}

	for i, migration := range migrations {
		expected := i + 1
		if expected <= version {
			continue
		}

		if _, err := db.ExecContext(ctx, migration); err != nil {
			return fmt.Errorf("running migration %d: %w", expected, err)
		}
	}

	return nil
}

// CurrentVersion returns the current schema version from the database.
func CurrentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1").Scan(&version); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return version, nil
}
