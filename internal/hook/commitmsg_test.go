package hook_test

import (
	"context"
	"fmt"
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

func TestCommitMsg_RefusesStagedDuplicateCommentIDs(t *testing.T) {
	r, id := featureRepo(t)
	path := filepath.Join(r.Dir, ".beads", "issues.jsonl")
	clean, err := os.ReadFile(path)
	require.NoError(t, err)
	dups := `{"id":"t-da","title":"a","comments":[{"id":7,"issue_id":"t-da","author":"x","text":"n","created_at":"2026-01-01T00:00:00Z"}]}` + "\n" +
		`{"id":"t-db","title":"b","comments":[{"id":7,"issue_id":"t-db","author":"x","text":"n","created_at":"2026-01-01T00:00:00Z"}]}` + "\n"
	r.Write(".beads/issues.jsonl", string(clean)+dups)

	require.NoError(t, check(t, r, "feat: x\n\nCloses: "+id+"\n"), "duplicates the commit does not stage are not its business")

	r.G("add", ".beads/issues.jsonl")
	err = check(t, r, "feat: x\n\nCloses: "+id+"\n")
	require.ErrorContains(t, err, "comment id 7 is used 2 times (t-da, t-db)")
	require.ErrorContains(t, check(t, r, "Merge branch 'main' into feat/x\n"), "duplicate comment ids", "merges are checked too")

	t.Setenv(hook.SkipEnv, "1")
	require.NoError(t, check(t, r, "feat: x\n\nCloses: "+id+"\n"))
}

func dupRows(id int, a, b string) string {
	row := func(issue string) string {
		return fmt.Sprintf(`{"id":"%s","title":"%s","comments":[{"id":%d,"issue_id":"%s","author":"x","text":"n","created_at":"2026-01-01T00:00:00Z"}]}`, issue, issue, id, issue)
	}
	return row(a) + "\n" + row(b) + "\n"
}

func appendIssues(t *testing.T, r *testrepo.Repo, rows string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.Dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)
	r.Write(".beads/issues.jsonl", string(data)+rows)
}

// mainWithDuplicates gives main a duplicate comment id after feat/x forked,
// then starts merging main into feat/x without committing.
func mainWithDuplicates(t *testing.T) *testrepo.Repo {
	t.Helper()
	r := testrepo.New(t, "")
	r.Create("work")
	r.Commit("add issue")
	r.G("branch", "feat/x")
	appendIssues(t, r, dupRows(7, "t-da", "t-db"))
	r.Commit("beads: import with a duplicate")
	r.G("checkout", "-q", "feat/x")
	r.Write("x.go", "package x\n")
	r.Commit("feat: x")
	r.G("-c", "core.hooksPath=/dev/null", "merge", "-q", "--no-commit", "--no-ff", "main")
	return r
}

func TestCommitMsg_MergeThatInheritsDuplicatesPasses(t *testing.T) {
	r := mainWithDuplicates(t)
	require.NoError(t, check(t, r, "Merge branch 'main' into feat/x\n"))
}

func TestCommitMsg_MergeThatAddsADuplicateFails(t *testing.T) {
	r := mainWithDuplicates(t)
	appendIssues(t, r, dupRows(9, "t-dc", "t-dd"))
	r.G("add", ".beads/issues.jsonl")
	err := check(t, r, "Merge branch 'main' into feat/x\n")
	require.ErrorContains(t, err, "comment id 9 is used 2 times (t-dc, t-dd)")
	require.NotContains(t, err.Error(), "comment id 7", "the inherited duplicate is not reported")
}

func TestCommitMsg_CommitOnTopOfInheritedDuplicatesPasses(t *testing.T) {
	r := testrepo.New(t, "")
	id := r.Create("work")
	appendIssues(t, r, dupRows(7, "t-da", "t-db"))
	r.Commit("beads: import with a duplicate")
	r.G("checkout", "-q", "-b", "feat/y")
	appendIssues(t, r, `{"id":"t-new","title":"another"}`+"\n")
	r.G("add", ".beads/issues.jsonl")
	require.NoError(t, check(t, r, "beads: file another\n\nRefs: "+id+"\n"))
}
