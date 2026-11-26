package land

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
	"github.com/brynbellomy/beads-tools/internal/gitx"
)

// parseCount counts issues files parsed. It is unexported and only read by this
// package's own tests, so nothing outside can depend on it.
var parseCount atomic.Int64

// filesAt is the issues file as of a revision, parsed once per blob.
//
// A range check visits a commit's file and each parent's file, and neighbouring
// commits share most of those blobs: a 100-commit branch read the same 7 MB file
// about 100 times. Memoizing by blob SHA makes the work proportional to the blobs
// the range actually touched. A rev with no issues file is remembered as absent.
//
// The *beadsfile.File values returned are shared and must be treated as read-only;
// a file decodes its comment index on first use and never mutates it afterwards.
type filesAt struct {
	repo   gitx.Repo
	byRev  map[string]*beadsfile.File
	byBlob map[string]*beadsfile.File
	absent map[string]bool
}

// newFilesAt returns a cache that reads revisions of repo.
func newFilesAt(repo gitx.Repo) *filesAt {
	return &filesAt{
		repo:   repo,
		byRev:  map[string]*beadsfile.File{},
		byBlob: map[string]*beadsfile.File{},
		absent: map[string]bool{},
	}
}

// at returns the issues file as of rev, or nil when rev holds none. The returned
// file is shared and read-only.
func (c *filesAt) at(ctx context.Context, rev string) (*beadsfile.File, error) {
	if f, ok := c.byRev[rev]; ok {
		return f, nil
	}
	if c.absent[rev] {
		return nil, nil
	}
	blob, err := c.repo.Run(ctx, "rev-parse", rev+":"+beadsfile.Path)
	if err != nil {
		// No issues file at that revision, or nothing to resolve it against.
		c.absent[rev] = true
		return nil, nil
	}
	blob = strings.TrimSpace(blob)
	if f, ok := c.byBlob[blob]; ok {
		c.byRev[rev] = f
		return f, nil
	}
	data, ok, err := c.repo.Show(ctx, rev, beadsfile.Path)
	if err != nil {
		return nil, err
	}
	if !ok {
		c.absent[rev] = true
		return nil, nil
	}
	parseCount.Add(1)
	f, err := beadsfile.Parse(strings.NewReader(data))
	if err != nil {
		return nil, errors.With(err, "reading issues file").Set("rev", rev).Err()
	}
	c.byBlob[blob] = f
	c.byRev[rev] = f
	return f, nil
}

// many returns the issues file at each of revs, nil for a rev that holds none.
func (c *filesAt) many(ctx context.Context, revs []string) ([]*beadsfile.File, error) {
	out := make([]*beadsfile.File, len(revs))
	for i, rev := range revs {
		f, err := c.at(ctx, rev)
		if err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}
