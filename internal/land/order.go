package land

import (
	"fmt"
	"slices"
	"strings"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
)

// blockingTypes are the dependency types over which br refuses to close an
// issue while their target is open.
var blockingTypes = map[string]bool{"blocks": true, "waits-for": true, "conditional-blocks": true}

// Blocked is a planned close that br would refuse without --force, and why.
type Blocked struct {
	Planned
	Why string
}

// Order arranges planned so that each issue comes after every planned issue
// that blocks it, keeping planned's order otherwise. An issue br would refuse
// to close is returned as blocked instead: one with an open blocker outside
// planned, one in a blocking cycle, and one whose blocker is itself blocked.
func Order(planned []Planned, issues *beadsfile.File) ([]Planned, []Blocked, error) {
	index := make(map[string]int, len(planned))
	for i, p := range planned {
		index[p.ID] = i
	}
	inBatch := make([][]int, len(planned))
	why := make([]string, len(planned))
	for i, p := range planned {
		blockers, err := openBlockers(issues, p.ID)
		if err != nil {
			return nil, nil, err
		}
		var outside []string
		for _, b := range blockers {
			if j, ok := index[b]; ok {
				inBatch[i] = append(inBatch[i], j)
			} else {
				outside = append(outside, b)
			}
		}
		if len(outside) > 0 {
			why[i] = fmt.Sprintf("%s is blocked by %s, still open", p.ID, strings.Join(outside, ", "))
		}
	}

	const (
		unvisited = iota
		visiting
		visited
	)
	state := make([]int, len(planned))
	var stack []int
	var ordered []Planned
	var visit func(i int) bool
	visit = func(i int) bool {
		switch state[i] {
		case visited:
			return why[i] == ""
		case visiting:
			cycle := stack[slices.Index(stack, i):]
			names := make([]string, 0, len(cycle)+1)
			for _, c := range cycle {
				names = append(names, planned[c].ID)
			}
			chain := strings.Join(append(names, planned[i].ID), " blocked by ")
			for _, c := range cycle {
				if why[c] == "" {
					why[c] = fmt.Sprintf("%s is in a blocking cycle: %s", planned[c].ID, chain)
				}
			}
			return false
		}
		state[i] = visiting
		stack = append(stack, i)
		for _, j := range inBatch[i] {
			if !visit(j) && why[i] == "" {
				why[i] = fmt.Sprintf("%s is blocked by %s, which this run does not close", planned[i].ID, planned[j].ID)
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = visited
		if why[i] == "" {
			ordered = append(ordered, planned[i])
		}
		return why[i] == ""
	}
	for i := range planned {
		visit(i)
	}

	var blocked []Blocked
	for i, p := range planned {
		if why[i] != "" {
			blocked = append(blocked, Blocked{Planned: p, Why: why[i]})
		}
	}
	return ordered, blocked, nil
}

// openBlockers returns the issues that block id and are open, as br sees
// them: a blocker that is closed, a tombstone or missing blocks nothing.
func openBlockers(issues *beadsfile.File, id string) ([]string, error) {
	iss := issues.Get(id)
	if iss == nil {
		return nil, errors.WithNew("issue not found").Set("id", id).Err()
	}
	deps, err := beadsfile.ParseObjectArray(iss.Obj.Raw("dependencies"))
	if err != nil {
		return nil, errors.With(err, "reading dependencies").Set("id", id).Err()
	}
	var out []string
	for _, d := range deps {
		typ, _ := d.String("type")
		target, _ := d.String("depends_on_id")
		if !blockingTypes[typ] || slices.Contains(out, target) {
			continue
		}
		if t := issues.Get(target); t != nil && !IsClosed(t) {
			out = append(out, target)
		}
	}
	return out, nil
}
