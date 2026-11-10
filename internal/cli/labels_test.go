package cli_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/cli"
)

func TestLabels_SessionPrintsTheSessionLabel(t *testing.T) {
	code, out := runSessionArgv(t, t.TempDir(), "labels", "session", sessionID)
	require.Equal(t, cli.ExitOK, code, out)
	require.Equal(t, "session:"+sessionID+"\n", out)
}

func TestLabels_UserPrintsTheEscapedUserLabel(t *testing.T) {
	code, out := runSessionArgv(t, t.TempDir(), "labels", "user", "bryn@signals.io")
	require.Equal(t, cli.ExitOK, code, out)
	require.Equal(t, "user:bryn_40signals_2eio\n", out)
}

func TestLabels_NeedsASubcommandAndAValue(t *testing.T) {
	for _, argv := range [][]string{
		{"labels"},
		{"labels", "session"},
		{"labels", "user"},
		{"labels", "nonsense", "x"},
	} {
		code, out := runSessionArgv(t, t.TempDir(), argv...)
		require.Equal(t, cli.ExitError, code, "%v: %s", argv, out)
	}
}

func TestLabels_HelpPrintsUsage(t *testing.T) {
	code, out := runSessionArgv(t, t.TempDir(), "labels", "--help")
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "beads-tools labels session SESSION-ID")
	require.Contains(t, out, "beads-tools labels user EMAIL")
}
