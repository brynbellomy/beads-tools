package land_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/land/mocks"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// prRepo pushes main with one issue, then builds branch feat/g with the given
// commits on top. It returns the repo, the issue and the branch head.
func prRepo(t *testing.T, build func(r *testrepo.Repo, id string)) (*testrepo.Repo, string, string) {
	t.Helper()
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	id := r.Create("work")
	r.Commit("add issue")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/g")
	build(r, id)
	r.G("push", "-q", "origin", "feat/g")
	return r, id, r.G("rev-parse", "HEAD")
}

func viewOnly(t *testing.T, head string) *mocks.MockGitHub {
	gh := mocks.NewMockGitHub(t)
	gh.EXPECT().View(mock.Anything, 3).Return(land.PR{Number: 3, State: "OPEN", BaseRefName: "main", HeadRefName: "feat/g", HeadRefOid: head}, nil)
	return gh
}

func TestPR_RefusesCommitsThatNameNoIssue(t *testing.T) {
	r, _, head := prRepo(t, func(r *testrepo.Repo, id string) {
		r.Write("a.go", "package a\n")
		r.Commit("feat: a\n\nRefs: " + id)
		r.Write("b.go", "package b\n")
		r.Commit("feat: b without a trailer")
	})

	var out bytes.Buffer
	err := lander(r, viewOnly(t, head), &out).PR(context.Background(), 3, land.Options{})

	require.ErrorContains(t, err, "1 commit(s) on feat/g")
	require.Contains(t, out.String(), "feat: b without a trailer: names no issue")
	require.NotContains(t, out.String(), "feat: a")
	r.G("fetch", "-q", "origin")
	require.Equal(t, head, r.G("rev-parse", "origin/feat/g"), "a refused landing pushes nothing")
}

func TestPR_DryRunAlsoRefuses(t *testing.T) {
	r, _, head := prRepo(t, func(r *testrepo.Repo, _ string) {
		r.Write("b.go", "package b\n")
		r.Commit("feat: b")
	})
	err := lander(r, viewOnly(t, head), &bytes.Buffer{}).PR(context.Background(), 3, land.Options{DryRun: true})
	require.ErrorContains(t, err, "do not name a valid issue")
}

func TestPR_RefusesUnknownIssuesEvenWhenTrailersAreOptional(t *testing.T) {
	r, _, head := prRepo(t, func(r *testrepo.Repo, _ string) {
		r.Write("b.go", "package b\n")
		r.Commit("feat: b\n\nCloses: t-nope")
		r.Write("c.go", "package c\n")
		r.Commit("feat: c, no trailer, allowed when optional")
	})
	var out bytes.Buffer
	err := lander(r, viewOnly(t, head), &out).PR(context.Background(), 3, land.Options{TrailersOptional: true})
	require.ErrorContains(t, err, "1 commit(s)")
	require.Contains(t, out.String(), "names issues that do not exist: t-nope")
}

func TestPR_ExemptCommitsLand(t *testing.T) {
	r, id, head := prRepo(t, func(r *testrepo.Repo, id string) {
		r.Create("bookkeeping only")
		r.Commit("beads: file a follow-up")
		r.Write("a.go", "package a\n")
		bad := r.Commit("feat: a\n\nRefs: " + id)
		r.G("-c", "core.hooksPath=/dev/null", "revert", "--no-edit", bad)
		r.Write("a.go", "package a // again\n")
		r.Commit("feat: a again\n\nCloses: " + id)
		r.G("checkout", "-q", "-b", "side")
		r.Write("side.go", "package side\n")
		r.Commit("feat: side\n\nRefs: " + id)
		r.G("checkout", "-q", "feat/g")
		r.G("-c", "core.hooksPath=/dev/null", "merge", "--no-ff", "--no-edit", "side")
	})

	gh := viewOnly(t, head)
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").Return(nil)
	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 3, land.Options{}), out.String())
	require.Contains(t, out.String(), "close "+id)
}
