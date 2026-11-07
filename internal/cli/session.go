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
                    [--actor-format FORMAT] [--json]

Print the beads a sirene session touched: each bead's id, title, status, type,
the date it was last modified and its parent epic.

The link is the actor br records on every mutation. Sirene's beads-actor plugin
sets BD_ACTOR on every command an agent runs, to a format that holds the session
id, so each row of a checkout's events table names the session that made it.
The command reads the events table of each beads store the session touched, and
reports the issues those rows name.

The stores come from the session's beads-events.jsonl, which names every store
the session touched, and from the checkout of the working directory. Each store
is opened read-only.

flags:
  --dir DIR              the session's directory (default: found under
                         --work-dir, then --home)
  --work-dir DIR         the project whose session store holds the session
                         (default: the working directory)
  --home DIR             the sirene home whose session store holds the session
                         (default: $HOME)
  --actor-format FORMAT  the BD_ACTOR format the beads-actor plugin sets; it
                         must hold {session} (default: ` + sessionbeads.DefaultActorFormat + `)
  --json                 print a JSON array instead of a table
`

// runSession prints the beads a sirene session touched.
func runSession(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "the session's directory (default: found under --work-dir, then --home)")
	workDir := fs.String("work-dir", "", "the project whose session store holds the session (default: the working directory)")
	home := fs.String("home", "", "the sirene home whose session store holds the session (default: $HOME)")
	format := fs.String("actor-format", sessionbeads.DefaultActorFormat, "the BD_ACTOR format the beads-actor plugin sets; it must hold {session}")
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
	beads, err := sessionbeads.TouchedIn(ctx, sessionDir, *workDir, id, *format)
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
