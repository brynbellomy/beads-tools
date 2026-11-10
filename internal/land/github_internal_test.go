package land

import (
	stderrors "errors"
	"testing"

	"github.com/brynbellomy/go-utils/errors"
	"github.com/stretchr/testify/require"
)

func TestClassifyMergeError_MarksOnlyTheBaseBranchRace(t *testing.T) {
	ghErr := func(stderr string) error {
		return errors.With(stderrors.New("exit status 1"), "command failed").Set("cmd", "gh pr merge 3", "stderr", stderr).Err()
	}
	raced := classifyMergeError(ghErr("GraphQL: Base branch was modified. Review and try the merge again. (mergePullRequest)"))
	require.ErrorIs(t, raced, ErrBaseModified)
	require.NotErrorIs(t, classifyMergeError(ghErr("Pull request is not mergeable: the base branch policy prohibits the merge.")), ErrBaseModified)
	require.NoError(t, classifyMergeError(nil))
}

func TestMergeArgs_AdminAddsTheFlag(t *testing.T) {
	args, err := GH{}.mergeArgs("3", "abc", "squash")
	require.NoError(t, err)
	require.Equal(t, []string{"pr", "merge", "3", "--squash", "--match-head-commit", "abc"}, args)

	args, err = GH{Admin: true}.mergeArgs("3", "abc", "merge")
	require.NoError(t, err)
	require.Equal(t, []string{"pr", "merge", "3", "--merge", "--match-head-commit", "abc", "--admin"}, args)

	_, err = GH{Admin: true}.mergeArgs("3", "abc", "fast-forward")
	require.Error(t, err)
}
