// Package land closes issues when the work that closes them reaches the main
// branch. The link is the "Closes:" trailer on the commits themselves, so no
// state is kept anywhere: an issue that is already closed is the watermark.
package land

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/gitx"
	"github.com/brynbellomy/beads-tools/internal/trailers"
)

// Lander publishes issue closes through pull requests.
type Lander struct {
	Repo   gitx.Repo
	GitHub GitHub
	Beads  Beads
	Out    io.Writer
	Now    func() time.Time
	// MergeRetryDelay is the wait before the first retry of a refused merge;
	// each later retry waits one delay longer.
	MergeRetryDelay time.Duration
}

// mergeAttempts bounds how often pr tries to merge a head it pushed.
const mergeAttempts = 4

// DefaultSince is how far back sweep reports referenced issues that are still open.
const DefaultSince = "2 weeks ago"

// Options shape one landing run.
type Options struct {
	// DryRun prints the plan and changes nothing.
	DryRun bool
	// NoMerge opens the pull request but leaves merging to a person.
	NoMerge bool
	// Close names issues a pull request finishes but only references; pr
	// closes them in its landing commit.
	Close []string
	// KeepOpen confirms that the issues a pull request references and does
	// not close stay open.
	KeepOpen bool
	// TrailersOptional lets pr land commits that name no issue. Trailers that
	// name a missing issue still refuse the landing.
	TrailersOptional bool
	// Since bounds how far back sweep looks for referenced issues that are
	// still open, in any form git log --since accepts.
	Since string
}

// Planned is an issue a landing run will close.
type Planned struct {
	Close
	Commit trailers.Commit
}

// Skipped is a Closes trailer a landing run leaves alone, and why.
type Skipped struct {
	ID     string
	Commit trailers.Commit
	Why    string
}

// Plan works out which issues named by Closes trailers in commits still need
// closing in issues. Commits come newest first; each issue is credited to the
// oldest commit that closes it. A reverted commit closes nothing.
func Plan(commits []trailers.Commit, issues *beadsfile.File, reason func(trailers.Commit) string) ([]Planned, []Skipped) {
	isReverted := revertedBy(commits)
	var planned []Planned
	var skipped []Skipped
	seen := map[string]bool{}
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		for _, id := range c.Closes {
			if seen[id] {
				continue
			}
			seen[id] = true
			iss := issues.Get(id)
			switch {
			case isReverted(c.SHA):
				skipped = append(skipped, Skipped{ID: id, Commit: c, Why: "the closing commit was reverted"})
			case iss == nil:
				skipped = append(skipped, Skipped{ID: id, Commit: c, Why: "no such issue"})
			case IsClosed(iss):
			default:
				planned = append(planned, Planned{Close: Close{ID: id, Reason: reason(c)}, Commit: c})
			}
		}
	}
	return planned, skipped
}

// revertedBy reports whether a commit is reverted by one of commits, going by
// git's standard "This reverts commit <sha>." line.
func revertedBy(commits []trailers.Commit) func(sha string) bool {
	var reverted []string
	for _, c := range commits {
		if c.Reverts != "" {
			reverted = append(reverted, c.Reverts)
		}
	}
	return func(sha string) bool {
		for _, r := range reverted {
			if strings.HasPrefix(sha, r) {
				return true
			}
		}
		return false
	}
}

func (l *Lander) reportReferenced(refs []Referenced) {
	for _, r := range refs {
		l.printf("open  %s  (%s %s): referenced, not closed\n", r.ID, short(r.Commit.SHA), r.Commit.Subject)
	}
}

func (l *Lander) printf(format string, args ...any) {
	if l.Out != nil {
		fmt.Fprintf(l.Out, format, args...)
	}
}

func (l *Lander) report(planned []Planned, skipped []Skipped) {
	for _, p := range planned {
		l.printf("close %s  (%s %s)\n", p.ID, short(p.Commit.SHA), p.Commit.Subject)
	}
	for _, s := range skipped {
		l.printf("skip  %s  (%s %s): %s\n", s.ID, short(s.Commit.SHA), s.Commit.Subject, s.Why)
	}
}

func short(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	return sha
}

func (l *Lander) mergeMethod(ctx context.Context) string {
	return l.Repo.Config(ctx, "beadstools.mergeMethod", "merge")
}

// Sweep closes every issue a commit on the main branch closes but that is still
// open there, through a pull request it merges itself.
func (l *Lander) Sweep(ctx context.Context, opts Options) error {
	remote, main := l.Repo.Remote(ctx), l.Repo.MainBranch(ctx)
	if _, err := l.Repo.Run(ctx, "fetch", remote, main); err != nil {
		return err
	}
	ref := remote + "/" + main
	commits, err := trailers.Log(ctx, l.Repo, ref)
	if err != nil {
		return err
	}
	issues, err := issuesAt(ctx, l.Repo, ref)
	if err != nil {
		return err
	}
	planned, skipped := Plan(commits, issues, func(c trailers.Commit) string {
		return fmt.Sprintf("Closed by %s (%s)", short(c.SHA), c.Subject)
	})
	l.report(planned, skipped)
	since := opts.Since
	if since == "" {
		since = DefaultSince
	}
	recent, err := trailers.Log(ctx, l.Repo, "--since="+since, ref)
	if err != nil {
		return err
	}
	closing := map[string]bool{}
	for _, p := range planned {
		closing[p.ID] = true
	}
	if refs := ReferencedOpen(recent, issues, closing); len(refs) > 0 {
		l.printf("referenced since %s but still open (close them by hand if their work is done):\n", since)
		l.reportReferenced(refs)
	}
	if len(planned) == 0 {
		l.printf("nothing to close on %s\n", ref)
		return nil
	}
	if opts.DryRun {
		return nil
	}

	branch := "beads/sweep-" + l.Now().UTC().Format("20060102-150405")
	sha, err := l.commitCloses(ctx, ref, planned)
	if err != nil {
		return err
	}
	if _, err := l.Repo.Run(ctx, "push", remote, sha+":refs/heads/"+branch); err != nil {
		return err
	}
	url, err := l.GitHub.Create(ctx, main, branch, title(planned), body(planned))
	if err != nil {
		return err
	}
	l.printf("opened %s\n", url)
	if opts.NoMerge {
		return nil
	}
	if err := l.mergePushedHead(ctx, url, sha); err != nil {
		return err
	}
	l.printf("merged %s\n", url)
	if _, err := l.Repo.Run(ctx, "push", remote, "--delete", branch); err != nil {
		l.printf("warning: could not delete %s on %s: %v\n", branch, remote, err)
	}
	return nil
}

// PR lands pull request number: it closes the issues the PR's commits close in
// one commit on the PR's own branch, then merges the PR only if that commit is
// still its head. The work and its closes reach the main branch together.
func (l *Lander) PR(ctx context.Context, number int, opts Options) error {
	pr, err := l.GitHub.View(ctx, strconv.Itoa(number))
	if err != nil {
		return err
	}
	switch {
	case pr.State != "OPEN":
		return errors.WithNew("pull request is not open").Set("number", number, "state", pr.State).Err()
	case pr.IsCrossRepository:
		return errors.WithNew("pull request comes from a fork; its branch cannot be pushed to").Set("number", number).Err()
	}
	remote := l.Repo.Remote(ctx)
	if _, err := l.Repo.Run(ctx, "fetch", remote, pr.BaseRefName, pr.HeadRefName); err != nil {
		return err
	}
	head, err := l.Repo.Run(ctx, "rev-parse", remote+"/"+pr.HeadRefName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != pr.HeadRefOid {
		return errors.WithNew("the branch moved while reading the pull request; retry").
			Set("branch", pr.HeadRefName, "github", pr.HeadRefOid, "remote", strings.TrimSpace(head)).Err()
	}
	commits, err := trailers.Log(ctx, l.Repo, remote+"/"+pr.BaseRefName+".."+pr.HeadRefOid)
	if err != nil {
		return err
	}
	issues, err := issuesAt(ctx, l.Repo, pr.HeadRefOid)
	if err != nil {
		return err
	}
	unlinked, err := FindUnlinked(ctx, l.Repo, commits, issues, !opts.TrailersOptional)
	if err != nil {
		return err
	}
	if err := l.refuseDuplicateCommentIDs(issues, pr.HeadRefName); err != nil {
		return err
	}
	if len(unlinked) > 0 {
		for _, u := range unlinked {
			l.printf("refuse %s %s: %s\n", short(u.Commit.SHA), u.Commit.Subject, u.Why)
		}
		return errors.WithNew(fmt.Sprintf("%d commit(s) on %s do not name a valid issue; reword them "+
			"(git rebase -i %s/%s, then reword) to end with a Closes: or Refs: trailer, push, and land again",
			len(unlinked), pr.HeadRefName, remote, pr.BaseRefName)).Err()
	}
	planned, skipped := Plan(commits, issues, func(c trailers.Commit) string {
		return fmt.Sprintf("Closed by #%d (%s %s)", number, short(c.SHA), c.Subject)
	})
	closing := map[string]bool{}
	for _, p := range planned {
		closing[p.ID] = true
	}
	for _, id := range opts.Close {
		iss := issues.Get(id)
		switch {
		case iss == nil:
			return errors.WithNew("--close names an issue that does not exist").Set("id", id).Err()
		case IsClosed(iss) || closing[id]:
			continue
		}
		closing[id] = true
		planned = append(planned, Planned{
			Close:  Close{ID: id, Reason: fmt.Sprintf("Closed at the landing of #%d (--close)", number)},
			Commit: trailers.Commit{Subject: "--close at landing"},
		})
	}
	l.report(planned, skipped)
	refs := ReferencedOpen(commits, issues, closing)
	l.reportReferenced(refs)
	if len(refs) > 0 && !opts.KeepOpen {
		return errors.WithNew(fmt.Sprintf("%d issue(s) the PR references would stay open; pass --close <id> for each "+
			"one it finishes, or --keep-open if it finishes none of them", len(refs))).Err()
	}
	if opts.DryRun {
		return nil
	}

	sha := pr.HeadRefOid
	if len(planned) > 0 {
		sha, err = l.commitCloses(ctx, pr.HeadRefOid, planned)
		if err != nil {
			return err
		}
		// A plain push: if anyone pushed to the branch meanwhile, it fails
		// rather than discarding their commit.
		if _, err := l.Repo.Run(ctx, "push", remote, sha+":refs/heads/"+pr.HeadRefName); err != nil {
			return err
		}
		l.printf("pushed %s to %s\n", short(sha), pr.HeadRefName)
	}
	if opts.NoMerge {
		return nil
	}
	if err := l.mergePushedHead(ctx, strconv.Itoa(number), sha); err != nil {
		return err
	}
	l.printf("merged #%d\n", number)
	return nil
}

// mergePushedHead merges pull request ref (a number or URL) pinned to sha.
// GitHub refuses a merge while the base branch moves under it, so a refusal
// re-reads the pull request and retries, a bounded number of times, while sha
// is still its head.
func (l *Lander) mergePushedHead(ctx context.Context, ref, sha string) error {
	method := l.mergeMethod(ctx)
	var mergeErr error
	for attempt := 1; attempt <= mergeAttempts; attempt++ {
		if attempt > 1 {
			if err := pause(ctx, time.Duration(attempt-1)*l.MergeRetryDelay); err != nil {
				return errors.With(err, "waiting to retry the merge").Err()
			}
		}
		mergeErr = l.GitHub.Merge(ctx, ref, sha, method)
		if mergeErr == nil {
			return nil
		}
		pr, err := l.GitHub.View(ctx, ref)
		if err != nil {
			return errors.With(mergeErr, "merging pull request").Set("pr", ref, "view_error", err.Error()).Err()
		}
		switch {
		case pr.HeadRefOid != sha:
			return errors.With(mergeErr, "the branch moved after its head was pushed; land again").
				Set("pr", ref, "pushed", sha, "head", pr.HeadRefOid).Err()
		case pr.State == "MERGED":
			return nil
		case pr.State != "OPEN":
			return errors.With(mergeErr, "pull request is no longer open").Set("pr", ref, "state", pr.State).Err()
		}
		l.printf("merge of %s refused (attempt %d of %d): %v\n", ref, attempt, mergeAttempts, mergeErr)
	}
	return errors.With(mergeErr, "merging pull request").Set("pr", ref, "attempts", mergeAttempts).Err()
}

func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// refuseDuplicateCommentIDs fails when issues holds a comment id more than
// once, which br refuses to import.
func (l *Lander) refuseDuplicateCommentIDs(issues *beadsfile.File, where string) error {
	dups, err := issues.DuplicateCommentIDs()
	if err != nil {
		return err
	}
	if len(dups) == 0 {
		return nil
	}
	for _, d := range dups {
		l.printf("refuse %s: %s\n", where, d)
	}
	return errors.WithNew(fmt.Sprintf("the issues file on %s holds %d duplicate comment id(s); br refuses to import it. "+
		"Renumber all but one comment for each id (the merge driver does this when it merges the branch again) and push", where, len(dups))).Err()
}

// issuesAt parses the issues file as of rev.
func issuesAt(ctx context.Context, repo gitx.Repo, rev string) (*beadsfile.File, error) {
	data, ok, err := repo.Show(ctx, rev, beadsfile.Path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.WithNew("no issues file at revision").Set("rev", rev, "path", beadsfile.Path).Err()
	}
	return beadsfile.Parse(strings.NewReader(data))
}

// commitCloses checks rev out in a throwaway worktree, closes the planned
// issues there and commits only the issues file. It returns the new commit.
func (l *Lander) commitCloses(ctx context.Context, rev string, planned []Planned) (string, error) {
	dir, err := os.MkdirTemp("", "beads-tools-land-*")
	if err != nil {
		return "", errors.With(err, "creating worktree directory").Err()
	}
	os.Remove(dir)
	if _, err := l.Repo.Run(ctx, "worktree", "add", "--detach", dir, rev); err != nil {
		return "", err
	}
	defer func() {
		if _, err := l.Repo.Run(context.WithoutCancel(ctx), "worktree", "remove", "--force", dir); err != nil {
			l.printf("warning: could not remove worktree %s: %v\n", dir, err)
		}
	}()
	tree := gitx.Repo{Dir: dir}
	closes := make([]Close, len(planned))
	for i, p := range planned {
		closes[i] = p.Close
	}
	changed, err := CloseInTree(ctx, dir, closes, l.Beads)
	if err != nil {
		return "", err
	}
	if len(changed) == 0 {
		return "", errors.WithNew("nothing changed in the issues file").Err()
	}
	if _, err := tree.Run(ctx, "add", "--", beadsfile.Path); err != nil {
		return "", err
	}
	if _, err := tree.RunInput(ctx, commitMessage(planned), "commit", "--file=-", "--", beadsfile.Path); err != nil {
		return "", err
	}
	out, err := tree.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func title(planned []Planned) string {
	if len(planned) == 1 {
		return "beads: close " + planned[0].ID
	}
	return fmt.Sprintf("beads: close %d issues merged to main", len(planned))
}

func body(planned []Planned) string {
	var b strings.Builder
	b.WriteString("Issues whose closing commits are on the main branch:\n\n")
	for _, p := range planned {
		fmt.Fprintf(&b, "- `%s`: %s %s\n", p.ID, short(p.Commit.SHA), p.Commit.Subject)
	}
	b.WriteString("\nOnly these rows of `.beads/issues.jsonl` change.\n")
	return b.String()
}

func commitMessage(planned []Planned) string {
	var b strings.Builder
	b.WriteString(title(planned) + "\n\n")
	for _, p := range planned {
		fmt.Fprintf(&b, "%s: %s\n", p.ID, p.Reason)
	}
	b.WriteString("\n")
	for _, p := range planned {
		fmt.Fprintf(&b, "Refs: %s\n", p.ID)
	}
	return b.String()
}
