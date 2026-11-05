package hook_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/hook"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// row renders an issue whose dependencies are "target:type", stored on the
// issue as br stores them.
func row(id, status string, deps ...string) string {
	ds := make([]string, len(deps))
	for i, d := range deps {
		to, typ, _ := strings.Cut(d, ":")
		ds[i] = fmt.Sprintf(`{"issue_id":%q,"depends_on_id":%q,"type":%q}`, id, to, typ)
	}
	return fmt.Sprintf(`{"id":%q,"title":%q,"status":%q,"dependencies":[%s]}`, id, id, status, strings.Join(ds, ","))
}

// stage writes the issues file as rows and stages it.
func stage(r *testrepo.Repo, rows ...string) {
	r.Write(".beads/issues.jsonl", strings.Join(rows, "\n")+"\n")
	r.G("add", ".beads/issues.jsonl")
}

// beadsRepo commits rows as the issues file on main, then checks out feat/c.
func beadsRepo(t *testing.T, rows ...string) *testrepo.Repo {
	t.Helper()
	r := testrepo.New(t, "")
	stage(r, rows...)
	r.Commit("beads: issues")
	r.G("checkout", "-q", "-b", "feat/c")
	return r
}

func TestCommitMsg_RefusesARelatedEdgeThatClosesACycle(t *testing.T) {
	r := beadsRepo(t, row("t-a", "open", "t-b:related"), row("t-b", "open", "t-c:blocks"), row("t-c", "open"))
	stage(r, row("t-a", "open", "t-b:related"), row("t-b", "open", "t-c:blocks"), row("t-c", "open", "t-a:related"))

	err := check(t, r, "beads: relate\n")
	require.EqualError(t, err, "the staged .beads/issues.jsonl adds a dependency cycle: "+
		"t-c related to t-a, t-a related to t-b, t-b waits on t-c (blocks). "+
		"Remove one edge, usually a related one, and say why in a comment on the bead.")
	require.ErrorContains(t, check(t, r, "Merge branch 'main' into feat/c\n"), "adds a dependency cycle", "merge subjects are checked too")

	t.Setenv(hook.SkipEnv, "1")
	require.NoError(t, check(t, r, "beads: relate\n"))
}

func TestCommitMsg_WalksParentChildFromParentToChildAndCountsClosedBeads(t *testing.T) {
	r := beadsRepo(t, row("t-p", "open"), row("t-k", "open"), row("t-q", "open"))

	stage(r, row("t-p", "open", "t-k:related"), row("t-k", "open", "t-p:parent-child"), row("t-q", "open"))
	require.NoError(t, check(t, r, "beads: child\n"), "a parent-child edge does not run from the child to the parent")

	stage(r, row("t-p", "closed"), row("t-k", "closed", "t-p:parent-child", "t-q:related"), row("t-q", "tombstone", "t-p:related"))
	require.ErrorContains(t, check(t, r, "beads: child\n"),
		"adds a dependency cycle: t-p parent of t-k, t-k related to t-q, t-q related to t-p.")
}

func TestCommitMsg_CommitOnTopOfAnInheritedCyclePasses(t *testing.T) {
	r := beadsRepo(t, row("t-a", "open", "t-b:related"), row("t-b", "open", "t-a:blocks"), row("t-c", "open"))

	stage(r, row("t-a", "closed", "t-b:related", "t-c:related"), row("t-b", "open", "t-a:blocks"), row("t-c", "open"))
	require.NoError(t, check(t, r, "beads: close t-a\n"), "a status and an unrelated edge on the cycle's beads keep it inherited")

	stage(r, row("t-a", "open", "t-b:related", "t-c:related"), row("t-b", "open", "t-a:blocks"), row("t-c", "open", "t-b:related"))
	require.ErrorContains(t, check(t, r, "beads: relate t-c\n"),
		"adds a dependency cycle: t-a related to t-c, t-c related to t-b, t-b waits on t-a (blocks).",
		"an edge that closes a new cycle through the inherited cycle's beads is refused")
}

// mergeInProgress forks feat/m from rows, commits main and feat on each
// side, and starts merging main into feat/m without committing.
func mergeInProgress(t *testing.T, base, main, feat []string) *testrepo.Repo {
	t.Helper()
	r := testrepo.New(t, "")
	stage(r, base...)
	r.Commit("beads: issues")
	r.G("branch", "feat/m")
	stage(r, main...)
	r.Commit("beads: main")
	r.G("checkout", "-q", "feat/m")
	stage(r, feat...)
	r.Commit("beads: feat")
	r.G("-c", "core.hooksPath=/dev/null", "merge", "-q", "--no-commit", "--no-ff", "-s", "ours", "main")
	return r
}

func TestCommitMsg_MergeThatInheritsMainsCyclePasses(t *testing.T) {
	cycle := []string{row("t-a", "open", "t-b:related"), row("t-x", "open"), row("t-b", "open", "t-a:related")}
	r := mergeInProgress(t,
		[]string{row("t-a", "open"), row("t-x", "open"), row("t-b", "open")},
		cycle,
		[]string{row("t-a", "open"), row("t-x", "closed"), row("t-b", "open")})
	stage(r, row("t-a", "open", "t-b:related"), row("t-x", "closed"), row("t-b", "open", "t-a:related"))
	require.NoError(t, check(t, r, "Merge branch 'main' into feat/m\n"))
}

func TestCommitMsg_MergeThatAddsACycleFails(t *testing.T) {
	r := mergeInProgress(t,
		[]string{row("t-a", "open"), row("t-x", "open"), row("t-b", "open")},
		[]string{row("t-a", "open", "t-b:related"), row("t-x", "open"), row("t-b", "open")},
		[]string{row("t-a", "open"), row("t-x", "open"), row("t-b", "open", "t-a:related")})
	stage(r, row("t-a", "open", "t-b:related"), row("t-x", "open"), row("t-b", "open", "t-a:related"))
	require.ErrorContains(t, check(t, r, "Merge branch 'main' into feat/m\n"),
		"adds a dependency cycle: t-a related to t-b, t-b related to t-a.", "a cycle neither parent holds is new")
}
