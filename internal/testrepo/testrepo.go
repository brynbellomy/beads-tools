// Package testrepo builds throwaway git repositories with a br workspace for
// tests. Every command runs with an explicit directory; nothing changes the
// test process's working directory.
package testrepo

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/gitx"
)

// Repo is a scratch repository.
type Repo struct {
	T   *testing.T
	Dir string
}

// RequireBR skips the test when br is not installed.
func RequireBR(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("br"); err != nil {
		t.Skip("br not installed")
	}
}

// Git returns a runner for the repository.
func (r *Repo) Git() gitx.Repo { return gitx.Repo{Dir: r.Dir} }

// Run executes a command in the repository and returns trimmed stdout.
func (r *Repo) Run(name string, args ...string) string {
	r.T.Helper()
	out, err := gitx.Command(context.Background(), r.Dir, name, args...)
	require.NoError(r.T, err)
	return strings.TrimSpace(out)
}

// G runs git.
func (r *Repo) G(args ...string) string {
	r.T.Helper()
	return r.Run("git", args...)
}

// NewRemote creates a bare repository to act as origin.
func NewRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	_, err := gitx.Command(context.Background(), filepath.Dir(dir), "git", "init", "-q", "--bare", "-b", "main", dir)
	require.NoError(t, err)
	return dir
}

// New creates a repository on main with a br workspace (prefix t) and one
// commit. When remote is non-empty it becomes origin and main is pushed.
func New(t *testing.T, remote string) *Repo {
	t.Helper()
	RequireBR(t)
	dir := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	r := &Repo{T: t, Dir: dir}
	r.G("init", "-q", "-b", "main")
	r.G("config", "user.email", "test@example.com")
	r.G("config", "user.name", "test")
	r.G("config", "commit.gpgsign", "false")
	r.Run("br", "init", "--prefix", "t", "--quiet")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte(".beads/*.db*\n.beads/last-touched\n.beads/.br_history/\n.beads/.br_recovery/\n.beads/*.lock\n"), 0o644))
	r.G("add", "-A")
	r.G("commit", "-qm", "init")
	if remote != "" {
		r.G("remote", "add", "origin", remote)
		r.G("push", "-q", "-u", "origin", "main")
		r.G("remote", "set-head", "origin", "main")
	}
	return r
}

// Create makes an issue with br and returns its ID.
func (r *Repo) Create(title string, extra ...string) string {
	r.T.Helper()
	return r.Run("br", append([]string{"create", title, "--silent"}, extra...)...)
}

// Commit stages everything and commits with message.
func (r *Repo) Commit(message string) string {
	r.T.Helper()
	r.G("add", "-A")
	r.G("-c", "core.hooksPath=/dev/null", "commit", "-q", "-m", message)
	return r.G("rev-parse", "HEAD")
}

// Write writes a file relative to the repository root.
func (r *Repo) Write(path, content string) {
	r.T.Helper()
	full := filepath.Join(r.Dir, path)
	require.NoError(r.T, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(r.T, os.WriteFile(full, []byte(content), 0o644))
}

// Status returns an issue's status in the issues file at rev.
func (r *Repo) Status(rev, id string) string {
	r.T.Helper()
	for _, line := range strings.Split(r.G("show", rev+":.beads/issues.jsonl"), "\n") {
		var m map[string]any
		require.NoError(r.T, json.Unmarshal([]byte(line), &m))
		if m["id"] == id {
			s, _ := m["status"].(string)
			return s
		}
	}
	r.T.Fatalf("issue %s not at %s", id, rev)
	return ""
}

// ChangedRows returns the IDs whose lines differ between two revisions of the
// issues file.
func (r *Repo) ChangedRows(from, to string) []string {
	r.T.Helper()
	rows := func(rev string) map[string]string {
		m := map[string]string{}
		for _, line := range strings.Split(r.G("show", rev+":.beads/issues.jsonl"), "\n") {
			var obj struct {
				ID string `json:"id"`
			}
			require.NoError(r.T, json.Unmarshal([]byte(line), &obj))
			m[obj.ID] = line
		}
		return m
	}
	a, b := rows(from), rows(to)
	var out []string
	for id, line := range b {
		if a[id] != line {
			out = append(out, id)
		}
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}
