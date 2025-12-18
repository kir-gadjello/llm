package history

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Manager handles dual-write history (JSONL + SQLite)
type Manager struct {
	db          *sql.DB
	jsonlPath   string
	searchAvail bool
	mu          sync.Mutex
}

// New creates a new history manager
func New(dbPath, jsonlPath string) (*Manager, error) {
	db, ftsEnabled, err := initDB(dbPath)
	if err != nil {
		return nil, err
	}

	m := &Manager{
		db:          db,
		jsonlPath:   jsonlPath,
		searchAvail: ftsEnabled,
	}

	// Trigger lazy migration in background
	go m.EnsureMigrated()

	return m, nil
}

func (m *Manager) Close() {
	if m.db != nil {
		m.db.Close()
	}
}

// EnsureMigrated checks if DB is empty and if so, imports from JSONL
func (m *Manager) EnsureMigrated() {
	m.mu.Lock()
	defer m.mu.Unlock()

	var count int
	err := m.db.QueryRow("SELECT count(*) FROM sessions").Scan(&count)
	if err == nil && count > 0 {
		return // Already populated
	}

	// Check if JSONL exists
	if _, err := os.Stat(m.jsonlPath); os.IsNotExist(err) {
		return // Nothing to migrate
	}

	// Start Migration
	m.migrate()
}

func (m *Manager) migrate() {
	f, err := os.Open(m.jsonlPath)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Increase buffer size for large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	tx, err := m.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()

	stmtSession, _ := tx.Prepare("INSERT OR IGNORE INTO sessions(uuid, created_at, model, system_prompt, summary) VALUES(?, ?, ?, ?, ?)")
	stmtMsg, _ := tx.Prepare("INSERT INTO messages(session_uuid, role, content, created_at) VALUES(?, ?, ?, ?)")
	defer stmtSession.Close()
	defer stmtMsg.Close()

	for scanner.Scan() {
		line := scanner.Bytes()
		// Determine type loosely
		var base map[string]interface{}
		if err := json.Unmarshal(line, &base); err != nil {
			continue
		}

		if _, ok := base["user_msg"]; ok {
			// Session Start
			var s SessionStartEvent
			json.Unmarshal(line, &s)
			
			summary := s.UserMsg
			if len(summary) > 100 {
				summary = summary[:100] + "..."
			}
			
			stmtSession.Exec(s.SID, s.TS, s.Model, s.SystemPrompt, summary)
			
			// Also insert the first user message
			if s.UserMsg != "" {
				stmtMsg.Exec(s.SID, "user", s.UserMsg, s.TS)
			}
			// And system prompt if present
			if s.SystemPrompt != "" {
				stmtMsg.Exec(s.SID, "system", s.SystemPrompt, s.TS)
			}

		} else if _, ok := base["msg"]; ok {
			// Message
			var msg MessageEvent
			json.Unmarshal(line, &msg)
			stmtMsg.Exec(msg.SID, msg.Message.Role, msg.Message.Content, msg.TS)
		}
	}

	tx.Commit()
}

// === Write Methods ===

func (m *Manager) SaveSessionStart(data SessionStartEvent) error {
	// 1. Write to JSONL
	if err := m.appendJSONL(data); err != nil {
		return err
	}

	// 2. Write to DB
	summary := data.UserMsg
	if len(summary) > 100 {
		summary = summary[:100] + "..."
	}
	
	_, err := m.db.Exec("INSERT OR IGNORE INTO sessions(uuid, created_at, model, system_prompt, summary) VALUES(?, ?, ?, ?, ?)",
		data.SID, data.TS, data.Model, data.SystemPrompt, summary)
	
	// Also insert implicit messages
	if data.SystemPrompt != "" {
		m.db.Exec("INSERT INTO messages(session_uuid, role, content, created_at) VALUES(?, ?, ?, ?)",
			data.SID, "system", data.SystemPrompt, data.TS)
	}
	if data.UserMsg != "" {
		m.db.Exec("INSERT INTO messages(session_uuid, role, content, created_at) VALUES(?, ?, ?, ?)",
			data.SID, "user", data.UserMsg, data.TS)
	}

	return err
}

func (m *Manager) SaveMessage(data MessageEvent) error {
	if err := m.appendJSONL(data); err != nil {
		return err
	}

	_, err := m.db.Exec("INSERT INTO messages(session_uuid, role, content, created_at) VALUES(?, ?, ?, ?)",
		data.SID, data.Message.Role, data.Message.Content, data.TS)
	return err
}

func (m *Manager) SaveShellEvent(data ShellEvent) error {
	return m.appendJSONL(data)
}

func (m *Manager) appendJSONL(data interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, err := os.OpenFile(m.jsonlPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	bytes, err := json.Marshal(data)
	if err != nil {
		return err
	}
	
	_, err = f.Write(append(bytes, '\n'))
	return err
}

// === Read Methods ===

func (m *Manager) Search(query string) ([]SearchResult, error) {
	if !m.searchAvail {
		return nil, fmt.Errorf("search is unavailable (binary compiled without FTS5 support)")
	}

	// Ensure migration is done if this is the first search
	m.EnsureMigrated()

	ftsQuery := ParseQuery(query)
	if ftsQuery == "" {
		return nil, fmt.Errorf("empty query")
	}

	rows, err := m.db.Query(`
		SELECT session_uuid, role, content, highlight(messages_fts, 0, ' [1;31m', ' [0m') 
		FROM messages_fts 
		WHERE messages_fts MATCH ? 
		ORDER BY rank 
		LIMIT 50`, ftsQuery)
	
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var rawContent string // unused, we use highlight
		if err := rows.Scan(&r.SessionUUID, &r.Role, &rawContent, &r.Preview); err != nil {
			continue
		}
		// Get timestamp from session
		var ts int64
		m.db.QueryRow("SELECT created_at FROM sessions WHERE uuid = ?", r.SessionUUID).Scan(&ts)
		r.Timestamp = time.Unix(ts, 0)
		
		results = append(results, r)
	}
	return results, nil
}

// ResolveSessionUUID finds the full UUID given a prefix or full string
func (m *Manager) ResolveSessionUUID(partial string) (string, error) {
	var full string
	// Try exact match first
	err := m.db.QueryRow("SELECT uuid FROM sessions WHERE uuid = ?", partial).Scan(&full)
	if err == nil {
		return full, nil
	}
	
	// Try prefix match
	rows, err := m.db.Query("SELECT uuid FROM sessions WHERE uuid LIKE ? LIMIT 2", partial+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	
	var matches []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err == nil {
			matches = append(matches, u)
		}
	}
	
	if len(matches) == 0 {
		return "", fmt.Errorf("session not found")
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous session uuid: %s...", partial)
	}
	
	return matches[0], nil
}

func (m *Manager) GetSessionMessages(uuid string) ([]ChatMessage, error) {
	rows, err := m.db.Query("SELECT role, content FROM messages WHERE session_uuid = ? ORDER BY id ASC", uuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			continue
		}
		m.UUID = uuid // Not technically correct per msg, but fine for context loading
		msgs = append(msgs, m)
	}
	return msgs, nil
}

func (m *Manager) ListRecentSessions(limit int) ([]SessionSummary, error) {
	rows, err := m.db.Query("SELECT uuid, created_at, model, summary FROM sessions ORDER BY created_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionSummary
	for rows.Next() {
		var s SessionSummary
		var ts int64
		if err := rows.Scan(&s.UUID, &ts, &s.Model, &s.Summary); err != nil {
			continue
		}
		s.Timestamp = time.Unix(ts, 0)
		sessions = append(sessions, s)
	}
	return sessions, nil
}
