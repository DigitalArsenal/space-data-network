package sdnapps_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/sdnapps"
)

type fakeStore struct {
	mu    sync.Mutex
	byCID map[string][]byte
	pairs map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{byCID: map[string][]byte{}, pairs: map[string]bool{}}
}

func (f *fakeStore) StoreManifest(_ context.Context, source, sdsType string, fb []byte) (cid.Cid, error) {
	h, err := mh.Sum(fb, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	c := cid.NewCidV1(cid.Raw, h)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byCID[c.String()] = fb
	f.pairs[source+"|"+sdsType] = true
	return c, nil
}

func TestManifestsValidate(t *testing.T) {
	manifests, err := sdnapps.Manifests()
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	if len(manifests) != 2 {
		t.Fatalf("got %d manifests, want 2", len(manifests))
	}
	ids := map[string]bool{}
	for _, manifest := range manifests {
		if err := manifest.Validate(); err != nil {
			t.Fatalf("%s: Validate: %v", manifest.ID, err)
		}
		ids[manifest.ID] = true
		if len(manifest.Modules) != 0 {
			t.Errorf("%s: expected zero modules, got %d", manifest.ID, len(manifest.Modules))
		}
		if len(manifest.Pages) != 1 || !manifest.Pages[0].Entry || !manifest.Pages[0].IsInline() {
			t.Errorf("%s: expected one inline entry page, got %+v", manifest.ID, manifest.Pages)
		}
		if len(manifest.Dataflow) != 1 {
			t.Errorf("%s: expected one dataflow entry, got %d", manifest.ID, len(manifest.Dataflow))
		}
	}
	if !ids["conjunction"] || !ids["flow-editor"] {
		t.Fatalf("missing expected app ids, got %v", ids)
	}
}

func TestRecordsRoundTrip(t *testing.T) {
	records, err := sdnapps.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	want := map[string]struct {
		schema, transport, locatorHas string
		direction                     appmanifest.FlowDirection
	}{
		"conjunction": {"CDM", "gateway_route", "type=CDM", appmanifest.FlowDirectionToPage},
		"flow-editor": {"PLG", "gateway_route", "/api/v1/flows/bake", appmanifest.FlowDirectionFromPage},
	}
	for id, expected := range want {
		buf, ok := records[id]
		if !ok {
			t.Fatalf("missing record for %q", id)
		}
		manifest, err := appmanifest.FromAPP(buf)
		if err != nil {
			t.Fatalf("%s: FromAPP: %v", id, err)
		}
		if manifest.ID != id {
			t.Errorf("%s: round-tripped ID = %q", id, manifest.ID)
		}
		if err := manifest.Validate(); err != nil {
			t.Fatalf("%s: round-tripped Validate: %v", id, err)
		}
		page, err := manifest.Pages[0].DecodedContent()
		if err != nil {
			t.Fatalf("%s: DecodedContent: %v", id, err)
		}
		sum := sha256.Sum256(page)
		if got := hex.EncodeToString(sum[:]); got != manifest.Pages[0].ContentSHA256 {
			t.Errorf("%s: ContentSHA256 mismatch: got %s want %s", id, got, manifest.Pages[0].ContentSHA256)
		}
		flow := manifest.Dataflow[0]
		if flow.Direction != expected.direction || flow.SDSSchema != expected.schema || string(flow.Transport) != expected.transport || !strings.Contains(flow.Locator, expected.locatorHas) {
			t.Errorf("%s: unexpected dataflow: %+v", id, flow)
		}
	}
}

func TestSelfContained(t *testing.T) {
	foreign := regexp.MustCompile(`https?://`)
	manifests, err := sdnapps.Manifests()
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	for _, manifest := range manifests {
		content := manifest.Pages[0].Content
		if foreign.MatchString(content) {
			t.Errorf("%s: entry page contains an external-origin URL", manifest.ID)
		}
		if !strings.Contains(content, "/sdn/v1/") {
			t.Errorf("%s: entry page does not use the node's /sdn/v1/ API", manifest.ID)
		}
	}
}

func TestFlowEditorWiring(t *testing.T) {
	manifests, err := sdnapps.Manifests()
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	var page string
	for _, manifest := range manifests {
		if manifest.ID == "flow-editor" && len(manifest.Pages) == 1 {
			page = manifest.Pages[0].Content
		}
	}
	if page == "" {
		t.Fatal("flow-editor app or its entry page is missing")
	}
	for _, needle := range []string{"/api/v1/flows/palette", "/api/v1/flows/bake", "moduleRefs", "triggerBindings", "bakeMillis"} {
		if !strings.Contains(page, needle) {
			t.Errorf("flow editor page does not wire %q", needle)
		}
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Error("flow editor page contains an external-origin URL")
	}
}

func TestSeedIdempotent(t *testing.T) {
	store := newFakeStore()
	n, err := sdnapps.Seed(context.Background(), store)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if n != 2 || len(store.byCID) != 2 {
		t.Fatalf("first Seed stored %d records (%d distinct), want 2", n, len(store.byCID))
	}
	if _, err := sdnapps.Seed(context.Background(), store); err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if len(store.byCID) != 2 {
		t.Fatalf("distinct records after re-seed = %d, want 2", len(store.byCID))
	}
	if !store.pairs[sdnapps.Source+"|"+sdnapps.SDSType] || len(store.pairs) != 1 {
		t.Fatalf("unexpected source/type pairs: %v", store.pairs)
	}
}
