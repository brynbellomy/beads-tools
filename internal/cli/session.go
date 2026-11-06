package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/brynbellomy/beads-tools/internal/sessionbeads"
)

// runSession prints the beads a sirene session touched.
func runSession(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitError
	}
	id := args[0]
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "the session's directory (default: found under --work-dir, then --home)")
	workDir := fs.String("work-dir", "", "the project whose session store holds the session (default: the working directory)")
	home := fs.String("home", "", "the sirene home whose session store holds the session (default: $HOME)")
	format := fs.String("actor-format", sessionbeads.DefaultActorFormat, "the BD_ACTOR format the beads-actor plugin sets; it must hold {session}")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return ExitError
	}

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
	if err := sessionbeads.Report(stdout, beads); err != nil {
		fmt.Fprintf(stderr, "beads-tools: %v\n", err)
		return ExitError
	}
	return ExitOK
}
