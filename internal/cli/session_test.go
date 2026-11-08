package cli_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/cli"
)

const sessionID = "01a14743-2ec7-79eb-a9ab-39c56ca73589"

// sessionSchema is the part of br's schema the session report reads.
const sessionSchema = `
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

// sessionRepo builds a checkout with a beads store and a session directory that
// names it, and returns the checkout's directory.
func sessionRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	store := filepath.Join(repo, ".beads")
	require.NoError(t, os.MkdirAll(store, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))

	db, err := sql.Open("sqlite", filepath.Join(store, "beads.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(sessionSchema)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO issues (id, title, status, issue_type, updated_at) VALUES
		('t-epic', 'the epic', 'open', 'epic', '2026-10-01T00:00:00+00:00'),
		('t-a', 'a child', 'in_progress', 'bug', '2026-10-19T01:10:24+00:00'),
		('t-b', 'another session''s', 'open', 'task', '2026-10-19T01:10:24+00:00')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES ('t-a', 't-epic', 'parent-child')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO events (issue_id, event_type, actor) VALUES
		('t-a', 'created', 'agent:` + sessionID + `/engineer-e96b44ea'),
		('t-b', 'created', 'agent:019f0000-0000-7000-8000-000000000000/engineer')`)
	require.NoError(t, err)

	dir := filepath.Join(repo, ".sirene", "sessions", sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "beads-events.jsonl"),
		[]byte(`{"store":"`+store+`","issue_id":"t-a","event_type":"created","actor":"agent:`+sessionID+`/engineer-e96b44ea"}`+"\n"), 0o644))
	return repo
}

func runSession(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	// --home names an empty directory so no test reads the operator's home.
	argv := append([]string{"session"}, args...)
	argv = append(argv, "--home", t.TempDir())
	return runSessionArgv(t, dir, argv...)
}

// runSessionArgv runs the binary with argv as it is, so a test can place flags
// and the session id in any order.
func runSessionArgv(t *testing.T, dir string, argv ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(binary, argv...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), out.String()
	}
	require.NoError(t, err)
	return 0, out.String()
}

func TestSession_PrintsTheBeadsTheSessionTouched(t *testing.T) {
	repo := sessionRepo(t)

	code, out := runSession(t, repo, sessionID)
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "ID")
	require.Contains(t, out, "PARENT")
	require.Contains(t, out, "t-a")
	require.Contains(t, out, "a child")
	require.Contains(t, out, "in_progress")
	require.Contains(t, out, "bug")
	require.Contains(t, out, "2026-10-19T01:10:24+00:00")
	require.Contains(t, out, "t-epic")
	require.NotContains(t, out, "t-b", "another session's bead is not reported")
}

func TestSession_AnUnknownSessionIsAnError(t *testing.T) {
	repo := sessionRepo(t)

	code, out := runSession(t, repo, "019f0000-0000-7000-8000-000000000000")
	require.Equal(t, cli.ExitError, code, out)
	require.Contains(t, out, "no session")
}

func TestSession_AnExplicitDirNamesTheSession(t *testing.T) {
	repo := sessionRepo(t)
	dir := filepath.Join(repo, ".sirene", "sessions", sessionID)

	code, out := runSession(t, t.TempDir(), sessionID, "--dir", dir, "--work-dir", repo)
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "t-a")
}

func TestSession_ReportsABeadWhoseWorktreeWasRemoved(t *testing.T) {
	repo := sessionRepo(t)
	// A worktree that filed a bead and was then removed: the session's rows
	// name it, and its store is gone.
	gone := filepath.Join(t.TempDir(), "removed-worktree", ".beads")
	appendCopiedRow(t, repo, gone, "t-gone", "filed in a worktree since removed", "open", "2026-10-19T01:10:24+00:00")
	require.NoDirExists(t, gone)

	code, out := runSession(t, repo, sessionID, "--json")
	require.Equal(t, cli.ExitOK, code, out)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.Len(t, got, 2, "the removed worktree's bead is still the session's")
	byID := map[string]map[string]any{}
	for _, b := range got {
		byID[b["id"].(string)] = b
	}
	require.Equal(t, "filed in a worktree since removed", byID["t-gone"]["title"])
	require.Equal(t, "open", byID["t-gone"]["status"])
	require.Equal(t, "", byID["t-gone"]["type"], "no store can supply a type")
}

func TestSession_HasNoActorFormatFlag(t *testing.T) {
	repo := sessionRepo(t)

	code, out := runSession(t, repo, sessionID, "--actor-format", "agent:{agent}")
	require.Equal(t, cli.ExitError, code, out)
	require.Contains(t, out, "flag provided but not defined", "membership no longer depends on an actor format")
}

func TestSession_NeedsASessionID(t *testing.T) {
	repo := sessionRepo(t)

	code, out := runSessionArgv(t, repo, "session")
	require.Equal(t, cli.ExitError, code, out)
	require.Contains(t, out, "usage:")
}

func TestSession_HelpPrintsUsageAndExitsZero(t *testing.T) {
	repo := sessionRepo(t)

	for _, flag := range []string{"--help", "-h", "-help"} {
		t.Run(flag, func(t *testing.T) {
			code, out := runSessionArgv(t, repo, "session", flag)
			require.Equal(t, cli.ExitOK, code, out)
			require.Contains(t, out, "beads-tools session SESSION-ID")
			require.Contains(t, out, "--json")
			require.NotContains(t, out, "no session", "help is not a session id")
		})
	}
}

func TestSession_HelpWinsOverASessionID(t *testing.T) {
	repo := sessionRepo(t)

	code, out := runSessionArgv(t, repo, "session", sessionID, "--help")
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "beads-tools session SESSION-ID")
}

func TestSession_JSONPrintsAnArrayOfBeads(t *testing.T) {
	repo := sessionRepo(t)

	code, out := runSession(t, repo, sessionID, "--json")
	require.Equal(t, cli.ExitOK, code, out)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.Len(t, got, 1)
	require.Equal(t, map[string]any{
		"id":          "t-a",
		"title":       "a child",
		"status":      "in_progress",
		"type":        "bug",
		"updated_at":  "2026-10-19T01:10:24+00:00",
		"parent_epic": "t-epic",
	}, got[0])
}

// appendCopiedRow appends one row to the repo's session beads-events.jsonl, as
// sirene would when it copies the session's beads events.
func appendCopiedRow(t *testing.T, repo, store, issueID, title, status, at string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(repo, ".sirene", "sessions", sessionID, "beads-events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"store":"` + store + `","issue_id":"` + issueID + `","event_type":"created",` +
		`"actor":"agent:` + sessionID + `/engineer","title":"` + title + `","status":"` + status +
		`","created_at":"` + at + `"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func TestSession_JSONPrintsNullForNoParent(t *testing.T) {
	repo := sessionRepo(t)
	// A bead with no parent-child dependency, named by a copied row.
	db, err := sql.Open("sqlite", filepath.Join(repo, ".beads", "beads.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`INSERT INTO issues (id, title, status, issue_type, updated_at)
		VALUES ('t-c', 'no parent', 'open', 'task', '2026-10-20T00:00:00+00:00')`)
	require.NoError(t, err)
	appendCopiedRow(t, repo, filepath.Join(repo, ".beads"), "t-c", "no parent", "open", "2026-10-20T00:00:00+00:00")

	code, out := runSession(t, repo, sessionID, "--json")
	require.Equal(t, cli.ExitOK, code, out)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.Len(t, got, 2)
	require.Equal(t, "t-c", got[1]["id"])
	require.Contains(t, got[1], "parent_epic", "the key is present")
	require.Nil(t, got[1]["parent_epic"], "and null")
}

func TestSession_JSONPrintsAnEmptyArrayForNoBeads(t *testing.T) {
	// A session directory that names no store, in a checkout with no beads.
	empty := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(empty, ".git"), 0o755))
	dir := filepath.Join(empty, ".sirene", "sessions", sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	code, out := runSessionArgv(t, empty, "session", sessionID, "--dir", dir, "--json", "--home", t.TempDir())
	require.Equal(t, cli.ExitOK, code, out)
	require.Equal(t, "[]\n", out, "an empty result is [], not null")
}

func TestSession_FlagsWorkBeforeAndAfterTheSessionID(t *testing.T) {
	repo := sessionRepo(t)

	before, outBefore := runSessionArgv(t, repo, "session", "--json", sessionID, "--home", t.TempDir())
	after, outAfter := runSessionArgv(t, repo, "session", sessionID, "--json", "--home", t.TempDir())
	require.Equal(t, cli.ExitOK, before, outBefore)
	require.Equal(t, cli.ExitOK, after, outAfter)
	require.Equal(t, outAfter, outBefore, "the flag's position does not change the report")
	require.Contains(t, outBefore, `"t-a"`)
}

func TestSession_AFlagValueIsNotASessionID(t *testing.T) {
	repo := sessionRepo(t)
	dir := filepath.Join(repo, ".sirene", "sessions", sessionID)

	// --dir's value must not be read as the session id.
	code, out := runSessionArgv(t, repo, "session", "--dir", dir, sessionID, "--home", t.TempDir())
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "t-a")
}
