package beadsfile

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/brynbellomy/go-utils/errors"
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

// CommentIdentity is what makes two comments the same comment: who wrote it,
// when, and what it says. br numbers comments from a counter kept in one
// database, so an id is not part of a comment's identity.
type CommentIdentity struct {
	Author    string
	CreatedAt string
	Text      string
}

// IdentityOf returns a comment's identity.
func IdentityOf(c *Object) CommentIdentity {
	author, _ := c.String("author")
	createdAt, _ := c.String("created_at")
	text, _ := c.String("text")
	return CommentIdentity{Author: author, CreatedAt: createdAt, Text: text}
}

// String renders the identity as a map key.
func (i CommentIdentity) String() string {
	return strings.Join([]string{i.Author, i.CreatedAt, i.Text}, "\x00")
}

// CommentKey names one comment within one issue. Identity alone is not enough:
// one note posted on several issues in the same second carries the same
// identity on each, and identical comments within one issue are told apart by
// Occurrence, in file order.
type CommentKey struct {
	IssueID string
	CommentIdentity
	Occurrence int
}

// String describes the comment for a message.
func (k CommentKey) String() string {
	s := fmt.Sprintf("comment %q by %q at %q on %s", k.Text, k.Author, k.CreatedAt, k.IssueID)
	if k.Occurrence > 0 {
		s += fmt.Sprintf(" (occurrence %d)", k.Occurrence+1)
	}
	return s
}

// CommentKeys names each comment of one issue, in file order.
func CommentKeys(issueID string, cs []*Object) []CommentKey {
	seen := map[CommentIdentity]int{}
	keys := make([]CommentKey, len(cs))
	for i, c := range cs {
		identity := IdentityOf(c)
		keys[i] = CommentKey{IssueID: issueID, CommentIdentity: identity, Occurrence: seen[identity]}
		seen[identity]++
	}
	return keys
}

// commentEntry is one comment of one issue.
type commentEntry struct {
	key   CommentKey
	id    int64
	hasID bool
}

// eachComment calls fn for every comment in f, in file order.
func (f *File) eachComment(fn func(commentEntry) error) error {
	for _, iss := range f.Issues {
		cs, err := ParseObjectArray(iss.Obj.Raw("comments"))
		if err != nil {
			return errors.With(err, "reading comments").Set("id", iss.ID).Err()
		}
		for i, key := range CommentKeys(iss.ID, cs) {
			id, ok := CommentID(cs[i])
			if err := fn(commentEntry{key: key, id: id, hasID: ok}); err != nil {
				return err
			}
		}
	}
	return nil
}

// CommentIDs maps each comment in f to its id, and returns the largest id.
func (f *File) CommentIDs() (map[CommentKey]int64, int64, error) {
	ids := map[CommentKey]int64{}
	var maxID int64
	err := f.eachComment(func(e commentEntry) error {
		if e.hasID {
			ids[e.key] = e.id
			maxID = max(maxID, e.id)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return ids, maxID, nil
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
	err := f.eachComment(func(e commentEntry) error {
		if e.hasID {
			uses[e.id] = append(uses[e.id], e.key.IssueID)
		}
		return nil
	})
	if err != nil {
		return nil, err
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

// NewDuplicateCommentIDs splits head's duplicate comment ids into those no
// file in parents holds (added) and those one does (inherited). A parent holds
// a duplicate when it carries the same id on the same issues.
func NewDuplicateCommentIDs(head *File, parents ...*File) (added, inherited []DuplicateCommentID, err error) {
	dups, err := head.DuplicateCommentIDs()
	if err != nil || len(dups) == 0 {
		return nil, nil, err
	}
	held := map[string]bool{}
	for _, p := range parents {
		pd, err := p.DuplicateCommentIDs()
		if err != nil {
			return nil, nil, err
		}
		for _, d := range pd {
			held[d.key()] = true
		}
	}
	for _, d := range dups {
		if held[d.key()] {
			inherited = append(inherited, d)
		} else {
			added = append(added, d)
		}
	}
	return added, inherited, nil
}

// key names a duplicate by its id and the issues that share it.
func (d DuplicateCommentID) key() string {
	issues := slices.Clone(d.Issues)
	slices.Sort(issues)
	return strconv.FormatInt(d.ID, 10) + ":" + strings.Join(issues, ",")
}

// RenumberedComment is an existing comment whose id a change moved to one none
// of its parents holds for it. br keys comments by id across the whole file, so
// anything that recorded a comment by id now points at a different comment.
type RenumberedComment struct {
	Key CommentKey
	// Was holds the ids the parents give the comment, ordered.
	Was []int64
	Now int64
}

func (r RenumberedComment) String() string {
	was := make([]string, len(r.Was))
	for i, id := range r.Was {
		was[i] = strconv.FormatInt(id, 10)
	}
	if len(was) == 1 {
		return fmt.Sprintf("%s: id %s -> %d", r.Key, was[0], r.Now)
	}
	return fmt.Sprintf("%s: ids %s -> %d", r.Key, strings.Join(was, ", "), r.Now)
}

// NewRenumberedComments returns the existing comments head gives an id none of
// the parents holds for them. A comment is named by who wrote it, when, what it
// says, and which of several identical comments it is, so a comment that merely
// gains an id passes. So does an id any parent holds for the comment, which is
// how a merge inherits the ids its two sides carry instead of being refused.
//
// One case passes as well: an id a parent already carried on two comments, which
// a comment steps off. Such an id never named one comment, so moving off it is a
// repair, both of a file that arrived that way and of the collision the merge
// driver resolves when two branches add different comments under one id.
func NewRenumberedComments(head *File, parents ...*File) ([]RenumberedComment, error) {
	held := map[CommentKey]map[int64]bool{}
	parentOwners := map[int64]map[CommentKey]bool{}
	for _, p := range parents {
		err := p.eachComment(func(e commentEntry) error {
			if !e.hasID {
				return nil
			}
			if held[e.key] == nil {
				held[e.key] = map[int64]bool{}
			}
			held[e.key][e.id] = true
			if parentOwners[e.id] == nil {
				parentOwners[e.id] = map[CommentKey]bool{}
			}
			parentOwners[e.id][e.key] = true
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	var out []RenumberedComment
	err := head.eachComment(func(e commentEntry) error {
		was := held[e.key]
		if !e.hasID || len(was) == 0 || was[e.id] {
			return nil
		}
		var taken []int64
		for id := range was {
			if len(parentOwners[id]) > 1 {
				continue
			}
			taken = append(taken, id)
		}
		if len(taken) == 0 {
			return nil
		}
		slices.Sort(taken)
		out = append(out, RenumberedComment{Key: e.key, Was: taken, Now: e.id})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
