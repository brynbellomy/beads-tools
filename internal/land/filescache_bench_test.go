package land_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/gitx"
	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// TestCheck_LargeRangeIsLinear reports the cost of checking a long branch over a
// large issues file. Every commit used to read and parse the whole file again, and
// again for each parent, so the work grew with commits times file size. It now
// grows with the number of distinct blobs. Run with -v to see the numbers:
//
//	100 commits, ~0.8 MB file: ~200 parses and several seconds before, a handful
//
// of parses and well under a second after.
func TestCheck_LargeRangeIsLinear(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))

	const beads, commits = 20000, 100
	var issues strings.Builder
	for i := range beads {
		fmt.Fprintf(&issues, `{"id":"t-%04d","title":"bead %d","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},
`, i, i)
	}
	// Keep it valid JSONL: one line per bead, with a comment each.
	issues.Reset()
	for i := range beads {
		fmt.Fprintf(&issues, `{"id":"t-%04d","title":"bead %d at some length to make the file weigh something","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":%d,"issue_id":"t-%04d","author":"x","text":"a note","created_at":"2026-01-01T00:00:00Z"}]}`+"\n", i, i, i+1, i)
	}
	r.Write(".beads/issues.jsonl", issues.String())
	first := r.Commit("beads: the issues file")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/c")
	for i := range commits {
		r.Write("f.go", fmt.Sprintf("package f // %d\n", i))
		r.Commit(fmt.Sprintf("feat: f%d", i))
	}

	land.ResetParseCount()
	started := time.Now()
	_, err := land.Check(context.Background(), gitx.Repo{Dir: r.Dir}, first, "HEAD", false, io.Discard)
	elapsed := time.Since(started)
	require.NoError(t, err)
	t.Logf("%d bytes, %d commits: %d parses in %s", len(issues.String()), commits, land.ParseCount(), elapsed)
	require.LessOrEqual(t, land.ParseCount(), int64(3), "one blob behind the range, and nothing else")
}
