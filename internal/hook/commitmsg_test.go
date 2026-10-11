package hook_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/hook"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

func check(t *testing.T, r *testrepo.Repo, msg string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	require.NoError(t, os.WriteFile(path, []byte(msg), 0o644))
	return hook.CommitMsg(context.Background(), r.Git(), path)
}

func featureRepo(t *testing.T) (*testrepo.Repo, string) {
	t.Helper()
	r := testrepo.New(t, "")
	id := r.Create("work")
	r.Commit("add issue")
	r.G("checkout", "-q", "-b", "feat/x")
	r.Write("x.go", "package x\n")
	r.G("add", "x.go")
	return r, id
}

func TestCommitMsg_AcceptsKnownTrailers(t *testing.T) {
	r, id := featureRepo(t)
	require.NoError(t, check(t, r, "feat: x\n\nCloses: "+id+"\n"))
	require.NoError(t, check(t, r, "feat: x\n\nRefs: "+id+"\n"))
	require.NoError(t, check(t, r, "feat: x\n\nrefs: "+id+"\n"), "trailer keys are case-insensitive")
}

func TestCommitMsg_RejectsUnknownIDs(t *testing.T) {
	r, id := featureRepo(t)
	err := check(t, r, "feat: x\n\nCloses: "+id+", t-nope\n")
	require.ErrorContains(t, err, "t-nope")
}

func TestCommitMsg_RequiresATrailerOffMain(t *testing.T) {
	r, _ := featureRepo(t)
	require.ErrorContains(t, check(t, r, "feat: x\n"), "names no issue")
	require.ErrorContains(t, check(t, r, "feat: x\n\nCloses t-x is prose, not a trailer\n"), "names no issue")
}

func TestCommitMsg_Exemptions(t *testing.T) {
	r, _ := featureRepo(t)
	require.NoError(t, check(t, r, "fixup! feat: x\n"))
	require.NoError(t, check(t, r, "Merge branch 'main' into feat/x\n"))

	t.Setenv(hook.SkipEnv, "1")
	require.NoError(t, check(t, r, "feat: x\n"))
}

func TestCommitMsg_IssuesOnlyCommitNeedsNoTrailer(t *testing.T) {
	r, _ := featureRepo(t)
	r.G("reset", "-q")
	r.Create("another")
	r.G("add", ".beads/issues.jsonl")
	require.NoError(t, check(t, r, "beads: file another\n"))
}

func TestCommitMsg_MainNeedsNoTrailerAndConfigCanDisable(t *testing.T) {
	r := testrepo.New(t, "")
	r.Write("x.go", "package x\n")
	r.G("add", "x.go")
	require.NoError(t, check(t, r, "chore: on main\n"))

	r.G("checkout", "-q", "-b", "feat/y")
	r.G("config", "beadstools.requireTrailers", "false")
	require.NoError(t, check(t, r, "chore: anything\n"))
}

func TestCommitMsg_IssueCreatedInThisCommitCounts(t *testing.T) {
	r, _ := featureRepo(t)
	id := r.Create("new in this commit")
	require.NoError(t, check(t, r, "feat: x\n\nCloses: "+id+"\n"))
}

func TestCommitMsg_RepoWithoutBeadsIsLeftAlone(t *testing.T) {
	r := testrepo.New(t, "")
	require.NoError(t, os.RemoveAll(filepath.Join(r.Dir, ".beads")))
	r.G("rm", "-rq", "--cached", ".beads")
	r.G("checkout", "-q", "-b", "feat/z")
	r.Write("z.go", "package z\n")
	r.G("add", "z.go")
	require.NoError(t, check(t, r, "feat: z\n"))
}
