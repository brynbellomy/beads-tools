// Package hook implements the git hooks beads-tools installs.
package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
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

// CommitMsg checks the commit message in msgPath. A commit that changes the
// issues file, merges included, must not add a duplicate comment id or a
// dependency cycle that its parents do not already hold. Every
// ID a Closes or Refs trailer names must exist in the working tree's issues
// file, so an issue created for this commit counts. When
// beadstools.requireTrailers is on (the default), a commit off the main branch
// that changes more than .beads/ must name at least one issue. A repository
// without an issues file is left alone.
func CommitMsg(ctx context.Context, repo gitx.Repo, msgPath string) error {
	if os.Getenv(SkipEnv) == "1" {
		return nil
	}
	if err := checkStagedIssues(ctx, repo); err != nil {
		return err
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

// checkStagedIssues applies the rules for the issues file to the staged
// version when the commit changes it.
func checkStagedIssues(ctx context.Context, repo gitx.Repo) error {
	out, err := repo.Run(ctx, "diff", "--cached", "--name-only", "--", beadsfile.Path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	staged, ok, err := issuesAt(ctx, repo, "")
	if err != nil || !ok {
		return err
	}
	revs, err := parentRevs(ctx, repo)
	if err != nil {
		return err
	}
	parents := make([]*beadsfile.File, 0, len(revs))
	for _, rev := range revs {
		f, ok, err := issuesAt(ctx, repo, rev)
		if err != nil {
			return err
		}
		if !ok {
			f = &beadsfile.File{}
		}
		parents = append(parents, f)
	}
	if err := checkCommentIDs(staged, parents); err != nil {
		return err
	}
	return checkCycles(staged, parents)
}

// checkCommentIDs fails when staged holds a duplicate comment id, which br
// refuses to import, that no parent already holds on the same issues.
func checkCommentIDs(staged *beadsfile.File, parents []*beadsfile.File) error {
	dups, err := staged.DuplicateCommentIDs()
	if err != nil || len(dups) == 0 {
		return err
	}
	inherited := map[string]bool{}
	for _, p := range parents {
		pdups, err := p.DuplicateCommentIDs()
		if err != nil {
			return err
		}
		for _, d := range pdups {
			inherited[duplicateKey(d)] = true
		}
	}
	var lines []string
	for _, d := range dups {
		if !inherited[duplicateKey(d)] {
			lines = append(lines, "    "+d.String())
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return errors.WithNew(fmt.Sprintf("the staged %s adds duplicate comment ids, which br refuses to import:\n%s\n"+
		"Renumber all but one comment for each id.", beadsfile.Path, strings.Join(lines, "\n"))).Err()
}

// checkCycles fails when staged holds a dependency cycle, of any edge type,
// that no parent holds; a cycle is compared by its edges.
func checkCycles(staged *beadsfile.File, parents []*beadsfile.File) error {
	cycles, err := beadsfile.NewCycles(staged, parents...)
	if err != nil || len(cycles) == 0 {
		return err
	}
	lines := make([]string, len(cycles))
	for i, c := range cycles {
		lines[i] = fmt.Sprintf("the staged %s adds a dependency cycle: %s.", beadsfile.Path, c)
	}
	fix := "Remove one edge, usually a related one, and say why in a comment on the bead."
	if len(cycles) > 1 {
		fix = "Remove one edge of each, usually a related one, and say why in a comment on the bead."
	}
	return errors.WithNew(strings.Join(lines, "\n") + " " + fix).Err()
}

// issuesAt parses the issues file at rev, or in the index when rev is "". ok
// is false when rev has no issues file.
func issuesAt(ctx context.Context, repo gitx.Repo, rev string) (*beadsfile.File, bool, error) {
	data, ok, err := repo.Show(ctx, rev, beadsfile.Path)
	if err != nil || !ok {
		return nil, false, err
	}
	issues, err := beadsfile.Parse(strings.NewReader(data))
	if err != nil {
		return nil, false, errors.With(err, "reading issues file").Set("rev", rev).Err()
	}
	return issues, true, nil
}

// parentRevs returns the commits the commit being made will have as parents:
// HEAD, when there is one, and each MERGE_HEAD of a merge in progress.
func parentRevs(ctx context.Context, repo gitx.Repo) ([]string, error) {
	var revs []string
	if _, err := repo.Run(ctx, "rev-parse", "--verify", "-q", "HEAD"); err == nil {
		revs = append(revs, "HEAD")
	}
	path, err := repo.GitPath(ctx, "MERGE_HEAD")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return revs, nil
	}
	if err != nil {
		return nil, errors.With(err, "reading MERGE_HEAD").Err()
	}
	return append(revs, strings.Fields(string(data))...), nil
}

// duplicateKey names a duplicate by its id and the issues that share it.
func duplicateKey(d beadsfile.DuplicateCommentID) string {
	issues := slices.Clone(d.Issues)
	slices.Sort(issues)
	return strconv.FormatInt(d.ID, 10) + ":" + strings.Join(issues, ",")
}
