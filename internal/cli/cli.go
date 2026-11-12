// Package cli parses beads-tools' command line and dispatches subcommands.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/gitx"
	"github.com/brynbellomy/beads-tools/internal/hook"
	"github.com/brynbellomy/beads-tools/internal/install"
	"github.com/brynbellomy/beads-tools/internal/land"
	"github.com/brynbellomy/beads-tools/internal/merge"
)

// Exit codes.
const (
	ExitOK       = 0
	ExitConflict = 1
	ExitError    = 2
)

const usage = `beads-tools: git-side companion to br (beads_rust)

usage:
  beads-tools install [--bin CMD]
        configure this repository: merge driver for .beads/issues.jsonl and
        the commit-msg hook
  beads-tools merge [--strict] BASE OURS THEIRS [PATH]
        three-way merge issues files (git runs this as the merge driver);
        writes the result to OURS
  beads-tools hook commit-msg FILE
        check a commit message's Closes:/Refs: trailers; refuses a staged
        issues file that adds duplicate comment ids or a dependency cycle
  beads-tools land pr NUMBER [--dry-run] [--no-merge] [--close ID]... [--keep-open]
                 [--require-trailers=BOOL] [--admin]
        close the issues a pull request's commits close, on its branch,
        then merge it. issues it only references (Refs:) need a decision:
        --close each one it finishes, or --keep-open. refuses an issues file
        with duplicate comment ids or a dependency cycle the base lacks
  beads-tools land sweep [--dry-run] [--no-merge] [--since WHEN] [--admin]
        close issues whose closing commits are already on the main branch,
        through a pull request
  beads-tools check [--base REV] [--head REV] [--require-trailers=BOOL]
        apply land pr's trailer rules to base..head (default: the main
        branch on the remote, and HEAD) and its duplicate comment id check to
        the issues file at head, and refuses a dependency cycle head adds
        that neither base nor the merge base holds; exits 1 when any fails.
        Needs only git, for CI.
  beads-tools session SESSION-ID [--dir DIR] [--work-dir DIR] [--home DIR]
                 [--json]
        print the beads a sirene session touched: its id, title, status,
        type, the date it was last modified and its parent epic. The session's
        own beads-events.jsonl names them, so a bead stays reported after its
        worktree is removed or br rebuilds its store. The stores the file
        names are read live, read-only, for the type, the parent epic and a
        fresher title and status. --json prints a JSON array instead of a
        table.

--require-trailers overrides beadstools.requireTrailers for one run.
--admin makes both land commands merge with gh pr merge --admin.

git config (all optional):
  beadstools.remote           remote to publish to (origin)
  beadstools.mainBranch       branch that counts as merged (remote HEAD, else main)
  beadstools.mergeMethod      merge | squash | rebase (merge)
  beadstools.requireTrailers  require Closes:/Refs: off the main branch (true)
`

// Run executes args (without the program name) and returns the exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitError
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitError
	}
	repo := gitx.Repo{Dir: dir}
	switch args[0] {
	case "merge":
		return runMerge(args[1:], stderr)
	case "hook":
		if len(args) != 3 || args[1] != "commit-msg" {
			fmt.Fprint(stderr, usage)
			return ExitError
		}
		if err := hook.CommitMsg(ctx, repo, args[2]); err != nil {
			fmt.Fprintf(stderr, "beads-tools: %v\n", err)
			return ExitConflict
		}
		return ExitOK
	case "install":
		fs := flag.NewFlagSet("install", flag.ContinueOnError)
		fs.SetOutput(stderr)
		bin := fs.String("bin", "beads-tools", "command git runs for the merge driver and hook")
		if err := fs.Parse(args[1:]); err != nil {
			return ExitError
		}
		if err := install.Install(ctx, repo, *bin, stdout); err != nil {
			fmt.Fprintf(stderr, "beads-tools: %v\n", err)
			return ExitError
		}
		return ExitOK
	case "check":
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		fs.SetOutput(stderr)
		base := fs.String("base", "", "first excluded revision (default <remote>/<main branch>)")
		head := fs.String("head", "HEAD", "last revision checked; its issues file is the reference")
		require := fs.Bool("require-trailers", repo.ConfigBool(ctx, "beadstools.requireTrailers", true), "require a Closes:/Refs: trailer on every commit (overrides beadstools.requireTrailers)")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return ExitError
		}
		if *base == "" {
			*base = repo.Remote(ctx) + "/" + repo.MainBranch(ctx)
		}
		ok, err := land.Check(ctx, repo, *base, *head, *require, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "beads-tools: %v\n", err)
			return ExitError
		}
		if !ok {
			return ExitConflict
		}
		return ExitOK
	case "land":
		return runLand(ctx, repo, args[1:], stdout, stderr)
	case "session":
		return runSession(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "beads-tools: unknown command %q\n\n%s", args[0], usage)
		return ExitError
	}
}

func runMerge(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	strict := fs.Bool("strict", false, "exit 1 when a field needed a newer-wins decision")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	if fs.NArg() < 3 || fs.NArg() > 4 {
		fmt.Fprint(stderr, usage)
		return ExitError
	}
	name := beadsfile.Path
	if fs.NArg() == 4 {
		name = fs.Arg(3)
	}
	var files [3]*beadsfile.File
	for i := range files {
		f, err := beadsfile.ParseFile(fs.Arg(i))
		if err != nil {
			fmt.Fprintf(stderr, "beads-tools merge %s: %v\n", name, err)
			return ExitError
		}
		files[i] = f
	}
	out, rep, err := merge.Merge(files[0], files[1], files[2])
	if err != nil {
		fmt.Fprintf(stderr, "beads-tools merge %s: %v\n", name, err)
		return ExitError
	}
	if err := out.WriteFile(fs.Arg(1)); err != nil {
		fmt.Fprintf(stderr, "beads-tools merge %s: %v\n", name, err)
		return ExitError
	}
	if s := rep.String(); s != "" {
		fmt.Fprintf(stderr, "beads-tools merge %s:\n%s", name, s)
	}
	if *strict && !rep.Clean() {
		return ExitConflict
	}
	return ExitOK
}

func runLand(ctx context.Context, repo gitx.Repo, args []string, stdout, stderr io.Writer) int {
	cmd, ok := parseLand(ctx, repo, args, stderr)
	if !ok {
		return ExitError
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "beads-tools: locating own binary: %v\n", err)
		return ExitError
	}
	if err := cmd.run(ctx, cmd.lander(repo, self, stdout)); err != nil {
		fmt.Fprintf(stderr, "beads-tools: %v\n", err)
		return ExitError
	}
	return ExitOK
}

// landCommand is a parsed land pr or land sweep command line.
type landCommand struct {
	sub   string
	pr    int
	opts  land.Options
	admin bool
}

// parseLand parses args (the subcommand and its flags), reporting problems to
// stderr.
func parseLand(ctx context.Context, repo gitx.Repo, args []string, stderr io.Writer) (landCommand, bool) {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return landCommand{}, false
	}
	c := landCommand{sub: args[0]}
	fs := flag.NewFlagSet("land "+c.sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var requireTrailers *bool
	fs.BoolVar(&c.opts.DryRun, "dry-run", false, "print the plan and change nothing")
	fs.BoolVar(&c.opts.NoMerge, "no-merge", false, "push and open, but leave merging to a person")
	fs.BoolVar(&c.admin, "admin", false, "merge with gh pr merge --admin, past branch protection rules you may bypass")
	var flags []string
	switch c.sub {
	case "pr":
		fs.Func("close", "close an issue the PR finishes but only references (repeatable)", func(id string) error {
			c.opts.Close = append(c.opts.Close, id)
			return nil
		})
		fs.BoolVar(&c.opts.KeepOpen, "keep-open", false, "confirm the issues the PR references and does not close stay open")
		requireTrailers = fs.Bool("require-trailers", repo.ConfigBool(ctx, "beadstools.requireTrailers", true), "require a Closes:/Refs: trailer on every commit (overrides beadstools.requireTrailers)")
		if len(args) < 2 {
			fmt.Fprint(stderr, usage)
			return landCommand{}, false
		}
		n, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "beads-tools: %q is not a pull request number\n", args[1])
			return landCommand{}, false
		}
		c.pr, flags = n, args[2:]
	case "sweep":
		fs.StringVar(&c.opts.Since, "since", land.DefaultSince, "how far back to report referenced issues that are still open (git log --since)")
		flags = args[1:]
	default:
		fmt.Fprintf(stderr, "beads-tools: unknown land command %q\n", c.sub)
		return landCommand{}, false
	}
	if fs.Parse(flags) != nil {
		return landCommand{}, false
	}
	if requireTrailers != nil {
		c.opts.TrailersOptional = !*requireTrailers
	}
	return c, true
}

// lander builds the Lander that runs c; self is the binary git runs as the
// issues file's merge driver.
func (c landCommand) lander(repo gitx.Repo, self string, out io.Writer) *land.Lander {
	return &land.Lander{
		Repo:   repo,
		GitHub: land.GH{Dir: repo.Dir, Admin: c.admin},
		Beads:  land.BR{},
		Out:    out,
		Now:    time.Now,

		MergeRetryDelay: 5 * time.Second,
		MergeDriver:     land.DriverCommand(self),
	}
}

// run runs c with l.
func (c landCommand) run(ctx context.Context, l *land.Lander) error {
	if c.sub == "pr" {
		return l.PR(ctx, c.pr, c.opts)
	}
	return l.Sweep(ctx, c.opts)
}
