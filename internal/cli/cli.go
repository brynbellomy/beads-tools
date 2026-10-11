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
        check a commit message's Closes:/Refs: trailers
  beads-tools land pr NUMBER [--dry-run] [--no-merge] [--close ID]... [--keep-open]
        close the issues a pull request's commits close, on its branch,
        then merge it. issues it only references (Refs:) need a decision:
        --close each one it finishes, or --keep-open
  beads-tools land sweep [--dry-run] [--no-merge] [--since WHEN]
        close issues whose closing commits are already on the main branch,
        through a pull request
  beads-tools check [--base REV] [--head REV]
        apply land pr's trailer rules to base..head (default: the main
        branch on the remote, and HEAD); exits 1 when a commit fails them.
        Needs only git, for CI.

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
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return ExitError
		}
		if *base == "" {
			*base = repo.Remote(ctx) + "/" + repo.MainBranch(ctx)
		}
		ok, err := land.Check(ctx, repo, *base, *head, stdout)
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
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitError
	}
	fs := flag.NewFlagSet("land "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts land.Options
	fs.BoolVar(&opts.DryRun, "dry-run", false, "print the plan and change nothing")
	fs.BoolVar(&opts.NoMerge, "no-merge", false, "push and open, but leave merging to a person")
	if args[0] == "pr" {
		fs.Func("close", "close an issue the PR finishes but only references (repeatable)", func(id string) error {
			opts.Close = append(opts.Close, id)
			return nil
		})
		fs.BoolVar(&opts.KeepOpen, "keep-open", false, "confirm the issues the PR references and does not close stay open")
	}
	if args[0] == "sweep" {
		fs.StringVar(&opts.Since, "since", land.DefaultSince, "how far back to report referenced issues that are still open (git log --since)")
	}

	l := &land.Lander{
		Repo:   repo,
		GitHub: land.GH{Dir: repo.Dir},
		Beads:  land.BR{},
		Out:    stdout,
		Now:    time.Now,
	}
	var err error
	switch args[0] {
	case "pr":
		if len(args) < 2 {
			fmt.Fprint(stderr, usage)
			return ExitError
		}
		n, convErr := strconv.Atoi(args[1])
		if convErr != nil {
			fmt.Fprintf(stderr, "beads-tools: %q is not a pull request number\n", args[1])
			return ExitError
		}
		if fs.Parse(args[2:]) != nil {
			return ExitError
		}
		err = l.PR(ctx, n, opts)
	case "sweep":
		if fs.Parse(args[1:]) != nil {
			return ExitError
		}
		err = l.Sweep(ctx, opts)
	default:
		fmt.Fprintf(stderr, "beads-tools: unknown land command %q\n", args[0])
		return ExitError
	}
	if err != nil {
		fmt.Fprintf(stderr, "beads-tools: %v\n", err)
		return ExitError
	}
	return ExitOK
}
