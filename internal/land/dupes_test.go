package land_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/land/mocks"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// Two issues holding comment id 7, the shape sirene's main reached.
const (
	dupA = `{"id":"t-da","title":"a","status":"open","comments":[{"id":7,"issue_id":"t-da","author":"x","text":"n","created_at":"2026-01-01T00:00:00Z"}]}`
	dupB = `{"id":"t-db","title":"b","status":"open","comments":[{"id":7,"issue_id":"t-db","author":"x","text":"n","created_at":"2026-01-01T00:00:00Z"}]}`
)

func appendRows(t *testing.T, r *testrepo.Repo, rows ...string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.Dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)
	r.Write(".beads/issues.jsonl", string(data)+strings.Join(rows, "\n")+"\n")
}

func TestPR_RefusesDuplicateCommentIDs(t *testing.T) {
	r, _, head := prRepo(t, func(r *testrepo.Repo, id string) {
		appendRows(t, r, dupA, dupB)
		r.Commit("beads: import")
		r.Write("a.go", "package a\n")
		r.Commit("feat: a\n\nCloses: " + id)
	})

	for _, opts := range []land.Options{{}, {DryRun: true}} {
		var out bytes.Buffer
		err := lander(r, viewOnly(t, head), &out).PR(context.Background(), 3, opts)
		require.ErrorContains(t, err, "1 duplicate comment id(s)")
		require.Contains(t, out.String(), "comment id 7 is used 2 times (t-da, t-db)")
	}
	r.G("fetch", "-q", "origin")
	require.Equal(t, head, r.G("rev-parse", "origin/feat/g"), "a refused landing pushes nothing")
}

func TestCloseInTree_RefusesToWriteADuplicateCommentID(t *testing.T) {
	r := testrepo.New(t, "")
	other := `{"id":"t-other","title":"o","status":"open","comments":[{"id":7,"issue_id":"t-other","author":"x","text":"n","created_at":"2026-01-01T00:00:00Z"}]}`
	target := `{"id":"t-c","title":"c","status":"open"}`
	r.Write(".beads/issues.jsonl", other+"\n"+target+"\n")
	path := filepath.Join(r.Dir, ".beads", "issues.jsonl")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	beads := mocks.NewMockBeads(t)
	beads.EXPECT().Close(mock.Anything, mock.Anything, "t-c", "done").
		RunAndReturn(func(_ context.Context, dir, _, _ string) error {
			closed := `{"id":"t-c","title":"c","status":"closed","comments":[{"id":7,"issue_id":"t-c","author":"y","text":"m","created_at":"2026-01-02T00:00:00Z"}]}`
			return os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(other+"\n"+closed+"\n"), 0o644)
		})

	_, err = land.CloseInTree(context.Background(), r.Dir, []land.Close{{ID: "t-c", Reason: "done"}}, beads)
	require.ErrorContains(t, err, "duplicate comment id")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}
