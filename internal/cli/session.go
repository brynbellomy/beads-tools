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
  beads-tools session SESSION-ID [--dir DIR] [--json]
  beads-tools session --user EMAIL [--dir DIR] [--json]

Print the beads a session or a user touched: each bead's id, title, status,
type, the date it was last modified and its parent epic.

Membership is a label br writes on the bead: session:<session id> for a session
and user:<escaped email> for a user. The labels live in issues.jsonl, so they
are versioned with the branch. The files read are the main checkout's and each
live worktree's, and nothing else: no beads database and no events table.

flags:
  --user EMAIL           report the beads this user touched instead of a
                         session's
  --dir DIR              the checkout to read (default: the working directory)
  --json                 print a JSON array instead of a table
`

// runSession prints the beads a session or a user touched.
func runSession(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	fs.SetOutput(stderr)
	user := fs.String("user", "", "report the beads this user touched instead of a session's")
	dir := fs.String("dir", "", "the checkout to read (default: the working directory)")
	asJSON := fs.Bool("json", false, "print a JSON array instead of a table")

	flags, positional, help := splitArgs(fs, args)
	if help {
		fmt.Fprint(stdout, sessionUsage)
		return ExitOK
	}
	if err := fs.Parse(flags); err != nil {
		return ExitError
	}
	if *user != "" && len(positional) != 0 {
		fmt.Fprint(stderr, "beads-tools: name a session id or --user, not both\n\n"+sessionUsage)
		return ExitError
	}
	if *user == "" && len(positional) != 1 {
		fmt.Fprint(stderr, sessionUsage)
		return ExitError
	}
	if *dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "beads-tools: %v\n", err)
			return ExitError
		}
		*dir = wd
	}

	var beads []sessionbeads.Bead
	var err error
	if *user != "" {
		beads, err = sessionbeads.TouchedByUserIn(ctx, *dir, *user)
	} else {
		beads, err = sessionbeads.TouchedIn(ctx, *dir, positional[0])
	}
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
