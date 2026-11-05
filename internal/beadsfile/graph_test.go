package beadsfile_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
)

// issues builds a file from "id status from>to:type ..." rows, where each
// dependency is stored on its issue as br stores it.
func issues(t *testing.T, rows ...string) *beadsfile.File {
	t.Helper()
	var lines []string
	for _, row := range rows {
		f := strings.Fields(row)
		var deps []string
		for _, d := range f[2:] {
			to, typ, _ := strings.Cut(d, ":")
			deps = append(deps, fmt.Sprintf(`{"issue_id":%q,"depends_on_id":%q,"type":%q}`, f[0], to, typ))
		}
		lines = append(lines, fmt.Sprintf(`{"id":%q,"status":%q,"dependencies":[%s]}`, f[0], f[1], strings.Join(deps, ",")))
	}
	file, err := beadsfile.Parse(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	require.NoError(t, err)
	return file
}

func cycleStrings(cs []beadsfile.Cycle) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.String()
	}
	return out
}

func TestNewCycles_NamesARelatedEdgeThatClosesACycle(t *testing.T) {
	base := issues(t, "t-a open t-b:related", "t-b open t-c:blocks", "t-c open")
	head := issues(t, "t-a open t-b:related", "t-b open t-c:blocks", "t-c open t-a:related")

	cs, err := beadsfile.NewCycles(head, base)
	require.NoError(t, err)
	require.Equal(t, []string{"t-c related to t-a, t-a related to t-b, t-b waits on t-c (blocks)"}, cycleStrings(cs))
	require.Equal(t, []string{"t-c", "t-a", "t-b", "t-c"}, cs[0].IDs())
}

func TestNewCycles_WalksParentChildFromParentToChild(t *testing.T) {
	// The child stores its parent as a dependency; br dep cycles walks it from
	// the parent, so the child's related edge to a bead related to the parent
	// closes a cycle.
	head := issues(t, "t-epic open t-x:related", "t-kid open t-epic:parent-child t-y:related", "t-x open", "t-y open t-epic:related")
	cs, err := beadsfile.NewCycles(head)
	require.NoError(t, err)
	require.Equal(t, []string{"t-epic parent of t-kid, t-kid related to t-y, t-y related to t-epic"}, cycleStrings(cs))

	stored := issues(t, "t-epic open t-z:related", "t-kid open t-epic:parent-child", "t-z open t-kid:related")
	cs, err = beadsfile.NewCycles(stored)
	require.NoError(t, err)
	require.Empty(t, cs, "a parent-child edge does not run from the child to the parent")
}

func TestNewCycles_CountsClosedBeads(t *testing.T) {
	head := issues(t, "t-a closed t-b:waits-for", "t-b tombstone t-a:discovered-from")
	cs, err := beadsfile.NewCycles(head)
	require.NoError(t, err)
	require.Equal(t, []string{"t-a waits on t-b (waits-for), t-b depends on t-a (discovered-from)"}, cycleStrings(cs))
}

func TestNewCycles_InheritedCyclesPass(t *testing.T) {
	base := issues(t, "t-a open t-b:related", "t-b open t-a:blocks", "t-c open")

	edited := issues(t, "t-a closed t-b:related t-c:related", "t-b open t-a:blocks", "t-c open")
	cs, err := beadsfile.NewCycles(edited, base)
	require.NoError(t, err)
	require.Empty(t, cs, "a status and an unrelated edge on a cycle's bead do not make the cycle new")

	closing := issues(t, "t-a open t-b:related t-c:related", "t-b open t-a:blocks", "t-c open t-b:related")
	cs, err = beadsfile.NewCycles(closing, base)
	require.NoError(t, err)
	require.Equal(t, []string{"t-a related to t-c, t-c related to t-b, t-b waits on t-a (blocks)"}, cycleStrings(cs))

	parallel := issues(t, "t-a open t-b:related t-b:blocks", "t-b open t-a:blocks", "t-c open")
	cs, err = beadsfile.NewCycles(parallel, base)
	require.NoError(t, err)
	require.Equal(t, []string{"t-a waits on t-b (blocks), t-b waits on t-a (blocks)"}, cycleStrings(cs), "cycles are compared by their edges")
}

func TestNewCycles_EachParentHoldsItsOwnCycles(t *testing.T) {
	ours := issues(t, "t-a open t-b:related", "t-b open", "t-c open t-d:related", "t-d open t-c:related")
	theirs := issues(t, "t-a open", "t-b open t-a:related", "t-c open", "t-d open")

	inherit := issues(t, "t-a open", "t-b open t-a:related", "t-c open t-d:related", "t-d open t-c:related")
	cs, err := beadsfile.NewCycles(inherit, ours, theirs)
	require.NoError(t, err)
	require.Empty(t, cs)

	merged := issues(t, "t-a open t-b:related", "t-b open t-a:related", "t-c open t-d:related", "t-d open t-c:related")
	cs, err = beadsfile.NewCycles(merged, ours, theirs)
	require.NoError(t, err)
	require.Equal(t, []string{"t-a related to t-b, t-b related to t-a"}, cycleStrings(cs), "a cycle no single parent holds is new")
}

func TestGraph_CycleAt(t *testing.T) {
	g, err := beadsfile.NewGraph(issues(t, "t-x open t-y:blocks", "t-y open t-x:waits-for", "t-z open t-x:blocks"), func(e beadsfile.Edge) bool {
		return beadsfile.BlockingTypes[e.Type]
	})
	require.NoError(t, err)
	require.Equal(t, []string{"t-x", "t-y", "t-x"}, g.CycleAt("t-x").IDs())
	require.Nil(t, g.CycleAt("t-z"))
}
