package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	r.Write("b.go", "package b\n")
	r.Commit("feat: b\n\nCloses: " + id)
	code, out = runCheck(t, r.Dir, "--base", base)
	require.Equal(t, cli.ExitOK, code, out)
	require.NotContains(t, out, "note:", "a Closes: trailer in the range settles the reference")

	r.G("-c", "core.hooksPath=/dev/null", "revert", "--no-edit", "HEAD")
	code, out = runCheck(t, r.Dir, "--base", base)
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "note: "+id+" is referenced", "a reverted Closes: settles nothing")
}

func TestCheck_BadRevisionIsAnError(t *testing.T) {
	r := testrepo.New(t, "")
	code, _ := runCheck(t, r.Dir, "--base", "no-such-rev")
	require.Equal(t, cli.ExitError, code)
}

func TestCheck_RequireTrailersFlagOverridesConfig(t *testing.T) {
	r := testrepo.New(t, "")
	base := r.G("rev-parse", "HEAD")
	r.Write("a.go", "package a\n")
	r.Commit("feat: a, unlinked")

	code, out := runCheck(t, r.Dir, "--base", base, "--require-trailers=false")
	require.Equal(t, cli.ExitOK, code, out)

	r.G("config", "beadstools.requireTrailers", "false")
	code, out = runCheck(t, r.Dir, "--base", base, "--require-trailers=true")
	require.Equal(t, cli.ExitConflict, code, out)
	require.Contains(t, out, "names no issue")
}

func TestCheck_FailsOnDuplicateCommentIDs(t *testing.T) {
	r := testrepo.New(t, "")
	id := r.Create("task")
	base := r.Commit("add")
	path := filepath.Join(r.Dir, ".beads", "issues.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	r.Write(".beads/issues.jsonl", string(data)+
		`{"id":"t-da","title":"a","comments":[{"id":7,"issue_id":"t-da","author":"x","text":"n","created_at":"2026-01-01T00:00:00Z"}]}`+"\n"+
		`{"id":"t-db","title":"b","comments":[{"id":7,"issue_id":"t-db","author":"x","text":"n","created_at":"2026-01-01T00:00:00Z"}]}`+"\n")
	r.Commit("beads: import\n\nRefs: " + id)

	code, out := runCheck(t, r.Dir, "--base", base)
	require.Equal(t, cli.ExitConflict, code, out)
	require.Contains(t, out, "comment id 7 is used 2 times (t-da, t-db)")
	require.Contains(t, out, "1 duplicate comment id(s)")
}

// fakeGH puts a gh on PATH that answers pr view for one pull request and
// fails everything else.
func fakeGH(t *testing.T, prJSON string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1 $2\" = \"pr view\" ]; then\ncat <<'JSON'\n" + prJSON + "\nJSON\nexit 0\nfi\necho \"fake gh: $*\" >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func runLand(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"land", "pr"}, args...)...)
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

func TestLandPR_RequireTrailersFlagOverridesConfig(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	r.G("checkout", "-q", "-b", "feat/f")
	r.Write("f.go", "package f\n")
	head := r.Commit("feat: f, unlinked")
	r.G("push", "-q", "origin", "feat/f")
	fakeGH(t, `{"number":3,"state":"OPEN","baseRefName":"main","headRefName":"feat/f","headRefOid":"`+head+`"}`)

	code, out := runLand(t, r.Dir, "3", "--dry-run")
	require.Equal(t, cli.ExitError, code, out)
	require.Contains(t, out, "do not name a valid issue")

	code, out = runLand(t, r.Dir, "3", "--dry-run", "--require-trailers=false")
	require.Equal(t, cli.ExitOK, code, out)

	r.G("config", "beadstools.requireTrailers", "false")
	code, out = runLand(t, r.Dir, "3", "--dry-run")
	require.Equal(t, cli.ExitOK, code, "the config still applies without the flag: %s", out)
	code, out = runLand(t, r.Dir, "3", "--dry-run", "--require-trailers=true")
	require.Equal(t, cli.ExitError, code, out)
}

func TestCheck_RefusesADependencyCycleTheRangeAdds(t *testing.T) {
	r := testrepo.New(t, "")
	epic := r.Create("epic")
	kid := r.Create("child")
	other := r.Create("other")
	r.Run("br", "dep", "add", kid, epic, "--type", "parent-child", "--quiet")
	r.Run("br", "dep", "add", kid, other, "--type", "related", "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
	base := r.Commit("add issues")

	r.Run("br", "dep", "add", other, epic, "--type", "related", "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
	cycle := r.Commit("beads: relate")
	var br struct{ Cycles [][]string }
	require.NoError(t, json.Unmarshal([]byte(r.Run("br", "dep", "cycles", "--json")), &br))
	require.Len(t, br.Cycles, 1)
	walk := strings.Join(br.Cycles[0][:3], " ")
	require.Contains(t, []string{epic + " " + kid + " " + other, kid + " " + other + " " + epic, other + " " + epic + " " + kid}, walk,
		"br dep cycles walks the cycle in the same direction")

	code, out := runCheck(t, r.Dir, "--base", base)
	require.Equal(t, cli.ExitConflict, code, out)
	require.Contains(t, out, ".beads/issues.jsonl at HEAD adds a dependency cycle: "+
		other+" related to "+epic+", "+epic+" parent of "+kid+", "+kid+" related to "+other+"\n")
	require.Contains(t, out, "adds 1 dependency cycle(s); remove one edge of each, usually a related one")

	r.Run("br", "close", epic, kid, other, "--quiet")
	r.Run("br", "comments", "add", kid, "still a cycle", "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
	r.Commit("beads: close")
	code, out = runCheck(t, r.Dir, "--base", base)
	require.Equal(t, cli.ExitConflict, code, "a cycle among closed beads counts: %s", out)

	code, out = runCheck(t, r.Dir, "--base", cycle)
	require.Equal(t, cli.ExitOK, code, "a range that edits the beads of an inherited cycle passes: %s", out)
}

func TestCheck_PassesACycleTheMergeBaseHolds(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	a := r.Create("a")
	b := r.Create("b")
	r.Run("br", "dep", "add", a, b, "--type", "related", "--quiet")
	r.Run("br", "dep", "add", b, a, "--type", "related", "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
	r.Commit("beads: a cycle")
	r.G("push", "-q", "origin", "main")

	r.G("checkout", "-q", "-b", "feat/c")
	r.Write("c.go", "package c\n")
	r.Commit("feat: c\n\nRefs: " + a)

	r.G("checkout", "-q", "main")
	r.Run("br", "dep", "remove", b, a, "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
	r.Commit("beads: break the cycle")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "feat/c")

	code, out := runCheck(t, r.Dir)
	require.Equal(t, cli.ExitOK, code, "the branch only inherits the cycle main has since removed: %s", out)
}
