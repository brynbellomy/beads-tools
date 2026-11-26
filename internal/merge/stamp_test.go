package merge_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/merge"
)

// The case br gets wrong on its own: main renames a bead, the branch comments on
// it, and both sides end at the same updated_at. The merge keeps both changes
// and stamps the row one second past that tie, which is the newest value either
// parent gives it. Without the stamp br's next import keeps the row in its
// database and silently puts the old title back.
func TestMerge_TitleChangePlusCommentIsStampedPastBothParents(t *testing.T) {
	const (
		tied    = "2026-01-02T00:00:00Z"
		comment = `{"id":4,"issue_id":"t-a","author":"b","text":"on the branch","created_at":"2026-01-02T00:00:00Z"}`
	)
	base := file(t, `{"id":"t-a","title":"old","status":"open","updated_at":"2026-01-01T00:00:00Z"}`)
	ours := file(t, `{"id":"t-a","title":"old","status":"open","updated_at":"`+tied+`","comments":[`+comment+`]}`)
	theirs := file(t, `{"id":"t-a","title":"new","status":"open","updated_at":"`+tied+`"}`)

	out, rep := run(t, base, ours, theirs)

	m := issue(t, out, "t-a")
	require.Equal(t, "new", m["title"], "main's rename survives")
	require.Equal(t, []string{"on the branch"}, comments(t, m), "the branch's comment survives")
	require.Equal(t, "2026-01-02T00:00:01Z", m["updated_at"], "one unit past the newest parent")
	require.Equal(t, []merge.Stamp{{ID: "t-a", Was: tied, Now: "2026-01-02T00:00:01Z"}}, rep.Stamps)
	require.Contains(t, rep.String(), "stamped 1 merged row(s)")
}

// The other half of the hazard: our own row is the newer one, so the merge keeps
// our updated_at while taking theirs' field. That is a tie with the database, so
// the row is stamped too.
func TestMerge_NewerOursWithAFieldFromTheirsIsStamped(t *testing.T) {
	base := file(t, `{"id":"t-a","title":"old","status":"open","updated_at":"2026-01-01T00:00:00Z"}`)
	ours := file(t, `{"id":"t-a","title":"old","status":"open","updated_at":"2026-01-03T00:00:00Z","comments":[{"id":1,"issue_id":"t-a","author":"b","text":"x","created_at":"2026-01-03T00:00:00Z"}]}`)
	theirs := file(t, `{"id":"t-a","title":"new","status":"open","updated_at":"2026-01-02T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)

	m := issue(t, out, "t-a")
	require.Equal(t, "new", m["title"])
	require.Equal(t, "2026-01-03T00:00:01Z", m["updated_at"], "past ours, which is the newer parent")
	require.Equal(t, []merge.Stamp{{ID: "t-a", Was: "2026-01-03T00:00:00Z", Now: "2026-01-03T00:00:01Z"}}, rep.Stamps)
}

func TestMerge_StampIsDeterministic(t *testing.T) {
	base := file(t, `{"id":"t-a","title":"old","status":"open","updated_at":"2026-01-01T00:00:00Z"}`)
	ours := file(t, `{"id":"t-a","title":"old","status":"open","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":1,"issue_id":"t-a","author":"b","text":"x","created_at":"2026-01-02T00:00:00Z"}]}`)
	theirs := file(t, `{"id":"t-a","title":"new","status":"open","updated_at":"2026-01-02T00:00:00Z"}`)

	first, _ := run(t, base, ours, theirs)
	require.Equal(t, "2026-01-02T00:00:01Z", issue(t, first, "t-a")["updated_at"])
	for range 3 {
		same, _ := run(t, base, ours, theirs)
		require.Equal(t, first, same, "the same three inputs always merge to the same bytes")
	}
}

// A revert on one side removes a field the base had. The removal comes through
// and the row is stamped, because ours is the newer parent and the merge kept
// our updated_at.
func TestMerge_OneSidedRevertIsStamped(t *testing.T) {
	base := file(t, `{"id":"t-a","title":"a","status":"open","assignee":"z","updated_at":"2026-01-01T00:00:00Z"}`)
	ours := file(t, `{"id":"t-a","title":"a","status":"open","assignee":"z","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":1,"issue_id":"t-a","author":"b","text":"x","created_at":"2026-01-02T00:00:00Z"}]}`)
	theirs := file(t, `{"id":"t-a","title":"a","status":"open","updated_at":"2026-01-01T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)

	m := issue(t, out, "t-a")
	require.NotContains(t, m, "assignee", "the field theirs removed stays removed")
	require.Equal(t, []string{"x"}, comments(t, m))
	require.Equal(t, []merge.Stamp{{ID: "t-a", Was: "2026-01-02T00:00:00Z", Now: "2026-01-02T00:00:01Z"}}, rep.Stamps)
}

// Status and its close fields move together, and the merged row is stamped.
func TestMerge_CloseGroupChangeOnTheOtherSideIsStamped(t *testing.T) {
	base := file(t, `{"id":"t-a","title":"a","status":"open","updated_at":"2026-01-01T00:00:00Z"}`)
	ours := file(t, `{"id":"t-a","title":"a","status":"open","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":1,"issue_id":"t-a","author":"b","text":"x","created_at":"2026-01-02T00:00:00Z"}]}`)
	theirs := file(t, `{"id":"t-a","title":"a","status":"closed","closed_at":"2026-01-02T00:00:00Z","close_reason":"done","updated_at":"2026-01-02T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)

	m := issue(t, out, "t-a")
	require.Equal(t, "closed", m["status"])
	require.Equal(t, "done", m["close_reason"])
	require.Equal(t, "2026-01-02T00:00:00Z", m["closed_at"])
	require.Equal(t, "2026-01-02T00:00:01Z", m["updated_at"])
	require.Len(t, rep.Stamps, 1)
	require.Equal(t, []merge.Stamp{{ID: "t-a", Was: "2026-01-02T00:00:00Z", Now: "2026-01-02T00:00:01Z"}}, rep.Stamps)
}

// A comment-only change on the other side, at a tie: both comments survive, ours
// keeps its id, and the row is stamped.
func TestMerge_CommentOnlyChangeIsStamped(t *testing.T) {
	base := file(t, `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z"}`)
	ours := file(t, `{"id":"t-a","title":"a","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":1,"issue_id":"t-a","author":"o","text":"ours","created_at":"2026-01-02T00:00:00Z"}]}`)
	theirs := file(t, `{"id":"t-a","title":"a","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":7,"issue_id":"t-a","author":"t","text":"theirs","created_at":"2026-01-02T00:00:00Z"}]}`)

	out, rep := run(t, base, ours, theirs)

	m := issue(t, out, "t-a")
	require.Equal(t, []string{"ours", "theirs"}, comments(t, m))
	require.Equal(t, []int64{1, 7}, commentIDs(t, m), "two different comments, so the ids do not collide")
	require.Equal(t, []merge.Stamp{{ID: "t-a", Was: "2026-01-02T00:00:00Z", Now: "2026-01-02T00:00:01Z"}}, rep.Stamps)
}

// A row nobody touched keeps its bytes. So does a row that only theirs changed,
// when theirs' stamp is already newer than ours: br's import prefers the file by
// itself, so there is nothing to stamp and the line is not re-rendered.
func TestMerge_RowsThatNeedNoStampKeepTheirBytes(t *testing.T) {
	const (
		quiet   = `{"id":"t-q","title":"q","updated_at":"2026-01-01T00:00:00.123456Z"}`
		moving  = `{"id":"t-m","title":"m","updated_at":"2026-01-01T00:00:00Z"}`
		movedOn = `{"id":"t-m","title":"m2","updated_at":"2026-01-05T00:00:00Z"}`
	)
	base := file(t, quiet, moving)
	ours := file(t, quiet, moving)
	theirs := file(t, quiet, movedOn)

	out, rep := run(t, base, ours, theirs)

	require.Contains(t, out, quiet+"\n", "a row nobody touched keeps its microsecond timestamp exactly")
	require.Contains(t, out, movedOn+"\n", "a row already newer than ours keeps its bytes")
	require.Empty(t, rep.Stamps)
}

// The stamp keeps the precision br wrote, so a microsecond timestamp moves by a
// microsecond rather than by a second.
func TestMerge_StampKeepsTheRowsPrecision(t *testing.T) {
	base := file(t, `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00.500000Z"}`)
	ours := file(t, `{"id":"t-a","title":"a","updated_at":"2026-01-02T00:00:00.123456Z","comments":[{"id":1,"issue_id":"t-a","author":"b","text":"x","created_at":"2026-01-02T00:00:00Z"}]}`)
	theirs := file(t, `{"id":"t-a","title":"a2","updated_at":"2026-01-02T00:00:00.123456Z"}`)

	out, _ := run(t, base, ours, theirs)

	require.Equal(t, "2026-01-02T00:00:00.123457Z", issue(t, out, "t-a")["updated_at"])
}

// A timestamp written with an offset keeps that spelling, so br parses it back
// and the value is still past the newer parent.
func TestMerge_StampKeepsAnOffsetSpelling(t *testing.T) {
	base := file(t, `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00-06:00"}`)
	ours := file(t, `{"id":"t-a","title":"a","updated_at":"2026-01-02T00:00:00-06:00","comments":[{"id":1,"issue_id":"t-a","author":"b","text":"x","created_at":"2026-01-02T00:00:00Z"}]}`)
	theirs := file(t, `{"id":"t-a","title":"a2","updated_at":"2026-01-02T00:00:00-06:00"}`)

	out, _ := run(t, base, ours, theirs)

	require.Equal(t, "2026-01-02T00:00:01-06:00", issue(t, out, "t-a")["updated_at"])
}

// An issue only one side added is taken as that side wrote it: there is no row
// of ours for the merge to beat.
func TestMerge_AddedIssueIsNotStamped(t *testing.T) {
	added := `{"id":"t-new","title":"n","updated_at":"2026-01-02T00:00:00Z"}`
	base := file(t, epicBase)
	ours := file(t, epicBase)
	theirs := file(t, epicBase, added)

	out, rep := run(t, base, ours, theirs)

	require.Contains(t, out, added+"\n")
	require.Empty(t, rep.Stamps)
}

// A UTC timestamp written with an explicit +00:00 offset is valid RFC 3339 and br
// writes some of them. The stamp must keep that spelling and stay RFC 3339, or
// the row cannot be parsed back and can never be stamped again.
func TestMerge_StampKeepsAZeroOffsetSpelling(t *testing.T) {
	for _, tc := range []struct {
		name, was, want string
	}{
		{"plus zero, whole seconds", "2026-01-02T00:00:00+00:00", "2026-01-02T00:00:01+00:00"},
		{"minus zero, whole seconds", "2026-01-02T00:00:00-00:00", "2026-01-02T00:00:01-00:00"},
		{"plus zero, fractional", "2026-01-02T00:00:00.123456+00:00", "2026-01-02T00:00:00.123457+00:00"},
		{"minus zero, fractional", "2026-01-02T00:00:00.123456-00:00", "2026-01-02T00:00:00.123457-00:00"},
		{"a non-zero offset still works", "2026-01-02T00:00:00+05:30", "2026-01-02T00:00:01+05:30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := file(t, `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z"}`)
			ours := file(t, `{"id":"t-a","title":"a","updated_at":"`+tc.was+`","comments":[{"id":1,"issue_id":"t-a","author":"b","text":"x","created_at":"2026-01-02T00:00:00Z"}]}`)
			theirs := file(t, `{"id":"t-a","title":"a2","updated_at":"`+tc.was+`"}`)

			out, _ := run(t, base, ours, theirs)

			got := issue(t, out, "t-a")["updated_at"]
			require.Equal(t, tc.want, got)
			require.NotNil(t, mustParse(t, got.(string)), "the stamp must stay RFC 3339, or br cannot read the row back")
			require.True(t, mustParse(t, got.(string)).After(mustParse(t, tc.was)), "and it must move forward")
		})
	}
}

// mustParse parses an RFC 3339 timestamp, failing the test when it is not one.
func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err, "%q is not RFC 3339", s)
	return parsed
}

// M6: nothing distinguished "the later parent wins" from "the first parent
// wins" in newest, because in every other fixture ours was the later one. Here
// ours is strictly older: the merge keeps our field and takes theirs, so the
// merged row holds ours' updated_at and is not newer than ours, and the stamp
// must land past THEIRS, the newer parent. A first-parent-wins newest would
// stamp past ours instead and the row would lose the tie with the database.
func TestMerge_StampIsPastTheirsWhenTheirsIsNewer(t *testing.T) {
	base := file(t, `{"id":"t-a","title":"old","status":"open","updated_at":"2026-01-01T00:00:00Z"}`)
	ours := file(t, `{"id":"t-a","title":"ours","status":"open","updated_at":"2026-01-02T00:00:00Z"}`)
	theirs := file(t, `{"id":"t-a","title":"old","status":"closed","closed_at":"2026-01-03T00:00:00Z","close_reason":"done","updated_at":"2026-01-03T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)

	m := issue(t, out, "t-a")
	require.Equal(t, "ours", m["title"], "our field wins")
	require.Equal(t, "closed", m["status"], "theirs' close group wins")
	require.Equal(t, "2026-01-03T00:00:01Z", m["updated_at"], "one unit past theirs, the newer parent")
	require.Equal(t, []merge.Stamp{{ID: "t-a", Was: "2026-01-03T00:00:00Z", Now: "2026-01-03T00:00:01Z"}}, rep.Stamps)
}
