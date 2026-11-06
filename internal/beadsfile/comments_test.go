package beadsfile_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
)

// commented builds a file from "id commentID..." rows.
func commented(t *testing.T, rows ...string) *beadsfile.File {
	t.Helper()
	var lines []string
	for _, row := range rows {
		f := strings.Fields(row)
		var cs []string
		for _, c := range f[1:] {
			cs = append(cs, fmt.Sprintf(`{"id":%s,"issue_id":%q,"text":"n"}`, c, f[0]))
		}
		lines = append(lines, fmt.Sprintf(`{"id":%q,"comments":[%s]}`, f[0], strings.Join(cs, ",")))
	}
	file, err := beadsfile.Parse(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	require.NoError(t, err)
	return file
}

func dupStrings(ds []beadsfile.DuplicateCommentID) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.String()
	}
	return out
}

func TestNewDuplicateCommentIDs_SplitsAddedFromInherited(t *testing.T) {
	base := commented(t, "t-a 7", "t-b 7", "t-c 8")
	branch := commented(t, "t-a 7", "t-b", "t-c 8")
	head := commented(t, "t-a 7 9", "t-b 7", "t-c 8", "t-d 8 9")

	added, inherited, err := beadsfile.NewDuplicateCommentIDs(head, base, branch)
	require.NoError(t, err)
	require.Equal(t, []string{"comment id 8 is used 2 times (t-c, t-d)", "comment id 9 is used 2 times (t-a, t-d)"}, dupStrings(added))
	require.Equal(t, []string{"comment id 7 is used 2 times (t-a, t-b)"}, dupStrings(inherited))
}

func TestNewDuplicateCommentIDs_AnotherUseOfAnInheritedIDIsAdded(t *testing.T) {
	base := commented(t, "t-a 7", "t-b 7", "t-c")

	added, inherited, err := beadsfile.NewDuplicateCommentIDs(commented(t, "t-a 7", "t-b 7", "t-c 7"), base)
	require.NoError(t, err)
	require.Equal(t, []string{"comment id 7 is used 3 times (t-a, t-b, t-c)"}, dupStrings(added))
	require.Empty(t, inherited)

	added, inherited, err = beadsfile.NewDuplicateCommentIDs(commented(t, "t-a 7", "t-b 7 7", "t-c"), base)
	require.NoError(t, err)
	require.Equal(t, []string{"comment id 7 is used 3 times (t-a, t-b, t-b)"}, dupStrings(added), "uses are counted, not only issues")
	require.Empty(t, inherited)
}

func TestNewDuplicateCommentIDs_WithoutParentsEveryDuplicateIsAdded(t *testing.T) {
	added, inherited, err := beadsfile.NewDuplicateCommentIDs(commented(t, "t-a 7", "t-b 7"))
	require.NoError(t, err)
	require.Equal(t, []string{"comment id 7 is used 2 times (t-a, t-b)"}, dupStrings(added))
	require.Empty(t, inherited)
}
