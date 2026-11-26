package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// tied is the updated_at both sides are given, so the merge cannot settle the
// row by time. It is far above any real clock, which is what makes it safe to
// overwrite the stamp both databases carry: a test may pick a value, the driver
// may not.
const (
	tied      = "2035-01-01T00:00:00Z"
	tiedStamp = "2035-01-01T00:00:01Z"
)

// The reproduction this change exists for. Main renames a bead and a worktree
// comments on it; both sides carry the same updated_at. The merge driver writes
// a row holding both changes. br keeps its own row in the worktree's database
// and decides between that and the file by updated_at, keeping the database's on
// a tie, so without the stamp the next import silently restores the old title.
func TestStamp_AMergedRowWinsTheWorktreeImport(t *testing.T) {
	testrepo.RequireBR(t)
	main := testrepo.New(t, "")
	target := main.Create("the old title")
	main.Commit("add issue")
	install(t, main)

	// The branch gets its own store, as a sirene worktree does.
	work := &testrepo.Repo{T: t, Dir: filepath.Join(t.TempDir(), "work")}
	main.G("worktree", "add", "-q", "-b", "feat/w", work.Dir)
	require.NoError(t, os.WriteFile(filepath.Join(work.Dir, ".beads", "redirect"), []byte(".\n"), 0o644))
	work.Run("br", "sync", "--import-only", "--quiet")

	// Main renames the bead; the branch comments on it. Each checkout's database
	// takes its own row back at the tied instant.
	main.Run("br", "update", target, "--title", "the new title", "--quiet")
	main.Run("br", "sync", "--flush-only", "--quiet")
	tie(t, main.Dir, target)
	main.Commit("rename on main")

	work.Run("br", "comments", "add", target, "a note from the branch", "--quiet")
	work.Run("br", "sync", "--flush-only", "--quiet")
	tie(t, work.Dir, target)
	work.Commit("bead note\n\nRefs: " + target)

	// Neither side is newer, so the merge settles the row some other way: it
	// keeps both changes and stamps it past the tie.
	work.G("merge", "-q", "--no-edit", "main")
	require.Equal(t, "the new title", title(t, work.Dir, target), "the file holds the merged row")
	require.Equal(t, []string{"a note from the branch"}, comments(t, work.Dir)[target])
	require.Equal(t, tiedStamp, updatedAt(t, work.Dir, target), "the merged row is past both parents")

	// The import after a pull now has a file row newer than the database's.
	brImport(t, work.Dir)
	require.Equal(t, "the new title", title(t, work.Dir, target), "the import kept the merged title")

	// A write flushes the database back out. The merged title must survive it, or
	// the next commit restores the old one.
	work.Run("br", "comments", "add", target, "a second note", "--quiet")
	work.Run("br", "sync", "--flush-only", "--quiet")
	require.Equal(t, "the new title", title(t, work.Dir, target), "the flush wrote the merged title, not the old one")
	require.Equal(t, []string{"a note from the branch", "a second note"}, comments(t, work.Dir)[target])
}

// Why the stamp is needed at all: br keeps its database's row when the file's
// updated_at is not strictly newer.
func TestImport_ATieKeepsTheDatabasesRow(t *testing.T) {
	testrepo.RequireBR(t)
	r := testrepo.New(t, "")
	id := r.Create("the database's title")
	r.Commit("add issue")
	tie(t, r.Dir, id)

	t.Run("a tie", func(t *testing.T) {
		retitle(t, r.Dir, id, "the file's title", tied)
		brImport(t, r.Dir)
		r.Run("br", "sync", "--flush-only", "--quiet")
		require.Equal(t, "the database's title", title(t, r.Dir, id), "the database's row wins a tie, which is what the stamp avoids")
	})

	t.Run("one unit newer", func(t *testing.T) {
		retitle(t, r.Dir, id, "the file's title", tiedStamp)
		brImport(t, r.Dir)
		r.Run("br", "sync", "--flush-only", "--quiet")
		require.Equal(t, "the file's title", title(t, r.Dir, id), "past the tie the file's row wins, so the import keeps it")
	})
}

func brImport(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("br", "sync", "--import-only")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// tie sets one issue's updated_at in the issues file, then imports it so the
// checkout's database holds the same instant.
func tie(t *testing.T, dir, id string) {
	t.Helper()
	retitle(t, dir, id, title(t, dir, id), tied)
	brImport(t, dir)
	require.Equal(t, tied, updatedAt(t, dir, id))
}

// retitle writes one issue's title and updated_at straight into the issues file.
func retitle(t *testing.T, dir, id, title, at string) {
	t.Helper()
	path := filepath.Join(dir, ".beads", "issues.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		if m["id"] == id {
			m["title"], m["updated_at"] = title, at
			b, err := json.Marshal(m)
			require.NoError(t, err)
			line = string(b)
		}
		lines = append(lines, line)
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func rowOf(t *testing.T, dir, id string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		if m["id"] == id {
			return m
		}
	}
	t.Fatalf("issue %s not in %s", id, dir)
	return nil
}

func title(t *testing.T, dir, id string) string {
	t.Helper()
	s, _ := rowOf(t, dir, id)["title"].(string)
	return s
}

func updatedAt(t *testing.T, dir, id string) string {
	t.Helper()
	s, _ := rowOf(t, dir, id)["updated_at"].(string)
	return s
}

func comments(t *testing.T, dir string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)
	out := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m struct {
			ID       string `json:"id"`
			Comments []struct {
				Text string `json:"text"`
			} `json:"comments"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		for _, c := range m.Comments {
			out[m.ID] = append(out[m.ID], c.Text)
		}
	}
	return out
}
