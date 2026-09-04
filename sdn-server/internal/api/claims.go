package api

// Signed ownership/operator claim lanes for the node-first dashboard.
//
// Claims stay ordinary SDS records in the shared FlatSQL store. HTTP only
// adds/removes the standard little-endian size prefix used by the dashboard
// frame lanes; peer pubsub/sync continues to use the generic record paths.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	standardsCLM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CLM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/google/uuid"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	ClaimsPath       = "/api/v1/claims"
	ClaimsSchemaName = "CLM.fbs"

	ClaimObjectKindUnspecified int8 = 0
	ClaimObjectKindSatellite   int8 = 1
	ClaimObjectKindSite        int8 = 2
	ClaimObjectKindSensor      int8 = 3
	ClaimObjectKindService     int8 = 4

	ClaimRoleUnspecified int8 = 0
	ClaimRoleOwner       int8 = 1
	ClaimRoleOperator    int8 = 2
	ClaimRoleProvider    int8 = 3
)

// ClaimCountersignature is one peer's signature over ClaimSigningPayload.
type ClaimCountersignature struct {
	PeerID     string
	ProfileCID string
	SignedAt   uint64
	Signature  []byte
}

// ClaimRecord is the Go projection of the canonical SDS $CLM record.
type ClaimRecord struct {
	ClaimID            string
	ClaimantPeerID     string
	ClaimantProfileCID string
	ObjectKind         int8
	ObjectID           string
	ObjectName         string
	Role               int8
	Statement          string
	EvidenceURL        string
	CreatedAt          uint64
	UpdatedAt          uint64
	Deleted            bool
	Signature          []byte
	Countersignatures  []ClaimCountersignature
}

// ClaimFrameStore is the claims-specific view over the ordinary FlatSQL record
// store. Frames at this boundary are always size-prefixed $CLM buffers.
type ClaimFrameStore interface {
	LoadClaimFrames() ([][]byte, error)
	StoreClaimFrame(frame []byte, claimantPeerID string, signature []byte) (string, error)
}

// FlatSQLClaimFrameStore adapts the node's shared all-standards store. It does
// not create a claims database, table, transport, or sidecar.
type FlatSQLClaimFrameStore struct {
	store *storage.FlatSQLStore
}

func NewFlatSQLClaimFrameStore(store *storage.FlatSQLStore) *FlatSQLClaimFrameStore {
	return &FlatSQLClaimFrameStore{store: store}
}

func (s *FlatSQLClaimFrameStore) LoadClaimFrames() ([][]byte, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("claims: FlatSQL store is unavailable")
	}
	records, err := s.store.Query(ClaimsSchemaName, "")
	if err != nil {
		return nil, err
	}
	frames := make([][]byte, 0, len(records))
	for _, record := range records {
		frame, err := claimStoredRecordFrame(record)
		if err != nil {
			continue
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

func (s *FlatSQLClaimFrameStore) StoreClaimFrame(frame []byte, claimantPeerID string, signature []byte) (string, error) {
	if s == nil || s.store == nil {
		return "", errors.New("claims: FlatSQL store is unavailable")
	}
	frames, err := SplitFrames(frame)
	if err != nil || len(frames) != 1 || FrameIdentifier(frames[0]) != standardsCLM.CLMIdentifier {
		return "", errors.New("claims: expected exactly one size-prefixed CLM frame")
	}
	// FlatSQL stores canonical record bytes. The HTTP framing prefix is a lane
	// concern and QueryRawStream adds it again when records leave the engine.
	bare := append([]byte(nil), frames[0][framePrefixLength:]...)
	return s.store.Store(ClaimsSchemaName, bare, strings.TrimSpace(claimantPeerID), signature)
}

func claimStoredRecordFrame(record []byte) ([]byte, error) {
	switch {
	case len(record) >= frameIdentifierOffset+frameIdentifierLength && standardsCLM.SizePrefixedCLMBufferHasIdentifier(record):
		frames, err := SplitFrames(record)
		if err != nil || len(frames) != 1 {
			return nil, errors.New("claims: malformed size-prefixed CLM record")
		}
		return append([]byte(nil), frames[0]...), nil
	case len(record) >= framePrefixLength+frameIdentifierLength && standardsCLM.CLMBufferHasIdentifier(record):
		if uint64(len(record)) > uint64(^uint32(0)) {
			return nil, errors.New("claims: CLM record is too large")
		}
		frame := make([]byte, framePrefixLength+len(record))
		binary.LittleEndian.PutUint32(frame[:framePrefixLength], uint32(len(record)))
		copy(frame[framePrefixLength:], record)
		return frame, nil
	default:
		return nil, errors.New("claims: record is not CLM")
	}
}

// ClaimsHandlerOptions supplies node identity, signing, trust and persistence
// capabilities without making the handler own any of them.
type ClaimsHandlerOptions struct {
	SelfPeerID string
	SigningKey ed25519.PrivateKey
	Store      ClaimFrameStore
	ResolveEPM func(peerID string) []byte
	Trusted    func(peerID string) bool
	Protect    func(http.HandlerFunc) http.HandlerFunc
	Now        func() time.Time
	NewClaimID func() string
}

type ClaimsHandler struct {
	selfPeerID string
	signingKey ed25519.PrivateKey
	store      ClaimFrameStore
	resolveEPM func(string) []byte
	trusted    func(string) bool
	protect    func(http.HandlerFunc) http.HandlerFunc
	now        func() time.Time
	newClaimID func() string
}

func NewClaimsHandler(options ClaimsHandlerOptions) *ClaimsHandler {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newClaimID := options.NewClaimID
	if newClaimID == nil {
		newClaimID = uuid.NewString
	}
	protect := options.Protect
	if protect == nil {
		protect = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}
	return &ClaimsHandler{
		selfPeerID: strings.TrimSpace(options.SelfPeerID),
		signingKey: append(ed25519.PrivateKey(nil), options.SigningKey...),
		store:      options.Store,
		resolveEPM: options.ResolveEPM,
		trusted:    options.Trusted,
		protect:    protect,
		now:        now,
		newClaimID: newClaimID,
	}
}

func (h *ClaimsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(ClaimsPath, h.handleCollection)
	mux.HandleFunc(ClaimsPath+"/", h.handleClaim)
}

func (h *ClaimsHandler) handleCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodPost:
		h.protect(h.handlePost)(w, r)
	default:
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET or POST for claims.", 0)
	}
}

func (h *ClaimsHandler) handleClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST to verify a claim.", 0)
		return
	}
	h.protect(h.handleVerify)(w, r)
}

func (h *ClaimsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	peerID, objectKind, objectID, err := h.claimFilters(r)
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_claim_filter", err.Error(), 0)
		return
	}
	claims, err := h.heldClaims()
	if err != nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "claims_unavailable", "Claims are not available right now.", 5*time.Second)
		return
	}
	frames := make([][]byte, 0, len(claims))
	for _, claim := range claims {
		if peerID != "" && claim.ClaimantPeerID != peerID {
			continue
		}
		if objectKind != ClaimObjectKindUnspecified && (claim.ObjectKind != objectKind || claim.ObjectID != objectID) {
			continue
		}
		frame, err := EncodeClaimFrame(claim)
		if err == nil {
			frames = append(frames, frame)
		}
	}
	WriteFrameStream(w, http.StatusOK, frames, map[string]string{
		StreamSchemaHeader: ClaimsSchemaName,
		SelfPeerHeaderName: h.selfPeerID,
	})
}

func (h *ClaimsHandler) claimFilters(r *http.Request) (peerID string, objectKind int8, objectID string, err error) {
	query := r.URL.Query()
	for key := range query {
		if key != "peer" && key != "object" {
			return "", 0, "", fmt.Errorf("unknown claims filter %q", key)
		}
	}
	hasPeer, hasObject := query.Has("peer"), query.Has("object")
	if hasPeer && hasObject {
		return "", 0, "", errors.New("use either peer or object, not both")
	}
	if hasPeer {
		peerID = strings.TrimSpace(query.Get("peer"))
		if peerID == "" {
			return "", 0, "", errors.New("peer must not be empty")
		}
		return peerID, 0, "", nil
	}
	if hasObject {
		kind, id, ok := strings.Cut(strings.TrimSpace(query.Get("object")), ":")
		if !ok || strings.TrimSpace(id) == "" || strings.Contains(id, ":") {
			return "", 0, "", errors.New("object must be <satellite|site|sensor|service>:<id>")
		}
		objectKind, ok = claimObjectKindFromName(kind)
		if !ok {
			return "", 0, "", errors.New("object kind must be satellite, site, sensor, or service")
		}
		return "", objectKind, strings.TrimSpace(id), nil
	}
	if h == nil || h.selfPeerID == "" {
		return "", 0, "", errors.New("this node's peer id is unavailable")
	}
	return h.selfPeerID, 0, "", nil
}

func claimObjectKindFromName(name string) (int8, bool) {
	switch strings.TrimSpace(name) {
	case "satellite":
		return ClaimObjectKindSatellite, true
	case "site":
		return ClaimObjectKindSite, true
	case "sensor":
		return ClaimObjectKindSensor, true
	case "service":
		return ClaimObjectKindService, true
	default:
		return ClaimObjectKindUnspecified, false
	}
}

func (h *ClaimsHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	frames, err := ReadFrames(r.Body, MaxRequestFrameBytes)
	if err != nil || len(frames) != 1 || FrameIdentifier(frames[0]) != standardsCLM.CLMIdentifier {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_claim", "The request body must be exactly one size-prefixed $CLM frame.", 0)
		return
	}
	claim, err := DecodeClaimFrame(frames[0])
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_claim", "The claim frame could not be decoded.", 0)
		return
	}
	if len(claim.Signature) == 0 {
		claim, err = h.signUnsignedClaim(claim)
	} else {
		err = h.admitPresignedClaim(claim)
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errClaimNotFound) {
			status = http.StatusNotFound
		}
		WriteErrorFrame(w, status, "invalid_claim", err.Error(), 0)
		return
	}
	frame, err := EncodeClaimFrame(claim)
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_claim", err.Error(), 0)
		return
	}
	h.storeClaimResponse(w, frame, claim)
}

var errClaimNotFound = errors.New("claim not found")

func (h *ClaimsHandler) signUnsignedClaim(claim ClaimRecord) (ClaimRecord, error) {
	if h == nil || h.selfPeerID == "" || len(h.signingKey) != ed25519.PrivateKeySize || h.resolveEPM == nil {
		return ClaimRecord{}, errors.New("this node cannot sign claims")
	}
	if len(claim.Countersignatures) != 0 {
		return ClaimRecord{}, errors.New("an unsigned claim cannot carry countersignatures")
	}
	now := uint64(h.now().UTC().UnixMilli())
	if now == 0 {
		return ClaimRecord{}, errors.New("the claim timestamp is unavailable")
	}
	profile := h.resolveEPM(h.selfPeerID)
	profileCID, err := verifiedProfileCID(profile, h.selfPeerID)
	if err != nil {
		return ClaimRecord{}, errors.New("this node's signed profile is unavailable")
	}
	claims, err := h.heldClaims()
	if err != nil {
		return ClaimRecord{}, err
	}
	byID := claimsByID(claims)
	claim = normalizeClaim(claim)
	claim.ClaimantPeerID = h.selfPeerID
	claim.ClaimantProfileCID = profileCID
	claim.Signature = nil
	claim.Countersignatures = nil

	if claim.Deleted {
		existing, ok := byID[claim.ClaimID]
		if !ok {
			return ClaimRecord{}, fmt.Errorf("%w: the retracted claim is not held by this node", errClaimNotFound)
		}
		if existing.ClaimantPeerID != h.selfPeerID {
			return ClaimRecord{}, errors.New("this node cannot retract another claimant's claim")
		}
		if claim.ObjectKind != ClaimObjectKindUnspecified && claim.ObjectKind != existing.ObjectKind {
			return ClaimRecord{}, errors.New("the retraction changes the claimed object kind")
		}
		if claim.ObjectID != "" && claim.ObjectID != existing.ObjectID {
			return ClaimRecord{}, errors.New("the retraction changes the claimed object id")
		}
		claim = existing
		claim.Deleted = true
		claim.ClaimantProfileCID = profileCID
		claim.UpdatedAt = now
		claim.Signature = nil
		claim.Countersignatures = nil
	} else {
		if claim.ClaimID == "" {
			claim.ClaimID = strings.TrimSpace(h.newClaimID())
		}
		if claim.ClaimID == "" {
			return ClaimRecord{}, errors.New("a claim id could not be created")
		}
		if existing, ok := byID[claim.ClaimID]; ok {
			if existing.ClaimantPeerID != h.selfPeerID || existing.ObjectKind != claim.ObjectKind || existing.ObjectID != claim.ObjectID {
				return ClaimRecord{}, errors.New("the claim id already belongs to another claim")
			}
			claim.CreatedAt = existing.CreatedAt
		} else {
			claim.CreatedAt = now
		}
		claim.UpdatedAt = now
	}
	if err := ValidateClaimRecord(claim, false); err != nil {
		return ClaimRecord{}, err
	}
	payload, err := ClaimSigningPayload(claim)
	if err != nil {
		return ClaimRecord{}, err
	}
	claim.Signature = ed25519.Sign(h.signingKey, payload)
	if err := VerifyClaimSignatures(claim, h.resolveEPM); err != nil {
		return ClaimRecord{}, fmt.Errorf("the signed claim did not verify: %w", err)
	}
	return claim, nil
}

func (h *ClaimsHandler) admitPresignedClaim(claim ClaimRecord) error {
	if err := ValidateClaimRecord(claim, true); err != nil {
		return err
	}
	if err := VerifyClaimSignatures(claim, h.resolveEPM); err != nil {
		return fmt.Errorf("the claim signature is invalid: %w", err)
	}
	claims, err := h.heldClaims()
	if err != nil {
		return err
	}
	if existing, ok := claimsByID(claims)[claim.ClaimID]; ok {
		if existing.ClaimantPeerID != claim.ClaimantPeerID || existing.ObjectKind != claim.ObjectKind || existing.ObjectID != claim.ObjectID {
			return errors.New("the claim id already belongs to another claim")
		}
	} else if claim.Deleted {
		return fmt.Errorf("%w: the retracted claim is not held by this node", errClaimNotFound)
	}
	return nil
}

func (h *ClaimsHandler) handleVerify(w http.ResponseWriter, r *http.Request) {
	claimID, err := claimIDFromVerifyPath(r.URL)
	if err != nil {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", "That claim is not held by this node.", 0)
		return
	}
	claims, err := h.heldClaims()
	if err != nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "claims_unavailable", "Claims are not available right now.", 5*time.Second)
		return
	}
	claim, ok := claimsByID(claims)[claimID]
	if !ok {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", "That claim is not held by this node.", 0)
		return
	}
	if claim.Deleted {
		WriteErrorFrame(w, http.StatusConflict, "claim_retracted", "A retracted claim cannot be verified.", 0)
		return
	}
	if claim.ClaimantPeerID == h.selfPeerID || h.trusted == nil || !h.trusted(claim.ClaimantPeerID) {
		WriteErrorFrame(w, http.StatusForbidden, "claimant_not_trusted", "This node does not trust the claimant.", 0)
		return
	}
	if len(h.signingKey) != ed25519.PrivateKeySize || h.resolveEPM == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "signing_unavailable", "This node cannot verify claims right now.", 5*time.Second)
		return
	}
	profile := h.resolveEPM(h.selfPeerID)
	profileCID, err := verifiedProfileCID(profile, h.selfPeerID)
	if err != nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "profile_unavailable", "This node's signed profile is unavailable.", 5*time.Second)
		return
	}
	payload, err := ClaimSigningPayload(claim)
	if err != nil {
		WriteErrorFrame(w, http.StatusConflict, "invalid_claim", "The held claim is invalid.", 0)
		return
	}
	counter := ClaimCountersignature{
		PeerID:     h.selfPeerID,
		ProfileCID: profileCID,
		SignedAt:   uint64(h.now().UTC().UnixMilli()),
		Signature:  ed25519.Sign(h.signingKey, payload),
	}
	updated := false
	for i := range claim.Countersignatures {
		if claim.Countersignatures[i].PeerID == h.selfPeerID {
			claim.Countersignatures[i] = counter
			updated = true
			break
		}
	}
	if !updated {
		claim.Countersignatures = append(claim.Countersignatures, counter)
	}
	if err := VerifyClaimSignatures(claim, h.resolveEPM); err != nil {
		WriteErrorFrame(w, http.StatusInternalServerError, "invalid_countersignature", "The countersignature could not be verified.", 0)
		return
	}
	frame, err := EncodeClaimFrame(claim)
	if err != nil {
		WriteErrorFrame(w, http.StatusInternalServerError, "claim_encode_failed", "The countersigned claim could not be encoded.", 0)
		return
	}
	h.storeClaimResponse(w, frame, claim)
}

func claimIDFromVerifyPath(u *url.URL) (string, error) {
	if u == nil {
		return "", errClaimNotFound
	}
	rest := strings.Trim(strings.TrimPrefix(u.EscapedPath(), ClaimsPath+"/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "verify" {
		return "", errClaimNotFound
	}
	claimID, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(claimID) == "" {
		return "", errClaimNotFound
	}
	return strings.TrimSpace(claimID), nil
}

func (h *ClaimsHandler) storeClaimResponse(w http.ResponseWriter, frame []byte, claim ClaimRecord) {
	if h == nil || h.store == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "claims_unavailable", "Claims are not available right now.", 5*time.Second)
		return
	}
	cid, err := h.store.StoreClaimFrame(frame, claim.ClaimantPeerID, claim.Signature)
	if err != nil {
		WriteErrorFrame(w, http.StatusInternalServerError, "not_persisted", "The claim could not be stored.", 0)
		return
	}
	WriteFrameStream(w, http.StatusOK, [][]byte{frame}, map[string]string{
		StreamSchemaHeader: ClaimsSchemaName,
		SelfPeerHeaderName: h.selfPeerID,
		"X-SDN-Record-CID": cid,
	})
}

func (h *ClaimsHandler) heldClaims() ([]ClaimRecord, error) {
	if h == nil || h.store == nil {
		return nil, errors.New("claims: store is unavailable")
	}
	frames, err := h.store.LoadClaimFrames()
	if err != nil {
		return nil, err
	}
	latest := make(map[string]ClaimRecord)
	encoded := make(map[string][]byte)
	for _, frame := range frames {
		claim, err := DecodeClaimFrame(frame)
		if err != nil || ValidateClaimRecord(claim, true) != nil || VerifyClaimSignatures(claim, h.resolveEPM) != nil {
			continue
		}
		current, exists := latest[claim.ClaimID]
		if !exists || claim.UpdatedAt > current.UpdatedAt ||
			(claim.UpdatedAt == current.UpdatedAt && claim.Deleted && !current.Deleted) ||
			(claim.UpdatedAt == current.UpdatedAt && claim.Deleted == current.Deleted && len(claim.Countersignatures) > len(current.Countersignatures)) ||
			(claim.UpdatedAt == current.UpdatedAt && claim.Deleted == current.Deleted && len(claim.Countersignatures) == len(current.Countersignatures) && bytes.Compare(frame, encoded[claim.ClaimID]) > 0) {
			latest[claim.ClaimID] = claim
			encoded[claim.ClaimID] = append([]byte(nil), frame...)
		}
	}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ClaimRecord, 0, len(ids))
	for _, id := range ids {
		out = append(out, latest[id])
	}
	return out, nil
}

func claimsByID(claims []ClaimRecord) map[string]ClaimRecord {
	out := make(map[string]ClaimRecord, len(claims))
	for _, claim := range claims {
		out[claim.ClaimID] = claim
	}
	return out
}

// ValidateClaimRecord enforces the required SDS fields and enum ranges.
func ValidateClaimRecord(claim ClaimRecord, requireSignature bool) error {
	claim = normalizeClaim(claim)
	if claim.ClaimID == "" || claim.ClaimantPeerID == "" || claim.ObjectID == "" {
		return errors.New("CLAIM_ID, CLAIMANT_PEER_ID, and OBJECT_ID are required")
	}
	if claim.ObjectKind < ClaimObjectKindSatellite || claim.ObjectKind > ClaimObjectKindService {
		return errors.New("OBJECT_KIND must be Satellite, Site, Sensor, or Service")
	}
	if claim.Role < ClaimRoleOwner || claim.Role > ClaimRoleProvider {
		return errors.New("ROLE must be Owner, Operator, or Provider")
	}
	if claim.CreatedAt == 0 || claim.UpdatedAt == 0 || claim.UpdatedAt < claim.CreatedAt {
		return errors.New("CREATED_AT and UPDATED_AT must be valid millisecond timestamps")
	}
	if requireSignature && len(claim.Signature) == 0 {
		return errors.New("SIGNATURE is required")
	}
	for _, counter := range claim.Countersignatures {
		if counter.PeerID == "" || counter.SignedAt == 0 || len(counter.Signature) == 0 {
			return errors.New("each countersignature needs PEER_ID, SIGNED_AT, and SIGNATURE")
		}
	}
	return nil
}

func normalizeClaim(claim ClaimRecord) ClaimRecord {
	claim.ClaimID = strings.TrimSpace(claim.ClaimID)
	claim.ClaimantPeerID = strings.TrimSpace(claim.ClaimantPeerID)
	claim.ClaimantProfileCID = strings.TrimSpace(claim.ClaimantProfileCID)
	claim.ObjectID = strings.TrimSpace(claim.ObjectID)
	claim.ObjectName = strings.TrimSpace(claim.ObjectName)
	claim.Statement = strings.TrimSpace(claim.Statement)
	claim.EvidenceURL = strings.TrimSpace(claim.EvidenceURL)
	claim.Signature = append([]byte(nil), claim.Signature...)
	for i := range claim.Countersignatures {
		claim.Countersignatures[i].PeerID = strings.TrimSpace(claim.Countersignatures[i].PeerID)
		claim.Countersignatures[i].ProfileCID = strings.TrimSpace(claim.Countersignatures[i].ProfileCID)
		claim.Countersignatures[i].Signature = append([]byte(nil), claim.Countersignatures[i].Signature...)
	}
	return claim
}

// ClaimSigningPayload is the canonical bare $CLM record with SIGNATURE and
// COUNTERSIGNATURES cleared. Claimant signatures and every countersignature
// cover these exact same bytes.
func ClaimSigningPayload(claim ClaimRecord) ([]byte, error) {
	claim = normalizeClaim(claim)
	claim.Signature = nil
	claim.Countersignatures = nil
	if err := ValidateClaimRecord(claim, false); err != nil {
		return nil, err
	}
	return encodeClaim(claim, false)
}

// VerifyClaimSignatures verifies the claimant signature and every attached
// countersignature against the held, self-signed EPM for that peer.
func VerifyClaimSignatures(claim ClaimRecord, resolveEPM func(peerID string) []byte) error {
	if resolveEPM == nil {
		return errors.New("no profile resolver")
	}
	if err := ValidateClaimRecord(claim, true); err != nil {
		return err
	}
	payload, err := ClaimSigningPayload(claim)
	if err != nil {
		return err
	}
	if err := verifyClaimPeerSignature(resolveEPM(claim.ClaimantPeerID), claim.ClaimantPeerID, claim.ClaimantProfileCID, payload, claim.Signature); err != nil {
		return fmt.Errorf("claimant: %w", err)
	}
	for _, counter := range claim.Countersignatures {
		if err := verifyClaimPeerSignature(resolveEPM(counter.PeerID), counter.PeerID, counter.ProfileCID, payload, counter.Signature); err != nil {
			return fmt.Errorf("countersigner %s: %w", counter.PeerID, err)
		}
	}
	return nil
}

func verifyClaimPeerSignature(profile []byte, peerID, profileCID string, payload, signature []byte) error {
	cid, err := verifiedProfileCID(profile, peerID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(profileCID) != "" && strings.TrimSpace(profileCID) != cid {
		return errors.New("profile CID does not match the held profile")
	}
	if err := epm.VerifyDetachedSignature(profile, payload, signature); err != nil {
		return errors.New("signature does not verify against the held profile")
	}
	return nil
}

func verifiedProfileCID(profile []byte, peerID string) (string, error) {
	if len(profile) == 0 || epm.VerifyEPMSignature(profile) != nil {
		return "", errors.New("held profile is missing or invalid")
	}
	advertised, err := epm.PeerIDFromEPM(profile)
	if err != nil || strings.TrimSpace(advertised) != strings.TrimSpace(peerID) {
		return "", errors.New("held profile does not identify the signer")
	}
	cid, err := epm.ComputeEPMCID(profile)
	if err != nil {
		return "", errors.New("held profile has no content identifier")
	}
	return cid, nil
}

// EncodeClaimFrame serializes one canonical size-prefixed $CLM frame.
func EncodeClaimFrame(claim ClaimRecord) ([]byte, error) {
	claim = normalizeClaim(claim)
	if len(claim.Signature) != 0 {
		if err := ValidateClaimRecord(claim, true); err != nil {
			return nil, err
		}
	} else if err := validateClaimDraft(claim); err != nil {
		return nil, err
	}
	return encodeClaim(claim, true)
}

func validateClaimDraft(claim ClaimRecord) error {
	if claim.Deleted {
		if claim.ClaimID == "" {
			return errors.New("CLAIM_ID is required for a retraction")
		}
		return nil
	}
	if claim.ObjectKind < ClaimObjectKindSatellite || claim.ObjectKind > ClaimObjectKindService {
		return errors.New("OBJECT_KIND must be Satellite, Site, Sensor, or Service")
	}
	if claim.ObjectID == "" && !claim.Deleted {
		return errors.New("OBJECT_ID is required")
	}
	if claim.Role < ClaimRoleOwner || claim.Role > ClaimRoleProvider {
		return errors.New("ROLE must be Owner, Operator, or Provider")
	}
	return nil
}

func encodeClaim(claim ClaimRecord, sizePrefixed bool) ([]byte, error) {
	b := flatbuffers.NewBuilder(1024)
	counters := make([]flatbuffers.UOffsetT, len(claim.Countersignatures))
	for i, counter := range claim.Countersignatures {
		peerID := b.CreateString(counter.PeerID)
		profileCID := b.CreateString(counter.ProfileCID)
		signature := b.CreateByteVector(counter.Signature)
		standardsCLM.CLMCountersignatureStart(b)
		standardsCLM.CLMCountersignatureAddPEER_ID(b, peerID)
		if counter.ProfileCID != "" {
			standardsCLM.CLMCountersignatureAddPROFILE_CID(b, profileCID)
		}
		standardsCLM.CLMCountersignatureAddSIGNED_AT(b, counter.SignedAt)
		if len(counter.Signature) != 0 {
			standardsCLM.CLMCountersignatureAddSIGNATURE(b, signature)
		}
		counters[i] = standardsCLM.CLMCountersignatureEnd(b)
	}
	var counterVector flatbuffers.UOffsetT
	if len(counters) != 0 {
		standardsCLM.CLMStartCOUNTERSIGNATURESVector(b, len(counters))
		for i := len(counters) - 1; i >= 0; i-- {
			b.PrependUOffsetT(counters[i])
		}
		counterVector = b.EndVector(len(counters))
	}

	claimID := b.CreateString(claim.ClaimID)
	claimantPeerID := b.CreateString(claim.ClaimantPeerID)
	claimantProfileCID := b.CreateString(claim.ClaimantProfileCID)
	objectID := b.CreateString(claim.ObjectID)
	objectName := b.CreateString(claim.ObjectName)
	statement := b.CreateString(claim.Statement)
	evidenceURL := b.CreateString(claim.EvidenceURL)
	var signature flatbuffers.UOffsetT
	if len(claim.Signature) != 0 {
		signature = b.CreateByteVector(claim.Signature)
	}

	standardsCLM.CLMStart(b)
	standardsCLM.CLMAddCLAIM_ID(b, claimID)
	standardsCLM.CLMAddCLAIMANT_PEER_ID(b, claimantPeerID)
	if claim.ClaimantProfileCID != "" {
		standardsCLM.CLMAddCLAIMANT_PROFILE_CID(b, claimantProfileCID)
	}
	standardsCLM.CLMAddOBJECT_KIND(b, enumOf(standardsCLM.EnumValuesclmObjectKind, claim.ObjectKind))
	standardsCLM.CLMAddOBJECT_ID(b, objectID)
	if claim.ObjectName != "" {
		standardsCLM.CLMAddOBJECT_NAME(b, objectName)
	}
	standardsCLM.CLMAddROLE(b, enumOf(standardsCLM.EnumValuesclmClaimRole, claim.Role))
	if claim.Statement != "" {
		standardsCLM.CLMAddSTATEMENT(b, statement)
	}
	if claim.EvidenceURL != "" {
		standardsCLM.CLMAddEVIDENCE_URL(b, evidenceURL)
	}
	standardsCLM.CLMAddCREATED_AT(b, claim.CreatedAt)
	standardsCLM.CLMAddUPDATED_AT(b, claim.UpdatedAt)
	standardsCLM.CLMAddDELETED(b, claim.Deleted)
	if signature != 0 {
		standardsCLM.CLMAddSIGNATURE(b, signature)
	}
	if counterVector != 0 {
		standardsCLM.CLMAddCOUNTERSIGNATURES(b, counterVector)
	}
	root := standardsCLM.CLMEnd(b)
	if sizePrefixed {
		standardsCLM.FinishSizePrefixedCLMBuffer(b, root)
	} else {
		standardsCLM.FinishCLMBuffer(b, root)
	}
	return append([]byte(nil), b.FinishedBytes()...), nil
}

// DecodeClaimFrame parses exactly one size-prefixed $CLM frame.
func DecodeClaimFrame(frame []byte) (claim ClaimRecord, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			claim = ClaimRecord{}
			err = fmt.Errorf("claims: malformed CLM frame: %v", recovered)
		}
	}()
	frames, err := SplitFrames(frame)
	if err != nil || len(frames) != 1 || !standardsCLM.SizePrefixedCLMBufferHasIdentifier(frame) {
		return ClaimRecord{}, errors.New("claims: frame is not one size-prefixed CLM record")
	}
	root := standardsCLM.GetSizePrefixedRootAsCLM(frame, 0)
	claim = ClaimRecord{
		ClaimID:            string(root.CLAIM_ID()),
		ClaimantPeerID:     string(root.CLAIMANT_PEER_ID()),
		ClaimantProfileCID: string(root.CLAIMANT_PROFILE_CID()),
		ObjectKind:         int8(root.OBJECT_KIND()),
		ObjectID:           string(root.OBJECT_ID()),
		ObjectName:         string(root.OBJECT_NAME()),
		Role:               int8(root.ROLE()),
		Statement:          string(root.STATEMENT()),
		EvidenceURL:        string(root.EVIDENCE_URL()),
		CreatedAt:          root.CREATED_AT(),
		UpdatedAt:          root.UPDATED_AT(),
		Deleted:            root.DELETED(),
		Signature:          append([]byte(nil), root.SIGNATUREBytes()...),
	}
	for i := 0; i < root.COUNTERSIGNATURESLength(); i++ {
		var counter standardsCLM.CLMCountersignature
		if !root.COUNTERSIGNATURES(&counter, i) {
			continue
		}
		claim.Countersignatures = append(claim.Countersignatures, ClaimCountersignature{
			PeerID:     string(counter.PEER_ID()),
			ProfileCID: string(counter.PROFILE_CID()),
			SignedAt:   counter.SIGNED_AT(),
			Signature:  append([]byte(nil), counter.SIGNATUREBytes()...),
		})
	}
	return normalizeClaim(claim), nil
}
