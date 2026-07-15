package sdnruns

import "encoding/json"

// jsonMarshalIndent encodes a value as pretty JSON with a trailing newline (the
// on-disk run file form).
func jsonMarshalIndent(v interface{}) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// jsonUnmarshal decodes JSON into v.
func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
