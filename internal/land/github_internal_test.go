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
