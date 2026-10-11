// Command beads-tools is the git-side companion to br (beads_rust).
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/brynbellomy/beads-tools/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
