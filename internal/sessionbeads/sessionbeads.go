// Package sessionbeads answers which beads a sirene session touched.
//
// A sirene session's agents run br with BD_ACTOR set to a format that holds the
// session id (the beads-actor plugin, default "agent:{session}/{agent}"), so
// every mutation br records in a checkout's .beads/beads.db names the session.
// The session directory also keeps beads-events.jsonl, sirene's copy of those
// rows, which names every store the session touched.
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

// DefaultActorFormat is the BD_ACTOR format the beads-actor plugin ships with.
const DefaultActorFormat = "agent:{session}/{agent}"

const (
	beadsDirName       = ".beads"
	redirectName       = "redirect"
	databaseName       = "beads.db"
	beadsEventsFile    = "beads-events.jsonl"
	sessionPlaceholder = "{session}"
)

// Bead is one bead a session touched, as the report prints it.
type Bead struct {
	ID     string
	Title  string
	Status string
	Type   string
	// Updated is the store's own updated_at text, which br writes as RFC 3339
	// in UTC.
	Updated string
	// Parent is the bead's parent-child parent, empty when it has none.
	Parent string
}

// touchedQuery selects the issues the session's actor touched, with the
// parent-child parent of each. The actor match is a LIKE over the pattern the
// format gives, so it uses the actor index.
const touchedQuery = `
SELECT i.id, i.title, i.status, i.issue_type, CAST(i.updated_at AS TEXT),
       COALESCE((SELECT d.depends_on_id FROM dependencies d
                 WHERE d.issue_id = i.id AND d.type = 'parent-child'
                 ORDER BY d.depends_on_id LIMIT 1), '')
FROM issues i
WHERE i.id IN (SELECT e.issue_id FROM events e WHERE e.actor LIKE ? ESCAPE '\')
ORDER BY i.id`

// Touched returns the beads the session touched in stores, sorted by id. A
// store that holds no database contributes nothing. A bead that several stores
// hold is reported once, from the store whose row is newest.
func Touched(ctx context.Context, stores []string, sessionID, actorFormat string) ([]Bead, error) {
	pattern, err := actorPattern(actorFormat, sessionID)
	if err != nil {
		return nil, err
	}
	byID := map[string]Bead{}
	for _, store := range stores {
		beads, err := readStore(ctx, store, pattern)
		if err != nil {
			return nil, err
		}
		for _, b := range beads {
			if held, ok := byID[b.ID]; ok && !newer(b.Updated, held.Updated) {
				continue
			}
			byID[b.ID] = b
		}
	}
	out := make([]Bead, 0, len(byID))
	for _, b := range byID {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// newer reports whether a is later than b. Both are br's updated_at text, which
// is RFC 3339; text that does not parse compares as text.
func newer(a, b string) bool {
	at, aerr := time.Parse(time.RFC3339Nano, a)
	bt, berr := time.Parse(time.RFC3339Nano, b)
	if aerr != nil || berr != nil {
		return a > b
	}
	return at.After(bt)
}

// readStore returns the beads of store that pattern matches.
func readStore(ctx context.Context, store, pattern string) ([]Bead, error) {
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
	rows, err := db.QueryContext(ctx, touchedQuery, pattern)
	if err != nil {
		return nil, errors.With(err, "querying beads store").Set("store", store).Err()
	}
	defer rows.Close()
	var out []Bead
	for rows.Next() {
		var b Bead
		if err := rows.Scan(&b.ID, &b.Title, &b.Status, &b.Type, &b.Updated, &b.Parent); err != nil {
			return nil, errors.With(err, "reading a bead").Set("store", store).Err()
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.With(err, "reading beads").Set("store", store).Err()
	}
	return out, nil
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

// actorPattern returns the LIKE pattern that matches the actors format gives
// sessionID: the text before {session}, the id, the literal text after it up to
// the next placeholder, and then anything.
func actorPattern(format, sessionID string) (string, error) {
	i := strings.Index(format, sessionPlaceholder)
	if i < 0 {
		return "", errors.WithNew("the actor format has no {session}").Set("format", format).Err()
	}
	prefix := format[:i]
	rest := format[i+len(sessionPlaceholder):]
	suffix := rest
	if j := strings.Index(rest, "{"); j >= 0 {
		suffix = rest[:j]
	}
	return likeEscape(prefix) + likeEscape(sessionID) + likeEscape(suffix) + "%", nil
}

// likeEscape escapes the characters that are special in a LIKE pattern.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Stores returns the beads stores the session's copied rows name, in the order
// they first appear. A session directory with no beads-events.jsonl names none.
func Stores(sessionDir string) ([]string, error) {
	f, err := os.Open(filepath.Join(sessionDir, beadsEventsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.With(err, "reading the session's beads events").Set("dir", sessionDir).Err()
	}
	defer f.Close()
	var out []string
	seen := map[string]struct{}{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row struct {
			Store string `json:"store"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil || row.Store == "" {
			continue
		}
		if _, dup := seen[row.Store]; dup {
			continue
		}
		seen[row.Store] = struct{}{}
		out = append(out, row.Store)
	}
	if err := sc.Err(); err != nil {
		return nil, errors.With(err, "reading the session's beads events").Set("dir", sessionDir).Err()
	}
	return out, nil
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
