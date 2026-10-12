package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/cli"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "beads-tools-bin-*")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "beads-tools")
	build := exec.Command("go", "build", "-o", binary, "github.com/brynbellomy/beads-tools/cmd/beads-tools")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type row struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	CloseReason string `json:"close_reason"`
	Comments    []struct {
		ID   int64  `json:"id"`
		Text string `json:"text"`
	} `json:"comments"`
}

func rows(t *testing.T, dir string) map[string]row {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)
	out := map[string]row{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r row
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		out[r.ID] = r
	}
	return out
}

func texts(r row) []string {
	var out []string
	for _, c := range r.Comments {
		out = append(out, c.Text)
	}
	return out
}

func install(t *testing.T, r *testrepo.Repo) {
	t.Helper()
	r.Run(binary, "install", "--bin", binary)
	r.Commit("install beads-tools")
}

// The failure that motivated the tool: uncommitted issue edits stashed around
// a merge that changed the same issues.
func TestDriver_StashPopAcrossAMergeThatTouchedTheSameIssues(t *testing.T) {
	main := testrepo.New(t, "")
	epic := main.Create("epic", "--type", "epic")
	child := main.Create("child")
	main.Commit("issues")
	install(t, main)

	work := &testrepo.Repo{T: t, Dir: filepath.Join(t.TempDir(), "wt")}
	main.G("worktree", "add", "-q", "-b", "work", work.Dir)

	main.Run("br", "close", child, "--reason", "done in #5", "--quiet")
	main.Run("br", "comments", "add", epic, "from main", "--quiet")
	main.Commit("close child")

	work.Run("br", "sync", "--import-only", "--force", "--quiet")
	work.Run("br", "comments", "add", child, "local note", "--quiet")
	work.Run("br", "comments", "add", epic, "local epic note", "--quiet")
	work.G("stash", "-q")
	work.G("merge", "-q", "--no-edit", "main")
	work.G("stash", "pop", "-q")

	require.NotContains(t, work.G("status", "--porcelain"), "UU", "the pop merged without a conflict")
	got := rows(t, work.Dir)
	require.Equal(t, "closed", got[child].Status)
	require.Equal(t, "done in #5", got[child].CloseReason)
	require.Equal(t, []string{"local note"}, texts(got[child]))
	require.ElementsMatch(t, []string{"from main", "local epic note"}, texts(got[epic]))
	seen := map[int64]bool{}
	for _, r := range got {
		for _, c := range r.Comments {
			require.False(t, seen[c.ID], "comment id %d is unique", c.ID)
			seen[c.ID] = true
		}
	}
}

func TestDriver_BranchesThatBothAddChildrenKeepBoth(t *testing.T) {
	main := testrepo.New(t, "")
	epic := main.Create("epic", "--type", "epic")
	main.Commit("epic")
	install(t, main)

	main.G("branch", "side")
	a := main.Create("A child", "--parent", epic)
	main.Commit("a")

	side := &testrepo.Repo{T: t, Dir: filepath.Join(t.TempDir(), "side")}
	main.G("worktree", "add", "-q", side.Dir, "side")
	side.Run("br", "sync", "--import-only", "--force", "--quiet")
	b := side.Create("B child", "--parent", epic)
	require.Equal(t, a, b, "both sides minted the same child id")
	side.Commit("b")

	main.G("merge", "-q", "--no-edit", "side")

	titles := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(main.Dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		titles[m["title"].(string)] = true
	}
	require.True(t, titles["A child"] && titles["B child"], "neither child is lost")
}

// Minimized from sirene's merges 46199db11 and 63300e134: a note posted on
// two issues in the same second, then one comment added on each branch with the
// same id. git runs the driver; the untouched lines keep their bytes and every
// comment id stays unique.
func TestDriver_SameNoteOnSeveralIssuesSurvivesAMerge(t *testing.T) {
	const (
		a = `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":819,"issue_id":"t-a","author":"x","text":"SUBSUMED","created_at":"2026-07-17T19:14:12Z"}]}`
		b = `{"id":"t-b","title":"b","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":162,"issue_id":"t-b","author":"x","text":"SUBSUMED","created_at":"2026-07-17T19:14:12Z"}]}`
		x = `{"id":"t-x","title":"x","updated_at":"2026-01-01T00:00:00Z"}`
		y = `{"id":"t-y","title":"y","updated_at":"2026-01-01T00:00:00Z"}`
	)
	main := testrepo.New(t, "")
	install(t, main)
	main.Write(".beads/issues.jsonl", strings.Join([]string{a, b, x, y}, "\n")+"\n")
	main.Commit("issues")
	main.G("branch", "side")

	main.Write(".beads/issues.jsonl", strings.Join([]string{a, b,
		`{"id":"t-x","title":"x","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":820,"issue_id":"t-x","author":"o","text":"ours","created_at":"2026-01-02T00:00:00Z"}]}`,
		y}, "\n")+"\n")
	main.Commit("comment on x")

	side := &testrepo.Repo{T: t, Dir: filepath.Join(t.TempDir(), "side")}
	main.G("worktree", "add", "-q", side.Dir, "side")
	side.Write(".beads/issues.jsonl", strings.Join([]string{a, b, x,
		`{"id":"t-y","title":"y","updated_at":"2026-01-03T00:00:00Z","comments":[{"id":820,"issue_id":"t-y","author":"t","text":"theirs","created_at":"2026-01-03T00:00:00Z"}]}`,
	}, "\n")+"\n")
	side.Commit("comment on y")

	main.G("merge", "-q", "--no-edit", "side")

	data, err := os.ReadFile(filepath.Join(main.Dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(data), a+"\n")
	require.Contains(t, string(data), b+"\n")
	got := rows(t, main.Dir)
	require.Equal(t, int64(820), got["t-x"].Comments[0].ID)
	require.Equal(t, int64(821), got["t-y"].Comments[0].ID)
}

func TestMerge_BadInputExitsWithoutTouchingOurs(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		return p
	}
	good := `{"id":"t-a","title":"a"}` + "\n"
	base, ours := write("base", good), write("ours", good)
	theirs := write("theirs", "<<<<<<< HEAD\n")

	var stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"merge", base, ours, theirs}, &bytes.Buffer{}, &stderr)
	require.Equal(t, cli.ExitError, code)
	require.Contains(t, stderr.String(), "conflict marker")
	data, err := os.ReadFile(ours)
	require.NoError(t, err)
	require.Equal(t, good, string(data))
}

func TestMerge_StrictFailsOnNewerWinsDecisions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		return p
	}
	base := write("base", `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z"}`+"\n")
	ours := write("ours", `{"id":"t-a","title":"ours","updated_at":"2026-01-02T00:00:00Z"}`+"\n")
	theirs := write("theirs", `{"id":"t-a","title":"theirs","updated_at":"2026-01-03T00:00:00Z"}`+"\n")

	var stderr bytes.Buffer
	require.Equal(t, cli.ExitConflict, cli.Run(context.Background(), []string{"merge", "--strict", base, ours, theirs}, &bytes.Buffer{}, &stderr))
	require.Contains(t, stderr.String(), "conflict t-a [title]: kept theirs")
}

func TestInstall_IsIdempotentAndLeavesForeignHooksAlone(t *testing.T) {
	r := testrepo.New(t, "")
	r.Run(binary, "install", "--bin", binary)
	r.Run(binary, "install", "--bin", binary)
	attrs, err := os.ReadFile(filepath.Join(r.Dir, ".gitattributes"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(attrs), ".beads/issues.jsonl merge=beads"))
	require.Equal(t, binary+" merge %O %A %B %P", r.G("config", "merge.beads.driver"))

	other := testrepo.New(t, "")
	hookPath := filepath.Join(other.Dir, ".git", "hooks", "commit-msg")
	require.NoError(t, os.WriteFile(hookPath, []byte("#!/bin/sh\necho mine\n"), 0o755))
	out := other.Run(binary, "install", "--bin", binary)
	require.Contains(t, out, "left alone")
	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)
	require.Equal(t, "#!/bin/sh\necho mine\n", string(data))
}
