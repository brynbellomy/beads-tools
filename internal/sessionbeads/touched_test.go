package sessionbeads_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/sessionbeads"
)

// eventsWriter writes a session's beads-events.jsonl by hand, so a test states
// exactly which rows sirene copied.
type eventsWriter struct {
	t    *testing.T
	dir  string
	file *os.File
}

func newSession(t *testing.T) *eventsWriter {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".sirene", "sessions", session)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	f, err := os.Create(filepath.Join(dir, "beads-events.jsonl"))
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return &eventsWriter{t: t, dir: dir, file: f}
}

// copied appends one copied row. A row is what sirene recorded: the store, the
// issue, the event, the actor that made it, and the issue's title and status as
// that store held them at the time.
func (w *eventsWriter) copied(store, issueID, eventType, agent, title, status, at string) {
	w.t.Helper()
	line := `{"store":"` + store + `","issue_id":"` + issueID + `","event_type":"` + eventType +
		`","actor":"agent:` + session + `/` + agent + `","title":"` + title + `","status":"` + status +
		`","created_at":"` + at + `"}` + "\n"
	_, err := w.file.WriteString(line)
	require.NoError(w.t, err)
}

// toucher is the production entry point bound to this session's directory.
func (w *eventsWriter) touched(workDir string) ([]sessionbeads.Bead, error) {
	return sessionbeads.TouchedIn(context.Background(), w.dir, workDir)
}

// TestTouched_ReportsABeadWhoseStoreWasRemoved is the pin for the defect this
// change fixes: a bead the session touched, whose worktree has since been
// removed, still belongs to the session. Reading only live stores dropped it.
func TestTouched_ReportsABeadWhoseStoreWasRemoved(t *testing.T) {
	w := newSession(t)
	gone := filepath.Join(t.TempDir(), "removed-worktree", ".beads")
	w.copied(gone, "t-gone", "created", "engineer", "filed in a worktree since removed", "open", "2026-10-19T01:10:24+00:00")

	beads, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Len(t, beads, 1, "a removed store does not remove the bead from the session")
	require.Equal(t, sessionbeads.Bead{
		ID:      "t-gone",
		Title:   "filed in a worktree since removed",
		Status:  "open",
		Updated: "2026-10-19T01:10:24+00:00",
	}, beads[0])
}

// TestTouched_ReportsABeadWhoseEventsTableABRRebuildWiped pins the second half
// of the defect: br rebuilds beads.db from issues.jsonl, which restores every
// issue and no events at all. A live read of that store finds nothing.
func TestTouched_ReportsABeadWhoseEventsTableABRRebuildWiped(t *testing.T) {
	w := newSession(t)
	f := newStore(t, t.TempDir())
	f.issue("t-a", "still here after the rebuild", "open", "task", "2026-10-19T00:00:00+00:00")
	// The store holds the issue, and none of the session's event rows.
	w.copied(f.store, "t-a", "created", "engineer", "filed before the rebuild", "open", "2026-10-19T01:10:24+00:00")

	beads, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Len(t, beads, 1, "a wiped events table does not remove the bead from the session")
	require.Equal(t, "t-a", beads[0].ID)
	require.Equal(t, "still here after the rebuild", beads[0].Title, "the live store's title wins")
	require.Equal(t, "task", beads[0].Type, "the live store supplies the type")
}

// TestTouched_TrustsTheRowsWhenTheStoreIsGone pins that a bead with no live
// store is reported from its rows alone, with empty fields the rows cannot
// carry rather than a guess.
func TestTouched_TrustsTheRowsWhenTheStoreIsGone(t *testing.T) {
	w := newSession(t)
	gone := filepath.Join(t.TempDir(), "removed", ".beads")
	w.copied(gone, "t-a", "created", "engineer", "from the row", "open", "2026-10-19T01:10:24+00:00")

	beads, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "from the row", beads[0].Title)
	require.Equal(t, "open", beads[0].Status)
	require.Empty(t, beads[0].Type, "no store can supply a type")
	require.Empty(t, beads[0].Parent, "no store can supply a parent")
}

// TestTouched_MatchesABeadByRowNotByActor pins that membership is the session's
// own record, not an actor pattern: a row the session copied is the session's
// bead even when its actor does not look like this session's, and a live row
// with a matching actor is not a bead the session reported unless a copied row
// names it.
func TestTouched_MatchesABeadByRowNotByActor(t *testing.T) {
	w := newSession(t)
	f := newStore(t, t.TempDir())
	f.issue("t-copied", "copied by the session", "open", "task", "2026-10-19T00:00:00+00:00")
	f.issue("t-live-only", "the store holds it, no row names it", "open", "task", "2026-10-19T00:00:00+00:00")
	f.event("t-live-only", "created", "agent:"+session+"/engineer")
	w.copied(f.store, "t-copied", "created", "someone-else", "copied by the session", "open", "2026-10-19T01:10:24+00:00")

	beads, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "t-copied", beads[0].ID)
}

// TestTouched_ALaterRowWins pins the fold: rows are appended in event order, so
// the newest row's title and status are the bead's before any live read.
func TestTouched_ALaterRowWins(t *testing.T) {
	w := newSession(t)
	gone := filepath.Join(t.TempDir(), "removed", ".beads")
	w.copied(gone, "t-a", "created", "engineer", "the first title", "open", "2026-10-19T01:00:00+00:00")
	w.copied(gone, "t-a", "status_changed", "engineer", "the last title", "closed", "2026-10-19T02:00:00+00:00")

	beads, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "the last title", beads[0].Title)
	require.Equal(t, "closed", beads[0].Status)
	require.Equal(t, "2026-10-19T02:00:00+00:00", beads[0].Updated)
}

// TestTouched_AnOutOfOrderRowDoesNotOverwriteANewerOne pins that the fold
// compares times, not append positions, so a row sirene copied late cannot
// resurrect an older title.
func TestTouched_AnOutOfOrderRowDoesNotOverwriteANewerOne(t *testing.T) {
	w := newSession(t)
	gone := filepath.Join(t.TempDir(), "removed", ".beads")
	w.copied(gone, "t-a", "status_changed", "engineer", "the newer title", "closed", "2026-10-19T02:00:00+00:00")
	w.copied(gone, "t-a", "created", "engineer", "the older title", "open", "2026-10-19T01:00:00+00:00")

	beads, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "the newer title", beads[0].Title)
	require.Equal(t, "closed", beads[0].Status)
}

// TestTouched_FillsTheTypeAndTheParentFromALiveStore pins the fields only a
// store can supply.
func TestTouched_FillsTheTypeAndTheParentFromALiveStore(t *testing.T) {
	w := newSession(t)
	f := newStore(t, t.TempDir())
	f.issue("t-epic", "the epic", "open", "epic", "2026-10-01T00:00:00+00:00")
	f.issue("t-a", "a child", "in_progress", "bug", "2026-10-19T01:10:24.437623+00:00")
	f.parent("t-a", "t-epic")
	w.copied(f.store, "t-a", "status_changed", "engineer", "a child", "in_progress", "2026-10-19T01:10:24+00:00")

	beads, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "bug", beads[0].Type)
	require.Equal(t, "t-epic", beads[0].Parent)
	require.Equal(t, "2026-10-19T01:10:24.437623+00:00", beads[0].Updated, "the store's updated_at is the newest word")
}

// TestTouched_ReadsTheCheckoutForABeadWhoseOwnStoreIsGone pins the workDir
// fallback: a bead that landed on the base branch is in the checkout's store
// even when the worktree that filed it is gone.
func TestTouched_ReadsTheCheckoutForABeadWhoseOwnStoreIsGone(t *testing.T) {
	w := newSession(t)
	workDir := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".git"), 0o755))
	checkout := newStore(t, workDir)
	checkout.issue("t-a", "the current title", "closed", "bug", "2026-10-20T00:00:00+00:00")
	gone := filepath.Join(t.TempDir(), "removed", ".beads")
	w.copied(gone, "t-a", "created", "engineer", "the title when it was filed", "open", "2026-10-19T01:00:00+00:00")

	beads, err := w.touched(workDir)
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "the current title", beads[0].Title)
	require.Equal(t, "closed", beads[0].Status)
	require.Equal(t, "bug", beads[0].Type)
}

// TestTouched_ACheckoutNamesNoBeadTheRowsDoNot pins that the workDir read is an
// enrichment, never a source of membership.
func TestTouched_ACheckoutNamesNoBeadTheRowsDoNot(t *testing.T) {
	w := newSession(t)
	workDir := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".git"), 0o755))
	checkout := newStore(t, workDir)
	checkout.issue("t-elsewhere", "nobody in this session filed it", "open", "task", "2026-10-20T00:00:00+00:00")
	checkout.event("t-elsewhere", "created", "agent:"+session+"/engineer")

	beads, err := w.touched(workDir)
	require.NoError(t, err)
	require.Empty(t, beads, "the checkout supplies fields, not membership")
}

// TestTouched_IsSortedAndDeduplicatedAcrossStores pins one row per bead, sorted
// by id, however many stores the session touched.
func TestTouched_IsSortedAndDeduplicatedAcrossStores(t *testing.T) {
	w := newSession(t)
	root := t.TempDir()
	first := newStore(t, filepath.Join(root, "first"))
	first.issue("t-b", "b, from the first store", "open", "task", "2026-10-19T00:00:00+00:00")
	first.issue("t-a", "a, from the first store", "open", "task", "2026-10-19T00:00:00+00:00")
	second := newStore(t, filepath.Join(root, "second"))
	second.issue("t-a", "a, from the second store", "closed", "task", "2026-10-21T00:00:00+00:00")
	w.copied(first.store, "t-b", "created", "engineer", "b", "open", "2026-10-19T00:00:00+00:00")
	w.copied(first.store, "t-a", "created", "engineer", "a", "open", "2026-10-19T00:00:00+00:00")
	w.copied(second.store, "t-a", "closed", "engineer", "a", "closed", "2026-10-21T00:00:00+00:00")

	beads, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Len(t, beads, 2)
	require.Equal(t, "t-a", beads[0].ID)
	require.Equal(t, "t-b", beads[1].ID)
	require.Equal(t, "a, from the second store", beads[0].Title, "the newer store's row wins")
	require.Equal(t, "closed", beads[0].Status)
}

// TestTouched_ASkippedLineCostsOnlyItself pins that a torn last line, which
// sirene's append can leave, does not lose the rows before it.
func TestTouched_ASkippedLineCostsOnlyItself(t *testing.T) {
	w := newSession(t)
	gone := filepath.Join(t.TempDir(), "removed", ".beads")
	w.copied(gone, "t-a", "created", "engineer", "a good row", "open", "2026-10-19T00:00:00+00:00")
	_, err := w.file.WriteString(`{"store":"` + gone + `","issue_id":"t-b"`)
	require.NoError(t, err)

	beads, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "t-a", beads[0].ID)
}

// TestTouched_ASessionWithNoCopiedRowsHasNoBeads pins that a session with no
// file, and one with an empty file, report nothing rather than failing.
func TestTouched_ASessionWithNoCopiedRowsHasNoBeads(t *testing.T) {
	empty := t.TempDir()
	beads, err := sessionbeads.TouchedIn(context.Background(), empty, t.TempDir())
	require.NoError(t, err)
	require.Empty(t, beads)

	w := newSession(t)
	beads, err = w.touched(t.TempDir())
	require.NoError(t, err)
	require.Empty(t, beads)
}

// TestTouched_ReadsManyBeadsAcrossTheIDChunkBoundary pins that a session with
// more touched beads than one query binds still reports every one.
func TestTouched_ReadsManyBeadsAcrossTheIDChunkBoundary(t *testing.T) {
	w := newSession(t)
	f := newStore(t, t.TempDir())
	const n = 1200
	for i := 0; i < n; i++ {
		id := "t-" + pad(i)
		f.issue(id, "bead "+id, "open", "task", "2026-10-19T00:00:00+00:00")
		w.copied(f.store, id, "created", "engineer", "bead "+id, "open", "2026-10-19T00:00:00+00:00")
	}

	beads, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Len(t, beads, n)
	require.Equal(t, "task", beads[0].Type, "every bead is enriched, not just the first chunk")
	require.Equal(t, "task", beads[n-1].Type)
}

// TestTouchedLeavesEveryStoreUnchanged pins the read-only promise: a report must
// never change a store.
func TestTouchedLeavesEveryStoreUnchanged(t *testing.T) {
	w := newSession(t)
	f := newStore(t, t.TempDir())
	f.issue("t-a", "a bead", "open", "task", "2026-10-19T00:00:00+00:00")
	w.copied(f.store, "t-a", "created", "engineer", "a bead", "open", "2026-10-19T00:00:00+00:00")
	before := rowCount(t, f.store)

	_, err := w.touched(t.TempDir())
	require.NoError(t, err)
	require.Equal(t, before, rowCount(t, f.store))
}

// pad renders i as a fixed-width suffix so ids sort predictably.
func pad(i int) string {
	s := "0000" + itoa(i)
	return s[len(s)-4:]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// rowCount returns how many issues and events a store holds.
func rowCount(t *testing.T, store string) [2]int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(store, "beads.db"))
	require.NoError(t, err)
	defer db.Close()
	var out [2]int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM issues`).Scan(&out[0]))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&out[1]))
	return out
}
