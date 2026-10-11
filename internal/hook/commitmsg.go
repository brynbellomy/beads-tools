// Package hook implements the git hooks beads-tools installs.
package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/gitx"
	"github.com/brynbellomy/beads-tools/internal/trailers"
)

// SkipEnv set to 1 skips the commit-msg check for one commit.
const SkipEnv = "BEADS_TOOLS_SKIP"

// skippedSubjects are commits git or a person generates mechanically.
var skippedSubjects = []string{"Merge ", "fixup!", "squash!", "amend!", "Revert \""}

// CommitMsg checks the commit message in msgPath. Every ID a Closes or Refs
// trailer names must exist in the working tree's issues file, so an issue
// created for this commit counts. When beadstools.requireTrailers is on (the
// default), a commit off the main branch that changes more than .beads/ must
// name at least one issue. A repository without an issues file is left alone.
func CommitMsg(ctx context.Context, repo gitx.Repo, msgPath string) error {
	if os.Getenv(SkipEnv) == "1" {
		return nil
	}
	top, err := repo.TopLevel(ctx)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(msgPath)
	if err != nil {
		return errors.With(err, "reading commit message").Set("path", msgPath).Err()
	}
	msg := string(data)
	subject := firstLine(msg)
	for _, p := range skippedSubjects {
		if strings.HasPrefix(subject, p) {
			return nil
		}
	}
	if mergeHead, err := repo.GitPath(ctx, "MERGE_HEAD"); err == nil {
		if _, err := os.Stat(mergeHead); err == nil {
			return nil
		}
	}

	path := filepath.Join(top, beadsfile.Path)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	issues, err := beadsfile.ParseFile(path)
	if err != nil {
		return errors.With(err, "reading issues file").Err()
	}

	refs, err := trailers.ParseMessage(ctx, repo, msg)
	if err != nil {
		return err
	}
	var unknown []string
	for _, id := range refs.All() {
		if issues.Get(id) == nil {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return errors.WithNew(fmt.Sprintf("commit names issues that do not exist: %s", strings.Join(unknown, ", "))).Err()
	}

	if !refs.Empty() || !repo.ConfigBool(ctx, "beadstools.requireTrailers", true) {
		return nil
	}
	branch := repo.CurrentBranch(ctx)
	if branch == "" || branch == repo.MainBranch(ctx) {
		return nil
	}
	onlyBeads, err := stagedOnlyBeads(ctx, repo)
	if err != nil {
		return err
	}
	if onlyBeads {
		return nil
	}
	return errors.WithNew(fmt.Sprintf(
		"commit on %s names no issue. Add a trailer at the end of the message:\n\n"+
			"    Closes: <issue-id>   (done once this commit reaches the main branch)\n"+
			"    Refs: <issue-id>     (progress only)\n\n"+
			"Set %s=1 to skip this check for one commit.", branch, SkipEnv)).Err()
}

func firstLine(msg string) string {
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		return strings.TrimSpace(line)
	}
	return ""
}

func stagedOnlyBeads(ctx context.Context, repo gitx.Repo) (bool, error) {
	out, err := repo.Run(ctx, "diff", "--cached", "--name-only")
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
