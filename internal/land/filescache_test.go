package land_test

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/gitx"
	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// check walks every commit in a range against the issues file at that commit and
// at each of its parents, so neighbouring commits share most of those blobs. A
// 100-commit branch over a 7 MB file read and parsed the same file about 200
// times. Each distinct blob must be parsed once.
func TestCheck_ParsesEachBlobOnce(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	id := r.Create("work")
	r.Write(".beads/issues.jsonl", `{"id":"`+id+`","title":"work","created_at":"2026-01-01T00:00:00Z"}`+"\n"+
		`{"id":"t-a","title":"a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"first","created_at":"2026-01-01T00:00:00Z"}]}`+"\n")
	first := r.Commit("beads: the comment")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/c")

	// Only the first commit touches the issues file; the rest share its blob
	// with their parent.
	r.Write(".beads/issues.jsonl", `{"id":"`+id+`","title":"work","created_at":"2026-01-01T00:00:00Z"}`+"\n"+
		`{"id":"t-a","title":"a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"first","created_at":"2026-01-01T00:00:00Z"},{"id":8,"issue_id":"t-a","author":"x","text":"second","created_at":"2026-01-02T00:00:00Z"}]}`+"\n")
	r.Commit("beads: a second comment\n\nRefs: " + id)
	for i := range 12 {
		r.Write(fmt.Sprintf("f%d.go", i), fmt.Sprintf("package f%d\n", i))
		r.Commit(fmt.Sprintf("feat: f%d\n\nRefs: %s", i, id))
	}

	land.ResetParseCount()
	_, err := land.Check(context.Background(), gitx.Repo{Dir: r.Dir}, first, "HEAD", true, io.Discard)
	require.NoError(t, err)
	// The blob at first, the blob the branch wrote, and nothing else, however
	// many commits and parents point at them.
	require.LessOrEqual(t, land.ParseCount(), int64(4), "each distinct issues blob is parsed once")
}
