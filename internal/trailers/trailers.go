// Package trailers reads the commit trailers that tie commits to issues:
// "Closes: <id>" (done once this commit is on the main branch) and
// "Refs: <id>" (progress only).
package trailers

import (
	"context"
	"regexp"
	"strings"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/gitx"
)

const (
	KeyCloses = "Closes"
	KeyRefs   = "Refs"
)

// Refs is the issue IDs one commit names.
type Refs struct {
	Closes []string
	Refs   []string
}

// Empty reports whether the commit names no issue.
func (r Refs) Empty() bool { return len(r.Closes) == 0 && len(r.Refs) == 0 }

// All returns every named ID, closes first, without duplicates.
func (r Refs) All() []string { return dedupe(append(append([]string(nil), r.Closes...), r.Refs...)) }

// SplitIDs splits a trailer value on commas and whitespace.
func SplitIDs(value string) []string {
	return dedupe(strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}))
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := ids[:0:0]
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ParseMessage extracts the Closes and Refs trailers from a commit message,
// using git's own trailer rules.
func ParseMessage(ctx context.Context, repo gitx.Repo, message string) (Refs, error) {
	out, err := repo.RunInput(ctx, message, "interpret-trailers", "--parse")
	if err != nil {
		return Refs{}, errors.With(err, "parsing trailers").Err()
	}
	var refs Refs
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch {
		case strings.EqualFold(strings.TrimSpace(key), KeyCloses):
			refs.Closes = append(refs.Closes, SplitIDs(value)...)
		case strings.EqualFold(strings.TrimSpace(key), KeyRefs):
			refs.Refs = append(refs.Refs, SplitIDs(value)...)
		}
	}
	refs.Closes, refs.Refs = dedupe(refs.Closes), dedupe(refs.Refs)
	return refs, nil
}

// Commit is one commit and the issues it names.
type Commit struct {
	SHA     string
	Subject string
	Refs
	// Reverts is the commit this one reverts, from git's standard
	// "This reverts commit <sha>." line.
	Reverts string
}

var revertLine = regexp.MustCompile(`(?m)^This reverts commit ([0-9a-f]{7,40})`)

// Log reads every commit in revRange (anything git log accepts) with its
// trailers, newest first.
func Log(ctx context.Context, repo gitx.Repo, revRange ...string) ([]Commit, error) {
	const (
		field  = "\x1f"
		record = "\x1e"
	)
	format := strings.Join([]string{
		"%H", "%s",
		"%(trailers:key=" + KeyCloses + ",valueonly,separator=%x2C,unfold)",
		"%(trailers:key=" + KeyRefs + ",valueonly,separator=%x2C,unfold)",
		"%b",
	}, "%x1f") + "%x1e"
	args := append([]string{"log", "--format=" + format}, revRange...)
	out, err := repo.Run(ctx, args...)
	if err != nil {
		return nil, errors.With(err, "reading commit log").Err()
	}
	var commits []Commit
	for _, rec := range strings.Split(out, record) {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, field, 5)
		if len(parts) != 5 {
			return nil, errors.WithNew("unexpected git log record").Set("record", rec).Err()
		}
		c := Commit{
			SHA:     parts[0],
			Subject: parts[1],
			Refs:    Refs{Closes: SplitIDs(parts[2]), Refs: SplitIDs(parts[3])},
		}
		if m := revertLine.FindStringSubmatch(parts[4]); m != nil {
			c.Reverts = m[1]
		}
		commits = append(commits, c)
	}
	return commits, nil
}
