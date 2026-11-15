package update

// ONE DOCUMENT, ONE READING.
//
// A signed update document is read twice: CanonicalManifestBytes decodes it
// into map[string]any to rebuild the bytes the signature covers, and
// ParseManifest / ParseSignal decode it into a struct to act on it. The two
// readings only agree on a document in which no object repeats a key. Given a
// repeated key, the map keeps the LAST value and the struct MERGES the copies
// (encoding/json reuses an existing map or struct value when a key comes
// again), so the struct can hold values the signature never covered. A feed
// host that put a second "trust_roots" in front of a signed rotation manifest
// got its own root installed beside the signed one (repeated_key_test.go).
//
// So every signed document, and the trust file, must be exactly one JSON value
// in which no object, at any depth, names a key twice. Keys are compared after
// unescaping (a \u-escaped spelling is the same key) and with the case folding
// encoding/json applies when it matches a key to a struct field
// (bytes.EqualFold: "Version" is "version", U+017F is "s", the Kelvin sign is
// "k"), so no two keys of one object can reach the same field.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

var (
	// ErrRepeatedKey marks a document in which one object names a key twice.
	ErrRepeatedKey = errors.New("a JSON object names the same key twice")
	// ErrTrailingData marks a document with more after its one JSON value.
	ErrTrailingData = errors.New("the document has data after its JSON value")
)

type strictFrame struct {
	object  bool
	keys    map[string]string // folded key -> the key as written
	wantKey bool              // object: the next token is a key
	key     string            // object: the key whose value is being read
	index   int               // array: the element being read
}

// assertStrictJSONDocument refuses raw unless it is exactly one JSON value
// (surrounding whitespace allowed) in which no object, at any depth, repeats
// a key (see the comment at the top of this file).
func assertStrictJSONDocument(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var stack []*strictFrame
	top := func() *strictFrame {
		if len(stack) == 0 {
			return nil
		}
		return stack[len(stack)-1]
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return errors.New("the document ends before its JSON value does")
		}
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				stack = append(stack, &strictFrame{object: true, keys: map[string]string{}, wantKey: true})
				continue
			case '[':
				stack = append(stack, &strictFrame{})
				continue
			default: // '}' or ']': the container is a value that just ended
				stack = stack[:len(stack)-1]
			}
		} else if frame := top(); frame != nil && frame.object && frame.wantKey {
			key, _ := token.(string) // the decoder yields only strings here
			folded := foldJSONKey(key)
			if prior, seen := frame.keys[folded]; seen {
				if prior == key {
					return fmt.Errorf("%w: %q in %s", ErrRepeatedKey, key, strictPath(stack))
				}
				return fmt.Errorf("%w: %q and %q in %s (encoding/json reads them as one key)", ErrRepeatedKey, prior, key, strictPath(stack))
			}
			frame.keys[folded] = key
			frame.key = key
			frame.wantKey = false
			continue
		}
		// A value ended (a scalar, or a container closed just above).
		frame := top()
		if frame == nil {
			break
		}
		if frame.object {
			frame.wantKey = true
		} else {
			frame.index++
		}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrTrailingData
	}
	return nil
}

// strictPath names the object a repeated key was found in, for the error.
func strictPath(stack []*strictFrame) string {
	var path strings.Builder
	path.WriteString("$")
	for _, frame := range stack[:len(stack)-1] {
		if frame.object {
			fmt.Fprintf(&path, ".%s", frame.key)
		} else {
			fmt.Fprintf(&path, "[%d]", frame.index)
		}
	}
	return path.String()
}

// foldJSONKey maps a key to one representative of its case-folding class:
// two keys fold equal exactly when bytes.EqualFold (the comparison
// encoding/json uses to match a key to a struct field) says they are equal.
func foldJSONKey(key string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for folded := unicode.SimpleFold(r); folded != r; folded = unicode.SimpleFold(folded) {
			if folded < least {
				least = folded
			}
		}
		return least
	}, key)
}
