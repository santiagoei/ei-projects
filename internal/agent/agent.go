// Package agent reads opencode's session history straight out of its SQLite
// database, the way omarchy-agent-sessions does: one subprocess with a
// read-only-first connection, with column guards so an opencode schema
// change degrades the sessions feed instead of breaking the whole cockpit.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	// ErrNoBinary: sqlite3 is not installed; the sessions section just won't exist.
	ErrNoBinary = errors.New("sqlite3 not available")
	// ErrNoDatabase: opencode has not been run on this machine (or the db
	// lives elsewhere); same degradation.
	ErrNoDatabase = errors.New("no opencode database")
	// ErrSchema: the session table lost a column this reader depends on.
	ErrSchema = errors.New("opencode.db session schema changed; update ei-projects")
)

type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Directory string    `json:"directory"`
	Updated   time.Time `json:"updated"`
	Cost      float64   `json:"cost"`
}

// dbPath is opencode's data dir under XDG data home, env-overridable for tests.
func DBPath() string {
	if x := os.Getenv("EI_PROJECTS_OPENCODE_DB"); x != "" {
		return x
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "opencode", "opencode.db")
}

// queryJSON runs one SELECT the way the proven picking script does it:
// read-only first, and only if that refuses a plain connection. Opencode
// keeps a live WAL sidecar on a multi-hundred-megabyte db, and a read-only
// open can be refused mid-checkpoint; every query here is a SELECT, so the
// fallback reads without ever writing data.
func queryJSON(db, sql string) ([]byte, error) {
	if out, err := exec.Command("sqlite3", "-readonly", "-json", db, sql).Output(); err == nil {
		return out, nil
	}
	if out, err := exec.Command("sqlite3", "-json", db, sql).Output(); err == nil {
		return out, nil
	}
	return nil, ErrNoDatabase
}

// queryMeta is queryJSON for schema questions (sqlite_master, PRAGMA). A
// read-only connection mid-checkpoint can answer with exit 0 and NO
// output, which a schema probe must not trust as "no such table": empty
// for a healthy db is impossible, so a second opinion via a plain
// connection decides.
func queryMeta(db, sql string) ([]byte, error) {
	out, err := queryJSON(db, sql)
	if err != nil || len(strings.TrimSpace(string(out))) > 0 {
		return out, err
	}
	if out2, err2 := exec.Command("sqlite3", "-json", db, sql).Output(); err2 == nil && len(strings.TrimSpace(string(out2))) > 0 {
		return out2, nil
	}
	return out, nil
}

// required mirrors omarchy-agent-sessions' column check. cost is required
// too (the panel of scripts already depends on it); if opencode drops it,
// failing loudly for the feed is the designed behavior.
var required = []string{"id", "title", "directory", "parent_id", "time_updated", "time_archived", "cost"}

// sessionTable picks where sessions actually live: opencode 2.x writes
// session_v2 and leaves the old session table as a frozen legacy mirror,
// so the newest table wins when both exist (verified: the legacy mirror
// stopped advancing in October while session_v2 stays current). Older
// builds get the fallback; a database that holds neither degrades to "no
// sessions", not an error -- opencode has already moved storage once
// (the old JSON-file store) and a read-only connection that answers the
// schema probe with nothing at all mid-checkpoint is handled by queryMeta.
func sessionTable(db string) (string, error) {
	out, err := queryMeta(db, "SELECT name FROM sqlite_master WHERE type='table' AND name IN ('session_v2','session');")
	if err != nil {
		return "", err
	}
	var rows []struct{ Name string }
	if strings.TrimSpace(string(out)) == "" {
		return "", ErrNoDatabase // no session tables at all
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return "", ErrNoDatabase
	}
	have := map[string]bool{}
	for _, r := range rows {
		have[r.Name] = true
	}
	switch {
	case have["session_v2"]:
		return "session_v2", nil
	case have["session"]:
		return "session", nil
	}
	return "", ErrNoDatabase
}

// Recent returns up to limit newest top-level, non-archived sessions whose
// directory is root or lives under it -- the same prefix rule the picking
// script uses, so a session started in a repo inside the project counts
// for the project.
func Recent(root string, limit int) ([]Session, error) {
	db := DBPath()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, ErrNoBinary
	}
	if fi, err := os.Stat(db); err != nil || fi.Size() == 0 {
		return nil, ErrNoDatabase
	}

	table, err := sessionTable(db)
	if err != nil {
		return nil, err
	}
	have, err := columns(db, table)
	if err != nil {
		return nil, ErrSchema
	}
	for _, c := range required {
		if !have[c] {
			return nil, ErrSchema
		}
	}

	// The sqlite3 CLI takes SQL, not bound parameters; every path is escaped
	// the way the proven script does it (double the single quotes) and lives
	// inside a literal of its own.
	esc := strings.ReplaceAll(root, "'", "''")
	q := fmt.Sprintf(
		"SELECT id, title, directory, time_updated / 1000 AS ts, cost "+
			"FROM %s "+
			"WHERE parent_id IS NULL AND time_archived IS NULL "+
			"AND (directory = '%s' OR directory LIKE '%s' || '/%%') "+
			"ORDER BY time_updated DESC LIMIT %d;",
		table, esc, esc, limit)

	out, err := queryJSON(db, q)
	if err != nil {
		return nil, err
	}
	if trimmed := strings.TrimSpace(string(out)); trimmed == "" {
		return nil, nil // empty result: sqlite3 -json prints nothing
	}
	var rows []struct {
		ID        string  `json:"id"`
		Title     string  `json:"title"`
		Directory string  `json:"directory"`
		Ts        float64 `json:"ts"`
		Cost      float64 `json:"cost"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("session rows: %w", err)
	}
	sessions := make([]Session, 0, len(rows))
	for _, r := range rows {
		sessions = append(sessions, Session{
			ID: r.ID, Title: r.Title, Directory: r.Directory,
			Updated: time.Unix(int64(r.Ts), 0), Cost: r.Cost,
		})
	}
	return sessions, nil
}

func columns(db, table string) (map[string]bool, error) {
	out, err := queryMeta(db, "PRAGMA table_info("+table+");")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(rows))
	for _, r := range rows {
		have[r.Name] = true
	}
	return have, nil
}

// Todo is one work item an opencode session left behind.
type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress
}

// OpenTodos lists the unfinished todos (pending / in_progress) of one
// session: "what was left hanging in here". The todo table only exists
// once a session has made a list, so a missing one means "no open todos",
// not an error; every other feed failure is the caller's to swallow, the
// detail line is decoration and must never break the cockpit.
func OpenTodos(sessionID string) ([]Todo, error) {
	db := DBPath()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, ErrNoBinary
	}
	if fi, err := os.Stat(db); err != nil || fi.Size() == 0 {
		return nil, ErrNoDatabase
	}
	have, err := columns(db, "todo")
	if err != nil || !have["content"] || !have["status"] {
		return nil, nil // no todo table (or none of the columns): no todos
	}
	esc := strings.ReplaceAll(sessionID, "'", "''")
	q := fmt.Sprintf(
		"SELECT content, status FROM todo "+
			"WHERE session_id = '%s' AND status IN ('pending','in_progress') "+
			"ORDER BY position, time_created;", esc)
	out, err := queryJSON(db, q)
	if err != nil {
		return nil, nil
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	var rows []Todo
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, nil
	}
	for i := range rows {
		rows[i].Content = strings.Join(strings.Fields(rows[i].Content), " ")
	}
	return rows, nil
}
