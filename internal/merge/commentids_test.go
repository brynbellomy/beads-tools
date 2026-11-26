package merge_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/merge"
)

// Found on real data: one br command posted the same comment on several issues
// in the same second. Comments sharing author, time and text on different
// issues are still different comments with different ids.
func TestMerge_IdenticalCommentsOnDifferentIssuesKeepTheirIDs(t *testing.T) {
	a := `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"subsumed","created_at":"2026-01-01T00:00:00Z"}]}`
	b := `{"id":"t-b","title":"b","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":3,"issue_id":"t-b","author":"x","text":"subsumed","created_at":"2026-01-01T00:00:00Z"}]}`
	f := file(t, a, b)

	out, rep := run(t, f, f, f)

	require.Equal(t, a+"\n"+b+"\n", out, "a merge nobody changed anything in is the identity")
	require.Empty(t, rep.Renumbers)
}

// Found on real data: br on main renumbered comments on issues the branch never
// touched. Merging main into the branch must take main's ids, not revert them.
// The row moved, so it also carries a newer updated_at: br's import prefers its
// database's row on a tie, and the database holds the old id.
func TestMerge_OneSidedCommentRenumberIsKept(t *testing.T) {
	base := `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":1,"issue_id":"t-a","author":"x","text":"old","created_at":"2026-01-01T00:00:00Z"}]}`
	theirs := `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":61,"issue_id":"t-a","author":"x","text":"old","created_at":"2026-01-01T00:00:00Z"}]}`
	ours2 := `{"id":"t-b","title":"b","updated_at":"2026-01-02T00:00:00Z"}`

	out, rep := run(t, file(t, base), file(t, base, ours2), file(t, theirs))

	stamped := strings.Replace(theirs, `"updated_at":"2026-01-01T00:00:00Z"`, `"updated_at":"2026-01-01T00:00:01Z"`, 1)
	require.Equal(t, stamped+"\n"+ours2+"\n", out)
	require.Empty(t, rep.Renumbers)
	require.Equal(t, []merge.Stamp{{ID: "t-a", Was: "2026-01-01T00:00:00Z", Now: "2026-01-01T00:00:01Z"}}, rep.Stamps)
}
