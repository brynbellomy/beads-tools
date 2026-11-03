package land_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/land/mocks"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

const sweepBranch = "beads/sweep-20261010-120000"

// blockedRepo holds four issues: blocked waits on blocker, which the same sweep
// closes later; stuck waits on held, which stays open; downstream waits on stuck.
type blockedRepo struct {
	*testrepo.Repo
	remote, blocked, blocker, stuck, held, downstream string
}

func newBlockedRepo(t *testing.T) blockedRepo {
	remote := testrepo.NewRemote(t)
	r := blockedRepo{Repo: testrepo.New(t, remote), remote: remote}
	r.blocked = r.Create("blocked by blocker")
	r.blocker = r.Create("blocker")
	r.stuck = r.Create("blocked by held")
	r.held = r.Create("held open")
	r.downstream = r.Create("blocked by stuck")
	r.Run("br", "dep", "add", r.blocked, r.blocker, "--quiet")
	r.Run("br", "dep", "add", r.stuck, r.held, "--type", "waits-for", "--quiet")
	r.Run("br", "dep", "add", r.downstream, r.stuck, "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
	r.Commit("add issues")
	for _, id := range []string{r.blocked, r.stuck, r.downstream, r.blocker} {
		r.Write("work-"+id, id+"\n")
		r.Commit("work on " + id + "\n\nCloses: " + id)
	}
	r.G("push", "-q", "origin", "main")
	return r
}

func lineIndex(t *testing.T, out, line string) int {
	t.Helper()
	for i, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, line) {
			return i
		}
	}
	t.Fatalf("no line starting %q in:\n%s", line, out)
	return -1
}

func TestSweep_ClosesBlockersFirstAndSkipsIssuesWithOpenBlockers(t *testing.T) {
	r := newBlockedRepo(t)
	gh := mocks.NewMockGitHub(t)
	gh.EXPECT().Create(mock.Anything, "main", sweepBranch, "beads: close 2 issues merged to main", mock.Anything).
		Return("https://example.test/pr/1", nil)

	var out bytes.Buffer
	require.NoError(t, lander(r.Repo, gh, &out).Sweep(context.Background(), land.Options{NoMerge: true}))

	r.G("fetch", "-q", "origin")
	head := r.G("rev-parse", "origin/"+sweepBranch)
	require.Equal(t, "closed", r.Status(head, r.blocker))
	require.Equal(t, "closed", r.Status(head, r.blocked))
	require.Equal(t, "open", r.Status(head, r.stuck))
	require.Equal(t, "open", r.Status(head, r.downstream))
	require.Equal(t, "open", r.Status(head, r.held))

	o := out.String()
	require.Less(t, lineIndex(t, o, "close "+r.blocker), lineIndex(t, o, "close "+r.blocked))
	require.NotContains(t, o, "close "+r.stuck)
	require.NotContains(t, o, "close "+r.downstream)
	require.Less(t, lineIndex(t, o, "opened "), lineIndex(t, o, "not closed: "))
	require.True(t, strings.HasSuffix(o, "not closed: "+r.stuck+" is blocked by "+r.held+", still open\n"+
		"not closed: "+r.downstream+" is blocked by "+r.stuck+", which this run does not close\n"), o)
}

func TestSweep_DryRunShowsTheCloseOrderAndTheSkips(t *testing.T) {
	r := newBlockedRepo(t)
	before := r.G("ls-remote", r.remote)

	var out bytes.Buffer
	require.NoError(t, lander(r.Repo, mocks.NewMockGitHub(t), &out).Sweep(context.Background(), land.Options{DryRun: true}))

	o := out.String()
	require.Less(t, lineIndex(t, o, "close "+r.blocker), lineIndex(t, o, "close "+r.blocked))
	require.NotContains(t, o, "close "+r.stuck)
	require.Contains(t, o, "not closed: "+r.stuck+" is blocked by "+r.held+", still open\n")
	require.Contains(t, o, "not closed: "+r.downstream+" is blocked by "+r.stuck+", which this run does not close\n")
	require.Equal(t, before, r.G("ls-remote", r.remote))
}

func TestSweep_EveryCloseBlockedOpensNothing(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	a := r.Create("a")
	b := r.Create("b")
	r.Run("br", "dep", "add", a, b, "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
	r.Commit("work\n\nCloses: " + a)
	r.G("push", "-q", "origin", "main")

	var out bytes.Buffer
	require.NoError(t, lander(r, mocks.NewMockGitHub(t), &out).Sweep(context.Background(), land.Options{}))
	require.Contains(t, out.String(), "nothing to close")
	require.Contains(t, out.String(), "not closed: "+a+" is blocked by "+b+", still open\n")
}

func TestPR_SkipsAnIssueWithAnOpenBlockerAndLandsTheRest(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	done := r.Create("done")
	stuck := r.Create("stuck")
	held := r.Create("held")
	r.Run("br", "dep", "add", stuck, held, "--quiet")
	r.Run("br", "sync", "--flush-only", "--quiet")
	r.Commit("add issues")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/x")
	r.Write("feature.go", "package x\n")
	head := r.Commit("feat: x\n\nCloses: " + done + "\nCloses: " + stuck)
	r.G("push", "-q", "origin", "feat/x")

	gh := mocks.NewMockGitHub(t)
	gh.EXPECT().View(mock.Anything, "7").Return(land.PR{
		Number: 7, State: "OPEN", BaseRefName: "main", HeadRefName: "feat/x", HeadRefOid: head,
	}, nil)

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 7, land.Options{NoMerge: true}))

	r.G("fetch", "-q", "origin")
	tip := r.G("rev-parse", "origin/feat/x")
	require.Equal(t, "closed", r.Status(tip, done))
	require.Equal(t, "open", r.Status(tip, stuck))
	require.True(t, strings.HasSuffix(out.String(), "not closed: "+stuck+" is blocked by "+held+", still open\n"), out.String())
}

func planned(ids ...string) []land.Planned {
	out := make([]land.Planned, len(ids))
	for i, id := range ids {
		out[i] = land.Planned{Close: land.Close{ID: id}}
	}
	return out
}

func plannedIDs(ps []land.Planned) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

func TestOrder(t *testing.T) {
	issues, err := beadsfile.Parse(strings.NewReader(strings.Join([]string{
		`{"id":"t-a","status":"open","dependencies":[{"issue_id":"t-a","depends_on_id":"t-b","type":"blocks"}]}`,
		`{"id":"t-b","status":"open","dependencies":[{"issue_id":"t-b","depends_on_id":"t-c","type":"conditional-blocks"}]}`,
		`{"id":"t-c","status":"open"}`,
		`{"id":"t-d","status":"open","dependencies":[{"issue_id":"t-d","depends_on_id":"t-e","type":"blocks"},{"issue_id":"t-d","depends_on_id":"t-gone","type":"blocks"}]}`,
		`{"id":"t-e","status":"closed"}`,
		`{"id":"t-f","status":"open","dependencies":[{"issue_id":"t-f","depends_on_id":"t-c","type":"related"},{"issue_id":"t-f","depends_on_id":"t-h","type":"parent-child"}]}`,
		`{"id":"t-h","status":"open"}`,
		`{"id":"t-x","status":"open","dependencies":[{"issue_id":"t-x","depends_on_id":"t-y","type":"blocks"}]}`,
		`{"id":"t-y","status":"open","dependencies":[{"issue_id":"t-y","depends_on_id":"t-x","type":"waits-for"}]}`,
		`{"id":"t-z","status":"open","dependencies":[{"issue_id":"t-z","depends_on_id":"t-x","type":"blocks"}]}`,
	}, "\n") + "\n"))
	require.NoError(t, err)

	t.Run("blockers in the batch come first, ties keep their order", func(t *testing.T) {
		ordered, blocked, err := land.Order(planned("t-a", "t-d", "t-b", "t-f", "t-c"), issues)
		require.NoError(t, err)
		require.Empty(t, blocked)
		require.Equal(t, []string{"t-c", "t-b", "t-a", "t-d", "t-f"}, plannedIDs(ordered))
	})

	t.Run("a cycle is reported, and so is what waits on it", func(t *testing.T) {
		ordered, blocked, err := land.Order(planned("t-z", "t-x", "t-y", "t-d"), issues)
		require.NoError(t, err)
		require.Equal(t, []string{"t-d"}, plannedIDs(ordered))
		var why []string
		for _, b := range blocked {
			why = append(why, b.Why)
		}
		require.Equal(t, []string{
			"t-z is blocked by t-x, which this run does not close",
			"t-x is in a blocking cycle: t-x blocked by t-y blocked by t-x",
			"t-y is in a blocking cycle: t-x blocked by t-y blocked by t-x",
		}, why)
	})

	t.Run("an open blocker outside the batch blocks", func(t *testing.T) {
		ordered, blocked, err := land.Order(planned("t-a", "t-b"), issues)
		require.NoError(t, err)
		require.Empty(t, ordered)
		require.Len(t, blocked, 2)
		require.Equal(t, "t-a is blocked by t-b, which this run does not close", blocked[0].Why)
		require.Equal(t, "t-b is blocked by t-c, still open", blocked[1].Why)
	})
}
