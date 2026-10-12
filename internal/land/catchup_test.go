package land_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/land/mocks"
	"github.com/brynbellomy/beads-tools/internal/testrepo"
)

// addComment appends a comment with id to issue in the working tree's issues
// file, as br on some branch would.
func addComment(t *testing.T, r *testrepo.Repo, issue string, id int, text string) {
	t.Helper()
	path := filepath.Join(r.Dir, ".beads", "issues.jsonl")
	f, err := beadsfile.ParseFile(path)
	require.NoError(t, err)
	iss := f.Get(issue)
	require.NotNil(t, iss)
	cs, err := beadsfile.ParseObjectArray(iss.Obj.Raw("comments"))
	require.NoError(t, err)
	c, err := beadsfile.ParseObject([]byte(fmt.Sprintf(`{"id":%d,"issue_id":%q,"author":"t","text":%q,"created_at":"2026-01-01T00:00:%02dZ"}`, id, issue, text, len(cs))))
	require.NoError(t, err)
	raw, err := beadsfile.MarshalObjectArray(append(cs, c))
	require.NoError(t, err)
	obj := iss.Obj.Clone()
	obj.SetRaw("comments", raw)
	ni, err := beadsfile.NewIssue(obj)
	require.NoError(t, err)
	for i := range f.Issues {
		if f.Issues[i].ID == issue {
			f.Issues[i] = ni
		}
	}
	require.NoError(t, f.WriteFile(path))
}

// pushToMain commits a change to main from a separate clone and pushes it, as
// another PR merging would.
func pushToMain(t *testing.T, r *testrepo.Repo, path, content string) string {
	t.Helper()
	other := &testrepo.Repo{T: t, Dir: t.TempDir()}
	other.G("clone", "-q", "--branch", "main", r.G("remote", "get-url", "origin"), ".")
	other.G("config", "user.email", "other@example.com")
	other.G("config", "user.name", "other")
	other.Write(path, content)
	sha := other.Commit("other work on main")
	other.G("push", "-q", "origin", "main")
	return sha
}

func commentIDsAt(t *testing.T, r *testrepo.Repo, rev string) (map[string][]int64, []beadsfile.DuplicateCommentID) {
	t.Helper()
	f, err := beadsfile.Parse(strings.NewReader(r.G("show", rev+":.beads/issues.jsonl") + "\n"))
	require.NoError(t, err)
	out := map[string][]int64{}
	for _, iss := range f.Issues {
		cs, err := beadsfile.ParseObjectArray(iss.Obj.Raw("comments"))
		require.NoError(t, err)
		for _, c := range cs {
			id, _ := beadsfile.CommentID(c)
			out[iss.ID] = append(out[iss.ID], id)
		}
	}
	dups, err := f.DuplicateCommentIDs()
	require.NoError(t, err)
	return out, dups
}

func isAncestor(r *testrepo.Repo, a, b string) bool {
	_, err := r.Git().Run(context.Background(), "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// The way duplicates reached main: the PR and main each add comment 50 on
// different issues, and GitHub's text merge of the issues file is clean. land
// pr merges main into the head through the driver first, so the head it
// merges already contains main and holds no duplicate.
func TestPR_BringsTheHeadUpToDateThroughTheDriver(t *testing.T) {
	remote := testrepo.NewRemote(t)
	r := testrepo.New(t, remote)
	work := r.Create("work")
	a := r.Create("a")
	b := r.Create("b")
	r.Commit("add issues")
	r.G("push", "-q", "origin", "main")

	r.G("checkout", "-q", "-b", "feat/g")
	addComment(t, r, a, 50, "from the branch")
	r.Write("g.go", "package g\n")
	head := r.Commit("feat: g\n\nCloses: " + work)
	r.G("push", "-q", "origin", "feat/g")

	r.G("checkout", "-q", "main")
	addComment(t, r, b, 50, "from main")
	mainTip := r.Commit("beads: note on b")
	r.G("push", "-q", "origin", "main")

	gh := mocks.NewMockGitHub(t)
	gh.EXPECT().View(mock.Anything, "3").Return(land.PR{Number: 3, State: "OPEN", BaseRefName: "main", HeadRefName: "feat/g", HeadRefOid: head}, nil)
	var merged string
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) { merged = sha }).Return(nil).Once()

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 3, land.Options{}), out.String())

	r.G("fetch", "-q", "origin")
	require.Equal(t, r.G("rev-parse", "origin/feat/g"), merged, "the merge is pinned to the pushed head")
	require.True(t, isAncestor(r, mainTip, merged), "the merged head contains main")
	ids, dups := commentIDsAt(t, r, merged)
	require.Empty(t, dups)
	require.Equal(t, []int64{50}, ids[a], "the branch's comment keeps its id")
	require.Equal(t, []int64{51}, ids[b], "main's comment is renumbered by the driver")
	require.Equal(t, "closed", r.Status(merged, work))
}

func TestPR_RaceBringsTheHeadUpToDateBeforeRetrying(t *testing.T) {
	r, _, _ := closingPR(t)
	gh := mocks.NewMockGitHub(t)
	viewRemote(t, gh, r, stateOpen)
	var moved string
	var shas []string
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) {
			shas = append(shas, sha)
			moved = pushToMain(t, r, "other.go", "package other\n")
		}).
		Return(errBaseModified).Once()
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) { shas = append(shas, sha) }).
		Return(nil).Once()

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 3, land.Options{}), out.String())

	require.Len(t, shas, 2)
	require.False(t, isAncestor(r, moved, shas[0]))
	require.True(t, isAncestor(r, moved, shas[1]), "the retry merges a head that contains the moved base")
	require.True(t, isAncestor(r, shas[0], shas[1]), "the retried head builds on the pushed one")
	r.G("fetch", "-q", "origin")
	require.Equal(t, shas[1], r.G("rev-parse", "origin/feat/g"))
}

func TestPR_ConflictOutsideBeadsStopsWithoutMerging(t *testing.T) {
	r, _, head := prRepo(t, func(r *testrepo.Repo, id string) {
		r.G("checkout", "-q", "main")
		r.Write("a.go", "package a\n")
		r.Commit("chore: a")
		r.G("push", "-q", "origin", "main")
		r.G("checkout", "-q", "feat/g")
		r.G("-c", "core.hooksPath=/dev/null", "merge", "-q", "--no-edit", "main")
		r.Write("a.go", "package a // branch\n")
		r.Commit("feat: a\n\nCloses: " + id)
	})
	pushToMain(t, r, "a.go", "package a // main\n")

	var out bytes.Buffer
	err := lander(r, viewOnly(t, head), &out).PR(context.Background(), 3, land.Options{})
	require.ErrorContains(t, err, "conflicts outside .beads/ (a.go)")
	r.G("fetch", "-q", "origin")
	require.Equal(t, head, r.G("rev-parse", "origin/feat/g"), "nothing is pushed")
	require.Len(t, strings.Split(r.G("worktree", "list"), "\n"), 1, "no worktree is left behind")
}

func TestPR_ClosedPullRequestEndsTheRetries(t *testing.T) {
	r, _, _ := closingPR(t)
	gh := mocks.NewMockGitHub(t)
	closed := false
	viewRemote(t, gh, r, func() string {
		if closed {
			return "CLOSED"
		}
		return "OPEN"
	})
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(context.Context, string, string, string) { closed = true }).
		Return(errBaseModified).Once()

	err := lander(r, gh, &bytes.Buffer{}).PR(context.Background(), 3, land.Options{})
	require.ErrorContains(t, err, "no longer open")
}

func TestPR_OtherMergeRefusalsAreNotRetried(t *testing.T) {
	r, _, _ := closingPR(t)
	gh := mocks.NewMockGitHub(t)
	viewRemote(t, gh, r, stateOpen)
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Return(errors.New("Required status check \"ci\" is failing")).Once()

	var out bytes.Buffer
	err := lander(r, gh, &out).PR(context.Background(), 3, land.Options{})
	require.ErrorContains(t, err, "merging pull request")
	require.NotContains(t, out.String(), "attempt")
}

func TestDriverCommand_QuotesThePath(t *testing.T) {
	require.Equal(t, `'/opt/my tools/beads-tools' merge`, land.DriverCommand("/opt/my tools/beads-tools"))
	require.Equal(t, `'/it'\''s/beads-tools' merge`, land.DriverCommand("/it's/beads-tools"))
}

// The base moves after land pushed the head and before it merges: the remote
// publishes a commit on main the moment the PR branch arrives. land reads the
// base's tip right before merging and brings the head up to date again.
func TestPR_BaseMovingAfterThePushIsCaughtUpBeforeMerging(t *testing.T) {
	r, _, head := closingPR(t)
	other := &testrepo.Repo{T: t, Dir: t.TempDir()}
	other.G("clone", "-q", "--branch", "main", r.G("remote", "get-url", "origin"), ".")
	other.G("config", "user.email", "other@example.com")
	other.G("config", "user.name", "other")
	other.Write("other.go", "package other\n")
	moved := other.Commit("other work on main")
	marker := filepath.Join(t.TempDir(), "fired")
	hook := "#!/bin/sh\nwhile read old new ref; do\n" +
		"  if [ \"$ref\" = refs/heads/feat/g ] && [ ! -e '" + marker + "' ]; then\n" +
		"    touch '" + marker + "'\n" +
		"    env -u GIT_DIR git -C '" + other.Dir + "' push -q origin HEAD:main\n" +
		"  fi\ndone\n"
	require.NoError(t, os.WriteFile(filepath.Join(r.G("remote", "get-url", "origin"), "hooks", "post-receive"), []byte(hook), 0o755))

	gh := viewOnly(t, head)
	var merged string
	gh.EXPECT().Merge(mock.Anything, "3", mock.Anything, "merge").
		Run(func(_ context.Context, _, sha, _ string) { merged = sha }).Return(nil).Once()

	var out bytes.Buffer
	require.NoError(t, lander(r, gh, &out).PR(context.Background(), 3, land.Options{}), out.String())

	require.FileExists(t, marker, "main moved after the push")
	require.True(t, isAncestor(r, moved, merged), "the merged head contains the base's new tip")
	require.Contains(t, out.String(), "main moved since the head was built")
}

func TestLand_RefusesTheRebaseMergeMethod(t *testing.T) {
	r, _, _ := closingPR(t)
	r.G("config", "beadstools.mergeMethod", "rebase")
	gh := mocks.NewMockGitHub(t)

	err := lander(r, gh, &bytes.Buffer{}).PR(context.Background(), 3, land.Options{})
	require.ErrorContains(t, err, "mergeMethod=rebase is not supported")
	err = lander(r, gh, &bytes.Buffer{}).Sweep(context.Background(), land.Options{})
	require.ErrorContains(t, err, "mergeMethod=rebase is not supported")
}
