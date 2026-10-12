// Package merge three-way merges br issues files field by field, so that two
// branches touching different parts of the same issue never conflict.
package merge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
)

// Side names one input of a merge.
type Side string

const (
	Ours   Side = "ours"
	Theirs Side = "theirs"
)

// Conflict is a field both sides changed to different values. The merge keeps
// the side whose updated_at is newer (ours on a tie).
type Conflict struct {
	ID     string
	Fields []string
	Kept   Side
	Ours   string
	Theirs string
}

// Rename is an issue ID theirs minted independently of ours, renumbered so both
// issues survive.
type Rename struct {
	Old, New string
}

// Renumber is a comment ID that collided with a different comment.
type Renumber struct {
	IssueID  string
	Old, New int64
}

// Report describes every decision the merge made that a person might want to
// check.
type Report struct {
	Conflicts []Conflict
	Renames   []Rename
	Renumbers []Renumber
}

// Clean reports whether the merge needed no judgement calls on field values.
func (r *Report) Clean() bool { return len(r.Conflicts) == 0 }

// String renders the report for a terminal.
func (r *Report) String() string {
	var b strings.Builder
	for _, c := range r.Conflicts {
		fmt.Fprintf(&b, "conflict %s [%s]: kept %s\n  ours:   %s\n  theirs: %s\n", c.ID, strings.Join(c.Fields, ","), c.Kept, c.Ours, c.Theirs)
	}
	for _, rn := range r.Renames {
		fmt.Fprintf(&b, "renamed %s -> %s (both sides created %s; theirs renumbered)\n", rn.Old, rn.New, rn.Old)
	}
	for _, rn := range r.Renumbers {
		fmt.Fprintf(&b, "comment on %s renumbered %d -> %d (id collided with another comment)\n", rn.IssueID, rn.Old, rn.New)
	}
	return b.String()
}

// closeGroup fields describe one state transition and are resolved together.
var closeGroup = []string{"status", "closed_at", "close_reason"}

// Merge combines ours and theirs relative to base. Issues and fields only one
// side changed take that side's value; comments, labels and dependencies merge
// as sets; IDs that both sides minted for different issues are renumbered.
func Merge(base, ours, theirs *beadsfile.File) (*beadsfile.File, *Report, error) {
	rep := &Report{}
	theirs, err := renameCollisions(base, ours, theirs, rep)
	if err != nil {
		return nil, nil, err
	}

	ids := map[string]struct{}{}
	for _, f := range []*beadsfile.File{base, ours, theirs} {
		for _, id := range f.IDs() {
			ids[id] = struct{}{}
		}
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)

	var out []*beadsfile.Issue
	for _, id := range sorted {
		iss, err := mergeOne(base.Get(id), ours.Get(id), theirs.Get(id), rep)
		if err != nil {
			return nil, nil, errors.With(err, "merging issue").Set("id", id).Err()
		}
		if iss != nil {
			out = append(out, iss)
		}
	}
	if err := assignCommentIDs(base, ours, theirs, out, rep); err != nil {
		return nil, nil, err
	}
	f, err := beadsfile.New(out)
	if err != nil {
		return nil, nil, err
	}
	dups, err := f.DuplicateCommentIDs()
	if err != nil {
		return nil, nil, err
	}
	if len(dups) > 0 {
		return nil, nil, errors.WithNew("merge produced a duplicate comment id").Set("duplicate", dups[0].String()).Err()
	}
	return f, rep, nil
}

func mergeOne(b, o, t *beadsfile.Issue, rep *Report) (*beadsfile.Issue, error) {
	switch {
	case o != nil && t != nil && o.Obj.Equal(t.Obj):
		return o, nil
	case b == nil && t == nil:
		return o, nil
	case b == nil && o == nil:
		return t, nil
	case b == nil:
		return mergeIssue(beadsfile.NewObject(), o, t, rep)
	case o == nil && t == nil:
		return nil, nil
	case o == nil:
		if t.Obj.Equal(b.Obj) {
			return nil, nil
		}
		rep.Conflicts = append(rep.Conflicts, Conflict{ID: b.ID, Fields: []string{"(issue)"}, Kept: Theirs, Ours: "deleted", Theirs: "modified"})
		return t, nil
	case t == nil:
		if o.Obj.Equal(b.Obj) {
			return nil, nil
		}
		rep.Conflicts = append(rep.Conflicts, Conflict{ID: b.ID, Fields: []string{"(issue)"}, Kept: Ours, Ours: "modified", Theirs: "deleted"})
		return o, nil
	case o.Obj.Equal(b.Obj):
		return t, nil
	case t.Obj.Equal(b.Obj):
		return o, nil
	default:
		return mergeIssue(b.Obj, o, t, rep)
	}
}

func mergeIssue(b *beadsfile.Object, o, t *beadsfile.Issue, rep *Report) (*beadsfile.Issue, error) {
	res := o.Obj.Clone()
	winner := Ours
	if newer(t.Obj, o.Obj) {
		winner = Theirs
	}
	pick := func(side Side, ov, tv json.RawMessage) json.RawMessage {
		if side == Theirs {
			return tv
		}
		return ov
	}

	handled := map[string]bool{"id": true}

	// The close group resolves as one value so status and its timestamps
	// never come from different sides.
	bg, og, tg := group(b, closeGroup), group(o.Obj, closeGroup), group(t.Obj, closeGroup)
	switch {
	case og == tg, tg == bg:
	case og == bg:
		copyFields(res, t.Obj, closeGroup)
	default:
		rep.Conflicts = append(rep.Conflicts, Conflict{ID: o.ID, Fields: closeGroup, Kept: winner, Ours: og, Theirs: tg})
		if winner == Theirs {
			copyFields(res, t.Obj, closeGroup)
		}
	}
	for _, k := range closeGroup {
		handled[k] = true
	}

	for _, k := range []string{"comments", "dependencies", "labels"} {
		handled[k] = true
		merged, err := mergeSetField(k, b.Raw(k), o.Obj.Raw(k), t.Obj.Raw(k))
		if err != nil {
			return nil, errors.With(err, "merging field").Set("field", k).Err()
		}
		setOrDelete(res, t.Obj, k, merged)
	}

	handled["updated_at"] = true
	if ou, tu := o.Obj.Raw("updated_at"), t.Obj.Raw("updated_at"); !bytes.Equal(ou, tu) {
		setOrDelete(res, t.Obj, "updated_at", pick(winner, ou, tu))
	}

	keys := o.Obj.Keys()
	for _, k := range t.Obj.Keys() {
		if !o.Obj.Has(k) {
			keys = append(keys, k)
		}
	}
	for _, k := range b.Keys() {
		if !o.Obj.Has(k) && !t.Obj.Has(k) {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		if handled[k] {
			continue
		}
		bv, ov, tv := b.Raw(k), o.Obj.Raw(k), t.Obj.Raw(k)
		switch {
		case bytes.Equal(ov, tv), bytes.Equal(tv, bv):
		case bytes.Equal(ov, bv):
			setOrDelete(res, t.Obj, k, tv)
		default:
			rep.Conflicts = append(rep.Conflicts, Conflict{ID: o.ID, Fields: []string{k}, Kept: winner, Ours: show(ov), Theirs: show(tv)})
			setOrDelete(res, t.Obj, k, pick(winner, ov, tv))
		}
	}

	if res.Equal(o.Obj) {
		return o, nil
	}
	if res.Equal(t.Obj) {
		return t, nil
	}
	return beadsfile.NewIssue(res)
}

// setOrDelete writes raw into res, or removes the key when raw is nil. A key res
// lacks is placed after the key that precedes it in ref.
func setOrDelete(res, ref *beadsfile.Object, key string, raw json.RawMessage) {
	if raw == nil {
		res.Delete(key)
		return
	}
	if res.Has(key) {
		res.SetRaw(key, raw)
		return
	}
	prev := ""
	for _, k := range ref.Keys() {
		if k == key {
			break
		}
		if res.Has(k) {
			prev = k
		}
	}
	res.InsertAfter(prev, key, raw)
}

func copyFields(dst, src *beadsfile.Object, keys []string) {
	for _, k := range keys {
		setOrDelete(dst, src, k, src.Raw(k))
	}
}

func group(o *beadsfile.Object, keys []string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + show(o.Raw(k))
	}
	return strings.Join(parts, " ")
}

func show(raw json.RawMessage) string {
	if raw == nil {
		return "(absent)"
	}
	s := string(raw)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// newer reports whether a's updated_at is strictly later than b's.
func newer(a, b *beadsfile.Object) bool {
	at, aok := timeField(a, "updated_at")
	bt, bok := timeField(b, "updated_at")
	switch {
	case aok && bok:
		return at.After(bt)
	default:
		return aok && !bok
	}
}

func timeField(o *beadsfile.Object, key string) (time.Time, bool) {
	s, ok := o.String(key)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

// mergeSetField merges an array field as a set: an element survives when both
// sides have it or one side added it, and goes when either side removed it.
func mergeSetField(key string, b, o, t json.RawMessage) (json.RawMessage, error) {
	if bytes.Equal(o, t) || bytes.Equal(t, b) {
		return o, nil
	}
	if bytes.Equal(o, b) {
		return t, nil
	}
	if key == "labels" {
		return mergeLabels(b, o, t)
	}
	identity := commentIdentity
	if key == "dependencies" {
		identity = dependencyIdentity
	}
	bs, err := beadsfile.ParseObjectArray(b)
	if err != nil {
		return nil, err
	}
	ous, err := beadsfile.ParseObjectArray(o)
	if err != nil {
		return nil, err
	}
	ts, err := beadsfile.ParseObjectArray(t)
	if err != nil {
		return nil, err
	}
	inBase := keySet(bs, identity)
	inOurs := keySet(ous, identity)
	inTheirs := keySet(ts, identity)
	var out []*beadsfile.Object
	for _, e := range ous {
		k := identity(e)
		if inTheirs[k] || !inBase[k] {
			out = append(out, e)
		}
	}
	for _, e := range ts {
		k := identity(e)
		if !inOurs[k] && !inBase[k] {
			out = append(out, e)
		}
	}
	if key == "comments" {
		sort.SliceStable(out, func(i, j int) bool {
			ci, _ := out[i].String("created_at")
			cj, _ := out[j].String("created_at")
			return ci < cj
		})
	}
	if len(out) == 0 {
		if o == nil {
			return nil, nil
		}
		return json.RawMessage("[]"), nil
	}
	return beadsfile.MarshalObjectArray(out)
}

func mergeLabels(b, o, t json.RawMessage) (json.RawMessage, error) {
	var bs, ous, ts []string
	for _, p := range []struct {
		raw json.RawMessage
		dst *[]string
	}{{b, &bs}, {o, &ous}, {t, &ts}} {
		if len(p.raw) == 0 || string(p.raw) == "null" {
			continue
		}
		if err := json.Unmarshal(p.raw, p.dst); err != nil {
			return nil, errors.With(err, "decoding labels").Err()
		}
	}
	inBase, inOurs, inTheirs := strSet(bs), strSet(ous), strSet(ts)
	var out []string
	for _, l := range ous {
		if inTheirs[l] || !inBase[l] {
			out = append(out, l)
		}
	}
	for _, l := range ts {
		if !inOurs[l] && !inBase[l] {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		if o == nil {
			return nil, nil
		}
		return json.RawMessage("[]"), nil
	}
	parts := make([]string, len(out))
	for i, l := range out {
		parts[i] = string(beadsfile.MarshalString(l))
	}
	return json.RawMessage("[" + strings.Join(parts, ",") + "]"), nil
}

func strSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func keySet(objs []*beadsfile.Object, identity func(*beadsfile.Object) string) map[string]bool {
	m := make(map[string]bool, len(objs))
	for _, o := range objs {
		m[identity(o)] = true
	}
	return m
}

// commentIdentity names a comment by what was said, by whom, and when — not by
// its ID, which br assigns from a per-database counter.
func commentIdentity(c *beadsfile.Object) string {
	return string(c.Raw("author")) + "\x00" + string(c.Raw("created_at")) + "\x00" + string(c.Raw("text"))
}

func dependencyIdentity(d *beadsfile.Object) string {
	return string(d.Raw("depends_on_id")) + "\x00" + string(d.Raw("type"))
}

var childID = regexp.MustCompile(`^(.+)\.(\d+)$`)

// renameCollisions renumbers child issues theirs created under an ID ours also
// created for a different issue (told apart by created_at). It returns theirs
// with every reference to a renamed ID rewritten.
func renameCollisions(base, ours, theirs *beadsfile.File, rep *Report) (*beadsfile.File, error) {
	taken := map[string]bool{}
	for _, f := range []*beadsfile.File{base, ours, theirs} {
		for _, id := range f.IDs() {
			taken[id] = true
		}
	}
	renames := map[string]string{}
	for _, id := range theirs.IDs() {
		o, t := ours.Get(id), theirs.Get(id)
		if base.Get(id) != nil || o == nil {
			continue
		}
		if bytes.Equal(o.Obj.Raw("created_at"), t.Obj.Raw("created_at")) {
			continue
		}
		m := childID.FindStringSubmatch(id)
		if m == nil {
			return nil, errors.WithNew("both sides created different issues with the same non-child id; resolve by hand").Set("id", id).Err()
		}
		next, _ := strconv.Atoi(m[2])
		for taken[fmt.Sprintf("%s.%d", m[1], next)] {
			next++
		}
		nid := fmt.Sprintf("%s.%d", m[1], next)
		taken[nid] = true
		renames[id] = nid
		rep.Renames = append(rep.Renames, Rename{Old: id, New: nid})
	}
	if len(renames) == 0 {
		return theirs, nil
	}
	issues := make([]*beadsfile.Issue, 0, len(theirs.Issues))
	for _, iss := range theirs.Issues {
		obj := iss.Obj.Clone()
		changed := false
		if nid, ok := renames[iss.ID]; ok {
			obj.SetString("id", nid)
			changed = true
		}
		for _, k := range []string{"dependencies", "comments"} {
			arr, err := beadsfile.ParseObjectArray(obj.Raw(k))
			if err != nil {
				return nil, errors.With(err, "reading field").Set("id", iss.ID, "field", k).Err()
			}
			touched := false
			for _, e := range arr {
				for _, ref := range []string{"issue_id", "depends_on_id"} {
					if s, ok := e.String(ref); ok {
						if nid, ok := renames[s]; ok {
							e.SetString(ref, nid)
							touched = true
						}
					}
				}
			}
			if touched {
				raw, err := beadsfile.MarshalObjectArray(arr)
				if err != nil {
					return nil, err
				}
				obj.SetRaw(k, raw)
				changed = true
			}
		}
		if !changed {
			issues = append(issues, iss)
			continue
		}
		ni, err := beadsfile.NewIssue(obj)
		if err != nil {
			return nil, err
		}
		issues = append(issues, ni)
	}
	return beadsfile.New(issues)
}

// commentKey names one comment within one issue. Identity alone is not enough:
// br users post the same text on several issues in the same second.
type commentKey struct {
	issue, identity string
}

// commentRank orders claims on a comment id; a lower rank claims first.
type commentRank int

const (
	// rankBase: the comment carries the id base gave it, so an issue line
	// neither side changed never loses an id.
	rankBase commentRank = iota
	// rankOurs: the comment carries ours' id for it.
	rankOurs
	// rankAdded: the comment came from theirs alone.
	rankAdded
)

type commentSlot struct {
	issue int
	obj   *beadsfile.Object
	key   commentKey
	want  int64
	rank  commentRank
}

// assignCommentIDs makes comment ids in out unique across the file, as br
// requires. A comment ours has takes ours' id for it; claims then go in rank
// order, and a comment whose id is already claimed by a different comment gets
// the next id above every id in base, ours and theirs.
func assignCommentIDs(base, ours, theirs *beadsfile.File, out []*beadsfile.Issue, rep *Report) error {
	baseIDs, maxBase, err := commentIDs(base)
	if err != nil {
		return err
	}
	oursIDs, maxOurs, err := commentIDs(ours)
	if err != nil {
		return err
	}
	_, maxTheirs, err := commentIDs(theirs)
	if err != nil {
		return err
	}
	maxID := max(maxBase, maxOurs, maxTheirs)

	parsed := make([][]*beadsfile.Object, len(out))
	var slots []commentSlot
	for i, iss := range out {
		cs, err := beadsfile.ParseObjectArray(iss.Obj.Raw("comments"))
		if err != nil {
			return errors.With(err, "reading comments").Set("id", iss.ID).Err()
		}
		parsed[i] = cs
		for _, c := range cs {
			id, ok := beadsfile.CommentID(c)
			if !ok {
				continue
			}
			key := commentKey{issue: iss.ID, identity: commentIdentity(c)}
			s := commentSlot{issue: i, obj: c, key: key, want: id, rank: rankAdded}
			if oid, ok := oursIDs[key]; ok {
				s.want, s.rank = oid, rankOurs
			}
			if bid, ok := baseIDs[key]; ok && bid == s.want {
				s.rank = rankBase
			}
			slots = append(slots, s)
		}
	}
	sort.SliceStable(slots, func(a, b int) bool { return slots[a].rank < slots[b].rank })

	claimed := map[int64]bool{}
	touched := map[int]bool{}
	for _, s := range slots {
		id := s.want
		if claimed[id] {
			maxID++
			id = maxID
			rep.Renumbers = append(rep.Renumbers, Renumber{IssueID: out[s.issue].ID, Old: s.want, New: id})
		}
		claimed[id] = true
		if cur, _ := beadsfile.CommentID(s.obj); cur != id {
			s.obj.SetRaw("id", json.RawMessage(strconv.FormatInt(id, 10)))
			touched[s.issue] = true
		}
	}

	for i := range out {
		if !touched[i] {
			continue
		}
		raw, err := beadsfile.MarshalObjectArray(parsed[i])
		if err != nil {
			return err
		}
		obj := out[i].Obj.Clone()
		obj.SetRaw("comments", raw)
		ni, err := beadsfile.NewIssue(obj)
		if err != nil {
			return err
		}
		out[i] = ni
	}
	return nil
}

// commentIDs maps each comment in f to its id, and returns the largest id.
func commentIDs(f *beadsfile.File) (map[commentKey]int64, int64, error) {
	ids := map[commentKey]int64{}
	var maxID int64
	for _, iss := range f.Issues {
		cs, err := beadsfile.ParseObjectArray(iss.Obj.Raw("comments"))
		if err != nil {
			return nil, 0, errors.With(err, "reading comments").Set("id", iss.ID).Err()
		}
		for _, c := range cs {
			id, ok := beadsfile.CommentID(c)
			if !ok {
				continue
			}
			ids[commentKey{issue: iss.ID, identity: commentIdentity(c)}] = id
			maxID = max(maxID, id)
		}
	}
	return ids, maxID, nil
}
