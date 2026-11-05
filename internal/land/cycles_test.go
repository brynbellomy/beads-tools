package land_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

func relate(r *testrepo.Repo, from, to string) {
	r.Run("br", "dep", "add", from, to, "--type", "related", "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
}

func TestPR_RefusesADependencyCycleTheBranchAdds(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	a, b := r.Create("a"), r.Create("b")
	relate(r, a, b)
	r.Commit("beads: a and b")
	r.G("push", "-q", "origin", "main")

	r.G("checkout", "-q", "-b", "feat/g")
	relate(r, b, a)
	r.Commit("beads: relate")
	r.Write("a.go", "package a\n")
	head := r.Commit("feat: a\n\nCloses: " + a)
	r.G("push", "-q", "origin", "feat/g")

	for _, opts := range []land.Options{{}, {DryRun: true}} {
		var out bytes.Buffer
		err := lander(r, viewOnly(t, head), &out).PR(context.Background(), 3, opts)
		require.ErrorContains(t, err, "adds 1 dependency cycle(s) that origin/main does not hold")
		require.Contains(t, out.String(), "refuse feat/g: adds a dependency cycle: "+b+" related to "+a+", "+a+" related to "+b+"\n")
	}
	r.G("fetch", "-q", "origin")
	require.Equal(t, head, r.G("rev-parse", "origin/feat/g"), "a refused landing pushes nothing")
}

func TestPR_LandsOnABaseThatHoldsACycle(t *testing.T) {
	r := testrepo.New(t, testrepo.NewRemote(t))
	a, b := r.Create("a"), r.Create("b")
	relate(r, a, b)
	relate(r, b, a)
	r.Commit("beads: a cycle")
	r.G("push", "-q", "origin", "main")

	r.G("checkout", "-q", "-b", "feat/g")
	r.Run("br", "comments", "add", a, "on the cycle", "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
	r.Write("a.go", "package a\n")
	head := r.Commit("feat: a\n\nCloses: " + a)
	r.G("push", "-q", "origin", "feat/g")

	gh := viewOnly(t, head)
	var merged string
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) { merged = sha }).Return(nil)

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 3, land.Options{}), out.String())
	require.Equal(t, "closed", r.Status(merged, a), "closing a bead on the inherited cycle lands")
}
