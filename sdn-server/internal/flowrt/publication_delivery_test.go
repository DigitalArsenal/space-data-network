package flowrt

// Loop C.6: the SIGNED data-retrieval flow bundle (module publication
// standard — wasm payload + $REC trailer carrying MBL [manifest + ed25519
// signature entry] and the PNM publication notice) travels the module-delivery
// bundle codec, byte-verifies after decryption, installs into the FlowStore
// VERBATIM (signed at rest), and mounts + serves through the normal
// config-mount path with the trailer stripped only at wasm load.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/deliveryclient"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
)

func TestDeliveredSignedFlowArtifactByteVerifiesAndMounts(t *testing.T) {
	dist := dataRetrievalFlowDist(t)

	signedBytes, err := os.ReadFile(filepath.Join(dist, "runtime.wasm"))
	if err != nil {
		t.Fatalf("read signed artifact: %v", err)
	}
	if !modulert.HasPublicationTrailer(signedBytes) {
		t.Skipf("dist artifact is not publication-signed (no $REC trailer) — run flows/data-retrieval publish-sign first")
	}
	signedSum := sha256.Sum256(signedBytes)

	// --- Delivery leg: seal with the module-delivery bundle codec (the same
	// [iv][ciphertext||tag] layout the sdn-js consumer decrypts after a
	// grant), then decrypt and byte-verify the delivered artifact.
	contentKey := make([]byte, 32)
	iv := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, contentKey); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		t.Fatal(err)
	}
	sealed, err := deliveryclient.EncryptBundleAESGCM(contentKey, iv, signedBytes, nil)
	if err != nil {
		t.Fatalf("seal delivery bundle: %v", err)
	}
	delivered, err := deliveryclient.AESGCMBundleDecryptor{}.Decrypt(sealed, contentKey)
	if err != nil {
		t.Fatalf("decrypt delivery bundle: %v", err)
	}
	if deliveredSum := sha256.Sum256(delivered); deliveredSum != signedSum {
		t.Fatalf("delivered artifact does not byte-verify against the published artifact")
	}
	if !modulert.HasPublicationTrailer(delivered) {
		t.Fatalf("delivered artifact lost its $REC publication trailer")
	}

	// --- Install leg: the delivered bytes land in the FlowStore verbatim.
	flowJSON, err := os.ReadFile(filepath.Join(dist, "flow.json"))
	if err != nil {
		t.Fatalf("read flow.json: %v", err)
	}
	artifactJSON, err := os.ReadFile(filepath.Join(dist, "artifact.json"))
	if err != nil {
		t.Fatalf("read artifact.json: %v", err)
	}
	store, err := NewFlowStore(filepath.Join(t.TempDir(), "flows"))
	if err != nil {
		t.Fatalf("NewFlowStore: %v", err)
	}
	const programID = "com.digitalarsenal.flows.data-retrieval"
	if err := store.Install(programID, delivered, flowJSON, artifactJSON); err != nil {
		t.Fatalf("FlowStore.Install: %v", err)
	}
	atRest, err := os.ReadFile(store.WASMPath(programID))
	if err != nil {
		t.Fatalf("read installed artifact: %v", err)
	}
	if atRestSum := sha256.Sum256(atRest); atRestSum != signedSum {
		t.Fatalf("installed artifact differs from the published artifact (must stay signed at rest)")
	}

	// --- Mount leg: resolve by program ID through the FlowStore (exactly the
	// config `flows.mounts` path a delivery-installed node uses) and serve.
	dataStore := newSeededMountStore(t,
		time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix(),
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC).Unix())
	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("storage_query", caps.NewStorageCapFactory(dataStore))

	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{{Path: "/test/data/", Flow: programID}},
		FlowMountDeps{
			CapRegistry:    reg,
			NodeCtx:        &modulert.NodeContext{},
			MaxMemoryPages: 2048,
			Store:          store,
			EngineLink:     dataStore,
		})
	if err != nil {
		t.Fatalf("RegisterFlowMounts: %v", err)
	}
	defer func() {
		for _, mf := range mounted {
			mf.Close()
		}
	}()
	if len(mounted) != 1 || mounted[0].ProgramID() != programID {
		t.Fatalf("mounted = %#v", mounted)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/test/data/omm/bulk?limit=10")
	if err != nil {
		t.Fatalf("GET omm/bulk: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("omm/bulk status = %d body=%q", resp.StatusCode, body)
	}
	if len(body) == 0 {
		t.Fatalf("omm/bulk returned an empty stream")
	}

	notFound, err := http.Get(srv.URL + "/test/data/nope")
	if err != nil {
		t.Fatalf("GET nope: %v", err)
	}
	io.Copy(io.Discard, notFound.Body)
	notFound.Body.Close()
	if notFound.StatusCode != http.StatusNotFound {
		t.Fatalf("nope status = %d, want 404", notFound.StatusCode)
	}

	// The installed flow is discoverable like any delivered module.
	flows, err := store.List()
	if err != nil || len(flows) != 1 {
		t.Fatalf("List = %v, %v", flows, err)
	}
	var meta struct {
		ProgramID string `json:"programId"`
	}
	if err := json.Unmarshal(artifactJSON, &meta); err != nil || meta.ProgramID != programID {
		t.Fatalf("artifact.json programId = %q err=%v", meta.ProgramID, err)
	}
	if !bytes.Equal(atRest, delivered) {
		t.Fatalf("at-rest artifact mutated")
	}
}
