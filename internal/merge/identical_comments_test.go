package merge_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/merge"
)

// commentCopy returns the comment with another numeric id.
func commentCopy(id int64) string {
	return `{"id":` + strconv.FormatInt(id, 10) + `,"issue_id":"t-a","author":"x","text":"same","created_at":"2026-01-01T00:00:00Z"}`
}

// issueWithCopies returns one issue holding comments with the given ids.
func issueWithCopies(ids ...int64) string {
	cs := make([]string, len(ids))
	for i, id := range ids {
		cs[i] = commentCopy(id)
	}
	return `{"id":"t-a","title":"a","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","comments":[` + strings.Join(cs, ",") + `]}`
}

// issueWithCopiesAndPriority makes the row's non-comment fields differ too, so
// the set merge and ID assignment paths both exercise these repeated comments.
func issueWithCopiesAndPriority(ids ...int64) string {
	s := issueWithCopies(ids...)
	s = strings.Replace(s, `"title":"a"`, `"title":"a","priority":1`, 1)
	return strings.Replace(s, `"updated_at":"2026-01-01T00:00:00Z"`, `"updated_at":"2026-01-03T00:00:00Z"`, 1)
}

func issueWithCopiesAndAssignee(ids ...int64) string {
	s := issueWithCopies(ids...)
	s = strings.Replace(s, `"title":"a"`, `"title":"a","assignee":"z"`, 1)
	return strings.Replace(s, `"updated_at":"2026-01-01T00:00:00Z"`, `"updated_at":"2026-01-04T00:00:00Z"`, 1)
}

func TestMerge_RepeatedIdenticalCommentsAgreeWithTheGuard(t *testing.T) {
	tests := []struct {
		name   string
		base   []int64
		ours   []int64
		theirs []int64
		want   int
	}{
		{name: "insert in front", base: []int64{1}, ours: []int64{1}, theirs: []int64{3, 1}, want: 2},
		{name: "reorder", base: []int64{1, 2}, ours: []int64{1, 2}, theirs: []int64{2, 1}, want: 2},
		{name: "delete one", base: []int64{1, 2}, ours: []int64{1, 2}, theirs: []int64{1}, want: 1},
		{name: "both sides add", base: []int64{1}, ours: []int64{1, 2}, theirs: []int64{1, 3}, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := file(t, issueWithCopies(tt.base...))
			ours := file(t, issueWithCopiesAndPriority(tt.ours...))
			theirs := file(t, issueWithCopiesAndAssignee(tt.theirs...))
			merged, rep, err := merge.Merge(base, ours, theirs)
			require.NoError(t, err)
			require.True(t, rep.Clean(), rep.String())

			moved, err := beadsfile.NewRenumberedComments(merged, ours, theirs)
			require.NoError(t, err)
			require.Empty(t, moved, "the guard accepts every id assignment the driver writes")

			var cs []map[string]any
			require.NoError(t, json.Unmarshal(merged.Get("t-a").Obj.Raw("comments"), &cs))
			require.Len(t, cs, tt.want, "the driver neither drops nor duplicates a distinct comment")
		})
	}
}

func TestMerge_DeleteOneIdenticalCommentWhileTheirsAddsAnother(t *testing.T) {
	merged, rep, err := merge.Merge(
		file(t, issueWithCopies(1, 2)),
		file(t, issueWithCopiesAndPriority(1)),
		file(t, issueWithCopiesAndAssignee(1, 2, 3)),
	)
	require.NoError(t, err)
	require.True(t, rep.Clean(), rep.String())

	var cs []map[string]any
	require.NoError(t, json.Unmarshal(merged.Get("t-a").Obj.Raw("comments"), &cs))
	require.Len(t, cs, 3, "ours' deletion leaves theirs' surviving base copies and new addition")
}

func TestMerge_RepeatedIdenticalCommentsKeepTheirMultiplicity(t *testing.T) {
	for _, count := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			ids := make([]int64, count)
			for i := range ids {
				ids[i] = int64(i + 1)
			}
			merged, _, err := merge.Merge(file(t, issueWithCopies(1)), file(t, issueWithCopies(ids...)), file(t, issueWithCopies(ids...)))
			require.NoError(t, err)
			var cs []map[string]any
			require.NoError(t, json.Unmarshal(merged.Get("t-a").Obj.Raw("comments"), &cs))
			require.Len(t, cs, count)
		})
	}
}
