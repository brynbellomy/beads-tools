package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/cli"
	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// protectedGH puts a gh on PATH that acts as GitHub for one open pull request
// from branch under a policy only an administrator may bypass: pr merge
// without --admin is refused, and when raceOnce is set the first merge with
// --admin is refused because the base branch moved. pr view reports branch's
// tip on origin as the head, and pr create prints url. It returns the file
// that records each pr merge command line.
func protectedGH(t *testing.T, branch, url string, raceOnce bool) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "merges")
	race := "0"
	if raceOnce {
		race = "1"
	}
	script := "#!/bin/sh\n" +
		"branch='" + branch + "'; url='" + url + "'; log='" + log + "'; race=" + race + "\n" + `
case "$1 $2" in
"pr view")
	oid=$(git ls-remote origin "refs/heads/$branch" | cut -f1)
	printf '{"number":3,"state":"OPEN","url":"%s","baseRefName":"main","headRefName":"%s","headRefOid":"%s"}\n' "$url" "$branch" "$oid"
	;;
"pr create")
	echo "$url"
	;;
"pr merge")
	echo "$*" >> "$log"
	case " $* " in
	*" --admin "*) ;;
	*) echo "X Pull request is not mergeable: the base branch policy prohibits the merge." >&2; exit 1 ;;
	esac
	if [ "$race" = 1 ] && [ ! -e "$log.raced" ]; then
		: > "$log.raced"
		echo "GraphQL: Base branch was modified. Review and try the merge again. (mergePullRequest)" >&2
		exit 1
	fi
	;;
*)
	echo "fake gh: $*" >&2
	exit 1
	;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func merges(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// closingBranch pushes main with one open issue and branch feat/a whose commit
// closes it.
func closingBranch(t *testing.T) *testrepo.Repo {
	t.Helper()
	r := testrepo.New(t, testrepo.NewRemote(t))
	id := r.Create("task")
	r.Commit("beads: add\n\nRefs: " + id)
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/a")
	r.Write("a.go", "package a\n")
	r.Commit("feat: a\n\nCloses: " + id)
	r.G("push", "-q", "origin", "feat/a")
	return r
}

func TestLandPR_AdminReachesGHPRMerge(t *testing.T) {
	r := closingBranch(t)
	log := protectedGH(t, "feat/a", "https://example.invalid/pull/3", false)

	code, out := runLand(t, r.Dir, "3")
	require.Equal(t, cli.ExitError, code, out)
	require.Contains(t, out, "the base branch policy prohibits the merge")

	code, out = runLand(t, r.Dir, "3", "--admin")
	require.Equal(t, cli.ExitOK, code, out)
	require.Contains(t, out, "merged #3")

	got := merges(t, log)
	require.Len(t, got, 2)
	require.NotContains(t, got[0], "--admin")
	require.True(t, strings.HasSuffix(got[1], " --admin"), got[1])
}

func TestLandPR_AdminRetriesPassAdmin(t *testing.T) {
	r := closingBranch(t)
	log := protectedGH(t, "feat/a", "https://example.invalid/pull/3", true)

	var out bytes.Buffer
	err := cli.LandWith(context.Background(), r.Dir, []string{"pr", "3", "--admin"}, binary, &out,
		func(l *land.Lander) { l.MergeRetryDelay = 0 })
	require.NoError(t, err, out.String())
	require.Contains(t, out.String(), "attempt 1 of 4")
	require.Contains(t, out.String(), "merged #3")

	got := merges(t, log)
	require.Len(t, got, 2)
	for _, m := range got {
		require.True(t, strings.HasPrefix(m, "pr merge 3 --merge --match-head-commit "), m)
		require.True(t, strings.HasSuffix(m, " --admin"), m)
	}
}

func TestLandSweep_AdminRetriesPassAdmin(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	id := r.Create("task")
	r.Commit("beads: add\n\nRefs: " + id)
	r.Write("a.go", "package a\n")
	r.Commit("feat: a\n\nCloses: " + id)
	r.G("push", "-q", "origin", "main")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	url := "https://example.invalid/pull/9"
	log := protectedGH(t, "beads/sweep-20260102-030405", url, true)

	var out bytes.Buffer
	err := cli.LandWith(context.Background(), r.Dir, []string{"sweep", "--admin"}, binary, &out, func(l *land.Lander) {
		l.MergeRetryDelay = 0
		l.Now = func() time.Time { return now }
	})
	require.NoError(t, err, out.String())
	require.Contains(t, out.String(), "attempt 1 of 4")
	require.Contains(t, out.String(), "merged "+url)

	got := merges(t, log)
	require.Len(t, got, 2)
	for _, m := range got {
		require.True(t, strings.HasPrefix(m, "pr merge "+url+" --merge --match-head-commit "), m)
		require.True(t, strings.HasSuffix(m, " --admin"), m)
	}
}
