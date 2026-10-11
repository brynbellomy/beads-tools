package land_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/land/mocks"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
	"github.com/brynbellomy/beads-tools/internal/trailers"
)

func lander(r *testrepo.Repo, gh land.GitHub, out *bytes.Buffer) *land.Lander {
	return &land.Lander{
		Repo:   r.Git(),
		GitHub: gh,
		Beads:  land.BR{},
		Out:    out,
		Now:    func() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) },
	}
}

func TestSweep_ClosesOnlyIssuesWhoseClosingCommitIsOnMain(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	done := r.Create("done on main")
	other := r.Create("only referenced")
	untouched := r.Create("untouched")
	base := r.Commit("add issues")
	r.Write("code.go", "package x\n")
	r.Commit("feat: the work\n\nCloses: " + done + "\nRefs: " + other)
	r.G("push", "-q", "origin", "main")

	gh := mocks.NewMockGitHub(t)
	var pushedSHA string
	gh.EXPECT().Create(mock.Anything, "main", "beads/sweep-20261010-120000", "beads: close "+done, mock.Anything).
		Return("https://example.test/pr/1", nil)
	gh.EXPECT().Merge(mock.Anything, "https://example.test/pr/1", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) {
			pushedSHA = sha
			r.G("fetch", "-q", "origin")
			require.Equal(t, sha, r.G("rev-parse", "origin/beads/sweep-20261010-120000"), "merge is pinned to the pushed head")
		}).Return(nil)

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).Sweep(context.Background(), land.Options{}))

	require.Equal(t, "closed", r.Status(pushedSHA, done))
	require.Equal(t, "open", r.Status(pushedSHA, other))
	require.Equal(t, "open", r.Status(pushedSHA, untouched))
	require.Equal(t, []string{done}, r.ChangedRows(base, pushedSHA), "only the closed issue's row changes")
	require.Equal(t, []string{".beads/issues.jsonl"}, strings.Fields(r.G("diff", "--name-only", "origin/main", pushedSHA)))
	require.Contains(t, r.G("log", "-1", "--format=%B", pushedSHA), "Refs: "+done)

	r.G("fetch", "-q", "--prune", "origin")
	require.Empty(t, r.G("branch", "-r", "--list", "origin/beads/*"), "the sweep branch is deleted after merging")
}

func TestSweep_NothingToCloseOpensNothing(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	id := r.Create("x")
	r.Commit("add\n\nRefs: " + id)
	r.G("push", "-q", "origin", "main")

	var out bytes.Buffer
	require.NoError(t, lander(r, mocks.NewMockGitHub(t), &out).Sweep(context.Background(), land.Options{}))
	require.Contains(t, out.String(), "nothing to close")
}

func TestSweep_DryRunChangesNothing(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	id := r.Create("x")
	r.Commit("work\n\nCloses: " + id)
	r.G("push", "-q", "origin", "main")
	before := r.G("ls-remote", remote)

	var out bytes.Buffer
	require.NoError(t, lander(r, mocks.NewMockGitHub(t), &out).Sweep(context.Background(), land.Options{DryRun: true}))
	require.Contains(t, out.String(), "close "+id)
	require.Equal(t, before, r.G("ls-remote", remote))
	require.Len(t, strings.Split(r.G("worktree", "list"), "\n"), 1, "no worktree is left behind")
}

func TestPR_ClosesOnTheBranchThenMergesThatHead(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	target := r.Create("feature issue")
	bystander := r.Create("bystander")
	r.Commit("add issues")
	r.G("push", "-q", "origin", "main")

	r.G("checkout", "-q", "-b", "feat/x")
	r.Write("feature.go", "package x\n")
	head := r.Commit("feat: x\n\nCloses: " + target)
	r.G("push", "-q", "origin", "feat/x")

	gh := mocks.NewMockGitHub(t)
	gh.EXPECT().View(mock.Anything, 7).Return(land.PR{
		Number: 7, State: "OPEN", BaseRefName: "main", HeadRefName: "feat/x", HeadRefOid: head,
	}, nil)
	var merged string
	gh.EXPECT().Merge(mock.Anything, "7", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) { merged = sha }).Return(nil)

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 7, land.Options{}))

	r.G("fetch", "-q", "origin")
	tip := r.G("rev-parse", "origin/feat/x")
	require.Equal(t, tip, merged, "the merge is pinned to the close commit")
	require.Equal(t, head, r.G("rev-parse", tip+"^"), "the close commit sits directly on the PR head")
	require.Equal(t, "closed", r.Status(tip, target))
	require.Equal(t, "open", r.Status(tip, bystander))
	require.Equal(t, []string{target}, r.ChangedRows(head, tip))
	require.Contains(t, r.G("log", "-1", "--format=%B", tip), "Closed by #7")
}

func TestPR_KeepOpenMergesTheHeadAsIs(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	id := r.Create("progress")
	r.Commit("add issue")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/y")
	r.Write("y.go", "package y\n")
	head := r.Commit("chore: y\n\nRefs: " + id)
	r.G("push", "-q", "origin", "feat/y")

	gh := mocks.NewMockGitHub(t)
	gh.EXPECT().View(mock.Anything, 8).Return(land.PR{Number: 8, State: "OPEN", BaseRefName: "main", HeadRefName: "feat/y", HeadRefOid: head}, nil)
	gh.EXPECT().Merge(mock.Anything, "8", head, "merge").Return(nil)

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 8, land.Options{KeepOpen: true}))
}

func TestPR_RefusesWhenTheBranchMoved(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	r.G("checkout", "-q", "-b", "feat/z")
	r.Write("z.go", "package z\n")
	stale := r.Commit("one")
	r.Write("z.go", "package z // two\n")
	r.Commit("two")
	r.G("push", "-q", "origin", "feat/z")

	gh := mocks.NewMockGitHub(t)
	gh.EXPECT().View(mock.Anything, 9).Return(land.PR{Number: 9, State: "OPEN", BaseRefName: "main", HeadRefName: "feat/z", HeadRefOid: stale}, nil)

	err := lander(r, gh, &bytes.Buffer{}).PR(context.Background(), 9, land.Options{})
	require.ErrorContains(t, err, "moved")
}

func TestPlan_RevertedCommitClosesNothing(t *testing.T) {
	issues, err := beadsfile.Parse(strings.NewReader(
		`{"id":"t-a","status":"open"}` + "\n" + `{"id":"t-b","status":"open"}` + "\n" + `{"id":"t-c","status":"closed"}` + "\n"))
	require.NoError(t, err)
	commits := []trailers.Commit{ // newest first
		{SHA: "ccc333", Subject: "Revert \"a\"", Reverts: "aaa111"},
		{SHA: "bbb222", Subject: "b", Refs: trailers.Refs{Closes: []string{"t-b", "t-c", "t-missing"}}},
		{SHA: "aaa111", Subject: "a", Refs: trailers.Refs{Closes: []string{"t-a"}}},
	}
	planned, skipped := land.Plan(commits, issues, func(c trailers.Commit) string { return c.SHA })

	require.Len(t, planned, 1)
	require.Equal(t, "t-b", planned[0].ID)
	require.Equal(t, "bbb222", planned[0].Reason)
	var why []string
	for _, s := range skipped {
		why = append(why, s.ID+": "+s.Why)
	}
	require.ElementsMatch(t, []string{"t-a: the closing commit was reverted", "t-missing: no such issue"}, why)
}

func TestPlan_CreditsTheOldestClosingCommit(t *testing.T) {
	issues, err := beadsfile.Parse(strings.NewReader(`{"id":"t-a","status":"open"}` + "\n"))
	require.NoError(t, err)
	commits := []trailers.Commit{
		{SHA: "new", Refs: trailers.Refs{Closes: []string{"t-a"}}},
		{SHA: "old", Refs: trailers.Refs{Closes: []string{"t-a"}}},
	}
	planned, _ := land.Plan(commits, issues, func(c trailers.Commit) string { return c.SHA })
	require.Equal(t, "old", planned[0].Reason)
}
