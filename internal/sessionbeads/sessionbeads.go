// Package sessionbeads answers which beads a session or a user touched.
//
// The answer comes from the labels br writes on a bead: session:<id> and
// user:<escaped email>. Membership therefore lives in git, versioned with the
// branch, and is read from issues.jsonl files alone: the main checkout's and
// each live worktree's. Nothing here opens a beads database or reads an events
// table.
package sessionbeads

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/gitx"
	"github.com/brynbellomy/beads-tools/internal/labels"
)

const (
	beadsDirName = ".beads"
	redirectName = "redirect"
	issuesName   = "issues.jsonl"
)

// Bead is one bead a session or a user touched, as the report prints it.
type Bead struct {
	ID     string
	Title  string
	Status string
	// Type is the issue's type ("task", "bug", "epic", …).
	Type string
	// Updated is the bead's updated_at as its store holds it.
	Updated string
	// Parent is the bead's parent-child parent, empty when it has none.
	Parent string
}

// TouchedIn returns the beads that carry the session label for sessionID,
// sorted by id, read from the main checkout at workDir and each live worktree
// of it.
func TouchedIn(ctx context.Context, workDir, sessionID string) ([]Bead, error) {
	if sessionID == "" {
		return nil, errors.WithNew("no session id").Err()
	}
	return touched(ctx, workDir, labels.Session(sessionID))
}

// TouchedByUserIn returns the beads that carry the user label for email,
// sorted by id, read from the main checkout at workDir and each live worktree
// of it.
func TouchedByUserIn(ctx context.Context, workDir, email string) ([]Bead, error) {
	if email == "" {
		return nil, errors.WithNew("no email").Err()
	}
	return touched(ctx, workDir, labels.User(email))
}

// touched returns the beads of every store under workDir that carry label.
func touched(ctx context.Context, workDir, label string) ([]Bead, error) {
	stores, err := Stores(ctx, workDir)
	if err != nil {
		return nil, err
	}
	byID := map[string]Bead{}
	for _, store := range stores {
		beads, err := readStore(store, label)
		if err != nil {
			return nil, err
		}
		for _, b := range beads {
			byID[b.ID] = newer(byID[b.ID], b)
		}
	}
	out := make([]Bead, 0, len(byID))
	for _, b := range byID {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Stores returns the beads stores to read for workDir: the main checkout's and
// each live worktree's, each after its redirect file, deduplicated and sorted.
// A checkout with no .beads contributes nothing.
func Stores(ctx context.Context, workDir string) ([]string, error) {
	dirs, err := checkouts(ctx, workDir)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []string
	for _, dir := range dirs {
		store, ok := CheckoutStore(dir)
		if !ok {
			continue
		}
		if _, dup := seen[store]; dup {
			continue
		}
		seen[store] = struct{}{}
		out = append(out, store)
	}
	sort.Strings(out)
	return out, nil
}

// checkouts returns the working directories to read: workDir's checkout and
// each live worktree of it. A directory that is not in a git repository is
// itself alone.
func checkouts(ctx context.Context, workDir string) ([]string, error) {
	repo := gitx.Repo{Dir: workDir}
	out, err := repo.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return []string{workDir}, nil
	}
	var dirs []string
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			dirs = append(dirs, path)
		}
	}
	if len(dirs) == 0 {
		return []string{workDir}, nil
	}
	return dirs, nil
}

// readStore returns the beads of store that carry label.
func readStore(store, label string) ([]Bead, error) {
	path := filepath.Join(store, issuesName)
	f, err := beadsfile.ParseFile(path)
	if err != nil {
		return nil, errors.With(err, "reading issues file").Set("path", path).Err()
	}
	var out []Bead
	for _, iss := range f.Issues {
		if !hasLabel(iss.Obj, label) {
			continue
		}
		out = append(out, beadOf(iss.Obj))
	}
	return out, nil
}

// hasLabel reports whether the issue object carries label.
func hasLabel(obj *beadsfile.Object, label string) bool {
	raw := obj.Raw("labels")
	if len(raw) == 0 {
		return false
	}
	var held []string
	if err := json.Unmarshal(raw, &held); err != nil {
		return false
	}
	return labels.Has(held, label)
}

// beadOf reads the fields the report prints from one issue object.
func beadOf(obj *beadsfile.Object) Bead {
	b := Bead{}
	b.ID, _ = obj.String("id")
	b.Title, _ = obj.String("title")
	b.Status, _ = obj.String("status")
	b.Type, _ = obj.String("issue_type")
	b.Updated, _ = obj.String("updated_at")
	b.Parent = parentOf(obj)
	return b
}

// parentOf returns the issue's parent-child parent, empty when it has none.
func parentOf(obj *beadsfile.Object) string {
	raw := obj.Raw("dependencies")
	if len(raw) == 0 {
		return ""
	}
	deps, err := beadsfile.ParseObjectArray(raw)
	if err != nil {
		return ""
	}
	for _, dep := range deps {
		if t, _ := dep.String("type"); t != "parent-child" {
			continue
		}
		if id, _ := dep.String("depends_on_id"); id != "" {
			return id
		}
	}
	return ""
}

// newer returns the bead whose updated_at is later. A bead present in several
// stores is one row, and the newest word on it wins.
func newer(held, live Bead) Bead {
	if held.ID == "" {
		return live
	}
	if live.Updated > held.Updated {
		return live
	}
	return held
}

// CheckoutStore returns the beads store br uses in dir: the nearest .beads at
// or above dir within its checkout, after its redirect file. It reports false
// when the checkout has none. The path is canonical, so two checkouts that
// share one store name it the same way.
func CheckoutStore(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		beads := filepath.Join(dir, beadsDirName)
		if info, err := os.Stat(beads); err == nil && info.IsDir() {
			return canonical(redirected(beads)), true
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// canonical resolves the links of path, so one store reached by two paths is
// one store.
func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// redirected follows the redirect file of beads as br does: its trimmed content
// names the .beads directory to use, a relative path resolving against beads
// itself.
func redirected(beads string) string {
	data, err := os.ReadFile(filepath.Join(beads, redirectName))
	if err != nil {
		return beads
	}
	target := strings.TrimSpace(string(data))
	if target == "" {
		return beads
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(beads, target)
	}
	return filepath.Clean(target)
}
