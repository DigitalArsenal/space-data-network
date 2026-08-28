package auth

// The account-EPM endpoint contract (owner directive 2026-08-28).
//
// These tests pin what makes the endpoint safe and what makes it useful: it is
// self-scoped (there is no way to name another account), it refuses honestly
// when it cannot build or store, and what it stores is what it hands back.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// fakeAccountEPMStore is an in-memory stand-in for the node's record + pin
// lane. `pinned` is what the reconciler probes; dropping an entry from it is
// how a test deliberately unpins a binding.
type fakeAccountEPMStore struct {
	mu      sync.Mutex
	pinned  map[string]bool
	stores  int
	failNow bool
}

func newFakeAccountEPMStore() *fakeAccountEPMStore {
	return &fakeAccountEPMStore{pinned: map[string]bool{}}
}

func (f *fakeAccountEPMStore) StoreAccountEPM(_ context.Context, sourceName string, epmBytes []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNow {
		return "", errors.New("store is down")
	}
	if strings.TrimSpace(sourceName) == "" {
		return "", errors.New("source name is required")
	}
	f.stores++
	cid := fmt.Sprintf("cid-%x", ed25519.Sign(fixedTestSigner, epmBytes)[:8])
	f.pinned[cid] = true
	return cid, nil
}

func (f *fakeAccountEPMStore) AccountEPMPinned(_ context.Context, cid string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pinned[cid], nil
}

func (f *fakeAccountEPMStore) unpin(cid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pinned, cid)
}

// fixedTestSigner keeps the fake store's CIDs deterministic per byte string.
var fixedTestSigner = ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))

type accountEPMFixture struct {
	handler   *Handler
	userStore *UserStore
	store     *fakeAccountEPMStore
	token     string
	xpub      string
}

func newAccountEPMFixture(t *testing.T, signingPubKeyHex string) accountEPMFixture {
	t.Helper()

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = userStore.Close() })

	const xpub = "xpub-account-epm-operator"
	if err := userStore.AddUser(xpub, "Ada", peers.Marginal, signingPubKeyHex); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	token, err := sessions.CreateSession(xpub, peers.Marginal, "127.0.0.1", "test-agent", 24*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	handler := NewHandler(userStore, sessions, 24*time.Hour, "", "")
	store := newFakeAccountEPMStore()
	handler.SetAccountEPMServices(newTestEPMService(t), store)

	return accountEPMFixture{handler: handler, userStore: userStore, store: store, token: token, xpub: xpub}
}

// newTestEPMService is a node EPM service holding only a signing key: enough to
// ISSUE a custodial account record, which is all this endpoint needs.
func newTestEPMService(t *testing.T) AccountEPMBuilder {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}
	svc := epm.NewService(nil, nil, "", "", t.TempDir())
	if err := svc.SetRuntimeSigningKey(priv, "sdn/runtime-signing"); err != nil {
		t.Fatalf("SetRuntimeSigningKey: %v", err)
	}
	return svc
}

func testAccountSigningKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate account key: %v", err)
	}
	return hex.EncodeToString(pub)
}

func (f accountEPMFixture) do(t *testing.T, method, body string, withSession bool) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/auth/epm", reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if withSession {
		req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: f.token})
	}
	rec := httptest.NewRecorder()
	f.handler.handleAccountEPM(rec, req)
	return rec
}

// The happy path: a profile goes in, a signed record is stored and pinned, the
// CID comes back, and GET hands the same profile back through the same
// projection the node EPM endpoint uses.
func TestAccountEPM_PutStoresPinsAndGetRoundTrips(t *testing.T) {
	t.Parallel()

	fixture := newAccountEPMFixture(t, testAccountSigningKey(t))

	if rec := fixture.do(t, http.MethodGet, "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("GET before any publish = %d, want 404; body %s", rec.Code, rec.Body.String())
	}

	profile := `{"dn":"Ada Lovelace","job_title":"Analyst","email":"ada@example.test",` +
		`"photo_data_url":"data:image/png;base64,AAAA","address":{"country":"GB","locality":"London"}}`
	rec := fixture.do(t, http.MethodPut, profile, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var put struct {
		CID string `json:"cid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &put); err != nil || put.CID == "" {
		t.Fatalf("PUT body = %s (err %v)", rec.Body.String(), err)
	}

	// PINNED, not merely stored: the lane reports the CID as held.
	if pinned, _ := fixture.store.AccountEPMPinned(context.Background(), put.CID); !pinned {
		t.Fatalf("stored CID %s is not pinned", put.CID)
	}

	// The binding is durable — the row, not a cache.
	binding, ok, err := fixture.userStore.AccountEPM(fixture.xpub)
	if err != nil || !ok {
		t.Fatalf("AccountEPM binding = (%v, %v)", ok, err)
	}
	if binding.CID != put.CID {
		t.Fatalf("bound CID = %q, want %q", binding.CID, put.CID)
	}
	if err := epm.VerifyEPMSignature(binding.EPMData); err != nil {
		t.Fatalf("stored record does not verify: %v", err)
	}

	rec = fixture.do(t, http.MethodGet, "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		CID       string                 `json:"cid"`
		EPM       map[string]interface{} `json:"epm"`
		UpdatedAt string                 `json:"updated_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET body: %v (%s)", err, rec.Body.String())
	}
	if got.CID != put.CID {
		t.Fatalf("GET cid = %q, want %q", got.CID, put.CID)
	}
	if got.UpdatedAt == "" {
		t.Fatal("GET returned no updated_at")
	}
	for field, want := range map[string]string{
		"dn":             "Ada Lovelace",
		"job_title":      "Analyst",
		"email":          "ada@example.test",
		"photo_data_url": "data:image/png;base64,AAAA",
	} {
		if value, _ := got.EPM[field].(string); value != want {
			t.Fatalf("GET epm[%q] = %q, want %q", field, value, want)
		}
	}
}

func TestAccountEPM_Refusals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		signingKey  func(*testing.T) string
		unbind      bool
		method      string
		body        string
		withSession bool
		want        int
	}{
		{
			name:        "no session",
			signingKey:  testAccountSigningKey,
			method:      http.MethodPut,
			body:        `{"dn":"Ada"}`,
			withSession: false,
			want:        http.StatusUnauthorized,
		},
		{
			name:        "malformed JSON",
			signingKey:  testAccountSigningKey,
			method:      http.MethodPut,
			body:        `{"dn":`,
			withSession: true,
			want:        http.StatusBadRequest,
		},
		{
			name:        "account with no bound signing key",
			signingKey:  func(*testing.T) string { return "" },
			method:      http.MethodPut,
			body:        `{"dn":"Ada"}`,
			withSession: true,
			want:        http.StatusForbidden,
		},
		{
			name:        "no record lane bound",
			signingKey:  testAccountSigningKey,
			unbind:      true,
			method:      http.MethodPut,
			body:        `{"dn":"Ada"}`,
			withSession: true,
			want:        http.StatusNotImplemented,
		},
		{
			name:        "method not allowed",
			signingKey:  testAccountSigningKey,
			method:      http.MethodDelete,
			withSession: true,
			want:        http.StatusMethodNotAllowed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newAccountEPMFixture(t, tc.signingKey(t))
			if tc.unbind {
				fixture.handler.SetAccountEPMServices(nil, nil)
			}
			rec := fixture.do(t, tc.method, tc.body, tc.withSession)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body.String())
			}
			// Nothing may have been bound on a refused write.
			if _, ok, _ := fixture.userStore.AccountEPM(fixture.xpub); ok {
				t.Fatal("a refused request still bound an account EPM")
			}
		})
	}
}

// A store that cannot hold the record must not leave a binding behind: a
// binding whose CID nothing holds is exactly the dangling reference the fleet
// law exists to prevent.
func TestAccountEPM_StoreFailureLeavesNoBinding(t *testing.T) {
	t.Parallel()

	fixture := newAccountEPMFixture(t, testAccountSigningKey(t))
	fixture.store.failNow = true

	rec := fixture.do(t, http.MethodPut, `{"dn":"Ada"}`, true)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := fixture.userStore.AccountEPM(fixture.xpub); ok {
		t.Fatal("a failed store still bound an account EPM")
	}
}

// THE FLEET LAW: every EPM created by an account tied to this node stays
// pinned. Drop the pin behind the reconciler's back; it must put it back.
func TestAccountEPMReconciler_RepinsUnpinnedBinding(t *testing.T) {
	t.Parallel()

	fixture := newAccountEPMFixture(t, testAccountSigningKey(t))
	rec := fixture.do(t, http.MethodPut, `{"dn":"Ada Lovelace"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d; body %s", rec.Code, rec.Body.String())
	}
	binding, ok, err := fixture.userStore.AccountEPM(fixture.xpub)
	if err != nil || !ok {
		t.Fatalf("AccountEPM binding = (%v, %v)", ok, err)
	}

	reconciler := NewAccountEPMReconciler(fixture.userStore, fixture.store)
	if reconciler == nil {
		t.Fatal("NewAccountEPMReconciler returned nil for a bound lane")
	}

	// A pass over an intact binding re-pins nothing.
	if result := reconciler.Run(context.Background()); result.Checked != 1 || result.Repinned != 0 || len(result.Unsatisfied) != 0 {
		t.Fatalf("intact pass = %+v, want 1 checked / 0 re-pinned / 0 unsatisfied", result)
	}

	fixture.store.unpin(binding.CID)
	if pinned, _ := fixture.store.AccountEPMPinned(context.Background(), binding.CID); pinned {
		t.Fatal("unpin did not take effect")
	}

	result := reconciler.Run(context.Background())
	if result.Checked != 1 || result.Repinned != 1 || len(result.Unsatisfied) != 0 {
		t.Fatalf("reconcile pass = %+v, want 1 checked / 1 re-pinned / 0 unsatisfied", result)
	}
	if pinned, _ := fixture.store.AccountEPMPinned(context.Background(), binding.CID); !pinned {
		t.Fatalf("reconciler did not re-pin %s", binding.CID)
	}
	// The re-pin re-stored the SAME bytes, so the CID is unchanged.
	after, _, _ := fixture.userStore.AccountEPM(fixture.xpub)
	if after.CID != binding.CID {
		t.Fatalf("CID after reconcile = %q, want %q", after.CID, binding.CID)
	}
}
