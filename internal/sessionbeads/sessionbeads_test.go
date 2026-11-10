package sessionbeads_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/labels"
	"github.com/brynbellomy/beads-tools/internal/sessionbeads"
)

const session = "01a14743-2ec7-79eb-a9ab-39c56ca73589"

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

// store writes a beads store at dir/.beads holding issues and returns its path.
func store(t *testing.T, dir string, issues ...issue) string {
	t.Helper()
	path := filepath.Join(dir, ".beads")
	require.NoError(t, os.MkdirAll(path, 0o755))
	var buf bytes.Buffer
	for _, iss := range issues {
		line, err := json.Marshal(iss)
		require.NoError(t, err)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(path, "issues.jsonl"), buf.Bytes(), 0o644))
	return path
}

// checkout makes dir a git repository root, so the store walk stops there.
func checkout(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
}

func TestTouchedIn_ReportsTheBeadsCarryingTheSessionLabel(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	store(t, dir,
		issue{ID: "t-a", Title: "a child", Status: "in_progress", IssueType: "bug", UpdatedAt: "2026-10-19T01:10:24Z",
			Labels:       []string{labels.Session(session)},
			Dependencies: []dep{{IssueID: "t-a", DependsOnID: "t-epic", Type: "parent-child"}}},
		issue{ID: "t-b", Title: "another session's", Status: "open", IssueType: "task", UpdatedAt: "2026-10-19T01:10:24Z",
			Labels: []string{labels.Session("019f0000-0000-7000-8000-000000000000")}},
		issue{ID: "t-c", Title: "unlabelled", Status: "open", IssueType: "task", UpdatedAt: "2026-10-19T01:10:24Z"},
	)

	beads, err := sessionbeads.TouchedIn(context.Background(), dir, session)
	require.NoError(t, err)
	require.Equal(t, []sessionbeads.Bead{{
		ID: "t-a", Title: "a child", Status: "in_progress", Type: "bug",
		Updated: "2026-10-19T01:10:24Z", Parent: "t-epic",
	}}, beads)
}

func TestTouchedByUserIn_ReportsTheBeadsCarryingTheUserLabel(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	store(t, dir,
		issue{ID: "t-a", Title: "kenny's", Status: "open", IssueType: "task", UpdatedAt: "2026-10-19T00:00:00Z",
			Labels: []string{labels.User("bryn@signals.io")}},
		issue{ID: "t-b", Title: "someone else's", Status: "open", IssueType: "task", UpdatedAt: "2026-10-19T00:00:00Z",
			Labels: []string{labels.User("other@example.com")}},
	)

	beads, err := sessionbeads.TouchedByUserIn(context.Background(), dir, "bryn@signals.io")
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "t-a", beads[0].ID)
}

func TestTouchedIn_ReadsEveryLiveWorktree(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	store(t, dir, issue{ID: "t-main", Title: "main", Status: "open", IssueType: "task",
		UpdatedAt: "2026-10-19T00:00:00Z", Labels: []string{labels.Session(session)}})

	wt := filepath.Join(t.TempDir(), "wt")
	gitRepoWithWorktree(t, dir, wt)
	store(t, wt, issue{ID: "t-wt", Title: "worktree", Status: "open", IssueType: "task",
		UpdatedAt: "2026-10-20T00:00:00Z", Labels: []string{labels.Session(session)}})

	beads, err := sessionbeads.TouchedIn(context.Background(), dir, session)
	require.NoError(t, err)
	require.Len(t, beads, 2)
	require.Equal(t, "t-main", beads[0].ID)
	require.Equal(t, "t-wt", beads[1].ID)
}

// gitRepoWithWorktree makes dir a git repository and adds wt as a linked
// worktree of it, so `git worktree list` names both.
func gitRepoWithWorktree(t *testing.T, dir, wt string) {
	t.Helper()
	git := func(where string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = where
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git(dir, "init", "-q")
	git(dir, "config", "user.email", "t@example.com")
	git(dir, "config", "user.name", "t")
	git(dir, "add", "-A")
	git(dir, "commit", "-q", "-m", "seed")
	git(dir, "worktree", "add", "-q", "--detach", wt)
}

func TestTouchedIn_OneBeadInTwoStoresIsOneRowAndTheNewestWins(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	store(t, dir, issue{ID: "t-a", Title: "the older title", Status: "open", IssueType: "task",
		UpdatedAt: "2026-10-19T00:00:00Z", Labels: []string{labels.Session(session)}})

	wt := filepath.Join(t.TempDir(), "wt")
	gitRepoWithWorktree(t, dir, wt)
	store(t, wt, issue{ID: "t-a", Title: "the newer title", Status: "closed", IssueType: "task",
		UpdatedAt: "2026-10-21T00:00:00Z", Labels: []string{labels.Session(session)}})

	beads, err := sessionbeads.TouchedIn(context.Background(), dir, session)
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "the newer title", beads[0].Title)
	require.Equal(t, "closed", beads[0].Status)
}

func TestTouchedIn_ReadsNoDatabase(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	path := store(t, dir, issue{ID: "t-a", Title: "labelled", Status: "open", IssueType: "task",
		UpdatedAt: "2026-10-19T00:00:00Z", Labels: []string{labels.Session(session)}})
	require.NoFileExists(t, filepath.Join(path, "beads.db"), "the store has no database at all")

	beads, err := sessionbeads.TouchedIn(context.Background(), dir, session)
	require.NoError(t, err)
	require.Len(t, beads, 1)
}

func TestTouchedIn_AnEmptySessionIsNoBeads(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	store(t, dir, issue{ID: "t-a", Title: "unlabelled", Status: "open", IssueType: "task", UpdatedAt: "2026-10-19T00:00:00Z"})

	beads, err := sessionbeads.TouchedIn(context.Background(), dir, session)
	require.NoError(t, err)
	require.Empty(t, beads)
}

func TestTouchedIn_ACheckoutWithNoStoreIsNoBeads(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)

	beads, err := sessionbeads.TouchedIn(context.Background(), dir, session)
	require.NoError(t, err)
	require.Empty(t, beads)
}

func TestTouchedIn_RefusesAnEmptySessionID(t *testing.T) {
	_, err := sessionbeads.TouchedIn(context.Background(), t.TempDir(), "")
	require.ErrorContains(t, err, "no session id")
}

func TestTouchedByUserIn_RefusesAnEmptyEmail(t *testing.T) {
	_, err := sessionbeads.TouchedByUserIn(context.Background(), t.TempDir(), "")
	require.ErrorContains(t, err, "no email")
}

func TestTouchedIn_FollowsTheRedirectFile(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	// The worktree's .beads redirects to the main checkout's store, as br does
	// when a worktree shares one store.
	main := store(t, dir, issue{ID: "t-a", Title: "shared", Status: "open", IssueType: "task",
		UpdatedAt: "2026-10-19T00:00:00Z", Labels: []string{labels.Session(session)}})
	wt := filepath.Join(t.TempDir(), "wt")
	gitRepoWithWorktree(t, dir, wt)
	require.NoError(t, os.MkdirAll(filepath.Join(wt, ".beads"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".beads", "redirect"), []byte(main+"\n"), 0o644))

	beads, err := sessionbeads.TouchedIn(context.Background(), dir, session)
	require.NoError(t, err)
	require.Len(t, beads, 1, "the redirected store is read once, not twice")
}

func TestTouchedIn_IsSortedByID(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	store(t, dir,
		issue{ID: "t-c", Title: "c", Status: "open", IssueType: "task", UpdatedAt: "2026-10-19T00:00:00Z", Labels: []string{labels.Session(session)}},
		issue{ID: "t-a", Title: "a", Status: "open", IssueType: "task", UpdatedAt: "2026-10-19T00:00:00Z", Labels: []string{labels.Session(session)}},
		issue{ID: "t-b", Title: "b", Status: "open", IssueType: "task", UpdatedAt: "2026-10-19T00:00:00Z", Labels: []string{labels.Session(session)}},
	)

	beads, err := sessionbeads.TouchedIn(context.Background(), dir, session)
	require.NoError(t, err)
	require.Equal(t, []string{"t-a", "t-b", "t-c"}, []string{beads[0].ID, beads[1].ID, beads[2].ID})
}

func TestTouchedIn_ABeadWithNoParentHasNone(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	store(t, dir, issue{ID: "t-a", Title: "no parent", Status: "open", IssueType: "task",
		UpdatedAt: "2026-10-19T00:00:00Z", Labels: []string{labels.Session(session)}})

	beads, err := sessionbeads.TouchedIn(context.Background(), dir, session)
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Empty(t, beads[0].Parent)
}

func TestTouchedIn_IgnoresADependencyThatIsNotParentChild(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	store(t, dir, issue{ID: "t-a", Title: "blocked", Status: "open", IssueType: "task",
		UpdatedAt: "2026-10-19T00:00:00Z", Labels: []string{labels.Session(session)},
		Dependencies: []dep{{IssueID: "t-a", DependsOnID: "t-blocker", Type: "blocks"}}})

	beads, err := sessionbeads.TouchedIn(context.Background(), dir, session)
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Empty(t, beads[0].Parent, "a blocks edge is not a parent")
}

func TestStores_NamesTheMainCheckoutAndEachWorktree(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir)
	main := store(t, dir, issue{ID: "t-a"})
	wt := filepath.Join(t.TempDir(), "wt")
	gitRepoWithWorktree(t, dir, wt)
	wtStore := store(t, wt, issue{ID: "t-b"})

	stores, err := sessionbeads.Stores(context.Background(), dir)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{canonical(t, main), canonical(t, wtStore)}, stores)
}

// canonical resolves the links of path, so a test compares the store the reader
// names with the one it wrote.
func canonical(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}
