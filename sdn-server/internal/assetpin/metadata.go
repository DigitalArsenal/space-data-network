package assetpin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

var ErrInvalidMetadata = errors.New("assetpin: invalid metadata")

// Metadata is the strict, versioned provenance document accepted with an
// asset upload. All fields except SchemaVersion are JSON strings.
type Metadata struct {
	SchemaVersion  int
	CandidateKey   string
	EntityID       string
	SourceRecordID string
	SourceURL      string
	LicenseName    string
	Attribution    string
	SHA256         string
	VAMID          string
}

// ParseCanonicalMetadata accepts only the canonical byte representation of
// the version-one asset metadata object and returns those exact bytes.
func ParseCanonicalMetadata(raw []byte) (Metadata, []byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	opening, err := decoder.Token()
	if err != nil {
		return Metadata{}, nil, invalidMetadata("decode object", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return Metadata{}, nil, invalidMetadata("top-level value must be an object", nil)
	}

	metadata := Metadata{}
	values := make(map[string]any, 9)
	seen := make(map[string]struct{}, 9)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Metadata{}, nil, invalidMetadata("decode object key", err)
		}
		key, ok := token.(string)
		if !ok {
			return Metadata{}, nil, invalidMetadata("object key must be a string", nil)
		}
		if _, duplicate := seen[key]; duplicate {
			return Metadata{}, nil, invalidMetadata("duplicate field "+key, nil)
		}
		seen[key] = struct{}{}

		var value any
		if err := decoder.Decode(&value); err != nil {
			return Metadata{}, nil, invalidMetadata("decode field "+key, err)
		}
		switch key {
		case "schemaVersion":
			number, ok := value.(json.Number)
			if !ok || number.String() != "1" {
				return Metadata{}, nil, invalidMetadata("schemaVersion must be numeric 1", nil)
			}
			metadata.SchemaVersion = 1
			values[key] = number
		case "candidateKey":
			if metadata.CandidateKey, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.CandidateKey
		case "entityId":
			if metadata.EntityID, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.EntityID
		case "sourceRecordId":
			if metadata.SourceRecordID, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.SourceRecordID
		case "sourceUrl":
			if metadata.SourceURL, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.SourceURL
		case "licenseName":
			if metadata.LicenseName, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.LicenseName
		case "attribution":
			if metadata.Attribution, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.Attribution
		case "sha256":
			if metadata.SHA256, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.SHA256
		case "vamId":
			if metadata.VAMID, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.VAMID
		default:
			return Metadata{}, nil, invalidMetadata("unknown field "+key, nil)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return Metadata{}, nil, invalidMetadata("decode object close", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return Metadata{}, nil, invalidMetadata("object is not closed", nil)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Metadata{}, nil, invalidMetadata("trailing data", err)
	}

	if _, ok := seen["schemaVersion"]; !ok {
		return Metadata{}, nil, invalidMetadata("schemaVersion is required", nil)
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "candidateKey", value: metadata.CandidateKey},
		{name: "sourceUrl", value: metadata.SourceURL},
		{name: "licenseName", value: metadata.LicenseName},
		{name: "sha256", value: metadata.SHA256},
	} {
		if strings.TrimSpace(required.value) == "" {
			return Metadata{}, nil, invalidMetadata(required.name+" is required", nil)
		}
	}
	for _, textual := range []struct {
		name  string
		value string
	}{
		{name: "candidateKey", value: metadata.CandidateKey},
		{name: "entityId", value: metadata.EntityID},
		{name: "sourceRecordId", value: metadata.SourceRecordID},
		{name: "sourceUrl", value: metadata.SourceURL},
		{name: "licenseName", value: metadata.LicenseName},
		{name: "attribution", value: metadata.Attribution},
		{name: "sha256", value: metadata.SHA256},
		{name: "vamId", value: metadata.VAMID},
	} {
		if strings.TrimSpace(textual.value) != textual.value {
			return Metadata{}, nil, invalidMetadata(textual.name+" must not contain surrounding whitespace", nil)
		}
	}
	if !isLowerSHA256(metadata.SHA256) {
		return Metadata{}, nil, invalidMetadata("sha256 must be lowercase 64-hex", nil)
	}

	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(values); err != nil {
		return Metadata{}, nil, invalidMetadata("encode canonical object", err)
	}
	canonical := bytes.TrimSuffix(output.Bytes(), []byte{'\n'})
	if !bytes.Equal(raw, canonical) {
		return Metadata{}, nil, invalidMetadata("document is not canonical", nil)
	}
	return metadata, append([]byte(nil), canonical...), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func invalidMetadata(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrInvalidMetadata, message)
	}
	return fmt.Errorf("%w: %s: %v", ErrInvalidMetadata, message, cause)
}
