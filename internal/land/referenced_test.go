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

// refsRepo is a PR (#4, branch feat/r) whose only commit references a task and
// its epic with Refs:, and closes nothing.
func refsRepo(t *testing.T) (r *testrepo.Repo, task, epic, head string) {
	t.Helper()
	remote := testrepo.NewRemote(t)
	r = testrepo.New(t, remote)
	epic = r.Create("epic", "--type", "epic")
	task = r.Create("task")
	r.Commit("add issues")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/r")
	r.Write("r.go", "package r\n")
	head = r.Commit("feat: r\n\nRefs: " + task + "\nRefs: " + epic)
	r.G("push", "-q", "origin", "feat/r")
	return r, task, epic, head
}

func viewPR4(t *testing.T, head string) *mocks.MockGitHub {
	gh := mocks.NewMockGitHub(t)
	gh.EXPECT().View(mock.Anything, 4).Return(land.PR{Number: 4, State: "OPEN", BaseRefName: "main", HeadRefName: "feat/r", HeadRefOid: head}, nil)
	return gh
}

func TestPR_ReferencedIssuesNeedADecision(t *testing.T) {
	r, task, epic, head := refsRepo(t)

	var out bytes.Buffer
	err := lander(r, viewPR4(t, head), &out).PR(context.Background(), 4, land.Options{})

	require.ErrorContains(t, err, "1 issue(s) the PR references would stay open")
	require.Contains(t, out.String(), "open  "+task+"  ")
	require.NotContains(t, out.String(), epic, "epics are referenced by every commit and are not asked about")
	r.G("fetch", "-q", "origin")
	require.Equal(t, head, r.G("rev-parse", "origin/feat/r"), "an undecided landing pushes nothing")

	err = lander(r, viewPR4(t, head), &bytes.Buffer{}).PR(context.Background(), 4, land.Options{DryRun: true})
	require.ErrorContains(t, err, "would stay open", "--dry-run shows the same decision")
}

func TestPR_CloseFlagClosesAReferencedIssueAtLanding(t *testing.T) {
	r, task, epic, head := refsRepo(t)
	gh := viewPR4(t, head)
	var merged string
	gh.EXPECT().Merge(mock.Anything, "4", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) { merged = sha }).Return(nil)

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 4, land.Options{Close: []string{task}}), out.String())

	r.G("fetch", "-q", "origin")
	tip := r.G("rev-parse", "origin/feat/r")
	require.Equal(t, tip, merged)
	require.Equal(t, "closed", r.Status(tip, task))
	require.Equal(t, "open", r.Status(tip, epic))
	require.Contains(t, r.G("show", tip+":.beads/issues.jsonl"), "Closed at the landing of #4 (--close)")
}

func TestPR_CloseFlagRejectsUnknownIssues(t *testing.T) {
	r, _, _, head := refsRepo(t)
	err := lander(r, viewPR4(t, head), &bytes.Buffer{}).PR(context.Background(), 4, land.Options{Close: []string{"t-nope"}})
	require.ErrorContains(t, err, "does not exist")
}

func TestPR_ClosesTrailerSettlesTheReference(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	task := r.Create("task")
	r.Commit("add")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/r")
	r.Write("a.go", "package a\n")
	r.Commit("feat: a\n\nRefs: " + task)
	r.Write("b.go", "package b\n")
	head := r.Commit("feat: b\n\nCloses: " + task)
	r.G("push", "-q", "origin", "feat/r")

	gh := viewPR4(t, head)
	gh.EXPECT().Merge(mock.Anything, "4", mock.Anything, "merge").Return(nil)
	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 4, land.Options{}), out.String())
	require.NotContains(t, out.String(), "referenced, not closed")
}

func TestPR_ReferencedIssueAlreadyClosedNeedsNoDecision(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	task := r.Create("task")
	r.Commit("add")
	r.G("push", "-q", "origin", "main")
	r.G("checkout", "-q", "-b", "feat/r")
	r.Write("a.go", "package a\n")
	r.Commit("feat: a\n\nRefs: " + task)
	r.Run("br", "close", task, "--reason", "closed on the branch", "--quiet")
	head := r.Commit("beads: close " + task)
	r.G("push", "-q", "origin", "feat/r")

	gh := viewPR4(t, head)
	gh.EXPECT().Merge(mock.Anything, "4", head, "merge").Return(nil)
	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 4, land.Options{}), out.String())
	require.NotContains(t, out.String(), "referenced, not closed")
}

func TestSweep_ReportsRecentReferencedIssues(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	task := r.Create("task")
	r.Commit("add")
	r.Write("a.go", "package a\n")
	r.Commit("feat: a\n\nRefs: " + task)
	r.G("push", "-q", "origin", "main")

	var out bytes.Buffer
	l := lander(r, mocks.NewMockGitHub(t), &out)
	require.NoError(t, l.Sweep(context.Background(), land.Options{}))
	require.Contains(t, out.String(), "open  "+task+"  ")
	require.Contains(t, out.String(), "nothing to close")

	out.Reset()
	require.NoError(t, l.Sweep(context.Background(), land.Options{Since: "2030-01-01"}))
	require.NotContains(t, out.String(), task, "--since bounds the report")
}
