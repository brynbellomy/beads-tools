// Package land closes issues when the work that closes them reaches the main
// branch. The link is the "Closes:" trailer on the commits themselves, so no
// state is kept anywhere: an issue that is already closed is the watermark.
package land

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
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
	// MergeDriver is the command git runs as the merge driver for the issues
	// file when land brings a branch up to date (see DriverCommand).
	MergeDriver string
}

// mergeAttempts bounds how often land tries to merge a head it pushed.
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

// mergeMethod reads beadstools.mergeMethod. Rebase is refused: GitHub replays
// each commit onto the base as a text merge, which reintroduces the text merge
// of the issues file that bringing the head up to date avoids. A squash takes
// the head's tree when the head contains the base, so it is allowed.
func (l *Lander) mergeMethod(ctx context.Context) (string, error) {
	switch m := l.Repo.Config(ctx, "beadstools.mergeMethod", "merge"); m {
	case "merge", "squash":
		return m, nil
	case "rebase":
		return "", errors.WithNew("beadstools.mergeMethod=rebase is not supported: GitHub's rebase replays each commit " +
			"onto the base as a text merge, which can leave duplicate comment ids in .beads/issues.jsonl; use merge").Err()
	default:
		return "", errors.WithNew("unknown beadstools.mergeMethod").Set("method", m).Err()
	}
}

// Sweep closes every issue a commit on the main branch closes but that is still
// open there, through a pull request it merges itself.
func (l *Lander) Sweep(ctx context.Context, opts Options) error {
	method, err := l.mergeMethod(ctx)
	if err != nil {
		return err
	}
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
	w, err := l.openWorktree(ctx, ref)
	if err != nil {
		return err
	}
	defer l.closeWorktree(ctx, w)
	if err := l.commitCloses(ctx, w, planned); err != nil {
		return err
	}
	sha, err := w.head(ctx)
	if err != nil {
		return err
	}
	if err := l.push(ctx, remote, sha, branch); err != nil {
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
	refresh := func(ctx context.Context) (string, error) {
		if _, err := l.Repo.Run(ctx, "fetch", remote, main); err != nil {
			return "", err
		}
		if err := w.catchUp(ctx, ref, l.MergeDriver, "Merge "+ref+" into "+branch); err != nil {
			return "", err
		}
		sha, err := w.head(ctx)
		if err != nil {
			return "", err
		}
		issues, err := issuesAt(ctx, l.Repo, sha)
		if err != nil {
			return "", err
		}
		if err := l.refuseDuplicateCommentIDs(issues, branch); err != nil {
			return "", err
		}
		return sha, l.push(ctx, remote, sha, branch)
	}
	if err := l.mergePushedHead(ctx, url, sha, method, remote, main, refresh); err != nil {
		return err
	}
	l.printf("merged %s\n", url)
	if _, err := l.Repo.Run(ctx, "push", remote, "--delete", branch); err != nil {
		l.printf("warning: could not delete %s on %s: %v\n", branch, remote, err)
	}
	return nil
}

// PR lands pull request number. In a throwaway worktree it brings the PR's
// head up to date with the base branch through the merge driver, closes the
// issues the PR's commits close in one commit, pushes that head and merges the
// PR only if it is still the head. While the head contains the base, GitHub's
// merge takes the head's issues file as it is. The base can still move in the
// moment between the last check of its tip and GitHub's merge, and GitHub then
// merges the issues file as text; branch protection's "Require branches to be
// up to date before merging" makes GitHub refuse instead, and land catches up
// and retries.
func (l *Lander) PR(ctx context.Context, number int, opts Options) error {
	method, err := l.mergeMethod(ctx)
	if err != nil {
		return err
	}
	ref := strconv.Itoa(number)
	pr, err := l.GitHub.View(ctx, ref)
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

	w, err := l.openWorktree(ctx, pr.HeadRefOid)
	if err != nil {
		return err
	}
	defer l.closeWorktree(ctx, w)
	base := remote + "/" + pr.BaseRefName
	sha, commits, issues, err := l.catchUpPR(ctx, w, pr, base, opts)
	if err != nil {
		return err
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

	if len(planned) > 0 {
		if err := l.commitCloses(ctx, w, planned); err != nil {
			return err
		}
		if sha, err = w.head(ctx); err != nil {
			return err
		}
	}
	if sha != pr.HeadRefOid {
		if err := l.push(ctx, remote, sha, pr.HeadRefName); err != nil {
			return err
		}
	}
	if opts.NoMerge {
		return nil
	}
	refresh := func(ctx context.Context) (string, error) {
		if _, err := l.Repo.Run(ctx, "fetch", remote, pr.BaseRefName); err != nil {
			return "", err
		}
		sha, _, _, err := l.catchUpPR(ctx, w, pr, base, opts)
		if err != nil {
			return "", err
		}
		return sha, l.push(ctx, remote, sha, pr.HeadRefName)
	}
	if err := l.mergePushedHead(ctx, ref, sha, method, remote, pr.BaseRefName, refresh); err != nil {
		return err
	}
	l.printf("merged #%d\n", number)
	return nil
}

// catchUpPR merges base into the worktree's head through the merge driver and
// applies the landing rules to the result: no duplicate comment id, and every
// commit since base names valid issues. It returns the head, its commits since
// base and its issues file.
func (l *Lander) catchUpPR(ctx context.Context, w *worktree, pr PR, base string, opts Options) (string, []trailers.Commit, *beadsfile.File, error) {
	if err := w.catchUp(ctx, base, l.MergeDriver, "Merge "+base+" into "+pr.HeadRefName); err != nil {
		return "", nil, nil, err
	}
	sha, err := w.head(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	commits, err := trailers.Log(ctx, l.Repo, base+".."+sha)
	if err != nil {
		return "", nil, nil, err
	}
	issues, err := issuesAt(ctx, l.Repo, sha)
	if err != nil {
		return "", nil, nil, err
	}
	if err := l.refuseDuplicateCommentIDs(issues, pr.HeadRefName); err != nil {
		return "", nil, nil, err
	}
	unlinked, err := FindUnlinked(ctx, l.Repo, commits, issues, !opts.TrailersOptional)
	if err != nil {
		return "", nil, nil, err
	}
	if len(unlinked) > 0 {
		for _, u := range unlinked {
			l.printf("refuse %s %s: %s\n", short(u.Commit.SHA), u.Commit.Subject, u.Why)
		}
		return "", nil, nil, errors.WithNew(fmt.Sprintf("%d commit(s) on %s do not name a valid issue; reword them "+
			"(git rebase -i %s, then reword) to end with a Closes: or Refs: trailer, push, and land again",
			len(unlinked), pr.HeadRefName, base)).Err()
	}
	return sha, commits, issues, nil
}

// push publishes sha as branch with a plain push: if anyone pushed to the
// branch meanwhile, it fails rather than discarding their commit.
func (l *Lander) push(ctx context.Context, remote, sha, branch string) error {
	if _, err := l.Repo.Run(ctx, "push", remote, sha+":refs/heads/"+branch); err != nil {
		return err
	}
	l.printf("pushed %s to %s\n", short(sha), branch)
	return nil
}

// mergePushedHead merges pull request ref (a number or URL) pinned to sha.
// Right before each merge it reads the base branch's tip on remote, and when
// sha does not contain it, refresh brings the head up to date and pushes it.
// GitHub refuses a merge while the base branch moves under it; then, a bounded
// number of times and only while sha is still the head, the same refresh runs
// and the merge is retried on the head it returns. Any other refusal is final.
func (l *Lander) mergePushedHead(ctx context.Context, ref, sha, method, remote, base string, refresh func(context.Context) (string, error)) error {
	for attempt := 1; ; attempt++ {
		current, err := l.containsRemoteTip(ctx, remote, base, sha)
		if err != nil {
			return err
		}
		if !current {
			l.printf("%s moved since the head was built; bringing the head up to date\n", base)
			if sha, err = refresh(ctx); err != nil {
				return err
			}
		}
		mergeErr := l.GitHub.Merge(ctx, ref, sha, method)
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
		case !stderrors.Is(mergeErr, ErrBaseModified):
			return errors.With(mergeErr, "merging pull request").Set("pr", ref).Err()
		case attempt == mergeAttempts:
			return errors.With(mergeErr, "merging pull request").Set("pr", ref, "attempts", mergeAttempts).Err()
		}
		l.printf("merge of %s refused, the base branch moved (attempt %d of %d); bringing the head up to date\n", ref, attempt, mergeAttempts)
		if err := pause(ctx, time.Duration(attempt)*l.MergeRetryDelay); err != nil {
			return errors.With(err, "waiting to retry the merge").Err()
		}
		if sha, err = refresh(ctx); err != nil {
			return err
		}
	}
}

// containsRemoteTip reports whether sha contains the tip branch has on remote
// right now. A tip not yet fetched is not contained.
func (l *Lander) containsRemoteTip(ctx context.Context, remote, branch, sha string) (bool, error) {
	out, err := l.Repo.Run(ctx, "ls-remote", remote, "refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return false, errors.WithNew("base branch not found on the remote").Set("remote", remote, "branch", branch).Err()
	}
	if _, err := l.Repo.Run(ctx, "cat-file", "-e", fields[0]+"^{commit}"); err != nil {
		return false, nil
	}
	_, err = l.Repo.Run(ctx, "merge-base", "--is-ancestor", fields[0], sha)
	return err == nil, nil
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
		"If this branch never changed .beads/issues.jsonl it inherited them from the base branch unchanged (git runs the "+
		"merge driver only when both sides changed the file): repair the base first with a .beads/-only pull request "+
		"that renumbers all but one comment for each id, then land again", where, len(dups))).Err()
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
