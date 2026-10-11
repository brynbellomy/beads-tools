package land

import (
	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/trailers"
)

// Referenced is an issue commits name with Refs: that nothing closes, so it
// stays open after they land. It is credited to the newest commit naming it.
type Referenced struct {
	ID     string
	Commit trailers.Commit
}

// ReferencedOpen returns the issues the commits reference but that are still
// open in issues and not in closing. Epics are left out: every commit
// references its epic, and an epic closes when its children are done. A
// reverted commit references nothing.
func ReferencedOpen(commits []trailers.Commit, issues *beadsfile.File, closing map[string]bool) []Referenced {
	isReverted := revertedBy(commits)
	var out []Referenced
	seen := map[string]bool{}
	for _, c := range commits {
		if isReverted(c.SHA) {
			continue
		}
		for _, id := range c.Refs.Refs {
			if seen[id] || closing[id] {
				continue
			}
			seen[id] = true
			iss := issues.Get(id)
			if iss == nil || IsClosed(iss) {
				continue
			}
			if t, _ := iss.Obj.String("issue_type"); t == "epic" {
				continue
			}
			out = append(out, Referenced{ID: id, Commit: c})
		}
	}
	return out
}
