// Package install wires beads-tools into a repository: the merge driver for
// the issues file and the commit-msg hook.
package install

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/gitx"
)

// AttributesLine routes the issues file through the beads merge driver.
const AttributesLine = beadsfile.Path + " merge=beads"

const hookMarker = "beads-tools hook commit-msg"

// Install configures repo. bin is the command git runs, normally
// "beads-tools" on PATH. It never overwrites a hook it did not write; it
// prints what to add instead.
func Install(ctx context.Context, repo gitx.Repo, bin string, out io.Writer) error {
	top, err := repo.TopLevel(ctx)
	if err != nil {
		return err
	}
	if err := ensureLine(filepath.Join(top, ".gitattributes"), AttributesLine); err != nil {
		return err
	}
	fmt.Fprintf(out, ".gitattributes: %s\n", AttributesLine)

	driver := bin + " merge %O %A %B %P"
	for _, kv := range [][2]string{
		{"merge.beads.name", "br issues three-way merge (beads-tools)"},
		{"merge.beads.driver", driver},
	} {
		if _, err := repo.Run(ctx, "config", "--local", kv[0], kv[1]); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "git config merge.beads.driver = %s\n", driver)

	hooks, err := repo.GitPath(ctx, "hooks")
	if err != nil {
		return err
	}
	path := filepath.Join(hooks, "commit-msg")
	line := fmt.Sprintf("%s hook commit-msg \"$1\"", bin)
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && strings.Contains(string(existing), hookMarker):
		fmt.Fprintf(out, "%s: already installed\n", path)
	case err == nil:
		fmt.Fprintf(out, "%s exists and was left alone; add this line to it:\n    %s || exit 1\n", path, line)
	case os.IsNotExist(err):
		if err := os.MkdirAll(hooks, 0o755); err != nil {
			return errors.With(err, "creating hooks directory").Err()
		}
		script := "#!/bin/sh\nexec " + line + "\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			return errors.With(err, "writing hook").Set("path", path).Err()
		}
		fmt.Fprintf(out, "%s: installed\n", path)
	default:
		return errors.With(err, "reading hook").Set("path", path).Err()
	}
	return nil
}

func ensureLine(path, line string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return errors.With(err, "reading").Set("path", path).Err()
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		data = append(data, '\n')
	}
	data = append(data, line+"\n"...)
	return beadsfile.WriteAtomic(path, data)
}
