// Package agent reads opencode's session history straight out of its SQLite
// database, the way omarchy-agent-sessions does: read-only, one subprocess,
// with column guards so an opencode schema change degrades the sessions
// feed instead of breaking the whole cockpit.
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

// required mirrors omarchy-agent-sessions' column check. cost is required
// too (the panel of scripts already depends on it); if opencode drops it,
// failing loudly for the feed is the designed behavior.
var required = []string{"id", "title", "directory", "parent_id", "time_updated", "time_archived", "cost"}

// sessionTable picks where sessions actually live: opencode 2.x writes
// session_v2 and leaves the old session table as a frozen legacy mirror,
// so the newest table wins when both exist. Older builds get the fallback.
func sessionTable(db string) (string, error) {
	out, err := exec.Command("sqlite3", "-readonly", "-json", db,
		"SELECT name FROM sqlite_master WHERE type='table' AND name IN ('session_v2','session');").Output()
	if err != nil {
		return "", ErrNoDatabase
	}
	var rows []struct{ Name string }
	if err := json.Unmarshal(out, &rows); err != nil {
		return "", err
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
		return nil, err
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

	out, err := exec.Command("sqlite3", "-readonly", "-json", db, q).Output()
	if err != nil {
		return nil, fmt.Errorf("session query: %w", err)
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
	out, err := exec.Command("sqlite3", "-readonly", "-json", db, "PRAGMA table_info("+table+");").Output()
	if err != nil {
		return nil, fmt.Errorf("%s table? %w", table, err)
	}
	var rows []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("table_info: %w", err)
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
	out, err := exec.Command("sqlite3", "-readonly", "-json", db, q).Output()
	if err != nil {
		return nil, fmt.Errorf("todo query: %w", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	var rows []Todo
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("todo rows: %w", err)
	}
	for i := range rows {
		rows[i].Content = strings.Join(strings.Fields(rows[i].Content), " ")
	}
	return rows, nil
}
