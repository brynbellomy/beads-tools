package land

import (
	"context"
	"os"
	"path/filepath"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/gitx"
)

// Beads applies issue changes with br itself, so br's validation and the
// repository's workflow rules (.beads/config.yaml) still apply.
type Beads interface {
	// Close closes id in the JSONL-only workspace at dir.
	Close(ctx context.Context, dir, id, reason string) error
}

// BR runs the br binary.
type BR struct {
	Bin string
}

// Close implements Beads.
func (b BR) Close(ctx context.Context, dir, id, reason string) error {
	bin := b.Bin
	if bin == "" {
		bin = "br"
	}
	_, err := gitx.Command(ctx, dir, bin, "close", id, "--reason", reason, "--no-db", "--quiet")
	return err
}

// Close is one issue to close and the reason recorded on it.
type Close struct {
	ID     string
	Reason string
}

// closedStatuses are the statuses a close has nothing left to do for.
var closedStatuses = map[string]bool{"closed": true, "tombstone": true}

// IsClosed reports whether iss needs no close.
func IsClosed(iss *beadsfile.Issue) bool {
	s, _ := iss.Obj.String("status")
	return closedStatuses[s]
}

// CloseInTree closes issues in the issues file of the working tree at root and
// returns the IDs it changed.
//
// br rewrites rows it was not asked to touch when it exports (comment IDs,
// timestamp formatting), so br runs on a scratch copy and only the rows for the
// requested issues are spliced back. Every other line keeps its bytes.
func CloseInTree(ctx context.Context, root string, closes []Close, beads Beads) ([]string, error) {
	path := filepath.Join(root, beadsfile.Path)
	orig, err := beadsfile.ParseFile(path)
	if err != nil {
		return nil, err
	}
	var todo []Close
	for _, c := range closes {
		iss := orig.Get(c.ID)
		if iss == nil {
			return nil, errors.WithNew("issue not found").Set("id", c.ID).Err()
		}
		if !IsClosed(iss) {
			todo = append(todo, c)
		}
	}
	if len(todo) == 0 {
		return nil, nil
	}

	scratch, err := os.MkdirTemp("", "beads-tools-close-*")
	if err != nil {
		return nil, errors.With(err, "creating scratch workspace").Err()
	}
	defer os.RemoveAll(scratch)
	if err := copyBeadsDir(filepath.Join(root, ".beads"), filepath.Join(scratch, ".beads")); err != nil {
		return nil, err
	}
	for _, c := range todo {
		if err := beads.Close(ctx, scratch, c.ID, c.Reason); err != nil {
			return nil, errors.With(err, "br close").Set("id", c.ID).Err()
		}
	}
	edited, err := beadsfile.ParseFile(filepath.Join(scratch, beadsfile.Path))
	if err != nil {
		return nil, errors.With(err, "reading br's output").Err()
	}

	want := map[string]bool{}
	for _, c := range todo {
		want[c.ID] = true
	}
	for _, id := range orig.IDs() {
		if edited.Get(id) == nil {
			return nil, errors.WithNew("br dropped an issue it was not asked to touch").Set("id", id).Err()
		}
	}
	for _, id := range edited.IDs() {
		if orig.Get(id) == nil {
			return nil, errors.WithNew("br added an issue it was not asked to create").Set("id", id).Err()
		}
	}

	out := make([]*beadsfile.Issue, len(orig.Issues))
	var changed []string
	for i, iss := range orig.Issues {
		out[i] = iss
		if !want[iss.ID] {
			continue
		}
		next := edited.Get(iss.ID)
		if !IsClosed(next) {
			return nil, errors.WithNew("br did not close the issue").Set("id", iss.ID).Err()
		}
		out[i] = next
		changed = append(changed, iss.ID)
	}
	f, err := beadsfile.New(out)
	if err != nil {
		return nil, err
	}
	if err := f.WriteFile(path); err != nil {
		return nil, err
	}
	return changed, nil
}

// copyBeadsDir copies the files br needs to work JSONL-only: the issues file
// and its configuration. Databases are never copied.
func copyBeadsDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return errors.With(err, "creating scratch .beads").Err()
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return errors.With(err, "reading .beads").Set("path", src).Err()
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(filepath.Ext(name) == ".jsonl" || filepath.Ext(name) == ".yaml" || filepath.Ext(name) == ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return errors.With(err, "reading").Set("path", name).Err()
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, 0o644); err != nil {
			return errors.With(err, "writing").Set("path", name).Err()
		}
	}
	return nil
}
