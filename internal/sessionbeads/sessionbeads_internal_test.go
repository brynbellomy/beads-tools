package sessionbeads

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReadRows_KeepsEveryRowAndOnlyWhatItNeeds pins the fold's input: a row's
// store and issue id make it worth keeping, and a line that does not parse is
// dropped without losing the rows around it.
func TestReadRows_KeepsEveryRowAndOnlyWhatItNeeds(t *testing.T) {
	dir := t.TempDir()
	rows := `{"store":"/w/one/.beads","issue_id":"t-a"}` + "\n" +
		"\n" +
		`{"store":"/w/two/.beads","issue_id":"t-b"}` + "\n" +
		`{"store":"/w/one/.beads"}` + "\n" +
		`{"issue_id":"t-c"}` + "\n" +
		`not json` + "\n" +
		`{"store":"/w/one/.beads","issue_id":"t-a"}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, beadsEventsFile), []byte(rows), 0o644))

	got, err := readRows(dir)
	require.NoError(t, err)
	require.Len(t, got, 3, "a row needs both a store and an issue id")
	require.Equal(t, []string{"/w/one/.beads", "/w/two/.beads"}, storesOf(got), "distinct, in first-appearance order")
}

// TestReadRows_ASessionWithNoFileHasNoRows pins that a missing file is an empty
// session, not an error.
func TestReadRows_ASessionWithNoFileHasNoRows(t *testing.T) {
	got, err := readRows(t.TempDir())
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestOpenReadOnly_RefusesAWrite pins the read-only open: a report must never
// change a store.
func TestOpenReadOnly_RefusesAWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beads.db")
	w, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = w.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	db, err := openReadOnly(path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`INSERT INTO issues (id) VALUES ('t-a')`)
	require.ErrorContains(t, err, "readonly")
}

// TestNewer_ComparesTimesNotText pins the fold's ordering. The same instant
// written with and without a fraction sorts differently as text, and SQLite
// hands a DATETIME column back with a space instead of a T.
func TestNewer_ComparesTimesNotText(t *testing.T) {
	tests := []struct {
		name, a, b string
		want       bool
	}{
		{"later", "2026-10-19T02:00:00+00:00", "2026-10-19T01:00:00+00:00", true},
		{"earlier", "2026-10-19T01:00:00+00:00", "2026-10-19T02:00:00+00:00", false},
		{"the same instant", "2026-10-19T00:00:00+00:00", "2026-10-19T00:00:00.000000+00:00", false},
		{"fraction decides", "2026-10-19T00:00:00.000001+00:00", "2026-10-19T00:00:00+00:00", true},
		{"sqlite's shape", "2026-10-19 02:00:00+00:00", "2026-10-19T01:00:00+00:00", true},
		{"no time beats nothing", "2026-10-19T01:00:00+00:00", "", true},
		{"nothing beats no time", "", "2026-10-19T01:00:00+00:00", false},
		{"two nothings", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, newer(tt.a, tt.b))
		})
	}
}
