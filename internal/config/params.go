package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Param is a single key/value pair from a preset's params object.
// Value is bool, json.Number, or string. Object/array values are rejected at
// decode time per DESIGN.md §6.4.
type Param struct {
	Key   string
	Value any
}

// Params preserves source key order so users can group related flags for
// readability and have argv come out in the same order (DESIGN.md §6.5).
type Params []Param

// Get returns the value for key, or (nil, false) if absent.
func (p Params) Get(key string) (any, bool) {
	for _, pr := range p {
		if pr.Key == key {
			return pr.Value, true
		}
	}
	return nil, false
}

// Set replaces the value of an existing key (preserving its position) or
// appends a new pair at the end if the key is absent.
func (p *Params) Set(key string, value any) {
	for i := range *p {
		if (*p)[i].Key == key {
			(*p)[i].Value = value
			return
		}
	}
	*p = append(*p, Param{Key: key, Value: value})
}

// Delete removes the entry with the given key. Returns true if anything was
// removed.
func (p *Params) Delete(key string) bool {
	for i, pr := range *p {
		if pr.Key == key {
			*p = append((*p)[:i], (*p)[i+1:]...)
			return true
		}
	}
	return false
}

// UnmarshalJSON decodes a JSON object into Params, preserving key order.
func (p *Params) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		// `null` → empty Params.
		*p = nil
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("params: expected JSON object, got %v", tok)
	}

	out := make(Params, 0)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("params: expected string key, got %T", keyTok)
		}
		valTok, err := dec.Token()
		if err != nil {
			return err
		}
		v, err := scalarValue(key, valTok, dec)
		if err != nil {
			return err
		}
		out = append(out, Param{Key: key, Value: v})
	}
	// Consume the closing '}' so dec is fully drained.
	if _, err := dec.Token(); err != nil {
		return err
	}
	*p = out
	return nil
}

// scalarValue accepts bool, json.Number, or string. Objects and arrays are
// errors. The decoder is passed in so we can drain the nested tokens for a
// helpful error message rather than just "expected scalar".
func scalarValue(key string, tok json.Token, dec *json.Decoder) (any, error) {
	switch v := tok.(type) {
	case bool, json.Number, string:
		return v, nil
	case nil:
		return nil, fmt.Errorf("params[%q]: null is not a valid value", key)
	case json.Delim:
		// Drain the nested structure so the decoder is in a sane state for
		// the caller to keep reading sibling keys, even though we're going
		// to abort.
		_ = drainNested(dec, v)
		switch v {
		case '{':
			return nil, fmt.Errorf("params[%q]: object values are not supported", key)
		case '[':
			return nil, fmt.Errorf("params[%q]: array values are not supported", key)
		}
		return nil, fmt.Errorf("params[%q]: unexpected delimiter %v", key, v)
	default:
		return nil, fmt.Errorf("params[%q]: unsupported value type %T", key, tok)
	}
}

func drainNested(dec *json.Decoder, opener json.Delim) error {
	closer := json.Delim('}')
	if opener == '[' {
		closer = ']'
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				_ = closer
			}
		}
	}
	return nil
}

// MarshalJSON emits the Params as a JSON object, preserving order.
func (p Params) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, pr := range p {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(pr.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		v, err := json.Marshal(pr.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// String renders a debug-friendly representation: key=value pairs in order.
func (p Params) String() string {
	var b strings.Builder
	for i, pr := range p {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s=%v", pr.Key, pr.Value)
	}
	return b.String()
}
