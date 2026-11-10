package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/cli"
	"github.com/brynbellomy/beads-tools/internal/labels"
)

const sessionID = "01a14743-2ec7-79eb-a9ab-39c56ca73589"

// issue is one line of an issues.jsonl a test writes.
type issue struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	IssueType    string   `json:"issue_type"`
	UpdatedAt    string   `json:"updated_at"`
	Labels       []string `json:"labels,omitempty"`
	Dependencies []dep    `json:"dependencies,omitempty"`
}

type dep struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}

// writeStore writes a beads store at dir/.beads holding issues.
func writeStore(t *testing.T, dir string, issues ...issue) string {
	t.Helper()
	store := filepath.Join(dir, ".beads")
	require.NoError(t, os.MkdirAll(store, 0o755))
	var buf bytes.Buffer
	for _, iss := range issues {
		line, err := json.Marshal(iss)
		require.NoError(t, err)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(store, "issues.jsonl"), buf.Bytes(), 0o644))
	return store
}

// sessionRepo builds a checkout whose store holds one bead of this session, one
// of another session, and one of this user.
func sessionRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	writeStore(t, repo,
		issue{ID: "t-epic", Title: "the epic", Status: "open", IssueType: "epic", UpdatedAt: "2026-10-01T00:00:00Z"},
		issue{ID: "t-a", Title: "a child", Status: "in_progress", IssueType: "bug", UpdatedAt: "2026-10-19T01:10:24Z",
			Labels: []string{labels.Session(sessionID), labels.User("bryn@signals.io")},
			Dependencies: []dep{{IssueID: "t-a", DependsOnID: "t-epic", Type: "parent-child"}}},
		issue{ID: "t-b", Title: "another session's", Status: "open", IssueType: "task", UpdatedAt: "2026-10-19T01:10:24Z",
			Labels: []string{labels.Session("019f0000-0000-7000-8000-000000000000")}},
	)
	return repo
}

func runSession(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	return runSessionArgv(t, dir, append([]string{"session"}, args...)...)
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
	require.Contains(t, out, "2026-10-19T01:10:24Z")
	require.Contains(t, out, "t-epic")
	require.NotContains(t, out, "t-b", "another session's bead is not reported")
}

func TestSession_AnUnknownSessionReportsNoBeads(t *testing.T) {
	repo := sessionRepo(t)

	code, out := runSession(t, repo, "019f0000-0000-7000-8000-00000000ffff", "--json")
	require.Equal(t, cli.ExitOK, code, out)
	require.Equal(t, "[]\n", out, "a session that touched nothing is an empty list")
}

func TestSession_ReadsEveryLiveWorktree(t *testing.T) {
	repo := sessionRepo(t)
	// A linked worktree with its own store, holding a bead of this session.
	wt := filepath.Join(t.TempDir(), "wt")
	initGitRepo(t, repo, wt)
	writeStore(t, wt, issue{ID: "t-wt", Title: "filed in a worktree", Status: "open", IssueType: "task",
		UpdatedAt: "2026-10-20T00:00:00Z", Labels: []string{labels.Session(sessionID)}})

	code, out := runSession(t, repo, sessionID, "--json")
	require.Equal(t, cli.ExitOK, code, out)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	ids := map[string]bool{}
	for _, b := range got {
		ids[b["id"].(string)] = true
	}
	require.True(t, ids["t-a"], "the main checkout's bead is reported")
	require.True(t, ids["t-wt"], "the worktree's bead is reported")
	require.False(t, ids["t-b"], "another session's bead is not")
}

// initGitRepo makes repo a git repository with wt as a linked worktree, so
// `git worktree list` names both.
func initGitRepo(t *testing.T, repo, wt string) {
	t.Helper()
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git(repo, "init", "-q")
	git(repo, "config", "user.email", "t@example.com")
	git(repo, "config", "user.name", "t")
	git(repo, "add", "-A")
	git(repo, "commit", "-q", "-m", "seed")
	git(repo, "worktree", "add", "-q", "--detach", wt)
}

func TestSession_UserFlagReportsTheUsersBeads(t *testing.T) {
	repo := sessionRepo(t)

	code, out := runSession(t, repo, "--user", "bryn@signals.io", "--json")
	require.Equal(t, cli.ExitOK, code, out)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.Len(t, got, 1)
	require.Equal(t, "t-a", got[0]["id"])
}

func TestSession_UserFlagAndASessionIDAreRefusedTogether(t *testing.T) {
	repo := sessionRepo(t)

	code, out := runSession(t, repo, sessionID, "--user", "bryn@signals.io")
	require.Equal(t, cli.ExitError, code, out)
	require.Contains(t, out, "not both")
}

func TestSession_NeedsASessionIDOrAUser(t *testing.T) {
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
			require.Contains(t, out, "--user")
			require.Contains(t, out, "--json")
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
		"updated_at":  "2026-10-19T01:10:24Z",
		"parent_epic": "t-epic",
	}, got[0])
}

func TestSession_JSONPrintsNullForNoParent(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	writeStore(t, repo, issue{ID: "t-c", Title: "no parent", Status: "open", IssueType: "task",
		UpdatedAt: "2026-10-20T00:00:00Z", Labels: []string{labels.Session(sessionID)}})

	code, out := runSession(t, repo, sessionID, "--json")
	require.Equal(t, cli.ExitOK, code, out)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.Len(t, got, 1)
	require.Contains(t, got[0], "parent_epic", "the key is present")
	require.Nil(t, got[0]["parent_epic"], "and null")
}

func TestSession_JSONPrintsAnEmptyArrayForNoBeads(t *testing.T) {
	empty := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(empty, ".git"), 0o755))

	code, out := runSessionArgv(t, empty, "session", sessionID, "--json")
	require.Equal(t, cli.ExitOK, code, out)
	require.Equal(t, "[]\n", out, "an empty result is [], not null")
}

func TestSession_FlagsWorkBeforeAndAfterTheSessionID(t *testing.T) {
	repo := sessionRepo(t)

	before, outBefore := runSessionArgv(t, repo, "session", "--json", sessionID)
	after, outAfter := runSessionArgv(t, repo, "session", sessionID, "--json")
	require.Equal(t, cli.ExitOK, before, outBefore)
	require.Equal(t, cli.ExitOK, after, outAfter)
	require.Equal(t, outAfter, outBefore, "the flag's position does not change the report")
	require.Contains(t, outBefore, `"t-a"`)
}

func TestSession_AFlagValueIsNotASessionID(t *testing.T) {
	repo := sessionRepo(t)

	// --dir's value must not be read as the session id.
	code, out := runSessionArgv(t, repo, "session", "--dir", repo, sessionID)
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "t-a")
}

// TestSession_ReadsNoDatabase pins the ruling: membership is the label in
// issues.jsonl, so a store whose database names the bead but whose file does
// not is not reported, and one whose file names it but which has no database at
// all is.
func TestSession_ReadsNoDatabase(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	store := writeStore(t, repo, issue{ID: "t-a", Title: "labelled", Status: "open", IssueType: "task",
		UpdatedAt: "2026-10-19T01:10:24Z", Labels: []string{labels.Session(sessionID)}})
	require.NoFileExists(t, filepath.Join(store, "beads.db"), "the store has no database at all")

	code, out := runSession(t, repo, sessionID, "--json")
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, `"t-a"`)
}
