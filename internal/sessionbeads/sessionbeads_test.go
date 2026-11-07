package sessionbeads_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/sessionbeads"
)

const session = "01a14743-2ec7-79eb-a9ab-39c56ca73589"

func actor(agent string) string { return "agent:" + session + "/" + agent }

func TestTouched_ReturnsOnlyTheSessionsBeads(t *testing.T) {
	dir := t.TempDir()
	f := newStore(t, dir)
	f.issue("t-a", "the session's bead", "open", "task", "2026-10-19T01:10:24+00:00")
	f.issue("t-b", "another session's bead", "open", "task", "2026-10-19T01:10:24+00:00")
	f.issue("t-c", "untouched", "open", "task", "2026-10-19T01:10:24+00:00")
	f.event("t-a", "created", actor("engineer-e96b44ea"))
	f.event("t-b", "created", "agent:019f0000-0000-7000-8000-000000000000/engineer")
	f.event("t-c", "created", "brynbellomy")

	beads, err := sessionbeads.Touched(context.Background(), []string{f.store}, session, sessionbeads.DefaultActorFormat)
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "t-a", beads[0].ID)
	require.Equal(t, "the session's bead", beads[0].Title)
}

func TestTouched_ReportsEveryFieldAndTheParentEpic(t *testing.T) {
	dir := t.TempDir()
	f := newStore(t, dir)
	f.issue("t-epic", "the epic", "open", "epic", "2026-10-01T00:00:00+00:00")
	f.issue("t-a", "a child", "in_progress", "bug", "2026-10-19T01:10:24.437623+00:00")
	f.parent("t-a", "t-epic")
	f.event("t-a", "status_changed", actor("engineer"))

	beads, err := sessionbeads.Touched(context.Background(), []string{f.store}, session, sessionbeads.DefaultActorFormat)
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, sessionbeads.Bead{
		ID:      "t-a",
		Title:   "a child",
		Status:  "in_progress",
		Type:    "bug",
		Updated: "2026-10-19T01:10:24.437623+00:00",
		Parent:  "t-epic",
	}, beads[0])
}

func TestTouched_ABeadWithoutAParentHasNone(t *testing.T) {
	dir := t.TempDir()
	f := newStore(t, dir)
	f.issue("t-a", "no parent", "open", "task", "2026-10-19T01:10:24+00:00")
	f.event("t-a", "created", actor("engineer"))

	beads, err := sessionbeads.Touched(context.Background(), []string{f.store}, session, sessionbeads.DefaultActorFormat)
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Empty(t, beads[0].Parent)
}

func TestTouched_MergesStoresAndKeepsTheNewestRow(t *testing.T) {
	root := t.TempDir()
	old := newStore(t, filepath.Join(root, "old"))
	old.issue("t-a", "stale title", "open", "task", "2026-10-01T00:00:00+00:00")
	old.event("t-a", "created", actor("engineer"))
	fresh := newStore(t, filepath.Join(root, "fresh"))
	fresh.issue("t-a", "current title", "closed", "task", "2026-10-19T00:00:00+00:00")
	fresh.event("t-a", "closed", actor("engineer"))
	fresh.issue("t-b", "only here", "open", "task", "2026-10-19T00:00:00+00:00")
	fresh.event("t-b", "created", actor("engineer"))

	beads, err := sessionbeads.Touched(context.Background(), []string{old.store, fresh.store}, session, sessionbeads.DefaultActorFormat)
	require.NoError(t, err)
	require.Len(t, beads, 2)
	require.Equal(t, "t-a", beads[0].ID)
	require.Equal(t, "current title", beads[0].Title, "the newest row wins")
	require.Equal(t, "closed", beads[0].Status)
	require.Equal(t, "t-b", beads[1].ID)
}

func TestTouched_ComparesTimestampsAsTimesNotText(t *testing.T) {
	root := t.TempDir()
	// The same instant, written with and without a fraction: as text the
	// shorter one sorts first, so a text comparison would pick the wrong row.
	short := newStore(t, filepath.Join(root, "short"))
	short.issue("t-a", "the later row", "closed", "task", "2026-10-19T00:00:00+00:00")
	short.event("t-a", "closed", actor("engineer"))
	long := newStore(t, filepath.Join(root, "long"))
	long.issue("t-a", "the earlier row", "open", "task", "2026-10-19T00:00:00.000001+00:00")
	long.event("t-a", "created", actor("engineer"))

	beads, err := sessionbeads.Touched(context.Background(), []string{long.store, short.store}, session, sessionbeads.DefaultActorFormat)
	require.NoError(t, err)
	require.Len(t, beads, 1)
	require.Equal(t, "the earlier row", beads[0].Title, "the microsecond-later row wins")
}

func TestTouched_IgnoresAStoreWithNoDatabase(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty", ".beads")
	require.NoError(t, os.MkdirAll(empty, 0o755))

	beads, err := sessionbeads.Touched(context.Background(), []string{empty}, session, sessionbeads.DefaultActorFormat)
	require.NoError(t, err)
	require.Empty(t, beads)
}

func TestTouched_RefusesAFormatWithoutTheSession(t *testing.T) {
	_, err := sessionbeads.Touched(context.Background(), nil, session, "agent:{agent}")
	require.ErrorContains(t, err, "no {session}")
}

func TestTouched_MatchesAConfiguredFormat(t *testing.T) {
	dir := t.TempDir()
	f := newStore(t, dir)
	f.issue("t-a", "a bead", "open", "task", "2026-10-19T00:00:00+00:00")
	f.event("t-a", "created", session+"@engineer {team}")
	f.event("t-a", "created", "agent:"+session+"/engineer")

	beads, err := sessionbeads.Touched(context.Background(), []string{f.store}, session, "{session}@{agent} {team}")
	require.NoError(t, err)
	require.Len(t, beads, 1, "the format's own shape matches, and the default one does not")
}

func TestTouched_EscapesLikeWildcardsInTheSessionID(t *testing.T) {
	dir := t.TempDir()
	f := newStore(t, dir)
	f.issue("t-a", "a bead", "open", "task", "2026-10-19T00:00:00+00:00")
	f.event("t-a", "created", "agent:sess_1/engineer")
	f.event("t-a", "created", "agent:sessX1/engineer")

	beads, err := sessionbeads.Touched(context.Background(), []string{f.store}, "sess_1", sessionbeads.DefaultActorFormat)
	require.NoError(t, err)
	require.Len(t, beads, 1, "the underscore is a literal, not a LIKE wildcard")
}

func TestStores_ReturnsTheStoresTheSessionNamed(t *testing.T) {
	root := t.TempDir()
	dir := sessionDir(t, root, session, "/w/one/.beads", "/w/two/.beads", "/w/one/.beads")

	stores, err := sessionbeads.Stores(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"/w/one/.beads", "/w/two/.beads"}, stores)
}

func TestStores_ASessionWithNoCopiedRowsNamesNone(t *testing.T) {
	stores, err := sessionbeads.Stores(t.TempDir())
	require.NoError(t, err)
	require.Empty(t, stores)
}

func TestLocate_FindsTheSessionInTheProjectThenTheHome(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "project")
	home := filepath.Join(root, "home")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	require.NoError(t, os.MkdirAll(home, 0o755))
	projectDir := sessionDir(t, workDir, session)
	homeDir := sessionDir(t, home, "01a1b335-9a49-722e-9d54-3de6486740c8")

	got, ok := sessionbeads.Locate(workDir, home, session, "")
	require.True(t, ok)
	require.Equal(t, projectDir, got)

	got, ok = sessionbeads.Locate(workDir, home, "01a1b335-9a49-722e-9d54-3de6486740c8", "")
	require.True(t, ok)
	require.Equal(t, homeDir, got)

	_, ok = sessionbeads.Locate(workDir, home, "019f0000-0000-7000-8000-000000000000", "")
	require.False(t, ok)
}

func TestLocate_AnExplicitDirWins(t *testing.T) {
	root := t.TempDir()
	explicit := sessionDir(t, root, session)

	got, ok := sessionbeads.Locate("", "", session, explicit)
	require.True(t, ok)
	require.Equal(t, explicit, got)
}

func TestLocate_AnExplicitDirThatDoesNotExistIsNotFound(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "project")
	sessionDir(t, workDir, session)

	_, ok := sessionbeads.Locate(workDir, "", session, filepath.Join(root, "elsewhere"))
	require.False(t, ok, "--dir is authoritative, so the search does not fall back")
}

func TestCheckoutStore_FindsTheNearestBeadsAndFollowsTheRedirect(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".beads"), 0o755))
	sub := filepath.Join(repo, "internal", "pkg")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	store, ok := sessionbeads.CheckoutStore(sub)
	require.True(t, ok)
	require.Equal(t, filepath.Join(repo, ".beads"), store)

	require.NoError(t, os.WriteFile(filepath.Join(repo, ".beads", "redirect"), []byte(".\n"), 0o644))
	store, ok = sessionbeads.CheckoutStore(sub)
	require.True(t, ok)
	require.Equal(t, filepath.Join(repo, ".beads"), store)
}

func TestCheckoutStore_ACheckoutWithNoBeadsHasNone(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))

	_, ok := sessionbeads.CheckoutStore(repo)
	require.False(t, ok)
}

func TestTouchedIn_ReadsTheSessionsStoresAndTheCheckouts(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "project")
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".git"), 0o755))
	worktree := filepath.Join(root, "worktree")
	require.NoError(t, os.MkdirAll(worktree, 0o755))

	worktreeStore := newStore(t, worktree)
	worktreeStore.issue("t-a", "from the worktree", "open", "task", "2026-10-19T00:00:00+00:00")
	worktreeStore.event("t-a", "created", actor("engineer"))
	checkoutStore := newStore(t, workDir)
	checkoutStore.issue("t-b", "from the checkout", "open", "task", "2026-10-19T00:00:00+00:00")
	checkoutStore.event("t-b", "created", actor("engineer"))

	dir := sessionDir(t, workDir, session, worktreeStore.store)
	beads, err := sessionbeads.TouchedIn(context.Background(), dir, workDir, session, sessionbeads.DefaultActorFormat)
	require.NoError(t, err)
	require.Len(t, beads, 2)
	require.Equal(t, "t-a", beads[0].ID)
	require.Equal(t, "t-b", beads[1].ID)
}

func TestReport_PrintsATableWithADashForNoParent(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, sessionbeads.Report(&buf, []sessionbeads.Bead{
		{ID: "t-a", Title: "a child", Status: "open", Type: "task", Updated: "2026-10-19T00:00:00+00:00", Parent: "t-epic"},
		{ID: "t-b", Title: "no parent", Status: "closed", Type: "bug", Updated: "2026-10-20T00:00:00+00:00"},
	}))
	out := buf.String()
	require.Contains(t, out, "ID")
	require.Contains(t, out, "PARENT")
	require.Contains(t, out, "t-a")
	require.Contains(t, out, "t-epic")
	require.Regexp(t, `t-b\s+no parent\s+closed\s+bug\s+2026-10-20T00:00:00\+00:00\s+-`, out)
}

func TestReportJSON_PrintsTheFieldsAndANullParent(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, sessionbeads.ReportJSON(&buf, []sessionbeads.Bead{
		{ID: "t-a", Title: "a child", Status: "open", Type: "task", Updated: "2026-10-19T00:00:00+00:00", Parent: "t-epic"},
		{ID: "t-b", Title: "no parent", Status: "closed", Type: "bug", Updated: "2026-10-20T00:00:00+00:00"},
	}))
	var got []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 2)
	require.Equal(t, "t-epic", got[0]["parent_epic"])
	require.Nil(t, got[1]["parent_epic"])
	require.Equal(t, "2026-10-20T00:00:00+00:00", got[1]["updated_at"])
}

func TestReportJSON_AnEmptyListIsAnEmptyArray(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, sessionbeads.ReportJSON(&buf, nil))
	require.Equal(t, "[]\n", buf.String())
}

func TestReportJSON_DoesNotEscapeHTML(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, sessionbeads.ReportJSON(&buf, []sessionbeads.Bead{
		{ID: "t-a", Title: "a <b> & c", Status: "open", Type: "task", Updated: "2026-10-19T00:00:00+00:00"},
	}))
	require.Contains(t, buf.String(), "a <b> & c")
}

func TestReport_AnEmptyListPrintsOnlyTheHeader(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, sessionbeads.Report(&buf, nil))
	require.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("\n")))
}
