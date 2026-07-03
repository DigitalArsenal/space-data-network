package flatsqlrt

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// parity_test.go asserts the Go/WasmEdge host produces byte-identical outputs
// to the JS/V8 reference host (flatsql/standalone) for the shared vectors in
// shared-test-vectors/flatsql-parity.json. The JS twin is
// sdn-js/src/flatsql-parity.test.ts; regenerate expectations with
// sdn-js/scripts/generate-flatsql-parity-vectors.mjs.

type parityParam struct {
	T string          `json:"t"`
	V json.RawMessage `json:"v"`
}

type parityVectors struct {
	Schema              string `json:"schema"`
	FileID              string `json:"fileId"`
	Table               string `json:"table"`
	FixtureStreamBase64 string `json:"fixtureStreamBase64"`
	RawStreamCases      []struct {
		Name   string        `json:"name"`
		SQL    string        `json:"sql"`
		Params []parityParam `json:"params"`
	} `json:"rawStreamCases"`
	QueryCacheKeyCases []struct {
		Name            string        `json:"name"`
		Dataset         string        `json:"dataset"`
		ArtifactVersion string        `json:"artifactVersion"`
		QueryID         string        `json:"queryId"`
		Params          []parityParam `json:"params"`
	} `json:"queryCacheKeyCases"`
	ResponseArtifactKeyCases []struct {
		Name            string        `json:"name"`
		SchemaName      string        `json:"schemaName"`
		SchemaVersion   string        `json:"schemaVersion"`
		SQL             string        `json:"sql"`
		Format          string        `json:"format"`
		PublishEventKey string        `json:"publishEventKey"`
		Projection      []string      `json:"projection"`
		Params          []parityParam `json:"params"`
	} `json:"responseArtifactKeyCases"`
	Expected struct {
		RawStreams map[string]struct {
			SHA256     string `json:"sha256"`
			ByteLength int    `json:"byteLength"`
		} `json:"rawStreams"`
		QueryCacheKeys       map[string]string `json:"queryCacheKeys"`
		ResponseArtifactKeys map[string]string `json:"responseArtifactKeys"`
	} `json:"expected"`
}

func decodeParityParams(t *testing.T, params []parityParam) []interface{} {
	t.Helper()
	out := make([]interface{}, 0, len(params))
	for _, p := range params {
		switch p.T {
		case "null":
			out = append(out, nil)
		case "bool":
			var v bool
			if err := json.Unmarshal(p.V, &v); err != nil {
				t.Fatalf("bool param: %v", err)
			}
			out = append(out, v)
		case "i64":
			var v int64
			if err := json.Unmarshal(p.V, &v); err != nil {
				t.Fatalf("i64 param: %v", err)
			}
			out = append(out, v)
		case "f64":
			var v float64
			if err := json.Unmarshal(p.V, &v); err != nil {
				t.Fatalf("f64 param: %v", err)
			}
			out = append(out, v)
		case "str":
			var v string
			if err := json.Unmarshal(p.V, &v); err != nil {
				t.Fatalf("str param: %v", err)
			}
			out = append(out, v)
		case "bytes":
			var v string
			if err := json.Unmarshal(p.V, &v); err != nil {
				t.Fatalf("bytes param: %v", err)
			}
			b, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				t.Fatalf("bytes param base64: %v", err)
			}
			out = append(out, b)
		default:
			t.Fatalf("unknown param tag %q", p.T)
		}
	}
	return out
}

func loadParityVectors(t *testing.T) *parityVectors {
	t.Helper()
	path := filepath.Join("..", "..", "..", "shared-test-vectors", "flatsql-parity.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read parity vectors: %v", err)
	}
	var v parityVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse parity vectors: %v", err)
	}
	if v.Expected.RawStreams == nil {
		t.Fatal("parity vectors have no expected outputs — run sdn-js/scripts/generate-flatsql-parity-vectors.mjs")
	}
	return &v
}

func TestIsomorphismParityWithJSHost(t *testing.T) {
	vectors := loadParityVectors(t)
	rt := newTestRuntime(t)

	db, err := rt.CreateDatabase(vectors.Schema, "parity")
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(db.Destroy)
	if err := db.RegisterFileID(vectors.FileID, vectors.Table); err != nil {
		t.Fatalf("RegisterFileID: %v", err)
	}
	stream, err := base64.StdEncoding.DecodeString(vectors.FixtureStreamBase64)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if _, err := db.Ingest(stream); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	for _, c := range vectors.RawStreamCases {
		want, ok := vectors.Expected.RawStreams[c.Name]
		if !ok {
			t.Fatalf("no expected output for raw stream case %q", c.Name)
		}
		res, err := db.QueryRawFlatBufferStream(c.SQL, decodeParityParams(t, c.Params)...)
		if err != nil {
			t.Fatalf("case %q: %v", c.Name, err)
		}
		if len(res.Bytes) != want.ByteLength {
			t.Errorf("case %q: stream length %d, JS host produced %d", c.Name, len(res.Bytes), want.ByteLength)
		}
		sum := sha256.Sum256(res.Bytes)
		if got := hex.EncodeToString(sum[:]); got != want.SHA256 {
			t.Errorf("case %q: stream sha256 %s, JS host produced %s — HOSTS DIVERGED", c.Name, got, want.SHA256)
		}
	}

	for _, c := range vectors.QueryCacheKeyCases {
		want, ok := vectors.Expected.QueryCacheKeys[c.Name]
		if !ok {
			t.Fatalf("no expected key for query cache case %q", c.Name)
		}
		got, err := rt.BuildQueryCacheKey(c.Dataset, c.ArtifactVersion, c.QueryID, decodeParityParams(t, c.Params)...)
		if err != nil {
			t.Fatalf("case %q: %v", c.Name, err)
		}
		if got != want {
			t.Errorf("case %q: query cache key\n got %s\nwant %s", c.Name, got, want)
		}
	}

	for _, c := range vectors.ResponseArtifactKeyCases {
		want, ok := vectors.Expected.ResponseArtifactKeys[c.Name]
		if !ok {
			t.Fatalf("no expected key for response artifact case %q", c.Name)
		}
		got, err := rt.BuildResponseArtifactCacheKey(c.SchemaName, c.SchemaVersion, c.SQL, ResponseArtifactKeyOptions{
			Format:          c.Format,
			PublishEventKey: c.PublishEventKey,
			Projection:      c.Projection,
			Params:          decodeParityParams(t, c.Params),
		})
		if err != nil {
			t.Fatalf("case %q: %v", c.Name, err)
		}
		if got != want {
			t.Errorf("case %q: response artifact key\n got %s\nwant %s", c.Name, got, want)
		}
	}
}
