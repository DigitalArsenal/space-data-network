package trust

// Trust Rule Verdicts — `$TRV` (spacedatastandards.org v1.207.0): one
// evaluation of a `$TRP` against one subject, with the per-predicate results
// and bond evidence, signed in both forms and stored engine-routed. The signed
// FlatBuffer is also what the trust topics carry — it replaces the pre-1.207
// JSON event envelope.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	sdstrv "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TRV"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Verdict mirrors `$TRV` field for field.
type Verdict struct {
	ID              string            `json:"VERDICT_ID"`
	PolicyID        string            `json:"POLICY_ID"`
	SubjectID       string            `json:"SUBJECT_ID"`
	Passed          bool              `json:"PASSED"`
	PreviousPassed  bool              `json:"PREVIOUS_PASSED"`
	Score           float64           `json:"SCORE"`
	PreviousScore   float64           `json:"PREVIOUS_SCORE"`
	Results         []PredicateResult `json:"RESULTS"`
	Trigger         string            `json:"TRIGGER"`
	EvaluatedAtMs   int64             `json:"EVALUATED_AT"`
	EvaluatorPeerID string            `json:"EVALUATOR_PEER_ID"`
	Signature       []byte            `json:"EVALUATOR_SIGNATURE,omitempty"`
}

func buildBondEvidence(b *flatbuffers.Builder, e BondEvidence) flatbuffers.UOffsetT {
	chain := trustStringOffset(b, e.ChainID)
	addr := trustStringOffset(b, e.Address)
	token := trustStringOffset(b, e.TokenAddress)
	cur := trustStringOffset(b, e.ValueCurrency)
	block := trustStringOffset(b, e.BlockReference)
	query := trustStringOffset(b, e.SourceQuery)
	sdstrv.TRVBondEvidenceStart(b)
	sdstrv.TRVBondEvidenceAddCHAIN_ID(b, chain)
	sdstrv.TRVBondEvidenceAddADDRESS(b, addr)
	sdstrv.TRVBondEvidenceAddTOKEN_ADDRESS(b, token)
	sdstrv.TRVBondEvidenceAddBALANCE(b, e.Balance)
	sdstrv.TRVBondEvidenceAddDECIMALS(b, byte(e.Decimals))
	sdstrv.TRVBondEvidenceAddVALUE_CURRENCY(b, cur)
	sdstrv.TRVBondEvidenceAddNORMALIZED_VALUE(b, e.NormalizedValue)
	sdstrv.TRVBondEvidenceAddHELD_SINCE(b, trustInt64ToUint64(e.HeldSinceMs))
	sdstrv.TRVBondEvidenceAddOBSERVED_AT(b, trustInt64ToUint64(e.ObservedAtMs))
	sdstrv.TRVBondEvidenceAddBLOCK_REFERENCE(b, block)
	sdstrv.TRVBondEvidenceAddSOURCE_QUERY(b, query)
	return sdstrv.TRVBondEvidenceEnd(b)
}

func buildResult(b *flatbuffers.Builder, r PredicateResult) flatbuffers.UOffsetT {
	id := trustStringOffset(b, r.PredicateID)
	text := trustStringOffset(b, r.EvidenceText)
	var ev, matched flatbuffers.UOffsetT
	if len(r.BondEvidence) > 0 {
		offs := make([]flatbuffers.UOffsetT, len(r.BondEvidence))
		for i, e := range r.BondEvidence {
			offs[i] = buildBondEvidence(b, e)
		}
		sdstrv.TRVPredicateResultStartBOND_EVIDENCEVector(b, len(offs))
		for i := len(offs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offs[i])
		}
		ev = b.EndVector(len(offs))
	}
	matched = stringVector(b, r.TrusterIDsMatched, sdstrv.TRVPredicateResultStartTRUSTER_IDS_MATCHEDVector)
	sdstrv.TRVPredicateResultStart(b)
	sdstrv.TRVPredicateResultAddPREDICATE_ID(b, id)
	sdstrv.TRVPredicateResultAddKIND(b, sdstrv.EnumValuestrpPredicateKind[string(r.Kind)])
	sdstrv.TRVPredicateResultAddPASSED(b, r.Passed)
	sdstrv.TRVPredicateResultAddMEASURED_VALUE(b, r.MeasuredValue)
	sdstrv.TRVPredicateResultAddREQUIRED_VALUE(b, r.RequiredValue)
	if ev != 0 {
		sdstrv.TRVPredicateResultAddBOND_EVIDENCE(b, ev)
	}
	if matched != 0 {
		sdstrv.TRVPredicateResultAddTRUSTER_IDS_MATCHED(b, matched)
	}
	sdstrv.TRVPredicateResultAddEVIDENCE_TEXT(b, text)
	return sdstrv.TRVPredicateResultEnd(b)
}

// EncodeVerdict renders the `$TRV` FlatBuffer (identifier "$TRV").
func EncodeVerdict(v Verdict) []byte {
	b := flatbuffers.NewBuilder(2048)
	id := trustStringOffset(b, v.ID)
	policy := trustStringOffset(b, v.PolicyID)
	subject := trustStringOffset(b, v.SubjectID)
	trigger := trustStringOffset(b, v.Trigger)
	peer := trustStringOffset(b, v.EvaluatorPeerID)
	var results flatbuffers.UOffsetT
	if len(v.Results) > 0 {
		offs := make([]flatbuffers.UOffsetT, len(v.Results))
		for i, r := range v.Results {
			offs[i] = buildResult(b, r)
		}
		sdstrv.TRVStartRESULTSVector(b, len(offs))
		for i := len(offs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offs[i])
		}
		results = b.EndVector(len(offs))
	}
	sig := trustByteVector(b, v.Signature, sdstrv.TRVStartEVALUATOR_SIGNATUREVector)
	sdstrv.TRVStart(b)
	sdstrv.TRVAddVERDICT_ID(b, id)
	sdstrv.TRVAddPOLICY_ID(b, policy)
	sdstrv.TRVAddSUBJECT_ID(b, subject)
	sdstrv.TRVAddPASSED(b, v.Passed)
	sdstrv.TRVAddPREVIOUS_PASSED(b, v.PreviousPassed)
	sdstrv.TRVAddSCORE(b, v.Score)
	sdstrv.TRVAddPREVIOUS_SCORE(b, v.PreviousScore)
	if results != 0 {
		sdstrv.TRVAddRESULTS(b, results)
	}
	sdstrv.TRVAddTRIGGER(b, trigger)
	sdstrv.TRVAddEVALUATED_AT(b, trustInt64ToUint64(v.EvaluatedAtMs))
	sdstrv.TRVAddEVALUATOR_PEER_ID(b, peer)
	sdstrv.TRVAddEVALUATOR_SIGNATURE(b, sig)
	end := sdstrv.TRVEnd(b)
	sdstrv.FinishTRVBuffer(b, end)
	return b.FinishedBytes()
}

// DecodeVerdict reads a `$TRV` FlatBuffer.
func DecodeVerdict(data []byte) (Verdict, error) {
	if len(data) == 0 {
		return Verdict{}, errors.New("empty TRV record")
	}
	if !sdstrv.TRVBufferHasIdentifier(data) {
		return Verdict{}, errors.New("record is not TRV.fbs")
	}
	rec := sdstrv.GetRootAsTRV(data, 0)
	out := Verdict{
		ID:              string(rec.VERDICT_ID()),
		PolicyID:        string(rec.POLICY_ID()),
		SubjectID:       string(rec.SUBJECT_ID()),
		Passed:          rec.PASSED(),
		PreviousPassed:  rec.PREVIOUS_PASSED(),
		Score:           rec.SCORE(),
		PreviousScore:   rec.PREVIOUS_SCORE(),
		Trigger:         string(rec.TRIGGER()),
		EvaluatedAtMs:   trustUint64ToInt64(rec.EVALUATED_AT()),
		EvaluatorPeerID: string(rec.EVALUATOR_PEER_ID()),
		Signature:       append([]byte(nil), rec.EVALUATOR_SIGNATUREBytes()...),
	}
	for i := 0; i < rec.RESULTSLength(); i++ {
		var r sdstrv.TRVPredicateResult
		if !rec.RESULTS(&r, i) {
			continue
		}
		pr := PredicateResult{
			PredicateID:   string(r.PREDICATE_ID()),
			Kind:          predicateKindFromName(r.KIND().String()),
			Passed:        r.PASSED(),
			MeasuredValue: r.MEASURED_VALUE(),
			RequiredValue: r.REQUIRED_VALUE(),
			EvidenceText:  string(r.EVIDENCE_TEXT()),
		}
		for j := 0; j < r.BOND_EVIDENCELength(); j++ {
			var e sdstrv.TRVBondEvidence
			if !r.BOND_EVIDENCE(&e, j) {
				continue
			}
			pr.BondEvidence = append(pr.BondEvidence, BondEvidence{
				ChainID: string(e.CHAIN_ID()), Address: string(e.ADDRESS()), TokenAddress: string(e.TOKEN_ADDRESS()),
				Balance: e.BALANCE(), Decimals: uint32(e.DECIMALS()), ValueCurrency: string(e.VALUE_CURRENCY()), NormalizedValue: e.NORMALIZED_VALUE(),
				HeldSinceMs: trustUint64ToInt64(e.HELD_SINCE()), ObservedAtMs: trustUint64ToInt64(e.OBSERVED_AT()),
				BlockReference: string(e.BLOCK_REFERENCE()), SourceQuery: string(e.SOURCE_QUERY()),
			})
		}
		for j := 0; j < r.TRUSTER_IDS_MATCHEDLength(); j++ {
			pr.TrusterIDsMatched = append(pr.TrusterIDsMatched, string(r.TRUSTER_IDS_MATCHED(j)))
		}
		out.Results = append(out.Results, pr)
	}
	return out, nil
}

// CanonicalVerdictJSON is the IDL-order JSON form (signature appended last).
func CanonicalVerdictJSON(v Verdict, jsonSignature []byte) ([]byte, error) {
	results := v.Results
	if results == nil {
		results = []PredicateResult{}
	}
	body, err := json.Marshal(struct {
		ID              string            `json:"VERDICT_ID"`
		PolicyID        string            `json:"POLICY_ID"`
		SubjectID       string            `json:"SUBJECT_ID"`
		Passed          bool              `json:"PASSED"`
		PreviousPassed  bool              `json:"PREVIOUS_PASSED"`
		Score           float64           `json:"SCORE"`
		PreviousScore   float64           `json:"PREVIOUS_SCORE"`
		Results         []PredicateResult `json:"RESULTS"`
		Trigger         string            `json:"TRIGGER"`
		EvaluatedAtMs   int64             `json:"EVALUATED_AT"`
		EvaluatorPeerID string            `json:"EVALUATOR_PEER_ID"`
	}{v.ID, v.PolicyID, v.SubjectID, v.Passed, v.PreviousPassed, round6(v.Score), round6(v.PreviousScore), results, v.Trigger, v.EvaluatedAtMs, v.EvaluatorPeerID})
	if err != nil {
		return nil, err
	}
	if len(jsonSignature) == 0 {
		return body, nil
	}
	return []byte(strings.TrimSuffix(string(body), "}") + `,"EVALUATOR_SIGNATURE":"` + hex.EncodeToString(jsonSignature) + `"}`), nil
}

func round6(f float64) float64 { return math.Round(f*1e6) / 1e6 }

// SignVerdict signs both forms; returns the signed FlatBuffer and JSON.
func SignVerdict(v *Verdict, key ed25519.PrivateKey) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, errors.New("trust: ed25519 evaluator key required")
	}
	v.Signature = nil
	unsigned := EncodeVerdict(*v)
	v.Signature = ed25519.Sign(key, unsigned)
	signedFB := EncodeVerdict(*v)
	unsignedJSON, err := CanonicalVerdictJSON(*v, nil)
	if err != nil {
		return nil, nil, err
	}
	signedJSON, err := CanonicalVerdictJSON(*v, ed25519.Sign(key, unsignedJSON))
	if err != nil {
		return nil, nil, err
	}
	return signedFB, signedJSON, nil
}

// VerifyVerdictBytes checks the FlatBuffer-form signature.
func VerifyVerdictBytes(data []byte, pub ed25519.PublicKey) error {
	v, err := DecodeVerdict(data)
	if err != nil {
		return err
	}
	sig := append([]byte(nil), v.Signature...)
	if len(sig) != ed25519.SignatureSize {
		return errors.New("trust: TRV carries no evaluator signature")
	}
	v.Signature = nil
	if !ed25519.Verify(pub, EncodeVerdict(v), sig) {
		return errors.New("trust: invalid TRV FlatBuffer signature")
	}
	return nil
}

// VerifyVerdictJSON checks the canonical-JSON-form signature standing alone.
func VerifyVerdictJSON(document []byte, pub ed25519.PublicKey) error {
	var probe struct {
		Signature string `json:"EVALUATOR_SIGNATURE"`
	}
	if err := json.Unmarshal(document, &probe); err != nil {
		return err
	}
	sig, err := hex.DecodeString(probe.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("trust: TRV JSON carries no evaluator signature")
	}
	idx := strings.LastIndex(string(document), `,"EVALUATOR_SIGNATURE":`)
	if idx < 0 {
		return errors.New("trust: TRV JSON signature member misplaced")
	}
	if !ed25519.Verify(pub, []byte(string(document[:idx])+"}"), sig) {
		return errors.New("trust: invalid TRV canonical JSON signature")
	}
	return nil
}

// VerdictStore persists `$TRV` records engine-routed.
type VerdictStore struct {
	flat   *storage.FlatSQLStore
	peerID string
}

// NewVerdictStore binds the store.
func NewVerdictStore(flat *storage.FlatSQLStore, peerID string) (*VerdictStore, error) {
	if flat == nil {
		return nil, errors.New("trust: FlatSQL store is required")
	}
	return &VerdictStore{flat: flat, peerID: strings.TrimSpace(peerID)}, nil
}

// Put stores a signed verdict's FlatBuffer bytes.
func (s *VerdictStore) Put(v Verdict, signedFB []byte) error {
	if s == nil || s.flat == nil {
		return errors.New("trust: verdict store is nil")
	}
	if _, err := s.flat.Store(trustVerdictSchema, signedFB, s.peerID, v.Signature); err != nil {
		return fmt.Errorf("trust: store TRV record: %w", err)
	}
	return nil
}

// List reads stored verdicts newest first, filtered by policy and subject.
func (s *VerdictStore) List(policyID, subject string, limit int) ([]Verdict, error) {
	if s == nil || s.flat == nil {
		return nil, errors.New("trust: verdict store is nil")
	}
	if limit <= 0 {
		limit = 100
	}
	var out []Verdict
	var after int64
	for {
		records, err := s.flat.QueryRawRecords(storage.RawRecordQuery{SchemaName: trustVerdictSchema, Limit: trustProjectionLoadPageSize, UseRowIDCursor: true, AfterRowID: after})
		if err != nil {
			return nil, fmt.Errorf("trust: load TRV records: %w", err)
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
			v, err := DecodeVerdict(rec.Data)
			if err != nil {
				continue
			}
			if policyID != "" && v.PolicyID != policyID {
				continue
			}
			if subject != "" && v.SubjectID != subject {
				continue
			}
			out = append(out, v)
		}
		if len(records) < trustProjectionLoadPageSize {
			break
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
