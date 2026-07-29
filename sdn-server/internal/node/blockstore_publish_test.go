package node

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// rawCIDv1 is the CID a correct Kubo would return for `block put --format=raw
// --mhtype=sha2-256`. The test asserts the helper reports what the API says AND
// that what the API saw is byte-identical to what we handed it — a publish that
// silently truncates would still "succeed" without this.
func rawCIDv1(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	digest, err := mh.Encode(sum[:], mh.SHA2_256)
	if err != nil {
		t.Fatalf("encode multihash: %v", err)
	}
	return cid.NewCidV1(cid.Raw, digest).String()
}

type recordedCall struct {
	path  string
	query map[string][]string
	body  []byte
}

func newRecordingKubo(t *testing.T, calls *[]recordedCall, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		*calls = append(*calls, recordedCall{path: r.URL.Path, query: r.URL.Query(), body: body})
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v0/block/put":
			// Extract the multipart payload the client actually sent.
			payload := extractMultipartPayload(body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Key":  rawCIDv1(t, payload),
				"Size": len(payload),
			})
		case "/api/v0/pin/add":
			_ = json.NewEncoder(w).Encode(map[string]any{"Pins": r.URL.Query()["arg"]})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// extractMultipartPayload pulls the file part out of a multipart body without
// reparsing headers: the payload sits between the first blank line after the
// part headers and the trailing boundary.
func extractMultipartPayload(body []byte) []byte {
	const sep = "\r\n\r\n"
	start := -1
	for i := 0; i+len(sep) <= len(body); i++ {
		if string(body[i:i+len(sep)]) == sep {
			start = i + len(sep)
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := len(body)
	for i := len(body) - 1; i >= start; i-- {
		if body[i] == '-' && i >= 3 && string(body[i-3:i+1]) == "\r\n--" {
			end = i - 3
			break
		}
	}
	if end < start {
		return nil
	}
	return body[start:end]
}

func TestPutRawBlockToLocalBlockstorePinsExactBytes(t *testing.T) {
	var mu sync.Mutex
	var calls []recordedCall
	server := newRecordingKubo(t, &calls, &mu)
	defer server.Close()

	payload := []byte("$EPM identity record bytes \x00\x01\x02 not-ascii")
	got, err := putRawBlockToLocalBlockstore(context.Background(), server.URL, payload)
	if err != nil {
		t.Fatalf("putRawBlockToLocalBlockstore: %v", err)
	}
	if want := rawCIDv1(t, payload); got != want {
		t.Fatalf("CID = %s, want %s", got, want)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one Kubo call, got %d", len(calls))
	}
	call := calls[0]
	if call.path != "/api/v0/block/put" {
		t.Fatalf("path = %s", call.path)
	}
	for key, want := range map[string]string{
		"format": "raw",
		"mhtype": "sha2-256",
		"pin":    "true",
	} {
		if got := call.query[key]; len(got) != 1 || got[0] != want {
			t.Fatalf("query %s = %v, want %s — an unpinned put is the defect this fixes", key, got, want)
		}
	}
	if sent := string(extractMultipartPayload(call.body)); sent != string(payload) {
		t.Fatalf("Kubo received %q, want %q", sent, string(payload))
	}
}

func TestPutRawBlockToLocalBlockstoreRequiresAPIAndBytes(t *testing.T) {
	if _, err := putRawBlockToLocalBlockstore(context.Background(), "", []byte("x")); err == nil {
		t.Fatal("expected an error with no ipfs api url")
	}
	if _, err := putRawBlockToLocalBlockstore(context.Background(), "http://127.0.0.1:1", nil); err == nil {
		t.Fatal("expected an error with no bytes")
	}
}

func TestPutRawBlockToLocalBlockstoreSurfacesKuboFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "datastore is read-only", http.StatusInternalServerError)
	}))
	defer server.Close()
	if _, err := putRawBlockToLocalBlockstore(context.Background(), server.URL, []byte("x")); err == nil {
		t.Fatal("a refusing blockstore must be reported, not swallowed")
	}
}

func TestPinMaterializedDatasetDAGPinsEveryDistinctCID(t *testing.T) {
	var mu sync.Mutex
	var calls []recordedCall
	server := newRecordingKubo(t, &calls, &mu)
	defer server.Close()

	n := newBlockstorePinTestNode(server.URL)
	// Duplicate + empty entries are realistic: a single-shard publication
	// repeats the manifest CID, and IndexCID is optional.
	n.pinMaterializedDatasetDAG("bafmanifest", "bafshard", "", "bafshard", "bafindex")
	n.wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	pinned := map[string]bool{}
	for _, call := range calls {
		if call.path != "/api/v0/pin/add" {
			t.Fatalf("unexpected call to %s", call.path)
		}
		if got := call.query["recursive"]; len(got) != 1 || got[0] != "true" {
			t.Fatalf("recursive = %v, want true — a root-only pin does not hold the shard's leaves", got)
		}
		for _, arg := range call.query["arg"] {
			pinned[arg] = true
		}
	}
	for _, want := range []string{"bafmanifest", "bafshard", "bafindex"} {
		if !pinned[want] {
			t.Fatalf("%s was not pinned; pinned=%v", want, pinned)
		}
	}
	if len(pinned) != 3 {
		t.Fatalf("pinned %d CIDs, want 3 distinct", len(pinned))
	}
}

func TestPinMaterializedDatasetDAGIsInertWithoutIPFSAPI(t *testing.T) {
	n := newBlockstorePinTestNode("")
	n.pinMaterializedDatasetDAG("bafmanifest")
	done := make(chan struct{})
	go func() {
		n.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pinMaterializedDatasetDAG must not spawn work when admin.ipfs_api_url is unset")
	}
}

func TestParseBlockPutCIDAcceptsKeyOrCid(t *testing.T) {
	for _, body := range []string{`{"Key":"bafkqabc"}`, `{"Cid":"bafkqabc"}`} {
		got, err := parseBlockPutCID([]byte(body))
		if err != nil {
			t.Fatalf("parseBlockPutCID(%s): %v", body, err)
		}
		if got != "bafkqabc" {
			t.Fatalf("parseBlockPutCID(%s) = %s", body, got)
		}
	}
	if _, err := parseBlockPutCID([]byte(`{}`)); err == nil {
		t.Fatal("a response with no CID must be an error")
	}
	if _, err := parseBlockPutCID([]byte(`not json`)); err == nil {
		t.Fatal("a non-JSON response must be an error")
	}
}

// Regression: indexLocalNodeEPM runs on partially-built Nodes (constructors and
// tests that never call Start()), so both blockstore paths must tolerate a nil
// config and a nil ctx. Reaching through them segfaulted the whole package.
func TestBlockstorePathsTolerateUnstartedNode(t *testing.T) {
	for name, n := range map[string]*Node{
		"zero value":  {},
		"nil ctx":     {config: &config.Config{}},
		"nil pointer": nil,
	} {
		t.Run(name, func(t *testing.T) {
			n.publishLocalNodeEPMToBlockstore()
			n.pinMaterializedDatasetDAG("bafmanifest")
		})
	}
}

func TestBlockstoreConnectorReportsNotConfigured(t *testing.T) {
	cfg := &config.Config{}
	cfg.Admin.IPFSAPIURL = "   "
	if _, _, ok := (&Node{config: cfg}).blockstoreConnector(); ok {
		t.Fatal("a blank ipfs_api_url must read as NOT configured")
	}
	cfg.Admin.IPFSAPIURL = "http://127.0.0.1:5002"
	apiURL, ctx, ok := (&Node{config: cfg}).blockstoreConnector()
	if !ok || apiURL != "http://127.0.0.1:5002" || ctx == nil {
		t.Fatalf("connector = %q/%v/%v", apiURL, ctx, ok)
	}
}

func newBlockstorePinTestNode(ipfsAPIURL string) *Node {
	cfg := &config.Config{}
	cfg.Admin.IPFSAPIURL = ipfsAPIURL
	n := &Node{config: cfg}
	n.ctx = context.Background()
	return n
}
