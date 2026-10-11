package cli_test

import (
	"bytes"
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/cli"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

func runCheck(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"check"}, args...)...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), out.String()
	}
	require.NoError(t, err)
	return 0, out.String()
}

func TestCheck_PassesLinkedCommitsAndFailsUnlinkedOnes(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	id := r.Create("work")
	r.Commit("add issue")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/c")
	r.Write("a.go", "package a\n")
	r.Commit("feat: a\n\nRefs: " + id)

	code, out := runCheck(t, r.Dir)
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "1 commit(s) in origin/main..HEAD name valid issues", "the base defaults to the remote's main branch")

	r.Write("b.go", "package b\n")
	r.Commit("feat: b, unlinked")
	code, out = runCheck(t, r.Dir)
	require.Equal(t, cli.ExitConflict, code, out)
	require.Contains(t, out, "feat: b, unlinked: names no issue")
	require.Contains(t, out, "1 of 2 commit(s)")
}

func TestCheck_ExplicitRangeAndOptionalTrailers(t *testing.T) {
	r := testrepo.New(t, "")
	base := r.G("rev-parse", "HEAD")
	r.Write("a.go", "package a\n")
	r.Commit("feat: a, unlinked")
	head := r.G("rev-parse", "HEAD")
	r.Write("b.go", "package b\n")
	r.Commit("feat: b\n\nCloses: t-nope")

	code, out := runCheck(t, r.Dir, "--base", base, "--head", head)
	require.Equal(t, cli.ExitConflict, code, out)
	require.NotContains(t, out, "t-nope", "commits past --head are not checked")

	r.G("config", "beadstools.requireTrailers", "false")
	code, out = runCheck(t, r.Dir, "--base", base, "--head", head)
	require.Equal(t, cli.ExitOK, code, out)
	code, out = runCheck(t, r.Dir, "--base", base)
	require.Equal(t, cli.ExitConflict, code, "unknown issues fail even when trailers are optional")
	require.Contains(t, out, "names issues that do not exist: t-nope")
}

func TestCheck_NotesReferencedIssuesWithoutFailing(t *testing.T) {
	r := testrepo.New(t, "")
	id := r.Create("task")
	base := r.Commit("add")
	r.Write("a.go", "package a\n")
	r.Commit("feat: a\n\nRefs: " + id)

	code, out := runCheck(t, r.Dir, "--base", base)
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "note: "+id+" is referenced")
}

func TestCheck_BadRevisionIsAnError(t *testing.T) {
	r := testrepo.New(t, "")
	code, _ := runCheck(t, r.Dir, "--base", "no-such-rev")
	require.Equal(t, cli.ExitError, code)
}
