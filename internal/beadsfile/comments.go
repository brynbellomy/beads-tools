package beadsfile

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CommentID returns a comment's numeric id.
func CommentID(c *Object) (int64, bool) {
	raw := c.Raw("id")
	if raw == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	return n, err == nil
}

// DuplicateCommentID is a comment id that more than one comment carries. br
// keys comments by id across the whole file and refuses to import such a file.
type DuplicateCommentID struct {
	ID int64
	// Issues lists the issue of each comment carrying ID, in file order.
	Issues []string
}

func (d DuplicateCommentID) String() string {
	return fmt.Sprintf("comment id %d is used %d times (%s)", d.ID, len(d.Issues), strings.Join(d.Issues, ", "))
}

// DuplicateCommentIDs returns every comment id more than one comment in the
// file carries, ordered by id.
func (f *File) DuplicateCommentIDs() ([]DuplicateCommentID, error) {
	uses := map[int64][]string{}
	for _, iss := range f.Issues {
		cs, err := ParseObjectArray(iss.Obj.Raw("comments"))
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			if id, ok := CommentID(c); ok {
				uses[id] = append(uses[id], iss.ID)
			}
		}
	}
	var out []DuplicateCommentID
	for id, issues := range uses {
		if len(issues) > 1 {
			out = append(out, DuplicateCommentID{ID: id, Issues: issues})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
