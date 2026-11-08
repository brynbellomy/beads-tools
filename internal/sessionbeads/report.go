package sessionbeads

import (
	"encoding/json"
	"io"
	"text/tabwriter"

	"github.com/brynbellomy/go-utils/errors"
)

// Report prints beads as a table: one row per bead, with its id, title, status,
// type, the date it was last modified and its parent epic.
func Report(w io.Writer, beads []Bead) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := io.WriteString(tw, "ID\tTITLE\tSTATUS\tTYPE\tMODIFIED\tPARENT\n"); err != nil {
		return errors.With(err, "writing the report").Err()
	}
	for _, b := range beads {
		parent := b.Parent
		if parent == "" {
			parent = "-"
		}
		if _, err := io.WriteString(tw, b.ID+"\t"+b.Title+"\t"+b.Status+"\t"+b.Type+"\t"+b.Updated+"\t"+parent+"\n"); err != nil {
			return errors.With(err, "writing the report").Err()
		}
	}
	return errors.With(tw.Flush(), "writing the report").Err()
}

// ReportJSON prints beads as a JSON array, one object per bead. An empty list
// prints [], never null.
func ReportJSON(w io.Writer, beads []Bead) error {
	type row struct {
		ID         string  `json:"id"`
		Title      string  `json:"title"`
		Status     string  `json:"status"`
		Type       string  `json:"type"`
		Updated    string  `json:"updated_at"`
		ParentEpic *string `json:"parent_epic"`
	}
	rows := make([]row, 0, len(beads))
	for _, b := range beads {
		r := row{ID: b.ID, Title: b.Title, Status: b.Status, Type: b.Type, Updated: b.Updated}
		if b.Parent != "" {
			parent := b.Parent
			r.ParentEpic = &parent
		}
		rows = append(rows, r)
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return errors.With(enc.Encode(rows), "writing the report").Err()
}
