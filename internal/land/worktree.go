package land

import (
	"context"
	"os"
	"strings"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/gitx"
)

// DriverCommand is the merge driver command for the beads-tools binary at bin,
// quoted for the shell git runs it with, without the driver's arguments.
func DriverCommand(bin string) string {
	return "'" + strings.ReplaceAll(bin, "'", `'\''`) + "' merge"
}

// worktree is a throwaway checkout in which land builds a pull request's new
// head, so the operator's checkout never changes.
type worktree struct {
	git gitx.Repo
}

// openWorktree checks rev out, detached, in a fresh temporary directory.
func (l *Lander) openWorktree(ctx context.Context, rev string) (*worktree, error) {
	dir, err := os.MkdirTemp("", "beads-tools-land-*")
	if err != nil {
		return nil, errors.With(err, "creating worktree directory").Err()
	}
	os.Remove(dir)
	if _, err := l.Repo.Run(ctx, "worktree", "add", "--detach", dir, rev); err != nil {
		return nil, err
	}
	return &worktree{git: gitx.Repo{Dir: dir}}, nil
}

func (l *Lander) closeWorktree(ctx context.Context, w *worktree) {
	if _, err := l.Repo.Run(context.WithoutCancel(ctx), "worktree", "remove", "--force", w.git.Dir); err != nil {
		l.printf("warning: could not remove worktree %s: %v\n", w.git.Dir, err)
	}
}

func (w *worktree) head(ctx context.Context) (string, error) {
	out, err := w.git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// catchUp merges ref into HEAD unless HEAD already contains it. git runs
// driver as the merge driver for the issues file, whatever the repository's
// own configuration says, so the merge never leaves a duplicate comment id. A
// conflict aborts the merge and is returned.
func (w *worktree) catchUp(ctx context.Context, ref, driver, message string) error {
	behind, err := w.git.Run(ctx, "rev-list", "--count", "HEAD.."+ref)
	if err != nil {
		return err
	}
	if strings.TrimSpace(behind) == "0" {
		return nil
	}
	if driver == "" {
		return errors.WithNew("no merge driver configured to bring the branch up to date").Err()
	}
	attrs := w.git.Dir + ".attributes"
	if err := os.WriteFile(attrs, []byte(beadsfile.Path+" merge=beads\n"), 0o644); err != nil {
		return errors.With(err, "writing attributes file").Err()
	}
	defer os.Remove(attrs)
	_, mergeErr := w.git.Run(ctx,
		"-c", "core.attributesFile="+attrs,
		"-c", "merge.beads.name=beads-tools",
		"-c", "merge.beads.driver="+driver+" %O %A %B %P",
		"merge", "--no-ff", "--no-edit", "-m", message, ref)
	if mergeErr == nil {
		return nil
	}
	unmerged, err := w.git.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return errors.With(mergeErr, "merging the base branch").Set("ref", ref).Err()
	}
	if _, err := w.git.Run(ctx, "merge", "--abort"); err != nil {
		return errors.With(mergeErr, "merging the base branch; the merge could not be aborted").Set("ref", ref, "abort_error", err.Error()).Err()
	}
	var outside []string
	for _, p := range strings.Fields(unmerged) {
		if !strings.HasPrefix(p, ".beads/") {
			outside = append(outside, p)
		}
	}
	if len(outside) > 0 {
		return errors.WithNew("merging " + ref + " into the branch conflicts outside .beads/ (" + strings.Join(outside, ", ") +
			"); merge it by hand, push, and land again").Err()
	}
	return errors.With(mergeErr, "merging the base branch").Set("ref", ref).Err()
}

// commitCloses closes the planned issues in the worktree and commits only the
// issues file.
func (l *Lander) commitCloses(ctx context.Context, w *worktree, planned []Planned) error {
	closes := make([]Close, len(planned))
	for i, p := range planned {
		closes[i] = p.Close
	}
	changed, err := CloseInTree(ctx, w.git.Dir, closes, l.Beads)
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		return errors.WithNew("nothing changed in the issues file").Err()
	}
	if _, err := w.git.Run(ctx, "add", "--", beadsfile.Path); err != nil {
		return err
	}
	_, err = w.git.RunInput(ctx, commitMessage(planned), "commit", "--file=-", "--", beadsfile.Path)
	return err
}
