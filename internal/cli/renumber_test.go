package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/cli"
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

// writeIssues replaces the issues file with rows plus the bead the range's
// trailers name.
func writeIssues(t *testing.T, r *testrepo.Repo, id string, rows ...string) {
	t.Helper()
	all := append(append([]string(nil), rows...), `{"id":`+strconv.Quote(id)+`,"title":"work","created_at":"2026-01-01T00:00:00Z"}`)
	r.Write(".beads/issues.jsonl", strings.Join(all, "\n")+"\n")
}

// A branch whose middle commit renumbered a comment, and whose head is clean
// again, cannot land: the hook only ever sees one commit.
func TestCheck_RefusesARenumberInAMiddleCommit(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	id := r.Create("work")
	writeIssues(t, r, id, commentRow("t-a", "4 from main"))
	base := r.Commit("beads: the comment")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/c")

	writeIssues(t, r, id, commentRow("t-a", "5 from main"))
	r.Commit("beads: import\n\nRefs: " + id)
	r.Write("a.go", "package a\n")
	r.Commit("feat: a\n\nRefs: " + id)

	code, out := runCheck(t, r.Dir, "--base", base)
	require.Equal(t, cli.ExitConflict, code, out)
	require.Contains(t, out, `comment "from main" by "x" at "2026-01-01T00:00:00Z" on t-a: id 4 -> 5`)
	require.Contains(t, out, "1 comment(s) in "+base+"..HEAD changed id on the way")
}

func TestCheck_AcceptsANewCommentOnABranch(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	id := r.Create("work")
	writeIssues(t, r, id, commentRow("t-a", "7 first"))
	base := r.Commit("beads: the comment")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/c")

	writeIssues(t, r, id, commentRow("t-a", "7 first", "8 second"))
	r.Commit("beads: a note\n\nRefs: " + id)

	code, out := runCheck(t, r.Dir, "--base", base)
	require.Equal(t, cli.ExitOK, code, out)
}

// forPR points gh at one open pull request for its branch head.
func forPR(t *testing.T, number int, head, branch string) {
	t.Helper()
	fakeGH(t, `{"number":`+strconv.Itoa(number)+`,"state":"OPEN","baseRefName":"main","headRefName":"`+branch+`","headRefOid":"`+head+`"}`)
}

// land pr applies the rule to the whole PR, so a branch committed without the
// hook still cannot land a renumbering.
func TestLandPR_RefusesARenumberInTheRange(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	id := r.Create("work")
	writeIssues(t, r, id, commentRow("t-a", "4 from main"))
	r.Commit("beads: the comment")
	r.G("push", "-q", "origin", "main")

	r.G("checkout", "-q", "-b", "feat/g")
	writeIssues(t, r, id, commentRow("t-a", "5 from main"))
	bad := r.Commit("beads: import\n\nRefs: " + id)
	r.Write("a.go", "package a\n")
	head := r.Commit("feat: a\n\nCloses: " + id)
	r.G("push", "-q", "origin", "feat/g")

	for _, opts := range [][]string{{"--dry-run"}, {}} {
		forPR(t, 3, head, "feat/g")
		code, out := runLand(t, r.Dir, append([]string{"3"}, opts...)...)
		require.Equal(t, cli.ExitError, code, out)
		require.Contains(t, out, "changed id on the way")
		require.Contains(t, out, bad[:9]+" beads: import")
	}
	r.G("fetch", "-q", "origin")
	require.Equal(t, head, r.G("rev-parse", "origin/feat/g"), "a refused landing pushes nothing")
}

func TestLandPR_AcceptsANewCommentAndLands(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	id := r.Create("work")
	writeIssues(t, r, id, commentRow("t-a", "7 first"))
	r.Commit("beads: the comment")
	r.G("push", "-q", "origin", "main")

	r.G("checkout", "-q", "-b", "feat/g")
	writeIssues(t, r, id, commentRow("t-a", "7 first", "8 second"))
	r.Write("a.go", "package a\n")
	head := r.Commit("feat: a\n\nCloses: " + id)
	r.G("push", "-q", "origin", "feat/g")

	forPR(t, 3, head, "feat/g")
	code, out := runLand(t, r.Dir, "3", "--dry-run")
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "close "+id)
}

// The merge driver renumbers a comment when two branches added different
// comments under the same id, and the commit-msg hook runs on the merge commit
// that follows. That renumbering is the driver's, so the hook must let it
// through; the guard must not fight the driver.
func TestHook_AMergeTheDriverRenumberedPasses(t *testing.T) {
	testrepo.RequireBR(t)
	main := testrepo.New(t, "")
	id := main.Create("work")
	writeIssues(t, main, id, commentRow("t-a", "1 one"), commentRow("t-b", "5 two"))
	main.Commit("beads: two beads")
	install(t, main)
	main.G("branch", "side")

	// Ours adds comment 6 on t-a; theirs adds a different comment 6 on t-b.
	writeIssues(t, main, id, commentRow("t-a", "1 one", "6 ours"), commentRow("t-b", "5 two"))
	main.Commit("beads: our comment\n\nRefs: " + id)

	side := &testrepo.Repo{T: t, Dir: filepath.Join(t.TempDir(), "side")}
	main.G("worktree", "add", "-q", side.Dir, "side")
	writeIssues(t, side, id, commentRow("t-a", "1 one"), commentRow("t-b", "5 two", "6 theirs"))
	side.G("add", ".beads/issues.jsonl")
	side.G("-c", "core.hooksPath=/dev/null", "commit", "-qm", "beads: their comment\n\nRefs: "+id)

	// git runs the driver, then the hook on the merge commit.
	merge := side.G("merge", "--no-edit", "main")

	texts := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(side.Dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		if cs, ok := m["comments"].([]any); ok {
			for _, c := range cs {
				texts[c.(map[string]any)["text"].(string)] = true
			}
		}
	}
	require.True(t, texts["ours"] && texts["theirs"], "both comments survive the merge: %s", merge)
	require.Empty(t, side.G("status", "--porcelain"), "the merge is committed, so the hook passed")
}
