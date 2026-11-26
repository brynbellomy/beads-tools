package hook_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/hook"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// commentRow is one issue line carrying the given "id text" comments.
func commentRow(issue string, pairs ...string) string {
	rows := make([]string, len(pairs))
	for i, p := range pairs {
		id, text, _ := strings.Cut(p, " ")
		rows[i] = `{"id":` + id + `,"issue_id":` + strconv.Quote(issue) + `,"author":"x","text":` + strconv.Quote(text) +
			`,"created_at":"2026-01-01T00:00:0` + strconv.Itoa(i) + `Z"}`
	}
	return `{"id":` + strconv.Quote(issue) + `,"title":` + strconv.Quote(issue) + `,"comments":[` + strings.Join(rows, ",") + `]}`
}

// issuesFile joins rows into an issues file, keeping the one bead the hook's
// trailer check looks for. That check reads the working tree, so dropping the
// bead would make the trailer look unknown.
func issuesFile(id string, rows ...string) string {
	all := append(rows, `{"id":`+strconv.Quote(id)+`,"title":"work","created_at":"2026-01-01T00:00:00Z"}`)
	return strings.Join(all, "\n") + "\n"
}

// renumberRepo commits before as the issues file, then stages after on a branch.
// The hook decides from git alone, so no database is involved.
func renumberRepo(t *testing.T, before, after string) (*testrepo.Repo, string) {
	t.Helper()
	r := testrepo.New(t, "")
	id := r.Create("work")
	r.Write(".beads/issues.jsonl", issuesFile(id, before))
	r.Commit("beads: the comments")
	r.G("checkout", "-q", "-b", "feat/r")
	r.Write(".beads/issues.jsonl", issuesFile(id, after))
	r.G("add", ".beads/issues.jsonl")
	return r, id
}

// msg builds a commit message that names one bead, for the hook to check.
func msg(subject, id string) string {
	return subject + "\n\nRefs: " + id + "\n"
}

func TestCommitMsg_RefusesRenumberingAnExistingComment(t *testing.T) {
	// A stale store folded the pulled comment into its own counter and moved it.
	r, id := renumberRepo(t, commentRow("t-a", "4 from main"), commentRow("t-a", "5 from main"))

	err := check(t, r, "beads: flush\n\nRefs: "+id+"\n")
	require.ErrorContains(t, err, "renumbers comments that already existed")
	require.ErrorContains(t, err, `comment "from main" by "x" at "2026-01-01T00:00:00Z" on t-a: id 4 -> 5`)
	require.ErrorContains(t, err, "Leave each comment at the id it had")
}

func TestCommitMsg_AcceptsANewCommentAndAnEditedBead(t *testing.T) {
	before := `{"id":"t-a","title":"old","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"first","created_at":"2026-01-01T00:00:00Z"}]}`
	after := `{"id":"t-a","title":"new","status":"closed","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"first","created_at":"2026-01-01T00:00:00Z"},{"id":8,"issue_id":"t-a","author":"y","text":"second","created_at":"2026-01-02T00:00:00Z"}]}`
	r, id := renumberRepo(t, before, after)
	require.NoError(t, check(t, r, "beads: edit and comment\n\nRefs: "+id+"\n"))
}

// Moving one bead's comment is judged on that bead alone.
func TestCommitMsg_CommentsOnDifferentBeadsAreJudgedSeparately(t *testing.T) {
	r, id := renumberRepo(t,
		strings.Join([]string{commentRow("t-a", "7 note"), commentRow("t-b", "8 note")}, "\n"),
		strings.Join([]string{commentRow("t-a", "9 note"), commentRow("t-b", "8 note")}, "\n"))

	err := check(t, r, "beads: flush\n\nRefs: "+id+"\n")
	require.ErrorContains(t, err, "on t-a: id 7 -> 9")
	require.NotContains(t, err.Error(), "on t-b", "t-b's comment did not move")
}

// Two byte-identical comments on one bead are interchangeable, so reordering
// them renumbers nothing and the hook lets it through. An id given up is still
// refused, which the neighbouring tests cover.
func TestCommitMsg_IdenticalCommentsWithinOneBeadMayBeReordered(t *testing.T) {
	same := func(a, b string) string {
		return `{"id":"t-a","title":"a","comments":[{"id":` + a + `,"issue_id":"t-a","author":"x","text":"same","created_at":"2026-01-01T00:00:00Z"},{"id":` + b + `,"issue_id":"t-a","author":"x","text":"same","created_at":"2026-01-01T00:00:00Z"}]}`
	}
	r, id := renumberRepo(t, same("7", "8"), same("7", "8"))
	require.NoError(t, check(t, r, msg("beads: nothing moved", id)))

	r, id = renumberRepo(t, same("7", "8"), same("8", "7"))
	require.NoError(t, check(t, r, msg("beads: reordered", id)),
		"the same two ids stay on the same two identical comments")

	gaveOneUp := `{"id":"t-a","title":"a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"same","created_at":"2026-01-01T00:00:00Z"}]}`
	r, id = renumberRepo(t, same("7", "8"), gaveOneUp)
	err := check(t, r, msg("beads: dropped one", id))
	require.ErrorContains(t, err, "renumbers comments that already existed")
}

// A merge commit may inherit an id either parent holds for the comment: ours has
// 7, the branch being merged has 61.
func TestCommitMsg_MergeMayInheritAnIDEitherParentHolds(t *testing.T) {
	r := testrepo.New(t, "")
	id := r.Create("work")
	r.Write(".beads/issues.jsonl", issuesFile(id, commentRow("t-a", "7 note")))
	r.Commit("beads: the comment")

	r.G("checkout", "-q", "-b", "side")
	r.Write(".beads/issues.jsonl", issuesFile(id, commentRow("t-a", "61 note")))
	r.G("add", ".beads/issues.jsonl")
	r.G("-c", "core.hooksPath=/dev/null", "commit", "-qm", "beads: renumbered upstream\n\nRefs: "+id)

	r.G("checkout", "-q", "main")
	r.Write("m.go", "package m\n")
	r.Commit("feat: m\n\nRefs: " + id)

	r.G("merge", "-q", "--no-commit", "--no-ff", "side")
	require.NoError(t, check(t, r, "Merge branch 'side' into main\n"), "61 is the merged branch's id for the comment")
}

// An id neither side holds is a renumbering, a merge included.
func TestCommitMsg_MergeMayNotInventAnID(t *testing.T) {
	r := testrepo.New(t, "")
	id := r.Create("work")
	r.Write(".beads/issues.jsonl", issuesFile(id, commentRow("t-a", "7 note")))
	r.Commit("beads: the comment")
	r.G("checkout", "-q", "-b", "feat/n")
	r.Write(".beads/issues.jsonl", issuesFile(id, commentRow("t-a", "99 note")))
	r.G("add", ".beads/issues.jsonl")

	err := check(t, r, "beads: flush\n\nRefs: "+id+"\n")
	require.ErrorContains(t, err, "renumbers comments that already existed")
	require.ErrorContains(t, err, "id 7 -> 99")
}

func TestCommitMsg_SkipEnvStillSkips(t *testing.T) {
	r, id := renumberRepo(t, commentRow("t-a", "4 from main"), commentRow("t-a", "5 from main"))
	t.Setenv(hook.SkipEnv, "1")
	require.NoError(t, check(t, r, "beads: flush\n\nRefs: "+id+"\n"))
}

// A comment created in the same commit has no id in the parent, so it is not a
// renumbering.
func TestCommitMsg_ACommentCreatedInThisCommitIsFine(t *testing.T) {
	r, id := renumberRepo(t, `{"id":"t-a","title":"a"}`, commentRow("t-a", "12 a new note"))
	require.NoError(t, check(t, r, "beads: a note\n\nRefs: "+id+"\n"))
}

// A duplicate id the commit does not stage is not its business.
func TestCommitMsg_ADuplicateIDTheCommitDoesNotStageIsNotItsBusiness(t *testing.T) {
	r := testrepo.New(t, "")
	id := r.Create("work")
	r.Write(".beads/issues.jsonl", issuesFile(id, commentRow("t-a", "7 one"), commentRow("t-b", "7 two")))
	r.G("add", ".beads/issues.jsonl")
	r.G("-c", "core.hooksPath=/dev/null", "commit", "-qm", "beads: a duplicate on main")
	r.G("checkout", "-q", "-b", "feat/d")
	r.Write("d.go", "package d\n")
	r.G("add", "d.go")
	require.NoError(t, check(t, r, "feat: d\n\nRefs: "+id+"\n"))
}

// A revert names no bead, and the trailer rule exempts one, as land pr does. The
// file-integrity checks are a different matter: a revert that would give a
// comment a new id is refused like any other commit, since merges and reverts
// bring old file states back and that is exactly when an id goes astray.
func TestCommitMsg_ARevertIsExemptFromTrailersButNotFromRenumbering(t *testing.T) {
	r, _ := renumberRepo(t, commentRow("t-a", "4 from main"), commentRow("t-a", "5 from main"))

	revert := "Revert \"beads: import\"\n\nThis reverts commit 0123456789abcdef.\n"
	err := check(t, r, revert)
	require.ErrorContains(t, err, "renumbers comments that already existed",
		"the renumber check still applies to a revert")

	// The same revert over a file whose comments kept their ids passes, and the
	// missing trailer does not stop it.
	r, _ = renumberRepo(t, commentRow("t-a", "4 from main"), commentRow("t-a", "4 from main"))
	require.NoError(t, check(t, r, "Revert \"feat: something\"\n\nThis reverts commit 0123456789abcdef.\n"))
}
