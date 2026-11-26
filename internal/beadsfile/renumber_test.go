package beadsfile_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
)

// issueWith builds one issue line whose comments are "id text" pairs.
func issueWith(id string, pairs ...string) string {
	var cs []string
	for _, p := range pairs {
		fields := strings.SplitN(p, " ", 2)
		text := ""
		if len(fields) == 2 {
			text = fields[1]
		}
		cs = append(cs, `{"id":`+fields[0]+`,"issue_id":`+strconv.Quote(id)+`,"author":"x","text":`+strconv.Quote(text)+`,"created_at":"2026-01-01T00:00:00Z"}`)
	}
	return `{"id":` + strconv.Quote(id) + `,"title":"t","comments":[` + strings.Join(cs, ",") + `]}`
}

func parseRows(t *testing.T, rows ...string) *beadsfile.File {
	t.Helper()
	f, err := beadsfile.Parse(strings.NewReader(strings.Join(rows, "\n") + "\n"))
	require.NoError(t, err)
	return f
}

func movedStrings(ms []beadsfile.RenumberedComment) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.String()
	}
	return out
}

func TestNewRenumberedComments_AnOrdinaryEditRenumbersNothing(t *testing.T) {
	before := parseRows(t, issueWith("t-a", "7 first"))
	after := parseRows(t, `{"id":"t-a","title":"retitled","status":"closed","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"first","created_at":"2026-01-01T00:00:00Z"}]}`)

	moved, err := beadsfile.NewRenumberedComments(after, before)
	require.NoError(t, err)
	require.Empty(t, moved)
}

func TestNewRenumberedComments_ANewCommentIsFine(t *testing.T) {
	before := parseRows(t, issueWith("t-a", "7 first"))
	after := parseRows(t, issueWith("t-a", "7 first", "8 second"))

	moved, err := beadsfile.NewRenumberedComments(after, before)
	require.NoError(t, err)
	require.Empty(t, moved)
}

// The reproduction this guard exists for: a stale database folded main's copies
// of two comments into its own counter, so the flush gave them new ids.
func TestNewRenumberedComments_AStaleStoreRenumberIsCaught(t *testing.T) {
	before := parseRows(t, issueWith("t-a", "4 from main", "5 also from main"))
	after := parseRows(t, issueWith("t-a", "5 from main", "6 also from main", "7 ours"))

	moved, err := beadsfile.NewRenumberedComments(after, before)
	require.NoError(t, err)
	require.Equal(t, []string{
		`comment "from main" by "x" at "2026-01-01T00:00:00Z" on t-a: id 4 -> 5`,
		`comment "also from main" by "x" at "2026-01-01T00:00:00Z" on t-a: id 5 -> 6`,
	}, movedStrings(moved), "both comments main held at their own ids were given new ones")
}

// The same text at the same instant on two beads is two comments, so moving one
// is judged on its own.
func TestNewRenumberedComments_CommentsOnDifferentIssuesAreIndependent(t *testing.T) {
	before := parseRows(t, issueWith("t-a", "7 note"), issueWith("t-b", "8 note"))
	after := parseRows(t, issueWith("t-a", "7 note"), issueWith("t-b", "9 note"))

	moved, err := beadsfile.NewRenumberedComments(after, before)
	require.NoError(t, err)
	require.Equal(t, []string{`comment "note" by "x" at "2026-01-01T00:00:00Z" on t-b: id 8 -> 9`}, movedStrings(moved),
		"the same text on another issue is another comment")
}

// Two byte-identical comments on one bead: the first stays put, the second moved.
// Identity alone cannot tell them apart, so the occurrence does.
func TestNewRenumberedComments_IdenticalCommentsWithinOneBead(t *testing.T) {
	before := parseRows(t, issueWith("t-a", "7 same", "8 same"))

	kept := parseRows(t, issueWith("t-a", "7 same", "8 same"))
	moved, err := beadsfile.NewRenumberedComments(kept, before)
	require.NoError(t, err)
	require.Empty(t, moved)

	swapped := parseRows(t, issueWith("t-a", "8 same", "7 same"))
	moved, err = beadsfile.NewRenumberedComments(swapped, before)
	require.NoError(t, err)
	require.Equal(t, []string{
		`comment "same" by "x" at "2026-01-01T00:00:00Z" on t-a: id 7 -> 8`,
		`comment "same" by "x" at "2026-01-01T00:00:00Z" on t-a (occurrence 2): id 8 -> 7`,
	}, movedStrings(moved), "the two are told apart by their order, and each is named")
}

// A merge may take an id either parent holds: the comment is inherited, not
// renumbered.
func TestNewRenumberedComments_AnIDEitherParentHoldsPasses(t *testing.T) {
	ours := parseRows(t, issueWith("t-a", "7 note"))
	theirs := parseRows(t, issueWith("t-a", "61 note"))

	merged := parseRows(t, issueWith("t-a", "61 note"))
	moved, err := beadsfile.NewRenumberedComments(merged, ours, theirs)
	require.NoError(t, err)
	require.Empty(t, moved, "61 is theirs' id for the comment")

	merged = parseRows(t, issueWith("t-a", "7 note"))
	moved, err = beadsfile.NewRenumberedComments(merged, ours, theirs)
	require.NoError(t, err)
	require.Empty(t, moved, "7 is ours' id for the comment")

	merged = parseRows(t, issueWith("t-a", "99 note"))
	moved, err = beadsfile.NewRenumberedComments(merged, ours, theirs)
	require.NoError(t, err)
	require.Len(t, moved, 1, "neither parent holds 99 for this comment")
	require.Equal(t, []int64{7, 61}, moved[0].Was)
}

// An id two comments already shared never named one comment, so a commit that
// leaves it shared must not be refused on its account: the duplicate check
// speaks for that. A comment stepping aside from the shared id passes for the
// same reason.
func TestNewRenumberedComments_ARepairOfASharedIDIsNotARenumber(t *testing.T) {
	shared := parseRows(t,
		`{"id":"t-a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":7,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)
	repaired := parseRows(t,
		`{"id":"t-a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":8,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)

	moved, err := beadsfile.NewRenumberedComments(repaired, shared)
	require.NoError(t, err)
	require.Empty(t, moved, "the comment keeps the text it had; only the id two comments shared moved")
}

func TestCommentKeys_NameEachCommentWithinItsIssue(t *testing.T) {
	same := `{"author":"x","text":"n","created_at":"2026-01-01T00:00:00Z"}`
	cs, err := beadsfile.ParseObjectArray([]byte("[" + same + "," + same + "]"))
	require.NoError(t, err)

	keys := beadsfile.CommentKeys("t-a", cs)
	require.Equal(t, []int{0, 1}, []int{keys[0].Occurrence, keys[1].Occurrence})
	require.Equal(t, "t-a", keys[0].IssueID)
	require.NotEqual(t, keys[0], keys[1])
}
