package land

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/gitx"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// checkParses runs Check over a range and returns how many issues files it parsed,
// so a test can prove a range reads each blob once instead of once per commit and
// again per parent.
func checkParses(t *testing.T, repo gitx.Repo, base, head string) int64 {
	t.Helper()
	parseCount.Store(0)
	_, err := Check(context.Background(), repo, base, head, true, io.Discard)
	require.NoError(t, err)
	return parseCount.Load()
}

// TestCheck_ParsesEachBlobOnce walks a range whose commits mostly do not touch the
// issues file, so neighbouring commits share one blob. Each distinct blob must be
// parsed once, not once per commit and again per parent: a 100-commit branch over
// a 7 MB file used to parse it about 100 times.
func TestCheck_ParsesEachBlobOnce(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	id := r.Create("work")
	r.Write(".beads/issues.jsonl",
		`{"id":"`+id+`","title":"work","created_at":"2026-01-01T00:00:00Z"}`+"\n"+
			`{"id":"t-a","title":"a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"first","created_at":"2026-01-01T00:00:00Z"}]}`+"\n")
	first := r.Commit("beads: the comment")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/c")

	r.Write(".beads/issues.jsonl",
		`{"id":"`+id+`","title":"work","created_at":"2026-01-01T00:00:00Z"}`+"\n"+
			`{"id":"t-a","title":"a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"first","created_at":"2026-01-01T00:00:00Z"},{"id":8,"issue_id":"t-a","author":"x","text":"second","created_at":"2026-01-02T00:00:00Z"}]}`+"\n")
	r.Commit("beads: a second comment\n\nRefs: " + id)
	for i := range 12 {
		r.Write(fmt.Sprintf("f%d.go", i), fmt.Sprintf("package f%d\n", i))
		r.Commit(fmt.Sprintf("feat: f%d\n\nRefs: %s", i, id))
	}

	require.LessOrEqual(t, checkParses(t, gitx.Repo{Dir: r.Dir}, first, "HEAD"), int64(4),
		"each distinct issues blob is parsed once, however many commits and parents point at it")
}

func TestFilesAt_ReusesTheDecodedCommentIndexForASharedBlob(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	r.Write(".beads/issues.jsonl", `{"id":"t-a","comments":[{"id":1,"issue_id":"t-a","author":"x","text":"same","created_at":"2026-01-01T00:00:00Z"}]}`+"\n")
	r.Commit("beads: one comment")
	r.Write("work.txt", "one")
	first := r.Commit("work: one")
	r.Write("work.txt", "two")
	second := r.Commit("work: two")

	cache := newFilesAt(gitx.Repo{Dir: r.Dir})
	a, err := cache.commentIndex(context.Background(), first)
	require.NoError(t, err)
	b, err := cache.commentIndex(context.Background(), second)
	require.NoError(t, err)
	require.Same(t, a, b, "revisions sharing a blob also share its decoded comments")
}

// oversized writes an issues file of beads comments into dir and returns its size.
func oversized(t *testing.T, dir string, beads int) int {
	t.Helper()
	var b []byte
	for i := range beads {
		b = append(b, []byte(fmt.Sprintf(
			`{"id":"t-%04d","title":"bead %d at some length to make the file weigh something","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":%d,"issue_id":"t-%04d","author":"x","text":"a note","created_at":"2026-01-01T00:00:00Z"}]}`+"\n",
			i, i, i+1, i))...)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// TestCheck_LargeRangeIsLinear reports the cost of checking a long branch over a
// large issues file, and asserts the work scales with distinct blobs rather than
// with commits times file size. Run with -v to see the numbers.
//
// The fixture is deliberately small: parse work scales with the file, and a
// 5.7 MB file took long enough under the race detector to time the package out.
func TestCheck_LargeRangeIsLinear(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	const beads, commits = 2000, 40
	size := oversized(t, r.Dir, beads)
	first := r.Commit("beads: the issues file")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/c")
	for i := range commits {
		r.Write("f.go", fmt.Sprintf("package f // %d\n", i))
		r.Commit(fmt.Sprintf("feat: f%d", i))
	}

	parseCount.Store(0)
	started := time.Now()
	_, err := Check(context.Background(), gitx.Repo{Dir: r.Dir}, first, "HEAD", true, io.Discard)
	elapsed := time.Since(started)
	require.NoError(t, err)
	t.Logf("%d bytes, %d commits: %d parses in %s (%s per commit)",
		size, commits, parseCount.Load(), elapsed, elapsed/time.Duration(commits))
	require.LessOrEqual(t, parseCount.Load(), int64(3), "one blob behind the range, and nothing else")
}
