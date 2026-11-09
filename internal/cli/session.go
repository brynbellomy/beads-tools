package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/brynbellomy/beads-tools/internal/sessionbeads"
)

// sessionUsage is the help of the session subcommand.
const sessionUsage = `usage:
  beads-tools session SESSION-ID [--dir DIR] [--work-dir DIR] [--home DIR]
                    [--json]

Print the beads a sirene session touched: each bead's id, title, status, type,
the date it was last modified and its parent epic.

The session's own beads-events.jsonl names the beads. Sirene copies every beads
event row its agents wrote into that file, so it outlives the removal of a
worktree and a br rebuild of a store's database. Each row carries the issue's
title, status, type and last-updated time as its store held them. The stores
the file names are read live, read-only, to fill in a fresher title, status,
type and updated time, and the parent epic; a bead whose store is gone is
reported with what the rows say.

flags:
  --dir DIR              the session's directory (default: found under
                         --work-dir, then --home)
  --work-dir DIR         the project whose session store holds the session
                         (default: the working directory)
  --home DIR             the sirene home whose session store holds the session
                         (default: $HOME)
  --json                 print a JSON array instead of a table
`

// runSession prints the beads a sirene session touched.
func runSession(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "the session's directory (default: found under --work-dir, then --home)")
	workDir := fs.String("work-dir", "", "the project whose session store holds the session (default: the working directory)")
	home := fs.String("home", "", "the sirene home whose session store holds the session (default: $HOME)")
	asJSON := fs.Bool("json", false, "print a JSON array instead of a table")

	flags, positional, help := splitArgs(fs, args)
	if help {
		fmt.Fprint(stdout, sessionUsage)
		return ExitOK
	}
	if err := fs.Parse(flags); err != nil {
		return ExitError
	}
	if len(positional) != 1 {
		fmt.Fprint(stderr, usage)
		return ExitError
	}
	id := positional[0]

	if *workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "beads-tools: %v\n", err)
			return ExitError
		}
		*workDir = wd
	}
	if *home == "" {
		*home = os.Getenv("HOME")
	}
	sessionDir, ok := sessionbeads.Locate(*workDir, *home, id, *dir)
	if !ok {
		if *dir != "" {
			fmt.Fprintf(stderr, "beads-tools: no session directory at %s\n", *dir)
		} else {
			fmt.Fprintf(stderr, "beads-tools: no session %q under %s or %s\n", id, *workDir, *home)
		}
		return ExitError
	}
	beads, err := sessionbeads.TouchedIn(ctx, sessionDir, *workDir)
	if err != nil {
		fmt.Fprintf(stderr, "beads-tools: %v\n", err)
		return ExitError
	}
	if *asJSON {
		err = sessionbeads.ReportJSON(stdout, beads)
	} else {
		err = sessionbeads.Report(stdout, beads)
	}
	if err != nil {
		fmt.Fprintf(stderr, "beads-tools: %v\n", err)
		return ExitError
	}
	return ExitOK
}

// splitArgs separates flags from positional arguments, so that a flag may come
// before or after the session id. A flag that takes a value consumes the next
// argument unless it is written as --name=value. Everything after -- is
// positional. help reports a -h or -help flag, which the flag package would
// otherwise treat as an error.
func splitArgs(fs *flag.FlagSet, args []string) (flags, positional []string, help bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return flags, append(positional, args[i+1:]...), help
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if name == "h" || name == "help" {
			help = true
			continue
		}
		flags = append(flags, a)
		if strings.Contains(a, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil || isBoolFlag(f) {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, positional, help
}

// isBoolFlag reports whether f takes no value.
func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}
