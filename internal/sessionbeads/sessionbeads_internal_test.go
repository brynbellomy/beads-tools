package sessionbeads

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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

func TestActorPattern_MatchesTheFormatsShape(t *testing.T) {
	tests := []struct {
		name, format, want string
	}{
		{"the default", DefaultActorFormat, `agent:sess-1/%`},
		{"a suffix after the agent", "{session}@{agent} {team}", `sess-1@%`},
		{"a literal percent", "a%b{session}", `a\%bsess-1%`},
		{"a literal underscore", "{session}_x", `sess-1\_x%`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := actorPattern(tt.format, "sess-1")
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestActorPattern_RefusesAFormatWithoutTheSession(t *testing.T) {
	_, err := actorPattern("agent:{agent}", "sess-1")
	require.ErrorContains(t, err, "no {session}")
}
