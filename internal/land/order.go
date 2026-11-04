package land

import (
	"fmt"
	"slices"
	"strings"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/beadsfile"
)

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
		if issues.Get(p.ID) == nil {
			return nil, nil, errors.WithNew("issue not found").Set("id", p.ID).Err()
		}
		index[p.ID] = i
	}
	open := func(e beadsfile.Edge) bool {
		t := issues.Get(e.To)
		return beadsfile.BlockingTypes[e.Type] && t != nil && !IsClosed(t)
	}
	g, err := beadsfile.NewGraph(issues, open)
	if err != nil {
		return nil, nil, err
	}
	inBatch, err := beadsfile.NewGraph(issues, func(e beadsfile.Edge) bool {
		_, from := index[e.From]
		_, to := index[e.To]
		return from && to && open(e)
	})
	if err != nil {
		return nil, nil, err
	}

	why := make([]string, len(planned))
	for i, p := range planned {
		var outside []string
		for _, e := range g.Out(p.ID) {
			if _, ok := index[e.To]; !ok && !slices.Contains(outside, e.To) {
				outside = append(outside, e.To)
			}
		}
		if len(outside) > 0 {
			why[i] = fmt.Sprintf("%s is blocked by %s, still open", p.ID, strings.Join(outside, ", "))
		}
	}
	for i, p := range planned {
		if why[i] != "" {
			continue
		}
		c := inBatch.CycleAt(p.ID)
		if c == nil {
			continue
		}
		chain := strings.Join(c.IDs(), " blocked by ")
		for _, e := range c {
			if j := index[e.From]; why[j] == "" {
				why[j] = fmt.Sprintf("%s is in a blocking cycle: %s", e.From, chain)
			}
		}
	}

	// Every cycle member is blocked now, so the walk below meets no cycle.
	visited := make([]bool, len(planned))
	var ordered []Planned
	var visit func(i int)
	visit = func(i int) {
		if visited[i] {
			return
		}
		visited[i] = true
		if why[i] != "" {
			return
		}
		for _, e := range inBatch.Out(planned[i].ID) {
			j := index[e.To]
			visit(j)
			if why[j] != "" && why[i] == "" {
				why[i] = fmt.Sprintf("%s is blocked by %s, which this run does not close", planned[i].ID, planned[j].ID)
			}
		}
		if why[i] == "" {
			ordered = append(ordered, planned[i])
		}
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
