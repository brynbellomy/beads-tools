package land_test

import (
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

// A row in the older timestamp format that br re-renders on any export.
const bystander = `{"id":"t-old","title":"old row","status":"closed","priority":2,"issue_type":"task","created_at":"2026-10-09T17:19:45.000000Z","updated_at":"2026-10-09T17:19:45.000000Z","closed_at":"2026-10-09T17:19:45.000000Z","close_reason":"x","compaction_level":0,"original_size":0}`

func TestCloseInTree_KeepsRowsBrWouldRewrite(t *testing.T) {
	r := testrepo.New(t, "")
	id := r.Create("to close")
	path := filepath.Join(r.Dir, ".beads", "issues.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	r.Write(".beads/issues.jsonl", string(data)+bystander+"\n")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	// Precondition: br on its own does rewrite the bystander row.
	probe := testrepo.New(t, "")
	probe.Write(".beads/issues.jsonl", string(before))
	probe.Run("br", "close", id, "--reason", "probe", "--no-db", "--quiet")
	probed, err := os.ReadFile(filepath.Join(probe.Dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)
	require.NotContains(t, string(probed), bystander, "br rewrites the bystander; otherwise this test proves nothing")

	changed, err := land.CloseInTree(context.Background(), r.Dir, []land.Close{{ID: id, Reason: "Closed by abc"}}, land.BR{})
	require.NoError(t, err)
	require.Equal(t, []string{id}, changed)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(after), bystander+"\n")
	beforeLines, afterLines := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	require.Len(t, afterLines, len(beforeLines))
	var diff int
	for i := range beforeLines {
		if beforeLines[i] != afterLines[i] {
			diff++
			require.Contains(t, afterLines[i], `"id":"`+id+`"`)
			require.Contains(t, afterLines[i], `"close_reason":"Closed by abc"`)
		}
	}
	require.Equal(t, 1, diff)
}

func TestCloseInTree_AlreadyClosedIsANoOp(t *testing.T) {
	r := testrepo.New(t, "")
	id := r.Create("x")
	r.Run("br", "close", id, "--reason", "earlier", "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
	before, err := os.ReadFile(filepath.Join(r.Dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)

	changed, err := land.CloseInTree(context.Background(), r.Dir, []land.Close{{ID: id, Reason: "again"}}, land.BR{})
	require.NoError(t, err)
	require.Empty(t, changed)
	after, err := os.ReadFile(filepath.Join(r.Dir, ".beads", "issues.jsonl"))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// rewriteBeads stands in for a br that misbehaves: it replaces the scratch
// issues file with whatever edit returns.
func rewriteBeads(t *testing.T, edit func(string) string) land.Beads {
	m := mocks.NewMockBeads(t)
	m.EXPECT().Close(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, dir, _, _ string) error {
			p := filepath.Join(dir, ".beads", "issues.jsonl")
			data, err := os.ReadFile(p)
			require.NoError(t, err)
			return os.WriteFile(p, []byte(edit(string(data))), 0o644)
		})
	return m
}

func TestCloseInTree_RefusesWhatBrGetsWrong(t *testing.T) {
	const (
		a = `{"id":"t-a","title":"a","status":"open"}`
		b = `{"id":"t-b","title":"b","status":"open"}`
	)
	cases := map[string]struct {
		edit func(string) string
		want string
	}{
		"dropped a row": {func(string) string { return a + "\n" }, "dropped"},
		"added a row":   {func(s string) string { return s + `{"id":"t-c","status":"open"}` + "\n" }, "added"},
		"did not close": {func(s string) string { return s }, "did not close"},
		"wrote garbage": {func(string) string { return "{not json\n" }, "br's output"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, ".beads"), 0o755))
			orig := a + "\n" + b + "\n"
			path := filepath.Join(dir, ".beads", "issues.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(orig), 0o644))

			_, err := land.CloseInTree(context.Background(), dir, []land.Close{{ID: "t-a", Reason: "r"}}, rewriteBeads(t, tc.edit))

			require.ErrorContains(t, err, tc.want)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, orig, string(data), "a refused close leaves the file alone")
		})
	}
}

func TestCloseInTree_UnknownIssueFails(t *testing.T) {
	r := testrepo.New(t, "")
	r.Create("x")
	_, err := land.CloseInTree(context.Background(), r.Dir, []land.Close{{ID: "t-nope", Reason: "r"}}, land.BR{})
	require.ErrorContains(t, err, "not found")
}
