package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	standardsEPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

func newResolvedSourcesStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func upsertSignedPublisherFixture(t *testing.T, store *storage.FlatSQLStore, peerID, name, chain, address, domain string) {
	t.Helper()
	epmBytes := buildSignedPublisherEPM(t, peerID, name, chain, address, domain)
	stored, err := json.Marshal(map[string]string{
		"epm_base64": base64.StdEncoding.EncodeToString(epmBytes),
	})
	if err != nil {
		t.Fatalf("marshal EPM directory fixture: %v", err)
	}
	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:      "node",
		PeerID:    peerID,
		LegalName: "untrusted projection",
		EPMCID:    "bafkrei" + strings.ToLower(strings.TrimPrefix(peerID, "16Uiu")),
		EPMJSON:   string(stored),
		Source:    "test",
	}); err != nil {
		t.Fatalf("UpsertDirectoryRecord: %v", err)
	}
}

func buildSignedPublisherEPM(t *testing.T, peerID, name, chain, address, domain string) []byte {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	publicHex := hex.EncodeToString(publicKey)
	issued := time.Now().Unix() - 60
	expires := issued + 3600
	domainPayload := ""
	domainSignature := ""
	if domain != "" {
		domainPayload = fmt.Sprintf("sdn-domain-proof/1\ndomain=%s\nkey=ed25519:%s\npeerid=%s\nissued=%d\nexpires=%d\n",
			domain, publicHex, peerID, issued, expires)
		domainSignature = hex.EncodeToString(ed25519.Sign(privateKey, []byte(domainPayload)))
	}

	build := func(epmSignature string) []byte {
		b := flatbuffers.NewBuilder(1024)
		nameOffset := b.CreateString(name)
		publicOffset := b.CreateString(publicHex)
		algorithmOffset := b.CreateString("ed25519")
		addressTypeOffset := b.CreateString("ed25519")
		standardsEPM.CryptoKeyStart(b)
		standardsEPM.CryptoKeyAddPUBLIC_KEY(b, publicOffset)
		standardsEPM.CryptoKeyAddALGORITHM(b, algorithmOffset)
		standardsEPM.CryptoKeyAddADDRESS_TYPE(b, addressTypeOffset)
		standardsEPM.CryptoKeyAddKEY_TYPE(b, standardsEPM.KeyTypeSigning)
		keyOffset := standardsEPM.CryptoKeyEnd(b)
		standardsEPM.EPMStartKEYSVector(b, 1)
		b.PrependUOffsetT(keyOffset)
		keysOffset := b.EndVector(1)

		peerAddressOffset := b.CreateString("/p2p/" + peerID)
		standardsEPM.EPMStartMULTIFORMAT_ADDRESSVector(b, 1)
		b.PrependUOffsetT(peerAddressOffset)
		addressesOffset := b.EndVector(1)

		var chainProofsOffset flatbuffers.UOffsetT
		if address != "" {
			chainOffset := b.CreateString(chain)
			addressOffset := b.CreateString(address)
			standardsEPM.ChainProofStart(b)
			standardsEPM.ChainProofAddCHAIN(b, chainOffset)
			standardsEPM.ChainProofAddADDRESS(b, addressOffset)
			proofOffset := standardsEPM.ChainProofEnd(b)
			standardsEPM.EPMStartCHAIN_PROOFSVector(b, 1)
			b.PrependUOffsetT(proofOffset)
			chainProofsOffset = b.EndVector(1)
		}

		var domainProofsOffset flatbuffers.UOffsetT
		if domain != "" {
			domainOffset := b.CreateString(domain)
			domainPublicOffset := b.CreateString(publicHex)
			keyPathOffset := b.CreateString("m/44'/0'/0'/0'/0'")
			domainSignatureOffset := b.CreateString(domainSignature)
			domainPayloadOffset := b.CreateString(hex.EncodeToString([]byte(domainPayload)))
			domainAlgorithmOffset := b.CreateString("ed25519")
			domainEncodingOffset := b.CreateString("raw-ed25519")
			standardsEPM.DomainProofStart(b)
			standardsEPM.DomainProofAddDOMAIN(b, domainOffset)
			standardsEPM.DomainProofAddPUBLIC_KEY(b, domainPublicOffset)
			standardsEPM.DomainProofAddKEY_PATH(b, keyPathOffset)
			standardsEPM.DomainProofAddSIGNATURE(b, domainSignatureOffset)
			standardsEPM.DomainProofAddSIGNED_PAYLOAD(b, domainPayloadOffset)
			standardsEPM.DomainProofAddALGORITHM(b, domainAlgorithmOffset)
			standardsEPM.DomainProofAddENCODING(b, domainEncodingOffset)
			proofOffset := standardsEPM.DomainProofEnd(b)
			standardsEPM.EPMStartDOMAIN_PROOFSVector(b, 1)
			b.PrependUOffsetT(proofOffset)
			domainProofsOffset = b.EndVector(1)
		}

		var signatureOffset flatbuffers.UOffsetT
		if epmSignature != "" {
			signatureOffset = b.CreateString(epmSignature)
		}
		standardsEPM.EPMStart(b)
		standardsEPM.EPMAddLEGAL_NAME(b, nameOffset)
		standardsEPM.EPMAddKEYS(b, keysOffset)
		standardsEPM.EPMAddMULTIFORMAT_ADDRESS(b, addressesOffset)
		standardsEPM.EPMAddENTITY_TYPE(b, standardsEPM.EntityTypeNode)
		standardsEPM.EPMAddSIGNATURE_TIMESTAMP(b, issued)
		if signatureOffset != 0 {
			standardsEPM.EPMAddSIGNATURE(b, signatureOffset)
		}
		if chainProofsOffset != 0 {
			standardsEPM.EPMAddCHAIN_PROOFS(b, chainProofsOffset)
		}
		if domainProofsOffset != 0 {
			standardsEPM.EPMAddDOMAIN_PROOFS(b, domainProofsOffset)
		}
		recordOffset := standardsEPM.EPMEnd(b)
		standardsEPM.FinishSizePrefixedEPMBuffer(b, recordOffset)
		return append([]byte(nil), b.FinishedBytes()...)
	}

	unsigned := build("")
	payload, err := epm.EPMSigningPayload(unsigned)
	if err != nil {
		t.Fatalf("EPMSigningPayload: %v", err)
	}
	signed := build(hex.EncodeToString(ed25519.Sign(privateKey, payload)))
	if err := epm.VerifyEPMSignature(signed); err != nil {
		t.Fatalf("fixture EPM signature: %v", err)
	}
	return signed
}

func TestResolveProducerOrganizationThreeRungLadder(t *testing.T) {
	store := newResolvedSourcesStore(t)
	const bondAddress = "0x1234bond"

	positiveBond := trust.NewEvaluator(trust.NewGraph(), trust.MemoryFundsProvider{}, trust.MemoryBondSource{
		ChainID: trust.ChainEthereum,
		Amounts: map[string]float64{bondAddress: 2500},
	})
	errorBond := trust.NewEvaluator(trust.NewGraph(), trust.MemoryFundsProvider{}, trust.MemoryBondSource{
		ChainID: trust.ChainEthereum,
		Errs:    map[string]error{bondAddress: errors.New("chain unavailable")},
	})

	tests := []struct {
		name           string
		peerID         string
		chain          string
		address        string
		domain         string
		evaluator      *trust.Evaluator
		wantState      string
		wantDomainText bool
	}{
		{name: "source tag", peerID: "16UiuSOURCE", wantState: organizationStateSourceTag},
		{name: "signed", peerID: "16UiuSIGNED", wantState: organizationStateSigned},
		{name: "bonded", peerID: "16UiuBONDED", chain: "ethereum", address: bondAddress, evaluator: positiveBond, wantState: organizationStateBonded},
		{name: "domain proof is evidence only", peerID: "16UiuDOMAIN", domain: "observatory.example", wantState: organizationStateSigned, wantDomainText: true},
		{name: "bond source error fails safe", peerID: "16UiuBONDERROR", chain: "ethereum", address: bondAddress, evaluator: errorBond, wantState: organizationStateSigned},
	}

	states := map[string]struct{}{}
	allowed := map[string]bool{
		organizationStateSourceTag: true,
		organizationStateSigned:    true,
		organizationStateBonded:    true,
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantState != organizationStateSourceTag {
				upsertSignedPublisherFixture(t, store, tc.peerID, "State University Radio Observatory", tc.chain, tc.address, tc.domain)
			}
			got := resolveProducerOrganization(store, "source-"+tc.name, tc.peerID, tc.evaluator)
			if got.State != tc.wantState {
				t.Fatalf("state = %q, want %q (organization=%+v)", got.State, tc.wantState, got)
			}
			if !allowed[got.State] {
				t.Fatalf("unexpected state vocabulary %q", got.State)
			}
			if len(got.Evidence) == 0 {
				t.Fatal("evidence is empty")
			}
			joined := strings.Join(got.Evidence, " ")
			if tc.wantDomainText && !strings.Contains(joined, tc.domain) {
				t.Fatalf("domain evidence missing from %q", joined)
			}
			if tc.wantDomainText && got.State != organizationStateSigned {
				t.Fatalf("domain proof changed state to %q", got.State)
			}
			states[got.State] = struct{}{}
		})
	}
	if len(states) != 3 {
		t.Fatalf("states = %v, want exactly source-tag, signed, bonded", states)
	}
}

func TestResolvedSourcesComposesDirectoryIdentity(t *testing.T) {
	store := newResolvedSourcesStore(t)

	seed := func(sourceName, peerID string, base, n int) {
		tags := storage.SourceTags{
			ProviderID:     "test-provider",
			SourceName:     sourceName,
			BatchID:        "b-" + sourceName,
			ContentKeyID:   "public",
			ProducerPeerID: peerID,
		}
		for i := 0; i < n; i++ {
			record := sds.NewOMMBuilder().
				WithNoradCatID(uint32(base + i)).
				WithObjectName(fmt.Sprintf("%s-%02d", sourceName, i)).
				WithEpoch("2026-05-12T00:00:00Z").
				Build()
			if _, err := store.StoreWithSourceTags("OMM.fbs", record, "source:"+sourceName, nil, tags); err != nil {
				t.Fatalf("store: %v", err)
			}
		}
	}
	seed("lane-known", "16UiuKNOWNPEER", 30000, 5)
	seed("lane-unknown", "16UiuMYSTERY", 40000, 3)
	upsertSignedPublisherFixture(t, store, "16UiuKNOWNPEER", "State University Radio Observatory", "", "", "")

	h := &DataQueryHandler{store: store}
	r := httptest.NewRequest("GET", "/api/v1/data/sources", nil)
	w := httptest.NewRecorder()
	h.handleResolvedSources(w, r)
	if w.Code != 200 {
		t.Fatalf("resolved sources -> %d: %s", w.Code, w.Body.String())
	}
	var resp resolvedSourcesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byLane := map[string]resolvedSourceRow{}
	for _, row := range resp.Sources {
		byLane[row.SourceName] = row
	}

	known, ok := byLane["lane-known"]
	if !ok {
		t.Fatalf("lane-known missing from %+v", resp.Sources)
	}
	if known.Organization == nil || known.Organization.Name != "State University Radio Observatory" {
		t.Fatalf("known lane organization = %+v, want the verified EPM legal name", known.Organization)
	}
	if known.Organization.State != organizationStateSigned {
		t.Fatalf("known lane state = %q, want signed", known.Organization.State)
	}
	if known.ProducerPeerID != "16UiuKNOWNPEER" || known.Count != 5 {
		t.Fatalf("known lane = %+v", known)
	}

	unknown, ok := byLane["lane-unknown"]
	if !ok {
		t.Fatalf("lane-unknown missing")
	}
	if unknown.Organization == nil || unknown.Organization.Name != "lane-unknown" || unknown.Organization.State != organizationStateSourceTag {
		t.Fatalf("unknown lane organization = %+v, want verbatim source-tag floor", unknown.Organization)
	}
	if len(unknown.Organization.Evidence) == 0 {
		t.Fatal("unknown lane has no evidence")
	}
}
