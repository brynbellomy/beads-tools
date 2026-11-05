package beadsfile

import (
	"fmt"
	"slices"
	"strings"

	"github.com/brynbellomy/go-utils/errors"
)

// Edge is one step of the dependency graph, in the direction br dep cycles
// walks it: a parent-child dependency goes from the parent to the child, and
// every other type from the dependent issue to the issue it depends on.
type Edge struct {
	From, To, Type string
}

func (e Edge) String() string {
	switch {
	case e.Type == "parent-child":
		return e.From + " parent of " + e.To
	case e.Type == "related":
		return e.From + " related to " + e.To
	case BlockingTypes[e.Type]:
		return fmt.Sprintf("%s waits on %s (%s)", e.From, e.To, e.Type)
	default:
		return fmt.Sprintf("%s depends on %s (%s)", e.From, e.To, e.Type)
	}
}

// BlockingTypes are the dependency types over which br refuses to close an
// issue while their target is open.
var BlockingTypes = map[string]bool{"blocks": true, "waits-for": true, "conditional-blocks": true}

// Graph is the dependency graph of an issues file.
type Graph struct {
	out map[string][]Edge
	all []Edge
}

// NewGraph builds the graph of f's dependencies, keeping the edges keep
// accepts, or every edge when keep is nil. Edges keep file order.
func NewGraph(f *File, keep func(Edge) bool) (*Graph, error) {
	g := &Graph{out: map[string][]Edge{}}
	for _, iss := range f.Issues {
		deps, err := ParseObjectArray(iss.Obj.Raw("dependencies"))
		if err != nil {
			return nil, errors.With(err, "reading dependencies").Set("id", iss.ID).Err()
		}
		for _, d := range deps {
			typ, _ := d.String("type")
			target, _ := d.String("depends_on_id")
			e := Edge{From: iss.ID, To: target, Type: typ}
			if typ == "parent-child" {
				e.From, e.To = target, iss.ID
			}
			if keep != nil && !keep(e) {
				continue
			}
			g.out[e.From] = append(g.out[e.From], e)
			g.all = append(g.all, e)
		}
	}
	return g, nil
}

// Out returns the edges leaving id.
func (g *Graph) Out(id string) []Edge { return g.out[id] }

// Edges returns every edge.
func (g *Graph) Edges() []Edge { return g.all }

// Has reports whether the graph holds e.
func (g *Graph) Has(e Edge) bool { return slices.Contains(g.out[e.From], e) }

// CycleThrough returns the shortest cycle that starts with e, or nil when e
// closes none.
func (g *Graph) CycleThrough(e Edge) Cycle {
	if !g.Has(e) {
		return nil
	}
	prev := map[string]Edge{}
	seen := map[string]bool{e.To: true}
	queue := []string{e.To}
	for len(queue) > 0 && !seen[e.From] {
		at := queue[0]
		queue = queue[1:]
		for _, next := range g.out[at] {
			if !seen[next.To] {
				seen[next.To] = true
				prev[next.To] = next
				queue = append(queue, next.To)
			}
		}
	}
	if !seen[e.From] {
		return nil
	}
	var back Cycle
	for at := e.From; at != e.To; at = prev[at].From {
		back = append(back, prev[at])
	}
	slices.Reverse(back)
	return append(Cycle{e}, back...)
}

// CycleAt returns the first cycle, in edge order, through an edge leaving id,
// or nil when id is on none.
func (g *Graph) CycleAt(id string) Cycle {
	for _, e := range g.out[id] {
		if c := g.CycleThrough(e); c != nil {
			return c
		}
	}
	return nil
}

// Cycle is a closed path of edges: each edge starts where the last one ended.
type Cycle []Edge

// IDs returns the issues along the cycle, its first issue repeated at the end.
func (c Cycle) IDs() []string {
	out := make([]string, 0, len(c)+1)
	for _, e := range c {
		out = append(out, e.From)
	}
	return append(out, c[0].From)
}

func (c Cycle) String() string {
	steps := make([]string, len(c))
	for i, e := range c {
		steps[i] = e.String()
	}
	return strings.Join(steps, ", ")
}

// heldBy reports whether g holds every edge of c.
func (c Cycle) heldBy(g *Graph) bool {
	for _, e := range c {
		if !g.Has(e) {
			return false
		}
	}
	return true
}

func (c Cycle) key() string {
	steps := make([]string, len(c))
	for i, e := range c {
		steps[i] = e.From + "\x00" + e.To + "\x00" + e.Type
	}
	slices.Sort(steps)
	return strings.Join(steps, "\x01")
}

// NewCycles returns the dependency cycles, over every edge type, that head
// holds and no file in parents does. A cycle is identified by its edges, so
// head inherits a parent's cycle however it edits the issues on it.
func NewCycles(head *File, parents ...*File) ([]Cycle, error) {
	g, err := NewGraph(head, nil)
	if err != nil {
		return nil, err
	}
	pgs := make([]*Graph, len(parents))
	for i, p := range parents {
		if pgs[i], err = NewGraph(p, nil); err != nil {
			return nil, err
		}
	}
	var out []Cycle
	seen := map[string]bool{}
	for _, e := range g.Edges() {
		if len(pgs) > 0 && !slices.ContainsFunc(pgs, func(pg *Graph) bool { return !pg.Has(e) }) {
			continue
		}
		c := g.CycleThrough(e)
		if c == nil || seen[c.key()] || slices.ContainsFunc(pgs, c.heldBy) {
			continue
		}
		seen[c.key()] = true
		out = append(out, c)
	}
	return out, nil
}
