package land_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/land/mocks"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

var errBaseModified = errors.Join(land.ErrBaseModified, errors.New("GraphQL: Base branch was modified. Review and try the merge again. (mergePullRequest)"))

// closingPR pushes main with one issue and branch feat/r whose commit closes
// it. It returns the repo, the issue and the branch head.
func closingPR(t *testing.T) (*testrepo.Repo, string, string) {
	t.Helper()
	r, id, head := prRepo(t, func(r *testrepo.Repo, id string) {
		r.Write("r.go", "package r\n")
		r.Commit("feat: r\n\nCloses: " + id)
	})
	return r, id, head
}

// remoteTip reads branch on the remote without updating the clone's
// remote-tracking refs, which land must fetch itself.
func remoteTip(r *testrepo.Repo, branch string) string {
	fields := strings.Fields(r.G("ls-remote", "origin", "refs/heads/"+branch))
	require.NotEmpty(r.T, fields)
	return fields[0]
}

// viewRemote answers View with the branch tip on the remote, as GitHub would,
// in the given state.
func viewRemote(t *testing.T, gh *mocks.MockGitHub, r *testrepo.Repo, state func() string) {
	t.Helper()
	gh.EXPECT().View(mock.Anything, "3").RunAndReturn(func(context.Context, string) (land.PR, error) {
		return land.PR{Number: 3, State: state(), BaseRefName: "main", HeadRefName: "feat/g", HeadRefOid: remoteTip(r, "feat/g")}, nil
	})
}

func stateOpen() string { return "OPEN" }

func TestPR_RetriesAMergeTheBaseBranchRaced(t *testing.T) {
	r, id, head := closingPR(t)
	gh := mocks.NewMockGitHub(t)
	viewRemote(t, gh, r, stateOpen)
	var shas []string
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) { shas = append(shas, sha) }).
		Return(errBaseModified).Once()
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) { shas = append(shas, sha) }).
		Return(nil).Once()

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 3, land.Options{}), out.String())

	tip := r.G("rev-parse", "origin/feat/g")
	require.Equal(t, []string{tip, tip}, shas, "both attempts are pinned to the pushed head")
	require.Equal(t, head, r.G("rev-parse", tip+"^"), "one closing commit was pushed")
	require.Equal(t, "closed", r.Status(tip, id))
	require.Contains(t, out.String(), "attempt 1 of 4")
	require.Contains(t, out.String(), "merged #3")
}

func TestPR_StopsRetryingWhenTheBranchMoves(t *testing.T) {
	r, _, _ := closingPR(t)
	gh := mocks.NewMockGitHub(t)
	viewRemote(t, gh, r, stateOpen)
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(context.Context, string, string, string) {
			other := &testrepo.Repo{T: t, Dir: t.TempDir()}
			other.G("clone", "-q", "--branch", "feat/g", r.G("remote", "get-url", "origin"), ".")
			other.G("config", "user.email", "late@example.com")
			other.G("config", "user.name", "late")
			other.Write("late.go", "package late\n")
			other.Commit("late push")
			other.G("push", "-q", "origin", "feat/g")
		}).
		Return(errBaseModified).Once()

	err := lander(r, gh, &bytes.Buffer{}).PR(context.Background(), 3, land.Options{})
	require.ErrorContains(t, err, "moved")
}

func TestPR_AMergeThatWentThroughDespiteAnErrorIsDone(t *testing.T) {
	r, _, _ := closingPR(t)
	gh := mocks.NewMockGitHub(t)
	merged := false
	viewRemote(t, gh, r, func() string {
		if merged {
			return "MERGED"
		}
		return "OPEN"
	})
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(context.Context, string, string, string) { merged = true }).
		Return(errors.New("connection reset")).Once()

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 3, land.Options{}))
	require.Contains(t, out.String(), "merged #3")
}

func TestPR_GivesUpAfterBoundedRetries(t *testing.T) {
	r, _, _ := closingPR(t)
	gh := mocks.NewMockGitHub(t)
	viewRemote(t, gh, r, stateOpen)
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").Return(errBaseModified).Times(4)

	err := lander(r, gh, &bytes.Buffer{}).PR(context.Background(), 3, land.Options{})
	require.ErrorContains(t, err, "Base branch was modified")
}

// A rerun after a run that pushed its closing commit but failed to merge: the
// issues are closed on the branch, so it pushes nothing and merges that head.
func TestPR_RerunAfterTheClosingCommitWasPushed(t *testing.T) {
	r, id, _ := closingPR(t)
	first := mocks.NewMockGitHub(t)
	viewRemote(t, first, r, stateOpen)
	first.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").Return(errBaseModified).Times(4)
	require.Error(t, lander(r, first, &bytes.Buffer{}).PR(context.Background(), 3, land.Options{}))
	r.G("fetch", "-q", "origin")
	pushed := r.G("rev-parse", "origin/feat/g")
	require.Equal(t, "closed", r.Status(pushed, id))

	again := mocks.NewMockGitHub(t)
	viewRemote(t, again, r, stateOpen)
	again.EXPECT().Merge(mock.Anything, "3", pushed, "merge").Return(nil).Once()
	var out bytes.Buffer
	require.NoError(t, lander(r, again, &out).PR(context.Background(), 3, land.Options{}), out.String())
	require.NotContains(t, out.String(), "pushed")
	r.G("fetch", "-q", "origin")
	require.Equal(t, pushed, r.G("rev-parse", "origin/feat/g"))
}

func TestSweep_RetriesAMergeTheBaseBranchRaced(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	id := r.Create("done on main")
	r.Commit("add issue")
	r.Write("code.go", "package x\n")
	r.Commit("feat: the work\n\nCloses: " + id)
	r.G("push", "-q", "origin", "main")

	const url = "https://example.test/pr/2"
	const branch = "beads/sweep-20261010-120000"
	gh := mocks.NewMockGitHub(t)
	gh.EXPECT().Create(mock.Anything, "main", branch, "beads: close "+id, mock.Anything).Return(url, nil)
	gh.EXPECT().View(mock.Anything, url).RunAndReturn(func(context.Context, string) (land.PR, error) {
		return land.PR{State: "OPEN", BaseRefName: "main", HeadRefName: branch, HeadRefOid: remoteTip(r, branch)}, nil
	})
	var shas []string
	var moved string
	gh.EXPECT().Merge(mock.Anything, url, mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) {
			shas = append(shas, sha)
			moved = pushToMain(t, r, "other.go", "package other\n")
		}).
		Return(errBaseModified).Once()
	gh.EXPECT().Merge(mock.Anything, url, mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) { shas = append(shas, sha) }).
		Return(nil).Once()

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).Sweep(context.Background(), land.Options{}), out.String())

	require.Len(t, shas, 2)
	require.True(t, isAncestor(r, moved, shas[1]), "the retry merges a head that contains the moved base")
	require.True(t, isAncestor(r, shas[0], shas[1]), "the retried head builds on the pushed one")
	require.Equal(t, "closed", r.Status(shas[1], id))
	require.Contains(t, out.String(), "attempt 1 of 4")
	require.Contains(t, out.String(), "merged "+url)
}
