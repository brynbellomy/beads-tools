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

// Two byte-identical comments on one bead are interchangeable: which of them
// carries which id cannot be observed, so reordering them renumbers nothing.
func TestNewRenumberedComments_IdenticalCommentsWithinOneBead(t *testing.T) {
	before := parseRows(t, issueWith("t-a", "7 same", "8 same"))

	kept := parseRows(t, issueWith("t-a", "7 same", "8 same"))
	moved, err := beadsfile.NewRenumberedComments(kept, before)
	require.NoError(t, err)
	require.Empty(t, moved)

	swapped := parseRows(t, issueWith("t-a", "8 same", "7 same"))
	moved, err = beadsfile.NewRenumberedComments(swapped, before)
	require.NoError(t, err)
	require.Empty(t, moved, "the same two ids stay on the same two identical comments")

	added := parseRows(t, issueWith("t-a", "7 same", "8 same", "9 same"))
	moved, err = beadsfile.NewRenumberedComments(added, before)
	require.NoError(t, err)
	require.Empty(t, moved, "a third identical comment is new, not a move")
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

// An id two beads share is not a repair: it is the duplicate br refuses to
// import. A comment moving off such an id is renumbered like any other, so the
// stale-store case cannot hide behind another bead's duplicate.
func TestNewRenumberedComments_ASharedIDAcrossBeadsIsNotARepair(t *testing.T) {
	before := parseRows(t,
		`{"id":"t-a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":7,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)
	repaired := parseRows(t,
		`{"id":"t-a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":8,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)

	moved, err := beadsfile.NewRenumberedComments(repaired, before)
	require.NoError(t, err)
	require.Equal(t, []string{`comment "b" by "x" at "2026-01-01T00:00:00Z" on t-b: id 7 -> 8`}, movedStrings(moved),
		"t-b's comment moved off id 7")
}

// Two comments of the SAME bead that shared an id are the repair case. The
// duplicate-id check speaks for the file; the renumber check must not refuse the
// comment that stepped aside, which is what the merge driver produces.
func TestNewRenumberedComments_ASharedIDWithinOneBeadIsARepair(t *testing.T) {
	before := parseRows(t,
		`{"id":"t-a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"one","created_at":"2026-01-01T00:00:00Z"},{"id":7,"issue_id":"t-a","author":"x","text":"two","created_at":"2026-01-01T00:00:01Z"}]}`)
	repaired := parseRows(t,
		`{"id":"t-a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"one","created_at":"2026-01-01T00:00:00Z"},{"id":9,"issue_id":"t-a","author":"x","text":"two","created_at":"2026-01-01T00:00:01Z"}]}`)

	moved, err := beadsfile.NewRenumberedComments(repaired, before)
	require.NoError(t, err)
	require.Empty(t, moved, "the second comment steps off a shared id; that is the repair")
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

// The exemption for an id a parent carried twice must key on the comment doing
// the moving, not on the id. Here a parent holds id 9 on two different beads, so
// id 9 never named one comment; but t-a's comment is not leaving an ambiguous
// id for a repair, it is being renumbered by a stale store. Keying the exemption
// on the id alone lets that slip through.
func TestNewRenumberedComments_ASharedIDDoesNotExcuseAnotherBeadsRenumber(t *testing.T) {
	before := parseRows(t,
		`{"id":"t-a","comments":[{"id":9,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":9,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)
	after := parseRows(t,
		`{"id":"t-a","comments":[{"id":19,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":9,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)

	moved, err := beadsfile.NewRenumberedComments(after, before)
	require.NoError(t, err)
	require.Equal(t, []string{`comment "a" by "x" at "2026-01-01T00:00:00Z" on t-a: id 9 -> 19`}, movedStrings(moved),
		"t-a's comment moved to an id no parent holds for it, and a shared id elsewhere is no excuse")
}

// The same probe as a merge commit: the shared id lives on a parent, and the
// merge carries t-a's renumbering.
func TestNewRenumberedComments_ASharedIDDoesNotExcuseARenumberInAMerge(t *testing.T) {
	ours := parseRows(t,
		`{"id":"t-a","comments":[{"id":9,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":9,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)
	theirs := parseRows(t,
		`{"id":"t-a","comments":[{"id":19,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":9,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)

	moved, err := beadsfile.NewRenumberedComments(theirs, ours, ours)
	require.NoError(t, err)
	require.Len(t, moved, 1)
	require.Equal(t, int64(19), moved[0].Now)
}

// Occurrence is a position among identical comments, so it must not decide the
// answer: inserting or deleting one identical comment earlier in the file shifts
// every later occurrence without changing which comment is which. What matters
// is the multiset of ids the parent held for an identity.
func TestNewRenumberedComments_PositionAmongIdenticalCommentsIsNotARenumber(t *testing.T) {
	const same = "7 same"
	three := parseRows(t, issueWith("t-a", same, same, "8 third"))

	t.Run("a comment inserted in front", func(t *testing.T) {
		// A new identical comment is added before the existing ones; the ones
		// that were there keep their ids, just at a different position.
		inserted := parseRows(t, issueWith("t-a", "5 new", same, same, "8 third"))
		moved, err := beadsfile.NewRenumberedComments(inserted, three)
		require.NoError(t, err)
		require.Empty(t, moved)
	})

	t.Run("the first of the identical comments deleted", func(t *testing.T) {
		deleted := parseRows(t, issueWith("t-a", same, "8 third"))
		moved, err := beadsfile.NewRenumberedComments(deleted, three)
		require.NoError(t, err)
		require.Empty(t, moved, "one of the two identical comments went; the other kept its id")
	})

	t.Run("the second of the identical comments deleted", func(t *testing.T) {
		deleted := parseRows(t, issueWith("t-a", same, "8 third"))
		moved, err := beadsfile.NewRenumberedComments(deleted, three)
		require.NoError(t, err)
		require.Empty(t, moved)
	})

	t.Run("an id the parent held is still given up", func(t *testing.T) {
		// Both identical comments keep both ids, but the third comment moved.
		movedThird := parseRows(t, issueWith("t-a", same, same, "9 third"))
		moved, err := beadsfile.NewRenumberedComments(movedThird, three)
		require.NoError(t, err)
		require.Equal(t, []string{`comment "third" by "x" at "2026-01-01T00:00:00Z" on t-a: id 8 -> 9`}, movedStrings(moved))
	})
}

// Two byte-identical comments on one bead are interchangeable, so which of them
// carries which id is not observable: a reordering keeps the same multiset of
// ids for that identity and must not be refused. Occurrence is a position in
// the file, so keying on it here refuses a change that renumbered nothing.
func TestNewRenumberedComments_AReorderOfIdenticalCommentsIsNotARenumber(t *testing.T) {
	before := parseRows(t, issueWith("t-a", "7 same", "8 same"))
	swapped := parseRows(t, issueWith("t-a", "8 same", "7 same"))

	moved, err := beadsfile.NewRenumberedComments(swapped, before)
	require.NoError(t, err)
	require.Empty(t, moved, "the same two ids stay on the same two identical comments")
}

// A respelled timestamp is the same instant, so it must not hide a renumber.
// br writes the same moment as Z, with an offset, or at another precision.
func TestNewRenumberedComments_ARespelledTimestampDoesNotHideARenumber(t *testing.T) {
	before := parseRows(t, `{"id":"t-a","comments":[{"id":4,"issue_id":"t-a","author":"x","text":"note","created_at":"2026-01-01T00:00:00Z"}]}`)
	after := parseRows(t, `{"id":"t-a","comments":[{"id":5,"issue_id":"t-a","author":"x","text":"note","created_at":"2026-01-01T01:00:00+01:00"}]}`)

	moved, err := beadsfile.NewRenumberedComments(after, before)
	require.NoError(t, err)
	require.Len(t, moved, 1, "the shifted id is a renumber even though the timestamp was respelled")
	require.Equal(t, int64(4), moved[0].Was[0])
	require.Equal(t, int64(5), moved[0].Now)
}

// The same respelling with no renumber is not a renumber either.
func TestNewRenumberedComments_ARespelledTimestampAloneIsNotARenumber(t *testing.T) {
	before := parseRows(t, `{"id":"t-a","comments":[{"id":4,"issue_id":"t-a","author":"x","text":"note","created_at":"2026-01-01T00:00:00Z"}]}`)
	after := parseRows(t, `{"id":"t-a","comments":[{"id":4,"issue_id":"t-a","author":"x","text":"note","created_at":"2026-01-01T00:00:00.000000000+00:00"}]}`)

	moved, err := beadsfile.NewRenumberedComments(after, before)
	require.NoError(t, err)
	require.Empty(t, moved)
}

// The cross-parent exemption must apply only to the (comment, id) pair the merge
// driver actually repaired. Two parents each using an id once, on different beads,
// make br's file-wide id namespace collide once the two are combined, and the
// driver resolves that by keeping OUR id and moving theirs' comment to a fresh
// one. That must not become a licence for any comment that held the id: a stale
// store walking a comment off it is still a renumbering.
func TestNewRenumberedComments_TwoParentsSharingAnIDDoNotExcuseAStaleStoreRenumber(t *testing.T) {
	// ours = parents[0], theirs = parents[1].
	ours := parseRows(t,
		`{"id":"t-a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`)
	theirs := parseRows(t,
		`{"id":"t-b","comments":[{"id":7,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)

	// What the driver writes: ours keeps 7, theirs' comment moves to a fresh id.
	driverRepair := parseRows(t,
		`{"id":"t-a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":8,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)
	moved, err := beadsfile.NewRenumberedComments(driverRepair, ours, theirs)
	require.NoError(t, err)
	require.Empty(t, moved, "the driver moved theirs' comment off the id the two sides share")

	// A comment of a third bead the parents never touched, walked off an id by a
	// stale store, is a renumbering. Keying the exemption on the id alone let such
	// a comment through whenever two parents shared that id.
	withThird := parseRows(t,
		`{"id":"t-a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":7,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-c","comments":[{"id":12,"issue_id":"t-c","author":"x","text":"c","created_at":"2026-01-01T00:00:00Z"}]}`)
	stale := parseRows(t,
		`{"id":"t-a","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":7,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-c","comments":[{"id":19,"issue_id":"t-c","author":"x","text":"c","created_at":"2026-01-01T00:00:00Z"}]}`)
	moved, err = beadsfile.NewRenumberedComments(stale, withThird, withThird)
	require.NoError(t, err)
	require.Equal(t, []string{`comment "c" by "x" at "2026-01-01T00:00:00Z" on t-c: id 12 -> 19`}, movedStrings(moved),
		"t-c never shared id 7, so the shared id has nothing to do with it")

	// A claimant moving while neither claimant keeps the id is not the driver's
	// repair either: the driver always leaves the id on one side.
	walked := parseRows(t,
		`{"id":"t-a","comments":[{"id":9,"issue_id":"t-a","author":"x","text":"a","created_at":"2026-01-01T00:00:00Z"}]}`,
		`{"id":"t-b","comments":[{"id":10,"issue_id":"t-b","author":"x","text":"b","created_at":"2026-01-01T00:00:00Z"}]}`)
	moved, err = beadsfile.NewRenumberedComments(walked, ours, theirs)
	require.NoError(t, err)
	require.Len(t, moved, 2, "a stale store walked both comments off the shared id")
}
