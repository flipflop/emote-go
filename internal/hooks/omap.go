package hooks

// Ordered JSON object support. Python's json.load/json.dump round-trips
// settings.json preserving key insertion order; Go's map[string]any would
// re-sort keys alphabetically on write, which is not a non-destructive merge.
// omap keeps the original key order and appends new keys at the end, exactly
// like a Python dict.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type omap struct {
	keys []string
	vals map[string]interface{}
}

func newOmap() *omap {
	return &omap{vals: map[string]interface{}{}}
}

func (m *omap) Get(key string) (interface{}, bool) {
	v, ok := m.vals[key]
	return v, ok
}

func (m *omap) Set(key string, value interface{}) {
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = value
}

func (m *omap) Delete(key string) {
	if _, ok := m.vals[key]; !ok {
		return
	}
	delete(m.vals, key)
	for i, k := range m.keys {
		if k == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

func (m *omap) Len() int { return len(m.keys) }

// Keys returns a copy of the key list (safe to iterate while mutating).
func (m *omap) Keys() []string {
	out := make([]string, len(m.keys))
	copy(out, m.keys)
	return out
}

// marshalNoEscape marshals compactly without HTML escaping, so & < > stay
// literal exactly as Python's json.dump writes them.
func marshalNoEscape(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func (m *omap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := marshalNoEscape(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := marshalNoEscape(m.vals[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// decodeValue reads one JSON value from dec, producing *omap for objects,
// []interface{} for arrays, and json.Number/string/bool/nil for scalars.
func decodeValue(dec *json.Decoder) (interface{}, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := newOmap()
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string: %v", keyTok)
				}
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				m.Set(key, val)
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return nil, err
			}
			return m, nil
		case '[':
			arr := []interface{}{}
			for dec.More() {
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	default:
		return tok, nil // string, json.Number, bool, nil
	}
}

// marshalIndent renders a settings document with 2-space indentation, the
// same layout Python's json.dump(indent=2) produces (no HTML escaping).
func marshalIndent(v interface{}) ([]byte, error) {
	compact, err := marshalNoEscape(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, compact, "", "  "); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// parseJSON parses a whole document into ordered values, rejecting trailing
// garbage (like Python's json.load).
func parseJSON(data []byte) (interface{}, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after JSON document")
	}
	return v, nil
}
