package beadsfile

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

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
	Author string
	// CreatedAt is the instant as it was written, for messages. Two spellings of
	// one instant (another zone, another precision) are compared by CreatedKey.
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

// CreatedKey is the instant CreatedAt names, in one canonical form, so that a
// respelled timestamp compares equal to the original. A timestamp that does not
// parse is compared as written, which keeps it distinct from a parseable one.
func (i CommentIdentity) CreatedKey() string {
	t, err := time.Parse(time.RFC3339Nano, i.CreatedAt)
	if err != nil {
		return "raw:" + i.CreatedAt
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// String renders the identity in its canonical timestamp form.
func (i CommentIdentity) String() string {
	return strings.Join([]string{i.Author, i.CreatedKey(), i.Text}, "\x00")
}

// CommentKey names a comment by its issue and identity, without a position.
// Identical comments on one issue are interchangeable and share one key.
type CommentKey struct {
	IssueID string
	CommentIdentity
}

// NewCommentKey returns the shared identity of a comment on issueID. Its
// creation instant is canonicalized so every caller uses the same map key,
// while String keeps that UTC form in its message.
func NewCommentKey(issueID string, c *Object) CommentKey {
	identity := IdentityOf(c)
	identity.CreatedAt = identity.CreatedKey()
	return CommentKey{IssueID: issueID, CommentIdentity: identity}
}

// String describes the comment for a message.
func (k CommentKey) String() string {
	return fmt.Sprintf("comment %q by %q at %q on %s", k.Text, k.Author, k.CreatedAt, k.IssueID)
}

// commentEntry is one comment of one issue.
type commentEntry struct {
	key   CommentKey
	id    int64
	hasID bool
}

// eachComment calls fn for every comment in f, in file order.
//
// The comments array of each issue is decoded once per file and kept: the guard
// walks a file's comments three times over, and once per parent as well, where it
// used to re-decode every comment on every walk. A file is read-only once
// parsed, so the index is safe to share.
func (f *File) eachComment(fn func(commentEntry) error) error {
	idx, err := f.commentIndex()
	if err != nil {
		return err
	}
	for _, e := range idx {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// commentIndex returns every comment in f, in file order, decoding it once.
func (f *File) commentIndex() ([]commentEntry, error) {
	if f.comments != nil {
		return *f.comments, nil
	}
	var out []commentEntry
	for _, iss := range f.Issues {
		cs, err := ParseObjectArray(iss.Obj.Raw("comments"))
		if err != nil {
			return nil, errors.With(err, "reading comments").Set("id", iss.ID).Err()
		}
		for _, c := range cs {
			id, ok := CommentID(c)
			out = append(out, commentEntry{
				key:   NewCommentKey(iss.ID, c),
				id:    id,
				hasID: ok,
			})
		}
	}
	f.comments = &out
	return out, nil
}

// CommentIDs groups each comment's IDs by shared identity in file order, and
// returns the largest ID. Ordered IDs let the merge driver assign interchangeable
// copies without making their positions part of their identity.
func (f *File) CommentIDs() (map[CommentKey][]int64, int64, error) {
	ids := map[CommentKey][]int64{}
	var maxID int64
	err := f.eachComment(func(e commentEntry) error {
		if e.hasID {
			ids[e.key] = append(ids[e.key], e.id)
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

// CommentIndex is a read-only view of a file's decoded comment index. It
// does not expose the file or its mutable objects.
type CommentIndex struct {
	file *File
}

// CommentIndex returns the file's decoded comment index.
func (f *File) CommentIndex() *CommentIndex {
	return &CommentIndex{file: f}
}

// NewRenumberedComments returns existing comments whose IDs no parent holds.
// CommentKey matches comments by bead, author, canonical creation instant and
// text, without position; identical comments on one bead are interchangeable.
func NewRenumberedComments(head *File, parents ...*File) ([]RenumberedComment, error) {
	return head.CommentIndex().RenumberedComments(parents...)
}

// RenumberedComments returns head's existing comments whose IDs no parent holds.
// Parent analysis is local to this call; only the head's decoded comments are
// retained, so different parents can reuse this index safely.
func (i *CommentIndex) RenumberedComments(parents ...*File) ([]RenumberedComment, error) {
	head := i.file
	// held records IDs lost from each comment since its parents.
	held := map[CommentKey]map[int64]bool{}
	// heldBy distinguishes inheritance of one parent's whole ID set from loss.
	heldBy := map[CommentKey][]map[int64]bool{}
	// sharedWithinBead exempts repairs of IDs duplicated within one bead.
	sharedWithinBead := map[CommentKey]map[int64]bool{}
	// dupOneParent excludes inherited duplicates from the cross-parent exemption.
	dupOneParent := map[int64]bool{}
	for _, p := range parents {
		noteIDs := map[CommentKey]map[int64]bool{}
		beadCounts := map[string]map[int64]int{}
		entries := map[int64][]CommentKey{}
		perParentUses := map[int64]int{}
		err := p.eachComment(func(e commentEntry) error {
			if !e.hasID {
				return nil
			}
			if held[e.key] == nil {
				held[e.key] = map[int64]bool{}
			}
			held[e.key][e.id] = true
			if noteIDs[e.key] == nil {
				noteIDs[e.key] = map[int64]bool{}
			}
			noteIDs[e.key][e.id] = true
			if beadCounts[e.key.IssueID] == nil {
				beadCounts[e.key.IssueID] = map[int64]int{}
			}
			beadCounts[e.key.IssueID][e.id]++
			entries[e.id] = append(entries[e.id], e.key)
			perParentUses[e.id]++
			return nil
		})
		if err != nil {
			return nil, err
		}
		for note, ids := range noteIDs {
			heldBy[note] = append(heldBy[note], ids)
		}
		for id, n := range perParentUses {
			if n > 1 {
				dupOneParent[id] = true
			}
		}
		for bead, counts := range beadCounts {
			for id, n := range counts {
				if n < 2 {
					continue
				}
				for _, note := range entries[id] {
					if note.IssueID != bead {
						continue
					}
					if sharedWithinBead[note] == nil {
						sharedWithinBead[note] = map[int64]bool{}
					}
					sharedWithinBead[note][id] = true
				}
			}
		}
	}

	// crossClaims permits one claimant to leave an ID each parent used on a
	// different bead when the other claimant keeps it; that is a driver repair.
	sides := map[int64]map[CommentKey]int{}
	for _, p := range parents {
		thisSide := map[CommentKey]bool{}
		err := p.eachComment(func(e commentEntry) error {
			if !e.hasID || thisSide[e.key] {
				return nil
			}
			thisSide[e.key] = true
			if sides[e.id] == nil {
				sides[e.id] = map[CommentKey]int{}
			}
			sides[e.id][e.key]++
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	crossClaims := map[int64][]CommentKey{}
	for id, notes := range sides {
		if dupOneParent[id] || len(parents) < 2 {
			continue
		}
		beads := map[string]bool{}
		for note := range notes {
			beads[note.IssueID] = true
		}
		if len(beads) < 2 {
			continue
		}
		for note := range notes {
			crossClaims[id] = append(crossClaims[id], note)
		}
	}

	// stillHeld records the IDs the head now gives each comment.
	stillHeld := map[CommentKey]map[int64]bool{}
	var order []CommentKey
	firstSeen := map[CommentKey]CommentKey{}
	err := head.eachComment(func(e commentEntry) error {
		if _, ok := firstSeen[e.key]; !ok {
			firstSeen[e.key] = e.key
			order = append(order, e.key)
		}
		if !e.hasID {
			return nil
		}
		if stillHeld[e.key] == nil {
			stillHeld[e.key] = map[int64]bool{}
		}
		stillHeld[e.key][e.id] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []RenumberedComment
	for _, note := range order {
		before := held[note]
		if len(before) == 0 {
			continue
		}
		now := stillHeld[note]
		if inheritsAny(heldBy[note], now) {
			continue
		}
		var lost []int64
		for id := range before {
			if now[id] || sharedWithinBead[note][id] || keptByTheOther(note, id, crossClaims, stillHeld) {
				continue
			}
			// Deleting one of two identical comments reports the other ID as
			// renumbered: the set identity remains, but an ID its parent held is gone.
			lost = append(lost, id)
		}
		if len(lost) == 0 {
			continue
		}
		slices.Sort(lost)
		out = append(out, RenumberedComment{Key: firstSeen[note], Was: lost, Now: smallestNew(before, now)})
	}
	return out, nil
}

// keptByTheOther reports whether the driver may have moved note off an ID
// because the other claimant from a cross-parent collision still holds it.
func keptByTheOther(note CommentKey, id int64, claims map[int64][]CommentKey, stillHeld map[CommentKey]map[int64]bool) bool {
	for _, claimant := range claims[id] {
		if claimant != note && stillHeld[claimant][id] {
			return true
		}
	}
	return false
}

// inheritsAny reports whether head gives the comment exactly the ids one parent
// gave it, as a merge taking that side's ids does. Matching a whole set rather
// than any single id is what tells an inherited id from the survivor of a
// renumbering: a comment that had 7 and 8 and now has only 7 gave an id up. Each
// parent is matched on its own, since a merge takes either side, not a mixture.
func inheritsAny(parents []map[int64]bool, now map[int64]bool) bool {
	for _, before := range parents {
		if len(before) != len(now) {
			continue
		}
		same := true
		for id := range now {
			if !before[id] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

// smallestNew returns the smallest id head gave the comment that no parent did,
// or the smallest it has when every id is an old one.
func smallestNew(before, now map[int64]bool) int64 {
	var added, all []int64
	for id := range now {
		all = append(all, id)
		if !before[id] {
			added = append(added, id)
		}
	}
	if len(added) > 0 {
		slices.Sort(added)
		return added[0]
	}
	slices.Sort(all)
	if len(all) > 0 {
		return all[0]
	}
	return 0
}
