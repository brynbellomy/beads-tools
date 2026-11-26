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

// String renders the identity as a map key. It is the identity two comments are
// matched by, so it uses the canonical instant rather than the spelling.
func (i CommentIdentity) String() string {
	return strings.Join([]string{i.Author, i.CreatedKey(), i.Text}, "\x00")
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

// noteKey is what two comments are matched by: the bead and the identity in its
// canonical form. It is a string rather than the CommentIdentity struct so that a
// respelled timestamp compares equal, and it carries no position, since identical
// comments on one bead are interchangeable.
type noteKey struct {
	IssueID string
	ident   string
}

// noteOf returns the key two comments are matched by.
func noteOf(issueID string, c CommentIdentity) noteKey {
	return noteKey{IssueID: issueID, ident: c.String()}
}

// commentEntry is one comment of one issue.
type commentEntry struct {
	key   CommentKey
	note  noteKey
	idx   int // which of several identical comments on the bead, in file order
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
		for i, key := range CommentKeys(iss.ID, cs) {
			id, ok := CommentID(cs[i])
			out = append(out, commentEntry{
				key:   key,
				note:  noteOf(iss.ID, key.CommentIdentity),
				idx:   key.Occurrence,
				id:    id,
				hasID: ok,
			})
		}
	}
	f.comments = &out
	return out, nil
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
// the parents holds for them. A comment is named by the bead it is on, who wrote
// it, when, and what it says, so it carries no position: a reordering among
// byte-identical comments never renumbers anything. An id counts as given up only
// when head no longer holds it, and only when the merge driver had no reason to
// move that comment off it.
//
// The driver reconciles br's file-wide id namespace in two places, and both are
// per (comment, id), never per id alone:
//
//   - within one parent, an id the same bead used twice;
//   - across the two parents, an id each side used once on a different bead.
//
// Any other id a comment leaves is a renumbering, however many other comments
// happened to use that id.
func NewRenumberedComments(head *File, parents ...*File) ([]RenumberedComment, error) {
	// held: the ids each parent gave each comment, so a lost id can be spotted.
	held := map[noteKey]map[int64]bool{}
	// heldBy: the same ids, one set per parent, so a merge that took either
	// side's ids wholesale reads as inheritance rather than loss.
	heldBy := map[noteKey][]map[int64]bool{}
	// sharedWithinBead: (comment, id) pairs one parent used twice on one bead,
	// which never named a single comment and which the driver repairs.
	sharedWithinBead := map[noteKey]map[int64]bool{}
	// dupOneParent: ids one parent already used twice, which no merge created.
	dupOneParent := map[int64]bool{}

	for _, p := range parents {
		noteIDs := map[noteKey]map[int64]bool{}
		beadCounts := map[string]map[int64]int{}
		entries := map[int64][]noteKey{}
		perParentUses := map[int64]int{}
		err := p.eachComment(func(e commentEntry) error {
			if !e.hasID {
				return nil
			}
			if held[e.note] == nil {
				held[e.note] = map[int64]bool{}
			}
			held[e.note][e.id] = true
			if noteIDs[e.note] == nil {
				noteIDs[e.note] = map[int64]bool{}
			}
			noteIDs[e.note][e.id] = true
			if beadCounts[e.note.IssueID] == nil {
				beadCounts[e.note.IssueID] = map[int64]int{}
			}
			beadCounts[e.note.IssueID][e.id]++
			entries[e.id] = append(entries[e.id], e.note)
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
		// An id this parent put on the same bead more than once.
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

	// An id each parent used once, on different beads: a collision only combining
	// the two makes, which the driver resolves by keeping the id on one side and
	// moving the other comment to a fresh one. crossClaims records the two comments
	// that made such a collision, so only one of them may leave the id. An id one
	// parent used twice is a duplicate, not a collision, and exempts nobody.
	sides := map[int64]map[noteKey]int{}
	for _, p := range parents {
		thisSide := map[noteKey]bool{}
		err := p.eachComment(func(e commentEntry) error {
			if !e.hasID || thisSide[e.note] {
				return nil
			}
			thisSide[e.note] = true
			if sides[e.id] == nil {
				sides[e.id] = map[noteKey]int{}
			}
			sides[e.id][e.note]++
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	crossClaims := map[int64][]noteKey{}
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

	// stillHeld: the ids head gives each comment now.
	stillHeld := map[noteKey]map[int64]bool{}
	var order []noteKey
	firstSeen := map[noteKey]CommentKey{}
	err := head.eachComment(func(e commentEntry) error {
		if _, ok := firstSeen[e.note]; !ok {
			firstSeen[e.note] = e.key
			order = append(order, e.note)
		}
		if !e.hasID {
			return nil
		}
		if stillHeld[e.note] == nil {
			stillHeld[e.note] = map[int64]bool{}
		}
		stillHeld[e.note][e.id] = true
		return nil
	})
	if err != nil {
		return nil, err
	}

	var out []RenumberedComment
	for _, note := range order {
		before := held[note]
		if len(before) == 0 {
			// A comment head adds: no parent had an id to keep.
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

// keptByTheOther reports whether note may leave id because it is one of the two
// comments a cross-parent collision put on that id, and the other one still holds
// it. That is the state the merge driver leaves behind when it keeps one side's id
// and moves the other's comment.
func keptByTheOther(note noteKey, id int64, claims map[int64][]noteKey, stillHeld map[noteKey]map[int64]bool) bool {
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
