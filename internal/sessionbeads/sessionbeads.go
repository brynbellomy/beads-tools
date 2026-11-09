// Package sessionbeads answers which beads a sirene session touched.
//
// The answer comes from the session's own beads-events.jsonl. Sirene copies
// every beads event row its agents wrote into that file, so the file names the
// beads of the session and outlives the removal of a worktree and a br rebuild
// of a store's database. Each row carries the issue's title, status, type and
// updated time as its store held them. A live read of the stores the file names
// fills in what the rows do not carry: a fresher title, status, type and
// updated time, and the parent epic.
package sessionbeads

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/brynbellomy/go-utils/errors"
)

const (
	beadsDirName    = ".beads"
	redirectName    = "redirect"
	databaseName    = "beads.db"
	beadsEventsFile = "beads-events.jsonl"
)

// Bead is one bead a session touched, as the report prints it.
type Bead struct {
	ID     string
	Title  string
	Status string
	// Type is the issue's type ("task", "bug", "epic", …). A live store that
	// still holds the bead supplies it; otherwise the session's copied rows
	// do, so it is empty only when neither carries one.
	Type string
	// Updated is the time of the bead's newest word on it: the store's own
	// updated_at when a store still holds it, else the newest copied row's
	// updated_at, else the newest copied row's created_at.
	Updated string
	// Parent is the bead's parent-child parent, empty when it has none. Only
	// a live store supplies it.
	Parent string
}

// row is one line of a session's beads-events.jsonl: a beads event row that
// sirene copied out of a store, with the issue's title, status, type and
// updated time as that store held them at the time. The last two are empty on
// a row sirene wrote before it copied them.
type row struct {
	Store     string `json:"store"`
	IssueID   string `json:"issue_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	IssueType string `json:"issue_type"`
	UpdatedAt string `json:"updated_at"`
	CreatedAt string `json:"created_at"`
}

// lastChange is the latest moment the row knows of for the issue: the store's
// updated_at when the row carries one, else the event's own time.
func (r row) lastChange() string {
	if r.UpdatedAt != "" {
		return r.UpdatedAt
	}
	return r.CreatedAt
}

// idChunk bounds the ids of one issues query, under SQLite's bound-variable
// limit.
const idChunk = 500

// TouchedIn returns the beads the session touched, sorted by id: every issue
// the copied rows of sessionDir name, with a live read of the stores those rows
// name and of the checkout at workDir filling in what the rows do not carry. A
// bead whose store was removed, or whose store's events table a br rebuild
// wiped, is still reported, with what its rows say.
func TouchedIn(ctx context.Context, sessionDir, workDir string) ([]Bead, error) {
	rows, err := readRows(sessionDir)
	if err != nil {
		return nil, err
	}
	byID := fromRows(rows)
	for _, store := range storesOf(rows) {
		if err := enrich(ctx, store, byID); err != nil {
			return nil, err
		}
	}
	// The checkout may hold a bead the session's rows name whose own store has
	// gone, and it holds the current state of a bead that landed. It names no
	// bead the rows do not.
	if store, ok := CheckoutStore(workDir); ok {
		if err := enrich(ctx, store, byID); err != nil {
			return nil, err
		}
	}
	out := make([]Bead, 0, len(byID))
	for _, b := range byID {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// fromRows folds the copied rows into one bead per issue. Rows are appended in
// event order, so a later row carries that store's later word: it wins, and an
// older one never overwrites a newer. Rows are ordered by the event time; the
// winning row also gives the issue's type and its updated time, which is the
// store's own updated_at when the row carries one and the event time otherwise.
func fromRows(rows []row) map[string]Bead {
	byID := make(map[string]Bead, len(rows))
	eventAt := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.IssueID == "" {
			continue
		}
		b := byID[r.IssueID]
		if b.ID != "" && !newer(r.CreatedAt, eventAt[r.IssueID]) {
			continue
		}
		b.ID = r.IssueID
		eventAt[r.IssueID] = r.CreatedAt
		if r.Title != "" {
			b.Title = r.Title
		}
		if r.Status != "" {
			b.Status = r.Status
		}
		if r.IssueType != "" {
			b.Type = r.IssueType
		}
		if at := r.lastChange(); at != "" {
			b.Updated = at
		}
		byID[r.IssueID] = b
	}
	return byID
}

// enrich folds the live issue rows of store into the beads it still holds. The
// store's row is the current state of a bead, so its fields win, and a store
// whose row is newer wins over one whose row is older.
func enrich(ctx context.Context, store string, byID map[string]Bead) error {
	wanted := make([]string, 0, len(byID))
	for id := range byID {
		wanted = append(wanted, id)
	}
	if len(wanted) == 0 {
		return nil
	}
	sort.Strings(wanted)
	for start := 0; start < len(wanted); start += idChunk {
		end := min(start+idChunk, len(wanted))
		live, err := readStore(ctx, store, wanted[start:end])
		if err != nil {
			return err
		}
		for _, row := range live {
			byID[row.ID] = mergeLive(byID[row.ID], row)
		}
	}
	return nil
}

// mergeLive folds a live issue row into the bead the rows already named.
func mergeLive(held, live Bead) Bead {
	b := held
	if live.Title != "" {
		b.Title = live.Title
	}
	if live.Status != "" {
		b.Status = live.Status
	}
	if live.Type != "" {
		b.Type = live.Type
	}
	if live.Parent != "" {
		b.Parent = live.Parent
	}
	// The bead's newest word on it. A live store's own updated_at is the
	// current state, so it wins a tie against a copied row's time.
	if b.Updated == "" || !newer(b.Updated, live.Updated) {
		b.Updated = live.Updated
	}
	return b
}

// storesOf returns the distinct stores rows name, in the order they first
// appear.
func storesOf(rows []row) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, r := range rows {
		if r.Store == "" {
			continue
		}
		if _, dup := seen[r.Store]; dup {
			continue
		}
		seen[r.Store] = struct{}{}
		out = append(out, r.Store)
	}
	return out
}

// readRows returns the copied rows of a session directory. A directory with no
// beads-events.jsonl holds none; a line that does not parse is skipped, so a
// torn last line costs only itself.
func readRows(sessionDir string) ([]row, error) {
	f, err := os.Open(filepath.Join(sessionDir, beadsEventsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.With(err, "reading the session's beads events").Set("dir", sessionDir).Err()
	}
	defer f.Close()
	var out []row
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if r.Store == "" || r.IssueID == "" {
			continue
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, errors.With(err, "reading the session's beads events").Set("dir", sessionDir).Err()
	}
	return out, nil
}

// issuesQuery reads the issues of a store in ids, with the parent-child parent
// of each. It is a template because the id list binds a variable count.
const issuesQuery = `
SELECT i.id, i.title, i.status, i.issue_type, CAST(i.updated_at AS TEXT),
       COALESCE((SELECT d.depends_on_id FROM dependencies d
                 WHERE d.issue_id = i.id AND d.type = 'parent-child'
                 ORDER BY d.depends_on_id LIMIT 1), '')
FROM issues i WHERE i.id IN (%s)`

// readStore returns the issues of store whose ids are ids. A store that holds
// no database contributes nothing, and one that holds none of the ids
// contributes nothing.
func readStore(ctx context.Context, store string, ids []string) ([]Bead, error) {
	path := filepath.Join(store, databaseName)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.With(err, "reading beads store").Set("store", store).Err()
	}
	db, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		marks[i] = "?"
	}
	rows, err := db.QueryContext(ctx, strings.Replace(issuesQuery, "%s", strings.Join(marks, ","), 1), args...)
	if err != nil {
		return nil, errors.With(err, "querying beads store").Set("store", store).Err()
	}
	defer rows.Close()
	var out []Bead
	for rows.Next() {
		var b Bead
		if err := rows.Scan(&b.ID, &b.Title, &b.Status, &b.Type, &b.Updated, &b.Parent); err != nil {
			return nil, errors.With(err, "reading an issue").Set("store", store).Err()
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.With(err, "reading issues").Set("store", store).Err()
	}
	return out, nil
}

// timeLayouts are the shapes a beads timestamp arrives in: br writes RFC 3339,
// and SQLite hands a DATETIME column back with a space and an offset.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

// newer reports whether a is later than b. A time beats no time; text that does
// not parse as a time compares as text.
func newer(a, b string) bool {
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	at, aok := parseTime(a)
	bt, bok := parseTime(b)
	if !aok || !bok {
		return a > b
	}
	return at.After(bt)
}

func parseTime(s string) (time.Time, bool) {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// openReadOnly opens a beads database so that nothing can write it.
func openReadOnly(path string) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errors.With(err, "opening beads store").Set("path", path).Err()
	}
	return db, nil
}

// Locate returns the directory of session id: dir when it is not empty, else
// the project layer of workDir, else the home's. It reports false when the
// directory it names does not exist.
func Locate(workDir, home, id, dir string) (string, bool) {
	if id == "" {
		return "", false
	}
	candidates := []string{dir}
	if dir == "" {
		if workDir != "" {
			candidates = append(candidates, filepath.Join(workDir, ".sirene", "sessions", id))
		}
		if home != "" {
			candidates = append(candidates, filepath.Join(home, ".sirene", "sessions", id))
		}
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c, true
		}
	}
	return "", false
}

// CheckoutStore returns the beads store br uses in dir: the nearest .beads at
// or above dir within its checkout, after its redirect file. It reports false
// when the checkout has none.
func CheckoutStore(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		beads := filepath.Join(dir, beadsDirName)
		if info, err := os.Stat(beads); err == nil && info.IsDir() {
			return redirected(beads), true
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// redirected follows the redirect file of beads as br does: its trimmed content
// names the .beads directory to use, a relative path resolving against beads
// itself.
func redirected(beads string) string {
	data, err := os.ReadFile(filepath.Join(beads, redirectName))
	if err != nil {
		return beads
	}
	target := strings.TrimSpace(string(data))
	if target == "" {
		return beads
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(beads, target)
	}
	return filepath.Clean(target)
}
