package api

import (
	"bytes"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type memoryClaimFrameStore struct {
	frames [][]byte
}

func (s *memoryClaimFrameStore) LoadClaimFrames() ([][]byte, error) {
	out := make([][]byte, len(s.frames))
	for i := range s.frames {
		out[i] = append([]byte(nil), s.frames[i]...)
	}
	return out, nil
}

func (s *memoryClaimFrameStore) StoreClaimFrame(frame []byte, _ string, _ []byte) (string, error) {
	s.frames = append(s.frames, append([]byte(nil), frame...))
	return storage.ComputeCID(frame[framePrefixLength:]), nil
}

type claimTestIdentity struct {
	profile []byte
	key     ed25519.PrivateKey
	cid     string
}

func newClaimTestIdentity(t *testing.T, peerID string) claimTestIdentity {
	t.Helper()
	profile, key := signedNodeEPMFixture(t, peerID, peerID+" Org")
	cid, err := epm.ComputeEPMCID(profile)
	if err != nil {
		t.Fatal(err)
	}
	return claimTestIdentity{profile: profile, key: key, cid: cid}
}

func signClaimFixture(t *testing.T, claim ClaimRecord, identity claimTestIdentity) ClaimRecord {
	t.Helper()
	claim.ClaimantProfileCID = identity.cid
	payload, err := ClaimSigningPayload(claim)
	if err != nil {
		t.Fatal(err)
	}
	claim.Signature = ed25519.Sign(identity.key, payload)
	return claim
}

func claimResolver(identities map[string]claimTestIdentity) func(string) []byte {
	return func(peerID string) []byte {
		return append([]byte(nil), identities[peerID].profile...)
	}
}

func claimFrameFixture(t *testing.T, claim ClaimRecord, identity claimTestIdentity) []byte {
	t.Helper()
	frame, err := EncodeClaimFrame(signClaimFixture(t, claim, identity))
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func claimHandlerFixture(t *testing.T, store ClaimFrameStore, identities map[string]claimTestIdentity, trusted func(string) bool) *ClaimsHandler {
	t.Helper()
	self := identities["self"]
	return NewClaimsHandler(ClaimsHandlerOptions{
		SelfPeerID: "self",
		SigningKey: self.key,
		Store:      store,
		ResolveEPM: claimResolver(identities),
		Trusted:    trusted,
		Protect: func(next http.HandlerFunc) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Test-Operator") != "yes" {
					WriteErrorFrame(w, http.StatusUnauthorized, "operator_required", "An operator session is required.", 0)
					return
				}
				next(w, r)
			}
		},
		Now:        func() time.Time { return time.UnixMilli(1_788_000_123_456).UTC() },
		NewClaimID: func() string { return "45c35242-d59d-4e44-8a87-a00fe2865f87" },
	})
}

func serveClaims(h *ClaimsHandler, method, target string, body []byte, operator bool) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if operator {
		req.Header.Set("X-Test-Operator", "yes")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodedClaimFrames(t *testing.T, body []byte) []ClaimRecord {
	t.Helper()
	frames, err := SplitFrames(body)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]ClaimRecord, 0, len(frames))
	for _, frame := range frames {
		claim, err := DecodeClaimFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, claim)
	}
	return out
}

func TestClaimSignAndCountersignRoundTrip(t *testing.T) {
	claimant := newClaimTestIdentity(t, "claimant")
	verifier := newClaimTestIdentity(t, "verifier")
	identities := map[string]claimTestIdentity{"claimant": claimant, "verifier": verifier}
	claim := signClaimFixture(t, ClaimRecord{
		ClaimID:        "claim-1",
		ClaimantPeerID: "claimant",
		ObjectKind:     ClaimObjectKindSatellite,
		ObjectID:       "25544",
		ObjectName:     "ISS",
		Role:           ClaimRoleOperator,
		Statement:      "We operate this satellite.",
		CreatedAt:      1_788_000_000_000,
		UpdatedAt:      1_788_000_000_000,
	}, claimant)
	payload, err := ClaimSigningPayload(claim)
	if err != nil {
		t.Fatal(err)
	}
	claim.Countersignatures = append(claim.Countersignatures, ClaimCountersignature{
		PeerID:     "verifier",
		ProfileCID: verifier.cid,
		SignedAt:   1_788_000_001_000,
		Signature:  ed25519.Sign(verifier.key, payload),
	})
	if err := VerifyClaimSignatures(claim, claimResolver(identities)); err != nil {
		t.Fatalf("signed claim did not verify: %v", err)
	}
	frame, err := EncodeClaimFrame(claim)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeClaimFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaimID != claim.ClaimID || len(got.Countersignatures) != 1 || !bytes.Equal(got.Signature, claim.Signature) {
		t.Fatalf("round trip = %+v", got)
	}
	if err := VerifyClaimSignatures(got, claimResolver(identities)); err != nil {
		t.Fatalf("round-tripped claim did not verify: %v", err)
	}
}

func TestClaimUnsignedPostFillsIdentityTimesIDAndSignature(t *testing.T) {
	self := newClaimTestIdentity(t, "self")
	identities := map[string]claimTestIdentity{"self": self}
	store := new(memoryClaimFrameStore)
	h := claimHandlerFixture(t, store, identities, nil)
	draft, err := EncodeClaimFrame(ClaimRecord{
		ObjectKind: ClaimObjectKindSite,
		ObjectID:   "site-42",
		ObjectName: "Example Site",
		Role:       ClaimRoleOwner,
		Statement:  "We own this site.",
	})
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := serveClaims(h, http.MethodPost, ClaimsPath, draft, false)
	if unauthorized.Code != http.StatusUnauthorized || len(store.frames) != 0 {
		t.Fatalf("unauthorized POST = %d, stored=%d", unauthorized.Code, len(store.frames))
	}
	rec := serveClaims(h, http.MethodPost, ClaimsPath, draft, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST claim = %d: %s", rec.Code, rec.Body.String())
	}
	claims := decodedClaimFrames(t, rec.Body.Bytes())
	if len(claims) != 1 {
		t.Fatalf("response claims = %d", len(claims))
	}
	claim := claims[0]
	if _, err := uuid.Parse(claim.ClaimID); err != nil {
		t.Fatalf("CLAIM_ID %q is not a UUID: %v", claim.ClaimID, err)
	}
	if claim.ClaimantPeerID != "self" || claim.ClaimantProfileCID != self.cid {
		t.Fatalf("claimant identity = %q / %q", claim.ClaimantPeerID, claim.ClaimantProfileCID)
	}
	if claim.CreatedAt != 1_788_000_123_456 || claim.UpdatedAt != claim.CreatedAt || len(claim.Signature) == 0 {
		t.Fatalf("server fields = %+v", claim)
	}
	if err := VerifyClaimSignatures(claim, claimResolver(identities)); err != nil {
		t.Fatalf("server signature: %v", err)
	}
	if len(store.frames) != 1 || rec.Header().Get("X-SDN-Record-CID") == "" || rec.Header().Get(StreamSchemaHeader) != ClaimsSchemaName {
		t.Fatalf("stored=%d headers=%v", len(store.frames), rec.Header())
	}

	retraction, err := EncodeClaimFrame(ClaimRecord{ClaimID: claim.ClaimID, Deleted: true})
	if err != nil {
		t.Fatal(err)
	}
	rec = serveClaims(h, http.MethodPost, ClaimsPath, retraction, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST retraction = %d: %s", rec.Code, rec.Body.String())
	}
	retracted := decodedClaimFrames(t, rec.Body.Bytes())
	if len(retracted) != 1 || !retracted[0].Deleted || retracted[0].ClaimID != claim.ClaimID || retracted[0].ObjectID != claim.ObjectID {
		t.Fatalf("retraction = %+v", retracted)
	}
	if err := VerifyClaimSignatures(retracted[0], claimResolver(identities)); err != nil {
		t.Fatalf("retraction signature: %v", err)
	}
	get := serveClaims(h, http.MethodGet, ClaimsPath, nil, false)
	projected := decodedClaimFrames(t, get.Body.Bytes())
	if len(projected) != 1 || !projected[0].Deleted {
		t.Fatalf("projected retraction = %+v", projected)
	}
}

func TestClaimPresignedBadSignatureIsRejected(t *testing.T) {
	self := newClaimTestIdentity(t, "self")
	claimant := newClaimTestIdentity(t, "claimant")
	identities := map[string]claimTestIdentity{"self": self, "claimant": claimant}
	store := new(memoryClaimFrameStore)
	h := claimHandlerFixture(t, store, identities, nil)
	claim := signClaimFixture(t, ClaimRecord{
		ClaimID:        "bad-signature",
		ClaimantPeerID: "claimant",
		ObjectKind:     ClaimObjectKindSensor,
		ObjectID:       "sensor-1",
		Role:           ClaimRoleOperator,
		CreatedAt:      1_788_000_000_000,
		UpdatedAt:      1_788_000_000_000,
	}, claimant)
	claim.Signature[0] ^= 0xff
	frame, err := EncodeClaimFrame(claim)
	if err != nil {
		t.Fatal(err)
	}
	rec := serveClaims(h, http.MethodPost, ClaimsPath, frame, true)
	if rec.Code != http.StatusBadRequest || len(store.frames) != 0 {
		t.Fatalf("bad signature POST = %d, stored=%d", rec.Code, len(store.frames))
	}
	frames, err := SplitFrames(rec.Body.Bytes())
	if err != nil || len(frames) != 1 || FrameIdentifier(frames[0]) != "$QRP" {
		t.Fatalf("error body is not one QRP: frames=%d err=%v", len(frames), err)
	}
}

func TestClaimPeerAndObjectFiltersIncludeTombstones(t *testing.T) {
	self := newClaimTestIdentity(t, "self")
	peer := newClaimTestIdentity(t, "peer-a")
	identities := map[string]claimTestIdentity{"self": self, "peer-a": peer}
	baseTime := uint64(1_788_000_000_000)
	store := &memoryClaimFrameStore{frames: [][]byte{
		claimFrameFixture(t, ClaimRecord{ClaimID: "self-satellite", ClaimantPeerID: "self", ObjectKind: ClaimObjectKindSatellite, ObjectID: "25544", Role: ClaimRoleOwner, CreatedAt: baseTime, UpdatedAt: baseTime}, self),
		claimFrameFixture(t, ClaimRecord{ClaimID: "peer-satellite", ClaimantPeerID: "peer-a", ObjectKind: ClaimObjectKindSatellite, ObjectID: "25544", Role: ClaimRoleOperator, CreatedAt: baseTime, UpdatedAt: baseTime}, peer),
		claimFrameFixture(t, ClaimRecord{ClaimID: "peer-service", ClaimantPeerID: "peer-a", ObjectKind: ClaimObjectKindService, ObjectID: "bafy-listing", Role: ClaimRoleProvider, CreatedAt: baseTime, UpdatedAt: baseTime}, peer),
		claimFrameFixture(t, ClaimRecord{ClaimID: "self-tombstone", ClaimantPeerID: "self", ObjectKind: ClaimObjectKindSite, ObjectID: "site-old", Role: ClaimRoleOwner, CreatedAt: baseTime, UpdatedAt: baseTime + 1, Deleted: true}, self),
	}}
	h := claimHandlerFixture(t, store, identities, nil)

	tests := []struct {
		target   string
		wantIDs  map[string]bool
		wantTomb bool
	}{
		{target: ClaimsPath, wantIDs: map[string]bool{"self-satellite": true, "self-tombstone": true}, wantTomb: true},
		{target: ClaimsPath + "?peer=peer-a", wantIDs: map[string]bool{"peer-satellite": true, "peer-service": true}},
		{target: ClaimsPath + "?object=satellite:25544", wantIDs: map[string]bool{"self-satellite": true, "peer-satellite": true}},
	}
	for _, tc := range tests {
		rec := serveClaims(h, http.MethodGet, tc.target, nil, false)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", tc.target, rec.Code)
		}
		claims := decodedClaimFrames(t, rec.Body.Bytes())
		got := map[string]bool{}
		tomb := false
		for _, claim := range claims {
			got[claim.ClaimID] = true
			tomb = tomb || claim.Deleted
		}
		if len(got) != len(tc.wantIDs) {
			t.Fatalf("GET %s ids=%v want=%v", tc.target, got, tc.wantIDs)
		}
		for id := range tc.wantIDs {
			if !got[id] {
				t.Fatalf("GET %s missed %s: %v", tc.target, id, got)
			}
		}
		if tomb != tc.wantTomb {
			t.Fatalf("GET %s tombstone=%v want=%v", tc.target, tomb, tc.wantTomb)
		}
	}

	bad := serveClaims(h, http.MethodGet, ClaimsPath+"?peer=peer-a&object=site:x", nil, false)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous filter = %d, want 400", bad.Code)
	}
}

func TestClaimVerifyAppendsCountersignatureAndRejectsUntrustedClaimant(t *testing.T) {
	self := newClaimTestIdentity(t, "self")
	claimant := newClaimTestIdentity(t, "claimant")
	identities := map[string]claimTestIdentity{"self": self, "claimant": claimant}
	remoteFrame := claimFrameFixture(t, ClaimRecord{
		ClaimID:        "remote-claim",
		ClaimantPeerID: "claimant",
		ObjectKind:     ClaimObjectKindSatellite,
		ObjectID:       "43013",
		Role:           ClaimRoleOperator,
		CreatedAt:      1_788_000_000_000,
		UpdatedAt:      1_788_000_000_000,
	}, claimant)
	store := &memoryClaimFrameStore{frames: [][]byte{remoteFrame}}
	isTrusted := true
	h := claimHandlerFixture(t, store, identities, func(peerID string) bool { return isTrusted && peerID == "claimant" })

	unauthorized := serveClaims(h, http.MethodPost, ClaimsPath+"/remote-claim/verify", nil, false)
	if unauthorized.Code != http.StatusUnauthorized || len(store.frames) != 1 {
		t.Fatalf("unauthorized verify = %d stored=%d", unauthorized.Code, len(store.frames))
	}
	rec := serveClaims(h, http.MethodPost, ClaimsPath+"/remote-claim/verify", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify = %d: %s", rec.Code, rec.Body.String())
	}
	claims := decodedClaimFrames(t, rec.Body.Bytes())
	if len(claims) != 1 || len(claims[0].Countersignatures) != 1 {
		t.Fatalf("verified response = %+v", claims)
	}
	counter := claims[0].Countersignatures[0]
	if counter.PeerID != "self" || counter.ProfileCID != self.cid || counter.SignedAt != 1_788_000_123_456 {
		t.Fatalf("countersignature = %+v", counter)
	}
	if err := VerifyClaimSignatures(claims[0], claimResolver(identities)); err != nil {
		t.Fatalf("countersigned claim did not verify: %v", err)
	}

	// The same UPDATED_AT is claimant-signed and cannot change; projection
	// must nevertheless prefer the version carrying the new attestation.
	get := serveClaims(h, http.MethodGet, ClaimsPath+"?peer=claimant", nil, false)
	projected := decodedClaimFrames(t, get.Body.Bytes())
	if len(projected) != 1 || len(projected[0].Countersignatures) != 1 {
		t.Fatalf("projected countersigned claim = %+v", projected)
	}

	isTrusted = false
	forbidden := serveClaims(h, http.MethodPost, ClaimsPath+"/remote-claim/verify", nil, true)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("untrusted verify = %d, want 403", forbidden.Code)
	}
	errorFrames, err := SplitFrames(forbidden.Body.Bytes())
	if err != nil || len(errorFrames) != 1 || FrameIdentifier(errorFrames[0]) != "$QRP" {
		t.Fatalf("403 body is not one QRP: frames=%d err=%v", len(errorFrames), err)
	}
}

func TestClaimRouteAndAuthGrammar(t *testing.T) {
	self := newClaimTestIdentity(t, "self")
	h := claimHandlerFixture(t, new(memoryClaimFrameStore), map[string]claimTestIdentity{"self": self}, nil)
	if rec := serveClaims(h, http.MethodGet, ClaimsPath, nil, false); rec.Code != http.StatusOK {
		t.Fatalf("public GET = %d", rec.Code)
	}
	if rec := serveClaims(h, http.MethodPost, ClaimsPath, nil, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("protected POST = %d", rec.Code)
	}
	if rec := serveClaims(h, http.MethodPost, ClaimsPath+"/missing/verify", nil, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("protected verify = %d", rec.Code)
	}
	if rec := serveClaims(h, http.MethodPut, ClaimsPath, nil, true); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT collection = %d", rec.Code)
	}
	if rec := serveClaims(h, http.MethodPost, ClaimsPath+"/id/not-verify", nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("bad verify route = %d", rec.Code)
	}
}

func TestClaimFlatSQLStoreRoutesCLMThroughTheStandardsEngine(t *testing.T) {
	claimant := newClaimTestIdentity(t, "claimant")
	claim := signClaimFixture(t, ClaimRecord{
		ClaimID:        "engine-claim",
		ClaimantPeerID: "claimant",
		ObjectKind:     ClaimObjectKindService,
		ObjectID:       "bafy-service",
		Role:           ClaimRoleProvider,
		CreatedAt:      1_788_000_000_000,
		UpdatedAt:      1_788_000_000_000,
	}, claimant)
	frame, err := EncodeClaimFrame(claim)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := validator.RouteBuffer("", frame)
	if err != nil || decision.Schema != ClaimsSchemaName || !decision.FromHeader {
		t.Fatalf("CLM route = %+v err=%v", decision, err)
	}
	if err := validator.Validate(t.Context(), ClaimsSchemaName, frame); err != nil {
		t.Fatalf("CLM validation: %v", err)
	}
	flat, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flat.Close() })
	store := NewFlatSQLClaimFrameStore(flat)
	if _, err := store.StoreClaimFrame(frame, claim.ClaimantPeerID, claim.Signature); err != nil {
		t.Fatal(err)
	}
	stream, err := flat.QueryRawStream("SELECT _data FROM CLM")
	if err != nil {
		t.Fatalf("SELECT _data FROM CLM: %v", err)
	}
	claims := decodedClaimFrames(t, stream.Bytes)
	if len(claims) != 1 || claims[0].ClaimID != claim.ClaimID {
		t.Fatalf("engine claims = %+v", claims)
	}
	stored, err := store.LoadClaimFrames()
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored claims=%d err=%v", len(stored), err)
	}
}
