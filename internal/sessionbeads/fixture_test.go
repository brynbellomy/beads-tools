package sessionbeads_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"
)

// schema is the part of br's schema this package reads.
const schema = `
CREATE TABLE issues (
  id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'open',
  issue_type TEXT NOT NULL DEFAULT 'task', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, issue_id TEXT NOT NULL, event_type TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '', old_value TEXT, new_value TEXT, comment TEXT,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE dependencies (
  issue_id TEXT NOT NULL, depends_on_id TEXT NOT NULL, type TEXT NOT NULL DEFAULT 'blocks',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, created_by TEXT NOT NULL DEFAULT '',
  metadata TEXT DEFAULT '{}', thread_id TEXT DEFAULT '', PRIMARY KEY (issue_id, depends_on_id));
`

// fixture is a beads store built for one test.
type fixture struct {
	t     *testing.T
	store string
	db    *sql.DB
}

// newStore creates an empty beads store at dir/.beads.
func newStore(t *testing.T, dir string) *fixture {
	t.Helper()
	store := filepath.Join(dir, ".beads")
	require.NoError(t, os.MkdirAll(store, 0o755))
	db, err := sql.Open("sqlite", filepath.Join(store, "beads.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(schema)
	require.NoError(t, err)
	return &fixture{t: t, store: store, db: db}
}

// issue adds an issue and returns its id.
func (f *fixture) issue(id, title, status, typ, updated string) {
	f.t.Helper()
	_, err := f.db.Exec(`INSERT INTO issues (id, title, status, issue_type, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, title, status, typ, updated)
	require.NoError(f.t, err)
}

// event records one mutation by actor.
func (f *fixture) event(issueID, eventType, actor string) {
	f.t.Helper()
	_, err := f.db.Exec(`INSERT INTO events (issue_id, event_type, actor) VALUES (?, ?, ?)`, issueID, eventType, actor)
	require.NoError(f.t, err)
}

// parent records that child belongs to parent.
func (f *fixture) parent(child, parent string) {
	f.t.Helper()
	_, err := f.db.Exec(`INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES (?, ?, 'parent-child')`, child, parent)
	require.NoError(f.t, err)
}

// sessionDir writes a session directory holding beads-events.jsonl rows that
// name stores.
func sessionDir(t *testing.T, root, id string, stores ...string) string {
	t.Helper()
	dir := filepath.Join(root, ".sirene", "sessions", id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	var lines []byte
	for _, store := range stores {
		lines = append(lines, []byte(`{"store":"`+store+`","issue_id":"x","event_type":"created","actor":"agent:`+id+`/a"}`+"\n")...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "beads-events.jsonl"), lines, 0o644))
	return dir
}
