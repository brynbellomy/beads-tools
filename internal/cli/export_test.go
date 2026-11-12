package cli

import (
	"context"
	"errors"
	"io"

	"github.com/brynbellomy/beads-tools/internal/gitx"
	"github.com/brynbellomy/beads-tools/internal/land"
)

// LandWith runs a land command line from dir as runLand does, with self as the
// merge driver binary and tune applied to the Lander before it runs.
func LandWith(ctx context.Context, dir string, args []string, self string, out io.Writer, tune func(*land.Lander)) error {
	repo := gitx.Repo{Dir: dir}
	cmd, ok := parseLand(ctx, repo, args, out)
	if !ok {
		return errors.New("land command line refused")
	}
	l := cmd.lander(repo, self, out)
	tune(l)
	return cmd.run(ctx, l)
}
