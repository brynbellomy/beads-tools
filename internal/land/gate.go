package land

import (
	"context"
	"fmt"
	"strings"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/gitx"
	"github.com/brynbellomy/beads-tools/internal/trailers"
)

// Unlinked is a commit that may not land as it is, and why.
type Unlinked struct {
	Commit trailers.Commit
	Why    string
}

// FindUnlinked returns the commits that break the landing rules the commit-msg
// hook enforces: a trailer may only name issues that exist in issues, and,
// when require is set, every commit must name one. Merges, reverts and commits
// that change only .beads/ need no trailer. Landing checks this itself so that
// clones without the hook cannot land unlinked work.
func FindUnlinked(ctx context.Context, repo gitx.Repo, commits []trailers.Commit, issues *beadsfile.File, require bool) ([]Unlinked, error) {
	var out []Unlinked
	for _, c := range commits {
		var missing []string
		for _, id := range c.All() {
			if issues.Get(id) == nil {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			out = append(out, Unlinked{Commit: c, Why: fmt.Sprintf("names issues that do not exist: %s", strings.Join(missing, ", "))})
			continue
		}
		if !require || !c.Empty() || len(c.Parents) > 1 || c.Reverts != "" {
			continue
		}
		onlyBeads, err := onlyTouchesBeads(ctx, repo, c.SHA)
		if err != nil {
			return nil, err
		}
		if !onlyBeads {
			out = append(out, Unlinked{Commit: c, Why: "names no issue"})
		}
	}
	return out, nil
}

func onlyTouchesBeads(ctx context.Context, repo gitx.Repo, sha string) (bool, error) {
	out, err := repo.Run(ctx, "diff-tree", "--no-commit-id", "--name-only", "-r", "--root", sha)
	if err != nil {
		return false, err
	}
	files := strings.Fields(out)
	if len(files) == 0 {
		return false, nil
	}
	for _, f := range files {
		if !strings.HasPrefix(f, ".beads/") {
			return false, nil
		}
	}
	return true, nil
}
