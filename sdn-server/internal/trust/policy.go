package trust

// Trust Rule Policies — `$TRP` (spacedatastandards.org v1.207.0).
//
// A policy is a compound rule set the node evaluates against subjects on an
// interval (EVALUATION_INTERVAL_MS, 0.1 Hz by default) and early on events.
// It is an SDS record like every other fact the node holds: encoded as the
// `$TRP` FlatBuffer, signed in BOTH forms (the FlatBuffer bytes and the
// canonical IDL-order JSON rendering — the dual-format signing law), stored
// engine-routed through the node FlatSQL store, and projected back as the
// latest record per POLICY_ID.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdstrp "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TRP"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	trustPolicySchema  = "TRP.fbs"
	trustVerdictSchema = "TRV.fbs"

	// DefaultEvaluationIntervalMs is the 0.1 Hz baseline (owner ruling
	// 2026-09-01); a policy or the runtime setting may override it.
	DefaultEvaluationIntervalMs uint32 = 10000
	// MinEvaluationIntervalMs bounds how hard a policy may drive the engine.
	MinEvaluationIntervalMs uint32 = 250
)

// PredicateKind names the four `$TRP` predicate kinds.
type PredicateKind string

const (
	PredicateMinValueLocked     PredicateKind = "MinValueLocked"
	PredicateValueForDuration   PredicateKind = "ValueForDuration"
	PredicateAllowedTokens      PredicateKind = "AllowedTokens"
	PredicateTrustedConnections PredicateKind = "TrustedConnections"
)

// Combinator joins a group's predicates and subgroups.
type Combinator string

const (
	CombinatorAll Combinator = "All"
	CombinatorAny Combinator = "Any"
)

// Asset identifies a chain and, optionally, a token on it. CHAIN_ID is a
// CAIP-2 style identifier ("eip155:1", "solana:…"), never a vendor name.
type Asset struct {
	ChainID      string `json:"CHAIN_ID"`
	TokenAddress string `json:"TOKEN_ADDRESS,omitempty"`
	TokenSymbol  string `json:"TOKEN_SYMBOL,omitempty"`
	Decimals     uint32 `json:"DECIMALS,omitempty"`
}

// Predicate is one typed rule.
type Predicate struct {
	ID             string        `json:"PREDICATE_ID"`
	Kind           PredicateKind `json:"KIND"`
	MinValue       uint64        `json:"MIN_VALUE,omitempty"`
	ValueCurrency  string        `json:"VALUE_CURRENCY,omitempty"`
	MinHeldSeconds uint64        `json:"MIN_HELD_SECONDS,omitempty"`
	Assets         []Asset       `json:"ASSETS,omitempty"`
	RequiredCount  uint32        `json:"REQUIRED_COUNT,omitempty"`
	TrusterIDs     []string      `json:"TRUSTER_IDS,omitempty"`
	MinEdgeWeight  float64       `json:"MIN_EDGE_WEIGHT,omitempty"`
}

// Group is a compound rule: predicates and nested groups under one combinator.
type Group struct {
	ID         string      `json:"GROUP_ID"`
	Combinator Combinator  `json:"COMBINATOR"`
	Predicates []Predicate `json:"PREDICATES,omitempty"`
	Groups     []Group     `json:"GROUPS,omitempty"`
}

// Policy mirrors `$TRP` field for field.
type Policy struct {
	ID                   string   `json:"POLICY_ID"`
	Name                 string   `json:"NAME,omitempty"`
	Description          string   `json:"DESCRIPTION,omitempty"`
	Root                 Group    `json:"ROOT"`
	EvaluationIntervalMs uint32   `json:"EVALUATION_INTERVAL_MS"`
	EventSources         []string `json:"EVENT_SOURCES,omitempty"`
	Active               bool     `json:"ACTIVE"`
	CreatedAtMs          int64    `json:"CREATED_AT"`
	UpdatedAtMs          int64    `json:"UPDATED_AT"`
	EvaluatorPeerID      string   `json:"EVALUATOR_PEER_ID"`
	// Signature is the evaluator's ed25519 signature over the UNSIGNED
	// FlatBuffer bytes (EVALUATOR_SIGNATURE empty). The canonical JSON form
	// carries its own signature over the unsigned JSON bytes.
	Signature []byte `json:"EVALUATOR_SIGNATURE,omitempty"`
}

// Validate refuses a policy the engine could not evaluate honestly.
func (p Policy) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("trust: POLICY_ID required")
	}
	if p.EvaluationIntervalMs != 0 && p.EvaluationIntervalMs < MinEvaluationIntervalMs {
		return fmt.Errorf("trust: EVALUATION_INTERVAL_MS below %d", MinEvaluationIntervalMs)
	}
	count := 0
	var walk func(g Group, depth int) error
	walk = func(g Group, depth int) error {
		if depth > 8 {
			return errors.New("trust: rule groups nest deeper than 8")
		}
		if g.Combinator != CombinatorAll && g.Combinator != CombinatorAny {
			return fmt.Errorf("trust: group %q has no combinator", g.ID)
		}
		for _, pr := range g.Predicates {
			count++
			switch pr.Kind {
			case PredicateMinValueLocked, PredicateValueForDuration:
				if pr.MinValue == 0 {
					return fmt.Errorf("trust: predicate %q needs MIN_VALUE", pr.ID)
				}
				if pr.Kind == PredicateValueForDuration && pr.MinHeldSeconds == 0 {
					return fmt.Errorf("trust: predicate %q needs MIN_HELD_SECONDS", pr.ID)
				}
			case PredicateAllowedTokens:
				if len(pr.Assets) == 0 {
					return fmt.Errorf("trust: predicate %q needs ASSETS", pr.ID)
				}
			case PredicateTrustedConnections:
				if pr.RequiredCount == 0 {
					return fmt.Errorf("trust: predicate %q needs REQUIRED_COUNT", pr.ID)
				}
			default:
				return fmt.Errorf("trust: predicate %q has unknown KIND %q", pr.ID, pr.Kind)
			}
		}
		for _, sub := range g.Groups {
			if err := walk(sub, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(p.Root, 0); err != nil {
		return err
	}
	if count == 0 {
		return errors.New("trust: a policy needs at least one predicate")
	}
	return nil
}

// IntervalMs is the policy's own cadence, or the default.
func (p Policy) IntervalMs() uint32 {
	if p.EvaluationIntervalMs == 0 {
		return DefaultEvaluationIntervalMs
	}
	return p.EvaluationIntervalMs
}

/* ── enum mapping ────────────────────────────────────────────────────── */
// The generated enum types are unexported; the bindings expose name maps.

// PredicateKindFromSDS maps the IDL enum word back to the kind.
func predicateKindFromName(name string) PredicateKind {
	switch PredicateKind(name) {
	case PredicateValueForDuration, PredicateAllowedTokens, PredicateTrustedConnections:
		return PredicateKind(name)
	default:
		return PredicateMinValueLocked
	}
}

func combinatorFromName(name string) Combinator {
	if name == string(CombinatorAny) {
		return CombinatorAny
	}
	return CombinatorAll
}

/* ── FlatBuffer encoding ─────────────────────────────────────────────── */

func stringVector(b *flatbuffers.Builder, values []string, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(values) == 0 {
		return 0
	}
	offs := make([]flatbuffers.UOffsetT, len(values))
	for i, v := range values {
		offs[i] = b.CreateString(v)
	}
	start(b, len(offs))
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	return b.EndVector(len(offs))
}

func buildAsset(b *flatbuffers.Builder, a Asset) flatbuffers.UOffsetT {
	chain := trustStringOffset(b, a.ChainID)
	token := trustStringOffset(b, a.TokenAddress)
	symbol := trustStringOffset(b, a.TokenSymbol)
	sdstrp.TRPAssetStart(b)
	sdstrp.TRPAssetAddCHAIN_ID(b, chain)
	sdstrp.TRPAssetAddTOKEN_ADDRESS(b, token)
	sdstrp.TRPAssetAddTOKEN_SYMBOL(b, symbol)
	sdstrp.TRPAssetAddDECIMALS(b, byte(a.Decimals))
	return sdstrp.TRPAssetEnd(b)
}

func buildPredicate(b *flatbuffers.Builder, p Predicate) flatbuffers.UOffsetT {
	id := trustStringOffset(b, p.ID)
	currency := trustStringOffset(b, p.ValueCurrency)
	var assets flatbuffers.UOffsetT
	if len(p.Assets) > 0 {
		offs := make([]flatbuffers.UOffsetT, len(p.Assets))
		for i, a := range p.Assets {
			offs[i] = buildAsset(b, a)
		}
		sdstrp.TRPPredicateStartASSETSVector(b, len(offs))
		for i := len(offs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offs[i])
		}
		assets = b.EndVector(len(offs))
	}
	trusters := stringVector(b, p.TrusterIDs, sdstrp.TRPPredicateStartTRUSTER_IDSVector)
	sdstrp.TRPPredicateStart(b)
	sdstrp.TRPPredicateAddPREDICATE_ID(b, id)
	sdstrp.TRPPredicateAddKIND(b, sdstrp.EnumValuestrpPredicateKind[string(p.Kind)])
	sdstrp.TRPPredicateAddMIN_VALUE(b, p.MinValue)
	sdstrp.TRPPredicateAddVALUE_CURRENCY(b, currency)
	sdstrp.TRPPredicateAddMIN_HELD_SECONDS(b, p.MinHeldSeconds)
	if assets != 0 {
		sdstrp.TRPPredicateAddASSETS(b, assets)
	}
	sdstrp.TRPPredicateAddREQUIRED_COUNT(b, p.RequiredCount)
	if trusters != 0 {
		sdstrp.TRPPredicateAddTRUSTER_IDS(b, trusters)
	}
	sdstrp.TRPPredicateAddMIN_EDGE_WEIGHT(b, p.MinEdgeWeight)
	return sdstrp.TRPPredicateEnd(b)
}

func buildGroup(b *flatbuffers.Builder, g Group) flatbuffers.UOffsetT {
	id := trustStringOffset(b, g.ID)
	var preds, subs flatbuffers.UOffsetT
	if len(g.Predicates) > 0 {
		offs := make([]flatbuffers.UOffsetT, len(g.Predicates))
		for i, p := range g.Predicates {
			offs[i] = buildPredicate(b, p)
		}
		sdstrp.TRPGroupStartPREDICATESVector(b, len(offs))
		for i := len(offs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offs[i])
		}
		preds = b.EndVector(len(offs))
	}
	if len(g.Groups) > 0 {
		offs := make([]flatbuffers.UOffsetT, len(g.Groups))
		for i, sub := range g.Groups {
			offs[i] = buildGroup(b, sub)
		}
		sdstrp.TRPGroupStartGROUPSVector(b, len(offs))
		for i := len(offs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offs[i])
		}
		subs = b.EndVector(len(offs))
	}
	sdstrp.TRPGroupStart(b)
	sdstrp.TRPGroupAddGROUP_ID(b, id)
	sdstrp.TRPGroupAddCOMBINATOR(b, sdstrp.EnumValuestrpCombinator[string(g.Combinator)])
	if preds != 0 {
		sdstrp.TRPGroupAddPREDICATES(b, preds)
	}
	if subs != 0 {
		sdstrp.TRPGroupAddGROUPS(b, subs)
	}
	return sdstrp.TRPGroupEnd(b)
}

// EncodePolicy renders the `$TRP` FlatBuffer (identifier "$TRP").
func EncodePolicy(p Policy) []byte {
	b := flatbuffers.NewBuilder(1024)
	id := trustStringOffset(b, p.ID)
	name := trustStringOffset(b, p.Name)
	desc := trustStringOffset(b, p.Description)
	root := buildGroup(b, p.Root)
	events := stringVector(b, p.EventSources, sdstrp.TRPStartEVENT_SOURCESVector)
	peer := trustStringOffset(b, p.EvaluatorPeerID)
	sig := trustByteVector(b, p.Signature, sdstrp.TRPStartEVALUATOR_SIGNATUREVector)
	sdstrp.TRPStart(b)
	sdstrp.TRPAddPOLICY_ID(b, id)
	sdstrp.TRPAddNAME(b, name)
	sdstrp.TRPAddDESCRIPTION(b, desc)
	sdstrp.TRPAddROOT(b, root)
	sdstrp.TRPAddEVALUATION_INTERVAL_MS(b, p.EvaluationIntervalMs)
	if events != 0 {
		sdstrp.TRPAddEVENT_SOURCES(b, events)
	}
	sdstrp.TRPAddACTIVE(b, p.Active)
	sdstrp.TRPAddCREATED_AT(b, trustInt64ToUint64(p.CreatedAtMs))
	sdstrp.TRPAddUPDATED_AT(b, trustInt64ToUint64(p.UpdatedAtMs))
	sdstrp.TRPAddEVALUATOR_PEER_ID(b, peer)
	sdstrp.TRPAddEVALUATOR_SIGNATURE(b, sig)
	end := sdstrp.TRPEnd(b)
	sdstrp.FinishTRPBuffer(b, end)
	return b.FinishedBytes()
}

func readAsset(a *sdstrp.TRPAsset) Asset {
	return Asset{ChainID: string(a.CHAIN_ID()), TokenAddress: string(a.TOKEN_ADDRESS()), TokenSymbol: string(a.TOKEN_SYMBOL()), Decimals: uint32(a.DECIMALS())}
}

func readPredicate(p *sdstrp.TRPPredicate) Predicate {
	out := Predicate{
		ID:             string(p.PREDICATE_ID()),
		Kind:           predicateKindFromName(p.KIND().String()),
		MinValue:       p.MIN_VALUE(),
		ValueCurrency:  string(p.VALUE_CURRENCY()),
		MinHeldSeconds: p.MIN_HELD_SECONDS(),
		RequiredCount:  p.REQUIRED_COUNT(),
		MinEdgeWeight:  p.MIN_EDGE_WEIGHT(),
	}
	for i := 0; i < p.ASSETSLength(); i++ {
		var a sdstrp.TRPAsset
		if p.ASSETS(&a, i) {
			out.Assets = append(out.Assets, readAsset(&a))
		}
	}
	for i := 0; i < p.TRUSTER_IDSLength(); i++ {
		out.TrusterIDs = append(out.TrusterIDs, string(p.TRUSTER_IDS(i)))
	}
	return out
}

func readGroup(g *sdstrp.TRPGroup) Group {
	out := Group{ID: string(g.GROUP_ID()), Combinator: combinatorFromName(g.COMBINATOR().String())}
	for i := 0; i < g.PREDICATESLength(); i++ {
		var p sdstrp.TRPPredicate
		if g.PREDICATES(&p, i) {
			out.Predicates = append(out.Predicates, readPredicate(&p))
		}
	}
	for i := 0; i < g.GROUPSLength(); i++ {
		var sub sdstrp.TRPGroup
		if g.GROUPS(&sub, i) {
			out.Groups = append(out.Groups, readGroup(&sub))
		}
	}
	return out
}

// DecodePolicy reads a `$TRP` FlatBuffer.
func DecodePolicy(data []byte) (Policy, error) {
	if len(data) == 0 {
		return Policy{}, errors.New("empty TRP record")
	}
	if !sdstrp.TRPBufferHasIdentifier(data) {
		return Policy{}, errors.New("record is not TRP.fbs")
	}
	rec := sdstrp.GetRootAsTRP(data, 0)
	out := Policy{
		ID:                   string(rec.POLICY_ID()),
		Name:                 string(rec.NAME()),
		Description:          string(rec.DESCRIPTION()),
		EvaluationIntervalMs: rec.EVALUATION_INTERVAL_MS(),
		Active:               rec.ACTIVE(),
		CreatedAtMs:          trustUint64ToInt64(rec.CREATED_AT()),
		UpdatedAtMs:          trustUint64ToInt64(rec.UPDATED_AT()),
		EvaluatorPeerID:      string(rec.EVALUATOR_PEER_ID()),
		Signature:            append([]byte(nil), rec.EVALUATOR_SIGNATUREBytes()...),
	}
	if root := rec.ROOT(nil); root != nil {
		out.Root = readGroup(root)
	}
	for i := 0; i < rec.EVENT_SOURCESLength(); i++ {
		out.EventSources = append(out.EventSources, string(rec.EVENT_SOURCES(i)))
	}
	return out, nil
}

/* ── dual-format signing ─────────────────────────────────────────────── */

// CanonicalPolicyJSON is the IDL-order JSON rendering signed alongside the
// FlatBuffer. Keys are the IDL field names; EVALUATOR_SIGNATURE carries the
// JSON-form signature (hex) once signed and is absent while signing.
func CanonicalPolicyJSON(p Policy, jsonSignature []byte) ([]byte, error) {
	doc := map[string]any{}
	body, err := json.Marshal(struct {
		ID                   string   `json:"POLICY_ID"`
		Name                 string   `json:"NAME"`
		Description          string   `json:"DESCRIPTION"`
		Root                 Group    `json:"ROOT"`
		EvaluationIntervalMs uint32   `json:"EVALUATION_INTERVAL_MS"`
		EventSources         []string `json:"EVENT_SOURCES"`
		Active               bool     `json:"ACTIVE"`
		CreatedAtMs          int64    `json:"CREATED_AT"`
		UpdatedAtMs          int64    `json:"UPDATED_AT"`
		EvaluatorPeerID      string   `json:"EVALUATOR_PEER_ID"`
	}{p.ID, p.Name, p.Description, p.Root, p.EvaluationIntervalMs, nonNil(p.EventSources), p.Active, p.CreatedAtMs, p.UpdatedAtMs, p.EvaluatorPeerID})
	if err != nil {
		return nil, err
	}
	if len(jsonSignature) == 0 {
		return body, nil
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	// Re-marshal through an ordered wrapper: Go's map marshal sorts keys, so
	// the signed form is appended as a trailing member instead.
	trimmed := strings.TrimSuffix(string(body), "}")
	return []byte(trimmed + `,"EVALUATOR_SIGNATURE":"` + hex.EncodeToString(jsonSignature) + `"}`), nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// SignPolicy signs both forms with the evaluator key. It returns the signed
// FlatBuffer, the signed canonical JSON, and sets p.Signature.
func SignPolicy(p *Policy, key ed25519.PrivateKey) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, errors.New("trust: ed25519 evaluator key required")
	}
	p.Signature = nil
	unsigned := EncodePolicy(*p)
	p.Signature = ed25519.Sign(key, unsigned)
	signedFB := EncodePolicy(*p)
	unsignedJSON, err := CanonicalPolicyJSON(*p, nil)
	if err != nil {
		return nil, nil, err
	}
	signedJSON, err := CanonicalPolicyJSON(*p, ed25519.Sign(key, unsignedJSON))
	if err != nil {
		return nil, nil, err
	}
	return signedFB, signedJSON, nil
}

// VerifyPolicyBytes checks the FlatBuffer-form signature by rebuilding the
// unsigned bytes from the record itself.
func VerifyPolicyBytes(data []byte, pub ed25519.PublicKey) error {
	p, err := DecodePolicy(data)
	if err != nil {
		return err
	}
	sig := append([]byte(nil), p.Signature...)
	if len(sig) != ed25519.SignatureSize {
		return errors.New("trust: TRP carries no evaluator signature")
	}
	p.Signature = nil
	if !ed25519.Verify(pub, EncodePolicy(p), sig) {
		return errors.New("trust: invalid TRP FlatBuffer signature")
	}
	return nil
}

// VerifyPolicyJSON checks the canonical-JSON-form signature standing alone.
func VerifyPolicyJSON(document []byte, pub ed25519.PublicKey) error {
	var probe struct {
		Signature string `json:"EVALUATOR_SIGNATURE"`
	}
	if err := json.Unmarshal(document, &probe); err != nil {
		return err
	}
	sig, err := hex.DecodeString(probe.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("trust: TRP JSON carries no evaluator signature")
	}
	idx := strings.LastIndex(string(document), `,"EVALUATOR_SIGNATURE":`)
	if idx < 0 {
		return errors.New("trust: TRP JSON signature member misplaced")
	}
	unsigned := string(document[:idx]) + "}"
	if !ed25519.Verify(pub, []byte(unsigned), sig) {
		return errors.New("trust: invalid TRP canonical JSON signature")
	}
	return nil
}

/* ── the store ───────────────────────────────────────────────────────── */

// PolicyStore persists `$TRP` records engine-routed and projects the latest
// record per POLICY_ID.
type PolicyStore struct {
	flat   *storage.FlatSQLStore
	peerID string
	key    ed25519.PrivateKey
	nowMs  func() int64
}

// NewPolicyStore binds the store to the node's FlatSQL store and signing key.
func NewPolicyStore(flat *storage.FlatSQLStore, peerID string, key ed25519.PrivateKey) (*PolicyStore, error) {
	if flat == nil {
		return nil, errors.New("trust: FlatSQL store is required")
	}
	return &PolicyStore{flat: flat, peerID: strings.TrimSpace(peerID), key: key, nowMs: func() int64 { return time.Now().UnixMilli() }}, nil
}

// Put validates, stamps, signs (both forms) and stores a policy. The returned
// JSON is the signed canonical rendering the API serves back.
func (s *PolicyStore) Put(p Policy) (string, []byte, error) {
	if s == nil || s.flat == nil {
		return "", nil, errors.New("trust: policy store is nil")
	}
	p.ID = strings.TrimSpace(p.ID)
	if err := p.Validate(); err != nil {
		return "", nil, err
	}
	now := s.nowMs()
	if p.CreatedAtMs <= 0 {
		p.CreatedAtMs = now
	}
	p.UpdatedAtMs = now
	p.EvaluatorPeerID = s.peerID
	fb, signedJSON, err := SignPolicy(&p, s.key)
	if err != nil {
		return "", nil, err
	}
	cid, err := s.flat.Store(trustPolicySchema, fb, s.peerID, p.Signature)
	if err != nil {
		return "", nil, fmt.Errorf("trust: store TRP record: %w", err)
	}
	return cid, signedJSON, nil
}

// List projects the latest record per POLICY_ID (any Active state).
func (s *PolicyStore) List() ([]Policy, error) {
	if s == nil || s.flat == nil {
		return nil, errors.New("trust: policy store is nil")
	}
	latest := map[string]Policy{}
	var after int64
	for {
		records, err := s.flat.QueryRawRecords(storage.RawRecordQuery{SchemaName: trustPolicySchema, Limit: trustProjectionLoadPageSize, UseRowIDCursor: true, AfterRowID: after})
		if err != nil {
			return nil, fmt.Errorf("trust: load TRP records: %w", err)
		}
		if len(records) == 0 {
			break
		}
		for _, rec := range records {
			if rec == nil {
				continue
			}
			if rec.RowID > after {
				after = rec.RowID
			}
			p, err := DecodePolicy(rec.Data)
			if err != nil || p.ID == "" {
				continue
			}
			if cur, ok := latest[p.ID]; !ok || p.UpdatedAtMs >= cur.UpdatedAtMs {
				latest[p.ID] = p
			}
		}
		if len(records) < trustProjectionLoadPageSize {
			break
		}
	}
	out := make([]Policy, 0, len(latest))
	for _, p := range latest {
		out = append(out, p)
	}
	sortPolicies(out)
	return out, nil
}

func sortPolicies(list []Policy) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].ID < list[j-1].ID; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
