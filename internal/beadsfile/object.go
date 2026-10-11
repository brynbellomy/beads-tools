// Package beadsfile reads and writes br's issues.jsonl without re-rendering
// anything it was not asked to change.
package beadsfile

import (
	"bytes"
	"encoding/json"

	"github.com/brynbellomy/go-utils/errors"
)

// Object is a JSON object that remembers its key order and keeps each value as
// compact raw JSON, so an untouched value round-trips byte for byte.
type Object struct {
	keys   []string
	values map[string]json.RawMessage
}

// NewObject returns an empty object.
func NewObject() *Object {
	return &Object{values: map[string]json.RawMessage{}}
}

// ParseObject decodes one JSON object, preserving key order.
func ParseObject(data []byte) (*Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, errors.With(err, "reading object").Err()
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.WithNew("not a JSON object").Err()
	}
	o := NewObject()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, errors.With(err, "reading key").Err()
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.WithNew("object key is not a string").Err()
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, errors.With(err, "reading value").Set("key", key).Err()
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			return nil, errors.With(err, "compacting value").Set("key", key).Err()
		}
		if _, dup := o.values[key]; dup {
			return nil, errors.WithNew("duplicate key").Set("key", key).Err()
		}
		o.keys = append(o.keys, key)
		o.values[key] = buf.Bytes()
	}
	if _, err := dec.Token(); err != nil {
		return nil, errors.With(err, "reading object end").Err()
	}
	if dec.More() {
		return nil, errors.WithNew("trailing data after object").Err()
	}
	return o, nil
}

// Keys returns the keys in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Has reports whether key is present.
func (o *Object) Has(key string) bool {
	_, ok := o.values[key]
	return ok
}

// Raw returns the compact raw value of key, or nil when absent.
func (o *Object) Raw(key string) json.RawMessage { return o.values[key] }

// String returns key's value when it is a JSON string.
func (o *Object) String(key string) (string, bool) {
	raw, ok := o.values[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// SetRaw sets key to a raw JSON value. A new key is appended after the existing
// keys unless InsertAfter places it.
func (o *Object) SetRaw(key string, raw json.RawMessage) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = raw
}

// SetString sets key to a JSON string.
func (o *Object) SetString(key, value string) {
	o.SetRaw(key, MarshalString(value))
}

// InsertAfter sets a key that is not yet present directly after prev, or first
// when prev is "" or absent.
func (o *Object) InsertAfter(prev, key string, raw json.RawMessage) {
	if o.Has(key) {
		o.values[key] = raw
		return
	}
	at := 0
	for i, k := range o.keys {
		if k == prev {
			at = i + 1
			break
		}
	}
	o.keys = append(o.keys, "")
	copy(o.keys[at+1:], o.keys[at:])
	o.keys[at] = key
	o.values[key] = raw
}

// Delete removes key.
func (o *Object) Delete(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Clone returns a deep copy.
func (o *Object) Clone() *Object {
	c := &Object{keys: append([]string(nil), o.keys...), values: make(map[string]json.RawMessage, len(o.values))}
	for k, v := range o.values {
		c.values[k] = append(json.RawMessage(nil), v...)
	}
	return c
}

// Equal reports whether both objects hold the same keys and values, ignoring
// key order.
func (o *Object) Equal(other *Object) bool {
	if len(o.values) != len(other.values) {
		return false
	}
	for k, v := range o.values {
		if !bytes.Equal(v, other.values[k]) {
			return false
		}
	}
	return true
}

// MarshalJSON renders the object compactly in key order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(MarshalString(k))
		buf.WriteByte(':')
		buf.Write(o.values[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// MarshalString renders s as a JSON string the way br does, without escaping
// HTML characters.
func MarshalString(s string) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// ParseObjectArray decodes a JSON array of objects. A null or absent value is
// an empty array.
func ParseObjectArray(raw json.RawMessage) ([]*Object, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, errors.With(err, "decoding array").Err()
	}
	out := make([]*Object, 0, len(elems))
	for _, e := range elems {
		o, err := ParseObject(e)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// MarshalObjectArray renders objects as a compact JSON array.
func MarshalObjectArray(objs []*Object) (json.RawMessage, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, o := range objs {
		if i > 0 {
			buf.WriteByte(',')
		}
		b, err := o.MarshalJSON()
		if err != nil {
			return nil, err
		}
		buf.Write(b)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}
