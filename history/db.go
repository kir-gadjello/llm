package history

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

const schemaCore = `
CREATE TABLE IF NOT EXISTS sessions (
    uuid TEXT PRIMARY KEY,
    created_at INTEGER,
    model TEXT,
    system_prompt TEXT,
    summary TEXT
);

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_uuid TEXT,
    role TEXT,
    content TEXT,
    created_at INTEGER,
    FOREIGN KEY(session_uuid) REFERENCES sessions(uuid)
);
`

const schemaFTS = `
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    content,
    role,
    session_uuid UNINDEXED,
    tokenize = 'porter'
);

CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(content, role, session_uuid) VALUES (new.content, new.role, new.session_uuid);
END;
`

func initDB(dbPath string) (*sql.DB, bool, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, false, fmt.Errorf("failed to create history dir: %w", err)
	}

	// Connect
	db, err := sql.Open("sqlite3", dbPath+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, false, err
	}

	// Initialize Core Schema (Must succeed)
	if _, err := db.Exec(schemaCore); err != nil {
		db.Close()
		return nil, false, fmt.Errorf("failed to init core schema: %w", err)
	}

	// Initialize FTS Schema (Can fail if FTS5 is missing)
	ftsEnabled := true
	if _, err := db.Exec(schemaFTS); err != nil {
		// Log internal warning or just disable FTS
		// We don't close DB, we just disable search features
		ftsEnabled = false
	}

	return db, ftsEnabled, nil
}

// CheckFTS verifies if the FTS5 extension is loaded and working
func CheckFTS() bool {
	// Try to create an FTS5 table in an in-memory database
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return false
	}
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE test USING fts5(content)")
	return err == nil
}
