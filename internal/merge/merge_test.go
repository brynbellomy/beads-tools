package merge_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/merge"
)

func file(t *testing.T, lines ...string) *beadsfile.File {
	t.Helper()
	f, err := beadsfile.Parse(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	require.NoError(t, err)
	return f
}

func run(t *testing.T, base, ours, theirs *beadsfile.File) (string, *merge.Report) {
	t.Helper()
	out, rep, err := merge.Merge(base, ours, theirs)
	require.NoError(t, err)
	b, err := out.Bytes()
	require.NoError(t, err)
	return string(b), rep
}

func issue(t *testing.T, out, id string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		if m["id"] == id {
			return m
		}
	}
	t.Fatalf("issue %s not in output", id)
	return nil
}

func comments(t *testing.T, m map[string]any) []string {
	t.Helper()
	raw, _ := m["comments"].([]any)
	out := make([]string, len(raw))
	for i, c := range raw {
		out[i] = c.(map[string]any)["text"].(string)
	}
	return out
}

const (
	epicBase   = `{"id":"t-e","title":"epic","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	child1Base = `{"id":"t-e.1","title":"child","status":"open","created_at":"2026-01-01T00:00:01Z","updated_at":"2026-01-01T00:00:01Z"}`
)

func TestMerge_SameIssueDifferentFieldsTakesBoth(t *testing.T) {
	base := file(t, epicBase)
	ours := file(t, `{"id":"t-e","title":"renamed epic","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`)
	theirs := file(t, `{"id":"t-e","title":"epic","status":"open","priority":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-03T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)

	require.True(t, rep.Clean(), rep.String())
	m := issue(t, out, "t-e")
	require.Equal(t, "renamed epic", m["title"])
	require.EqualValues(t, 1, m["priority"])
	require.Equal(t, "2026-01-03T00:00:00Z", m["updated_at"])
	require.Equal(t, `{"id":"t-e","title":"renamed epic","status":"open","priority":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-03T00:00:00Z"}`+"\n", out,
		"a key only theirs has lands where theirs placed it")
}

// The case br sync --merge --force loses: both sides comment on the epic and
// add a child, and only one side changes a sibling.
func TestMerge_ConcurrentCommentsChildrenAndStatus(t *testing.T) {
	base := file(t, epicBase, child1Base)
	ours := file(t,
		`{"id":"t-e","title":"epic","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":1,"issue_id":"t-e","author":"a","text":"from A","created_at":"2026-01-02T00:00:00Z"}]}`,
		child1Base,
		`{"id":"t-e.2","title":"A child","status":"open","created_at":"2026-01-02T00:00:01Z","updated_at":"2026-01-02T00:00:01Z","dependencies":[{"issue_id":"t-e.2","depends_on_id":"t-e","type":"parent-child"}]}`,
	)
	theirs := file(t,
		`{"id":"t-e","title":"epic","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-03T00:00:00Z","comments":[{"id":1,"issue_id":"t-e","author":"b","text":"from B","created_at":"2026-01-03T00:00:00Z"}]}`,
		`{"id":"t-e.1","title":"child","status":"in_progress","created_at":"2026-01-01T00:00:01Z","updated_at":"2026-01-03T00:00:01Z"}`,
		`{"id":"t-e.2","title":"B child","status":"open","created_at":"2026-01-03T00:00:02Z","updated_at":"2026-01-03T00:00:02Z","dependencies":[{"issue_id":"t-e.2","depends_on_id":"t-e","type":"parent-child"}],"comments":[{"id":2,"issue_id":"t-e.2","author":"b","text":"on B child","created_at":"2026-01-03T00:00:03Z"}]}`,
	)

	out, rep := run(t, base, ours, theirs)

	require.True(t, rep.Clean(), rep.String())
	require.Equal(t, []merge.Rename{{Old: "t-e.2", New: "t-e.3"}}, rep.Renames)
	require.Equal(t, []string{"from A", "from B"}, comments(t, issue(t, out, "t-e")))
	require.Equal(t, "in_progress", issue(t, out, "t-e.1")["status"])
	require.Equal(t, "A child", issue(t, out, "t-e.2")["title"])

	b := issue(t, out, "t-e.3")
	require.Equal(t, "B child", b["title"])
	dep := b["dependencies"].([]any)[0].(map[string]any)
	require.Equal(t, "t-e.3", dep["issue_id"], "references to the renamed issue follow it")
	require.Equal(t, "t-e.3", b["comments"].([]any)[0].(map[string]any)["issue_id"])

	ids := map[float64]int{}
	for _, id := range []string{"t-e", "t-e.3"} {
		for _, c := range issue(t, out, id)["comments"].([]any) {
			ids[c.(map[string]any)["id"].(float64)]++
		}
	}
	for id, n := range ids {
		require.Equal(t, 1, n, "comment id %v is used once", id)
	}
	require.Len(t, rep.Renumbers, 1, "theirs' comment that reused ours' id 1 is renumbered")
	require.Equal(t, "t-e", rep.Renumbers[0].IssueID)
}

// Found on real data: theirs' br renumbered an existing comment, and the issue
// came across whole from theirs. The comment must keep ours' ID rather than
// collide with ours' comment that owns theirs' number.
func TestMerge_SharedCommentsKeepOursIDs(t *testing.T) {
	a := `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":1,"issue_id":"t-a","author":"x","text":"old","created_at":"2026-01-01T00:00:00Z"}]}`
	b := `{"id":"t-b","title":"b","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":5,"issue_id":"t-b","author":"y","text":"other","created_at":"2026-01-01T00:00:00Z"}]}`
	base := file(t, a, b)
	ours := file(t, a, b)
	theirs := file(t,
		`{"id":"t-a","title":"a2","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":5,"issue_id":"t-a","author":"x","text":"old","created_at":"2026-01-01T00:00:00Z"}]}`,
		b)

	out, rep := run(t, base, ours, theirs)

	m := issue(t, out, "t-a")
	require.Equal(t, "a2", m["title"])
	require.EqualValues(t, 1, m["comments"].([]any)[0].(map[string]any)["id"])
	require.Empty(t, rep.Renumbers, "realigning to ours' id is not a collision")
}

func TestMerge_StatusConflictKeepsNewerCloseGroupTogether(t *testing.T) {
	base := file(t, child1Base)
	ours := file(t, `{"id":"t-e.1","title":"child","status":"in_progress","created_at":"2026-01-01T00:00:01Z","updated_at":"2026-01-02T00:00:00Z"}`)
	theirs := file(t, `{"id":"t-e.1","title":"child","status":"closed","created_at":"2026-01-01T00:00:01Z","updated_at":"2026-01-03T00:00:00Z","closed_at":"2026-01-03T00:00:00Z","close_reason":"done in #5"}`)

	out, rep := run(t, base, ours, theirs)

	require.Len(t, rep.Conflicts, 1)
	require.Equal(t, merge.Theirs, rep.Conflicts[0].Kept)
	m := issue(t, out, "t-e.1")
	require.Equal(t, "closed", m["status"])
	require.Equal(t, "done in #5", m["close_reason"])
	require.Equal(t, "2026-01-03T00:00:00Z", m["closed_at"])
}

func TestMerge_ReopenOnOneSideDropsCloseFields(t *testing.T) {
	closed := `{"id":"t-e.1","title":"child","status":"closed","created_at":"2026-01-01T00:00:01Z","updated_at":"2026-01-02T00:00:00Z","closed_at":"2026-01-02T00:00:00Z","close_reason":"x"}`
	base := file(t, closed)
	ours := file(t, `{"id":"t-e.1","title":"child v2","status":"closed","created_at":"2026-01-01T00:00:01Z","updated_at":"2026-01-03T00:00:00Z","closed_at":"2026-01-02T00:00:00Z","close_reason":"x"}`)
	theirs := file(t, `{"id":"t-e.1","title":"child","status":"open","created_at":"2026-01-01T00:00:01Z","updated_at":"2026-01-04T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)

	require.True(t, rep.Clean(), rep.String())
	m := issue(t, out, "t-e.1")
	require.Equal(t, "open", m["status"])
	require.Equal(t, "child v2", m["title"])
	require.NotContains(t, m, "closed_at")
	require.NotContains(t, m, "close_reason")
}

func TestMerge_ScalarConflictTieKeepsOurs(t *testing.T) {
	base := file(t, epicBase)
	ours := file(t, `{"id":"t-e","title":"ours","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`)
	theirs := file(t, `{"id":"t-e","title":"theirs","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)

	require.Len(t, rep.Conflicts, 1)
	require.Equal(t, []string{"title"}, rep.Conflicts[0].Fields)
	require.Equal(t, merge.Ours, rep.Conflicts[0].Kept)
	require.Equal(t, "ours", issue(t, out, "t-e")["title"])
}

func TestMerge_LabelsAndDependenciesMergeAsSets(t *testing.T) {
	base := file(t, `{"id":"t-x","title":"x","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","labels":["a","b"],"dependencies":[{"issue_id":"t-x","depends_on_id":"t-1","type":"blocks"}]}`)
	ours := file(t, `{"id":"t-x","title":"x","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","labels":["a","c"],"dependencies":[{"issue_id":"t-x","depends_on_id":"t-1","type":"blocks"},{"issue_id":"t-x","depends_on_id":"t-2","type":"blocks"}]}`)
	theirs := file(t, `{"id":"t-x","title":"x","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-03T00:00:00Z","labels":["a","b","d"],"dependencies":[{"issue_id":"t-x","depends_on_id":"t-3","type":"related"}]}`)

	out, rep := run(t, base, ours, theirs)

	require.True(t, rep.Clean(), rep.String())
	m := issue(t, out, "t-x")
	require.Equal(t, []any{"a", "c", "d"}, m["labels"], "b removed by ours, c and d added")
	var deps []string
	for _, d := range m["dependencies"].([]any) {
		deps = append(deps, d.(map[string]any)["depends_on_id"].(string))
	}
	require.Equal(t, []string{"t-2", "t-3"}, deps, "t-1 removed by theirs")
}

func TestMerge_DeleteUnmodifiedAndDeleteVsModify(t *testing.T) {
	a := `{"id":"t-a","title":"a","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	b := `{"id":"t-b","title":"b","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	base := file(t, a, b)
	ours := file(t, b)
	theirs := file(t, a, `{"id":"t-b","title":"b2","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)
	require.NotContains(t, out, `"t-a"`)
	require.Equal(t, "b2", issue(t, out, "t-b")["title"])
	require.Empty(t, rep.Conflicts)

	out, rep = run(t, base, file(t, a), theirs)
	require.Equal(t, "b2", issue(t, out, "t-b")["title"], "a modified issue survives the other side's delete")
	require.Len(t, rep.Conflicts, 1)
}

func TestMerge_UntouchedLinesKeepTheirBytes(t *testing.T) {
	odd := `{"id":"t-o","title":"<a> & b","created_at":"2026-01-01T00:00:00.000000Z","updated_at":"2026-01-01T00:00:00.000000Z","labels":[]}`
	base := file(t, odd, epicBase)
	ours := file(t, odd, `{"id":"t-e","title":"epic","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","priority":1}`)
	theirs := file(t, odd, `{"id":"t-e","title":"epic","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-03T00:00:00Z","assignee":"z"}`)

	out, _ := run(t, base, ours, theirs)

	require.Contains(t, out, odd+"\n")
	require.Contains(t, out, `"assignee":"z"`)
	require.NotContains(t, out, "\\"+"u003c", "br does not escape HTML")
}

func TestMerge_OutputIsSortedByID(t *testing.T) {
	x := `{"id":"t-x","title":"x","created_at":"2026-01-01T00:00:00Z"}`
	a := `{"id":"t-a","title":"a","created_at":"2026-01-01T00:00:00Z"}`
	out, _ := run(t, file(t), file(t, x), file(t, a))
	require.Equal(t, a+"\n"+x+"\n", out)
}

func TestMerge_NonChildIDCollisionFails(t *testing.T) {
	ours := file(t, `{"id":"t-abc","title":"a","created_at":"2026-01-01T00:00:00Z"}`)
	theirs := file(t, `{"id":"t-abc","title":"b","created_at":"2026-01-02T00:00:00Z"}`)
	_, _, err := merge.Merge(file(t), ours, theirs)
	require.Error(t, err)
}

func TestMerge_IdenticalCreationOnBothSidesIsOneIssue(t *testing.T) {
	x := `{"id":"t-e.1","title":"x","created_at":"2026-01-01T00:00:00Z"}`
	out, rep := run(t, file(t), file(t, x), file(t, x))
	require.Equal(t, x+"\n", out)
	require.Empty(t, rep.Renames)
}

func TestParse_RejectsConflictMarkers(t *testing.T) {
	_, err := beadsfile.Parse(strings.NewReader(epicBase + "\n<<<<<<< HEAD\n"))
	require.Error(t, err)
}

// Minimized from sirene's merge 46199db11: one note posted on several issues
// in the same second gives comments with equal author, time and text but
// distinct ids. Neither side touched those issues, and their lines must come
// through byte for byte.
const (
	bulkA = `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":819,"issue_id":"t-a","author":"x","text":"SUBSUMED","created_at":"2026-07-17T19:14:12Z"}]}`
	bulkB = `{"id":"t-b","title":"b","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":162,"issue_id":"t-b","author":"x","text":"SUBSUMED","created_at":"2026-07-17T19:14:12Z"}]}`
)

func commentIDs(t *testing.T, m map[string]any) []int64 {
	t.Helper()
	raw, _ := m["comments"].([]any)
	out := make([]int64, len(raw))
	for i, c := range raw {
		out[i] = int64(c.(map[string]any)["id"].(float64))
	}
	return out
}

func requireUniqueCommentIDs(t *testing.T, out string) {
	t.Helper()
	f := file(t, strings.TrimSpace(out))
	dups, err := f.DuplicateCommentIDs()
	require.NoError(t, err)
	require.Empty(t, dups)
}

func TestMerge_IssuesNeitherSideTouchedKeepTheirBytes(t *testing.T) {
	x := `{"id":"t-x","title":"x","updated_at":"2026-01-01T00:00:00Z"}`
	base := file(t, bulkA, bulkB, x)
	ours := file(t, bulkA, bulkB, `{"id":"t-x","title":"x","priority":1,"updated_at":"2026-01-02T00:00:00Z"}`)
	theirs := file(t, bulkA, bulkB, `{"id":"t-x","title":"x","assignee":"z","updated_at":"2026-01-03T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)

	require.Contains(t, out, bulkA+"\n")
	require.Contains(t, out, bulkB+"\n")
	require.Empty(t, rep.Renumbers)
	requireUniqueCommentIDs(t, out)
}

func TestMerge_SharedCommentKeepsItsOwnIssuesID(t *testing.T) {
	base := file(t, bulkA, bulkB)
	ours := file(t,
		`{"id":"t-a","title":"a","priority":1,"updated_at":"2026-01-02T00:00:00Z","comments":[{"id":819,"issue_id":"t-a","author":"x","text":"SUBSUMED","created_at":"2026-07-17T19:14:12Z"}]}`,
		bulkB)
	theirs := file(t,
		`{"id":"t-a","title":"a2","updated_at":"2026-01-03T00:00:00Z","comments":[{"id":819,"issue_id":"t-a","author":"x","text":"SUBSUMED","created_at":"2026-07-17T19:14:12Z"}]}`,
		bulkB)

	out, rep := run(t, base, ours, theirs)

	a := issue(t, out, "t-a")
	require.Equal(t, "a2", a["title"])
	require.EqualValues(t, 1, a["priority"])
	require.Equal(t, []int64{819}, commentIDs(t, a), "the comment both sides hold keeps its id, not t-b's")
	require.Contains(t, out, bulkB+"\n")
	require.Empty(t, rep.Renumbers)
}

func TestMerge_AddedCommentThatCollidesIsRenumberedAboveEveryInput(t *testing.T) {
	gone := `{"id":"t-gone","title":"gone","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":900,"issue_id":"t-gone","author":"x","text":"old","created_at":"2026-01-01T00:00:00Z"}]}`
	x := `{"id":"t-x","title":"x","updated_at":"2026-01-01T00:00:00Z"}`
	y := `{"id":"t-y","title":"y","updated_at":"2026-01-01T00:00:00Z"}`
	base := file(t, bulkA, bulkB, gone, x, y)
	ours := file(t, bulkA, bulkB,
		`{"id":"t-x","title":"x","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":848,"issue_id":"t-x","author":"o","text":"ours","created_at":"2026-01-02T00:00:00Z"}]}`,
		y)
	theirs := file(t, bulkA, bulkB, x,
		`{"id":"t-y","title":"y","updated_at":"2026-01-03T00:00:00Z","comments":[{"id":848,"issue_id":"t-y","author":"t","text":"theirs","created_at":"2026-01-03T00:00:00Z"}]}`)

	out, rep := run(t, base, ours, theirs)

	require.Equal(t, []int64{848}, commentIDs(t, issue(t, out, "t-x")), "ours' comment keeps its id")
	require.Equal(t, []int64{901}, commentIDs(t, issue(t, out, "t-y")), "theirs' colliding comment moves above base's 900")
	require.Equal(t, []merge.Renumber{{IssueID: "t-y", Old: 848, New: 901}}, rep.Renumbers)
	require.Contains(t, out, bulkA+"\n")
	require.Contains(t, out, bulkB+"\n")
	requireUniqueCommentIDs(t, out)
}

// Minimized from sirene's merge e813e2978: ours carried the duplicates an
// earlier merge introduced. The comment that held the id in base keeps it, and
// the copy that took it falls back to the id base gave it.
func TestMerge_OutputNeverHoldsADuplicateCommentID(t *testing.T) {
	base := file(t, bulkA, bulkB)
	corrupt := `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":162,"issue_id":"t-a","author":"x","text":"SUBSUMED","created_at":"2026-07-17T19:14:12Z"}]}`
	ours := file(t, corrupt, bulkB)
	theirs := file(t, bulkA, bulkB)

	out, rep := run(t, base, ours, theirs)

	requireUniqueCommentIDs(t, out)
	require.Contains(t, out, bulkB+"\n", "t-b held 162 in base and keeps it")
	require.Contains(t, out, bulkA+"\n", "t-a takes back base's 819")
	require.Empty(t, rep.Renumbers)
}

// Without an id a side holds to fall back to, a duplicate an input carries
// gets a fresh id.
func TestMerge_DuplicateWithNoOtherIDGetsAFreshOne(t *testing.T) {
	x := `{"id":"t-x","title":"x","updated_at":"2026-01-01T00:00:00Z"}`
	base := file(t, bulkB, x)
	dupOnX := `{"id":"t-x","title":"x","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":162,"issue_id":"t-x","author":"o","text":"new","created_at":"2026-01-02T00:00:00Z"}]}`
	ours := file(t, bulkB, dupOnX)
	theirs := file(t, bulkB, x)

	out, rep := run(t, base, ours, theirs)

	requireUniqueCommentIDs(t, out)
	require.Contains(t, out, bulkB+"\n")
	require.Equal(t, []merge.Renumber{{IssueID: "t-x", Old: 162, New: 163}}, rep.Renumbers)
}

// Found in review: main repaired a duplicate id, and a branch forked before the
// repair merges main. The renumbering only theirs made survives.
func TestMerge_OneSidedCommentIDChangeSurvives(t *testing.T) {
	a := `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":5,"issue_id":"t-a","author":"x","text":"one","created_at":"2026-01-01T00:00:00Z"}]}`
	b := `{"id":"t-b","title":"b","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":5,"issue_id":"t-b","author":"x","text":"two","created_at":"2026-01-01T00:00:00Z"}]}`
	bRepaired := `{"id":"t-b","title":"b","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":9,"issue_id":"t-b","author":"x","text":"two","created_at":"2026-01-01T00:00:00Z"}]}`
	x := `{"id":"t-x","title":"x","updated_at":"2026-01-01T00:00:00Z"}`
	base := file(t, a, b, x)
	ours := file(t, a, b, `{"id":"t-x","title":"x","priority":1,"updated_at":"2026-01-02T00:00:00Z"}`)
	theirs := file(t, a, bRepaired, `{"id":"t-x","title":"x","assignee":"z","updated_at":"2026-01-03T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)

	require.Contains(t, out, a+"\n")
	require.Contains(t, out, bRepaired+"\n")
	require.Empty(t, rep.Renumbers)
	requireUniqueCommentIDs(t, out)
}

func TestMerge_IdenticalCommentsWithinOneIssueKeepTheirIDs(t *testing.T) {
	a := `{"id":"t-a","title":"a","updated_at":"2026-01-01T00:00:00Z","comments":[{"id":7,"issue_id":"t-a","author":"x","text":"same","created_at":"2026-01-01T00:00:00Z"},{"id":8,"issue_id":"t-a","author":"x","text":"same","created_at":"2026-01-01T00:00:00Z"}]}`
	x := `{"id":"t-x","title":"x","updated_at":"2026-01-01T00:00:00Z"}`
	base := file(t, a, x)
	ours := file(t, a, `{"id":"t-x","title":"x","priority":1,"updated_at":"2026-01-02T00:00:00Z"}`)
	theirs := file(t, a, `{"id":"t-x","title":"x","assignee":"z","updated_at":"2026-01-03T00:00:00Z"}`)

	out, rep := run(t, base, ours, theirs)

	require.Contains(t, out, a+"\n")
	require.Empty(t, rep.Renumbers)
}

func TestMerge_RenumberingClearsTheirsMaxID(t *testing.T) {
	x := `{"id":"t-x","title":"x","updated_at":"2026-01-01T00:00:00Z"}`
	y := `{"id":"t-y","title":"y","updated_at":"2026-01-01T00:00:00Z"}`
	z := `{"id":"t-z","title":"z","updated_at":"2026-01-01T00:00:00Z"}`
	base := file(t, x, y, z)
	ours := file(t,
		`{"id":"t-x","title":"x","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":3,"issue_id":"t-x","author":"o","text":"ours","created_at":"2026-01-02T00:00:00Z"}]}`,
		y,
		`{"id":"t-z","title":"z","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":40,"issue_id":"t-z","author":"o","text":"ours too","created_at":"2026-01-02T00:00:00Z"}]}`)
	theirs := file(t, x,
		`{"id":"t-y","title":"y","updated_at":"2026-01-03T00:00:00Z","comments":[{"id":3,"issue_id":"t-y","author":"t","text":"theirs","created_at":"2026-01-03T00:00:00Z"},{"id":60,"issue_id":"t-y","author":"t","text":"later","created_at":"2026-01-03T00:00:01Z"}]}`,
		`{"id":"t-z","title":"z","updated_at":"2026-01-02T00:00:00Z","comments":[{"id":40,"issue_id":"t-z","author":"o","text":"ours too","created_at":"2026-01-02T00:00:00Z"}]}`)

	out, rep := run(t, base, ours, theirs)

	require.Equal(t, []int64{61, 60}, commentIDs(t, issue(t, out, "t-y")), "theirs' colliding comment moves above theirs' own 60")
	require.Equal(t, []merge.Renumber{{IssueID: "t-y", Old: 3, New: 61}}, rep.Renumbers)
	requireUniqueCommentIDs(t, out)
}
