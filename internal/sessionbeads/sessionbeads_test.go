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

func TestStores_ASessionWithNoCopiedRowsNamesNone(t *testing.T) {
	beads, err := sessionbeads.TouchedIn(context.Background(), t.TempDir(), t.TempDir())
	require.NoError(t, err)
	require.Empty(t, beads)
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
