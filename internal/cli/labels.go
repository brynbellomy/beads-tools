package cli

import (
	"fmt"
	"io"

	"github.com/brynbellomy/beads-tools/internal/labels"
)

// labelsUsage is the help of the labels subcommand.
const labelsUsage = `usage:
  beads-tools labels session SESSION-ID
  beads-tools labels user EMAIL

Print the label that records a session or a user on a bead.

br accepts only [A-Za-z0-9_-:] in a label and caps a label at 50 characters, so
an email cannot be written literally. Every byte outside [A-Za-z0-9-] is
escaped as an underscore and its two lowercase hex digits, which is reversible
and injective. An email whose escaped form does not fit keeps a prefix of it and
a hash of the email, which stays unique but no longer decodes.

This is the one definition of the format. Sirene's beads-actor plugin follows
it, and a conformance test there fails when the two drift.
`

// runLabels prints the label that records a session or a user on a bead.
func runLabels(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, labelsUsage)
		return ExitError
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, labelsUsage)
		return ExitOK
	case "session":
		if len(args) != 2 {
			fmt.Fprint(stderr, labelsUsage)
			return ExitError
		}
		fmt.Fprintln(stdout, labels.Session(args[1]))
		return ExitOK
	case "user":
		if len(args) != 2 {
			fmt.Fprint(stderr, labelsUsage)
			return ExitError
		}
		fmt.Fprintln(stdout, labels.User(args[1]))
		return ExitOK
	default:
		fmt.Fprintf(stderr, "beads-tools: unknown labels command %q\n\n%s", args[0], labelsUsage)
		return ExitError
	}
}
