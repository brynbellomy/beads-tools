package land_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/land/mocks"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// duplicateComment gives two new issues a comment with id, the shape a text
// merge of the issues file leaves on main.
func duplicateComment(t *testing.T, r *testrepo.Repo, id int) {
	t.Helper()
	x, y := r.Create("x"), r.Create("y")
	addComment(t, r, x, id, "one")
	addComment(t, r, y, id, "two")
}

// apart creates issues and returns two whose lines in the issues file are
// not adjacent, so a text merge of edits to each is clean.
func apart(t *testing.T, r *testrepo.Repo) (string, string) {
	t.Helper()
	for range 4 {
		r.Create("filler")
	}
	f, err := beadsfile.ParseFile(filepath.Join(r.Dir, beadsfile.Path))
	require.NoError(t, err)
	ids := f.IDs()
	return ids[0], ids[len(ids)-1]
}

// textDriver merges the issues file as plain text, as GitHub's merge does.
const textDriver = `sh -c 'exec git merge-file "$2" "$1" "$3"' sh`

// viewAndMerge answers View with head and expects one merge.
func viewAndMerge(t *testing.T, head string) (*mocks.MockGitHub, *string) {
	gh := viewOnly(t, head)
	var merged string
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) { merged = sha }).Return(nil).Once()
	return gh, &merged
}

func TestPR_LandsWithDuplicatesInheritedFromTheBase(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	work := r.Create("work")
	r.Commit("add issue")
	duplicateComment(t, r, 7)
	r.Commit("beads: main reached duplicate comment ids")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/g")
	r.Write("a.go", "package a\n")
	head := r.Commit("feat: a\n\nCloses: " + work)
	r.G("push", "-q", "origin", "feat/g")
	pushToMain(t, r, "other.go", "package other\n")

	gh, merged := viewAndMerge(t, head)
	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 3, land.Options{}), out.String())

	require.Contains(t, out.String(), "warning: feat/g inherits comment id 7 is used 2 times (")
	require.Equal(t, "closed", r.Status(*merged, work))
}

func TestPR_WarnsOfDuplicatesTheBranchAlreadyHolds(t *testing.T) {
	r, _, head := prRepo(t, func(r *testrepo.Repo, id string) {
		duplicateComment(t, r, 7)
		r.Commit("beads: import")
		r.Write("a.go", "package a\n")
		r.Commit("feat: a\n\nCloses: " + id)
	})

	gh, _ := viewAndMerge(t, head)
	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 3, land.Options{}), out.String())
	require.Contains(t, out.String(), "warning: feat/g inherits comment id 7 is used 2 times (")
}

// Two PRs each add comment 50 on a different issue. A text merge of the
// issues file is clean and leaves comment 50 on both, which neither side
// holds; land refuses to publish it.
func TestPR_RefusesDuplicatesTheMergeAdds(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	work := r.Create("work")
	a, b := apart(t, r)
	r.Commit("add issues")
	r.G("push", "-q", "origin", "main")

	r.G("checkout", "-q", "-b", "feat/g")
	addComment(t, r, a, 50, "from the branch")
	r.Write("g.go", "package g\n")
	head := r.Commit("feat: g\n\nCloses: " + work)
	r.G("push", "-q", "origin", "feat/g")

	r.G("checkout", "-q", "main")
	addComment(t, r, b, 50, "from main")
	r.Commit("beads: note on b")
	r.G("push", "-q", "origin", "main")

	var out bytes.Buffer
	l := lander(r, viewOnly(t, head), &out)
	l.MergeDriver = textDriver
	err := l.PR(context.Background(), 3, land.Options{})

	require.ErrorContains(t, err, "holds 1 duplicate comment id(s) that neither origin/main nor "+head+" holds")
	require.ErrorContains(t, err, "Merge the base branch into the branch locally")
	require.Contains(t, out.String(), "refuse feat/g: comment id 50 is used 2 times (")
	r.G("fetch", "-q", "origin")
	require.Equal(t, head, r.G("rev-parse", "origin/feat/g"), "a refused landing pushes nothing")
}

func TestCloseInTree_RefusesToWriteADuplicateCommentID(t *testing.T) {
	r := testrepo.New(t, "")
	other := `{"id":"t-other","title":"o","status":"open","comments":[{"id":7,"issue_id":"t-other","author":"x","text":"n","created_at":"2026-01-01T00:00:00Z"}]}`
	target := `{"id":"t-c","title":"c","status":"open"}`
	r.Write(".beads/issues.jsonl", other+"\n"+target+"\n")
	path := filepath.Join(r.Dir, ".beads", "issues.jsonl")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	beads := mocks.NewMockBeads(t)
	beads.EXPECT().Close(mock.Anything, mock.Anything, "t-c", "done").
		RunAndReturn(func(_ context.Context, dir, _, _ string) error {
			closed := `{"id":"t-c","title":"c","status":"closed","comments":[{"id":7,"issue_id":"t-c","author":"y","text":"m","created_at":"2026-01-02T00:00:00Z"}]}`
			return os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(other+"\n"+closed+"\n"), 0o644)
		})

	_, err = land.CloseInTree(context.Background(), r.Dir, []land.Close{{ID: "t-c", Reason: "done"}}, beads)
	require.ErrorContains(t, err, "duplicate comment id")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}
