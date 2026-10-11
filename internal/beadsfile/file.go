package beadsfile

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/brynbellomy/go-utils/errors"
)

// Path is the issues file's location relative to a repository root.
const Path = ".beads/issues.jsonl"

// Issue is one line of issues.jsonl. Line holds the bytes it was read from, so
// an issue nobody changed is written back exactly as it was.
type Issue struct {
	ID   string
	Obj  *Object
	Line []byte
}

// NewIssue wraps an object whose bytes are not yet rendered.
func NewIssue(obj *Object) (*Issue, error) {
	id, ok := obj.String("id")
	if !ok || id == "" {
		return nil, errors.WithNew("issue has no string id").Err()
	}
	return &Issue{ID: id, Obj: obj}, nil
}

// Bytes renders the issue line, reusing the original bytes when it has them.
func (i *Issue) Bytes() ([]byte, error) {
	if i.Line != nil {
		return i.Line, nil
	}
	return i.Obj.MarshalJSON()
}

// File is the parsed issues file.
type File struct {
	Issues []*Issue
	byID   map[string]*Issue
}

// Parse reads issues.jsonl. It rejects merge conflict markers, malformed lines
// and duplicate IDs rather than guessing.
func Parse(r io.Reader) (*File, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	f := &File{byID: map[string]*Issue{}}
	for n := 1; sc.Scan(); n++ {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		for _, marker := range [][]byte{[]byte("<<<<<<<"), []byte("======="), []byte(">>>>>>>"), []byte("|||||||")} {
			if bytes.HasPrefix(line, marker) {
				return nil, errors.WithNew("issues file contains a git conflict marker").Set("line", n).Err()
			}
		}
		obj, err := ParseObject(line)
		if err != nil {
			return nil, errors.With(err, "parsing issue").Set("line", n).Err()
		}
		iss, err := NewIssue(obj)
		if err != nil {
			return nil, errors.With(err, "parsing issue").Set("line", n).Err()
		}
		if _, dup := f.byID[iss.ID]; dup {
			return nil, errors.WithNew("duplicate issue id").Set("id", iss.ID, "line", n).Err()
		}
		iss.Line = append([]byte(nil), line...)
		f.Issues = append(f.Issues, iss)
		f.byID[iss.ID] = iss
	}
	if err := sc.Err(); err != nil {
		return nil, errors.With(err, "reading issues file").Err()
	}
	return f, nil
}

// ParseFile reads issues.jsonl from disk. A missing file is an empty file.
func ParseFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &File{byID: map[string]*Issue{}}, nil
	}
	if err != nil {
		return nil, errors.With(err, "reading issues file").Set("path", path).Err()
	}
	return Parse(bytes.NewReader(data))
}

// New builds a file from issues, which must have distinct IDs.
func New(issues []*Issue) (*File, error) {
	f := &File{byID: map[string]*Issue{}}
	for _, iss := range issues {
		if _, dup := f.byID[iss.ID]; dup {
			return nil, errors.WithNew("duplicate issue id").Set("id", iss.ID).Err()
		}
		f.Issues = append(f.Issues, iss)
		f.byID[iss.ID] = iss
	}
	return f, nil
}

// Get returns the issue with id, or nil.
func (f *File) Get(id string) *Issue { return f.byID[id] }

// IDs returns every issue ID in file order.
func (f *File) IDs() []string {
	out := make([]string, len(f.Issues))
	for i, iss := range f.Issues {
		out[i] = iss.ID
	}
	return out
}

// Sort orders issues by ID, as br's export does.
func (f *File) Sort() {
	sort.SliceStable(f.Issues, func(a, b int) bool { return f.Issues[a].ID < f.Issues[b].ID })
}

// WriteTo renders one issue per line.
func (f *File) WriteTo(w io.Writer) (int64, error) {
	var n int64
	for _, iss := range f.Issues {
		b, err := iss.Bytes()
		if err != nil {
			return n, errors.With(err, "rendering issue").Set("id", iss.ID).Err()
		}
		m, err := w.Write(append(b, '\n'))
		n += int64(m)
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// Bytes renders the whole file.
func (f *File) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteFile replaces path atomically.
func (f *File) WriteFile(path string) error {
	data, err := f.Bytes()
	if err != nil {
		return err
	}
	return WriteAtomic(path, data)
}

// WriteAtomic writes data to a temporary file beside path and renames it into
// place.
func WriteAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".beads-tools-*")
	if err != nil {
		return errors.With(err, "creating temp file").Set("path", path).Err()
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return errors.With(err, "writing temp file").Set("path", path).Err()
	}
	if err := tmp.Close(); err != nil {
		return errors.With(err, "closing temp file").Set("path", path).Err()
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return errors.With(err, "setting file mode").Set("path", path).Err()
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return errors.With(err, "replacing file").Set("path", path).Err()
	}
	return nil
}
