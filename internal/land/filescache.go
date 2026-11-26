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

// cachedFile is one parsed issues blob and its decoded comment index.
type cachedFile struct {
	file  *beadsfile.File
	index *beadsfile.CommentIndex
}

// filesAt is the issues file as of a revision, parsed once per blob.
//
// A range check visits a commit's file and each parent's file, and neighbouring
// commits share most of those blobs. Memoizing by blob SHA makes work proportional
// to blobs the range touched rather than commits. A rev with no file is absent.
//
// The *beadsfile.File values returned are shared and read-only. Each blob's
// decoded comment index is cached beside its parsed file.
type filesAt struct {
	repo   gitx.Repo
	byRev  map[string]*cachedFile
	byBlob map[string]*cachedFile
	absent map[string]bool
}

// newFilesAt returns a cache that reads revisions of repo.
func newFilesAt(repo gitx.Repo) *filesAt {
	return &filesAt{
		repo:   repo,
		byRev:  map[string]*cachedFile{},
		byBlob: map[string]*cachedFile{},
		absent: map[string]bool{},
	}
}

// at returns the issues file as of rev, or nil when rev holds none. The returned
// file is shared and read-only.
func (c *filesAt) at(ctx context.Context, rev string) (*beadsfile.File, error) {
	entry, err := c.entry(ctx, rev)
	if err != nil || entry == nil {
		return nil, err
	}
	return entry.file, nil
}

// commentIndex returns the decoded comment index of the blob at rev. It shares
// the cache's single blob lookup with at.
func (c *filesAt) commentIndex(ctx context.Context, rev string) (*beadsfile.CommentIndex, error) {
	entry, err := c.entry(ctx, rev)
	if err != nil || entry == nil {
		return nil, err
	}
	return entry.index, nil
}

// entry returns one parsed file and comment index for a revision.
func (c *filesAt) entry(ctx context.Context, rev string) (*cachedFile, error) {
	if entry, ok := c.byRev[rev]; ok {
		return entry, nil
	}
	if c.absent[rev] {
		return nil, nil
	}
	blob, err := c.repo.Run(ctx, "rev-parse", rev+":"+beadsfile.Path)
	if err != nil {
		c.absent[rev] = true
		return nil, nil
	}
	blob = strings.TrimSpace(blob)
	if entry, ok := c.byBlob[blob]; ok {
		c.byRev[rev] = entry
		return entry, nil
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
	entry := &cachedFile{file: f, index: f.CommentIndex()}
	c.byBlob[blob] = entry
	c.byRev[rev] = entry
	return entry, nil
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
