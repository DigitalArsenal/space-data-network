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

// fakeStore is an in-memory ManifestStore that dedups by content, mirroring
// sdnstore.StoreManifest's idempotency contract without pulling in the FlatSQL
// engine. It records every (source, type) pair and every stored CID.
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
	ms, err := sdnapps.Manifests()
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("got %d manifests, want 2", len(ms))
	}
	ids := map[string]bool{}
	for _, m := range ms {
		if err := m.Validate(); err != nil {
			t.Fatalf("%s: Validate: %v", m.ID, err)
		}
		ids[m.ID] = true
		// Each app is pure-UI: exactly one inline entry page, no member modules.
		if len(m.Modules) != 0 {
			t.Errorf("%s: expected zero modules, got %d", m.ID, len(m.Modules))
		}
		if len(m.Pages) != 1 || !m.Pages[0].Entry || !m.Pages[0].IsInline() {
			t.Errorf("%s: expected one inline entry page, got %+v", m.ID, m.Pages)
		}
		if len(m.Dataflow) != 1 {
			t.Errorf("%s: expected one dataflow entry, got %d", m.ID, len(m.Dataflow))
		}
	}
	if !ids["supplemental-omm"] || !ids["conjunction"] {
		t.Fatalf("missing expected app ids, got %v", ids)
	}
}

// TestRecordsRoundTrip proves each app survives the published $APP FlatBuffer
// round-trip (ToAPP -> bytes -> FromAPP): the inline UI page content and the
// declared dataflow contract come back field-for-field, and the record
// re-validates (ContentSHA256 over the decoded page still matches).
func TestRecordsRoundTrip(t *testing.T) {
	recs, err := sdnapps.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	want := map[string]struct {
		schema, transport, locatorHas string
	}{
		"supplemental-omm": {"OMM", "gateway_route", "type=OMM"},
		"conjunction":      {"CDM", "gateway_route", "type=CDM"},
	}
	for id, exp := range want {
		buf, ok := recs[id]
		if !ok {
			t.Fatalf("missing record for %q", id)
		}
		m, err := appmanifest.FromAPP(buf)
		if err != nil {
			t.Fatalf("%s: FromAPP: %v", id, err)
		}
		if m.ID != id {
			t.Errorf("%s: round-tripped ID = %q", id, m.ID)
		}
		if err := m.Validate(); err != nil {
			t.Fatalf("%s: round-tripped Validate: %v", id, err)
		}
		// Inline entry page survives with intact, verifiable content.
		if len(m.Pages) != 1 {
			t.Fatalf("%s: pages = %d, want 1", id, len(m.Pages))
		}
		p := m.Pages[0]
		decoded, err := p.DecodedContent()
		if err != nil {
			t.Fatalf("%s: DecodedContent: %v", id, err)
		}
		if !strings.Contains(string(decoded), "/sdn/v1/data") {
			t.Errorf("%s: entry page does not reference /sdn/v1/data", id)
		}
		sum := sha256.Sum256(decoded)
		if got := hex.EncodeToString(sum[:]); got != p.ContentSHA256 {
			t.Errorf("%s: ContentSHA256 mismatch: page declares %s, decoded hashes to %s", id, p.ContentSHA256, got)
		}
		// Dataflow contract survives field-for-field.
		if len(m.Dataflow) != 1 {
			t.Fatalf("%s: dataflow = %d, want 1", id, len(m.Dataflow))
		}
		f := m.Dataflow[0]
		if f.Direction != appmanifest.FlowDirectionToPage {
			t.Errorf("%s: dataflow direction = %q, want to_page", id, f.Direction)
		}
		if f.SDSSchema != exp.schema {
			t.Errorf("%s: dataflow sdsSchema = %q, want %q", id, f.SDSSchema, exp.schema)
		}
		if string(f.Transport) != exp.transport {
			t.Errorf("%s: dataflow transport = %q, want %q", id, f.Transport, exp.transport)
		}
		if !strings.Contains(f.Locator, exp.locatorHas) {
			t.Errorf("%s: dataflow locator = %q, want it to contain %q", id, f.Locator, exp.locatorHas)
		}
	}
}

// TestSelfContained asserts every app's inline UI page makes NO external-origin
// request: no http(s):// URL of any kind appears in the page bytes. The whole
// app is served from the node loopback; a foreign origin would break the
// self-contained contract.
func TestSelfContained(t *testing.T) {
	foreign := regexp.MustCompile(`https?://`)
	ms, err := sdnapps.Manifests()
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	for _, m := range ms {
		content := m.Pages[0].Content
		if foreign.MatchString(content) {
			loc := foreign.FindStringIndex(content)
			t.Errorf("%s: entry page contains an external-origin URL near %q", m.ID,
				content[loc[0]:min(loc[0]+60, len(content))])
		}
		if !strings.Contains(content, "/sdn/v1/") {
			t.Errorf("%s: entry page does not use the node's /sdn/v1/ API", m.ID)
		}
	}
}

// TestSeedIdempotent stores every app via Seed, then seeds again: the second
// pass writes no new distinct records (content-addressed dedup), and both apps
// land under the single (Source, "APP") pair.
func TestSeedIdempotent(t *testing.T) {
	fs := newFakeStore()
	n, err := sdnapps.Seed(context.Background(), fs)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if n != 2 {
		t.Fatalf("first Seed stored %d, want 2", n)
	}
	if len(fs.byCID) != 2 {
		t.Fatalf("distinct records after first Seed = %d, want 2", len(fs.byCID))
	}
	if _, err := sdnapps.Seed(context.Background(), fs); err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if len(fs.byCID) != 2 {
		t.Fatalf("distinct records after re-seed = %d, want 2 (idempotent)", len(fs.byCID))
	}
	if !fs.pairs[sdnapps.Source+"|"+sdnapps.SDSType] {
		t.Fatalf("apps were not stored under (%q, %q)", sdnapps.Source, sdnapps.SDSType)
	}
	if len(fs.pairs) != 1 {
		t.Fatalf("apps spread across %d (source,type) pairs, want 1", len(fs.pairs))
	}
}
