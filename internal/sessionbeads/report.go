package sessionbeads

import (
	"context"
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

// TouchedIn returns the beads the session touched: those of the stores its
// copied rows name, and those of the checkout at workDir, which a live session
// may not have copied yet.
func TouchedIn(ctx context.Context, sessionDir, workDir, sessionID, actorFormat string) ([]Bead, error) {
	stores, err := Stores(sessionDir)
	if err != nil {
		return nil, err
	}
	if store, ok := CheckoutStore(workDir); ok {
		stores = append(stores, store)
	}
	return Touched(ctx, stores, sessionID, actorFormat)
}
