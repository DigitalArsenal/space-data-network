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
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	standardsEPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
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

func upsertSignedPublisherFixture(t *testing.T, store *storage.FlatSQLStore, peerID, name, chain, address string, domains ...string) {
	t.Helper()
	epmBytes := buildSignedPublisherEPM(t, peerID, name, chain, address, domains...)
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

// buildSignedPublisherEPM signs a profile whose one DomainProof, when domains
// are given, binds all of them in that order.
func buildSignedPublisherEPM(t *testing.T, peerID, name, chain, address string, domains ...string) []byte {
	t.Helper()
	var proofs []domainProofSpec
	if len(domains) > 0 {
		proofs = append(proofs, domainProofSpec{domains: domains})
	}
	return buildSignedPublisherEPMProfile(t, peerID, name, "", chain, address, proofs...)
}

// domainProofVersionLine restates the first statement line of
// docs/domain-proofs.md so a change to the production constant fails here.
const domainProofVersionLine = "sdn-domain-proof/2"

// domainProofSpec describes one DomainProof of a fixture EPM. The builder signs
// it with the profile's signing key.
type domainProofSpec struct {
	// domains is the DOMAINS vector as stored, in order. nil stores no vector,
	// which is how a proof written under SDS 1.264.0 or 1.265.0 decodes.
	domains []string
	// legacyDomain fills the retired slot 0, where those releases stored their
	// one domain.
	legacyDomain string
	// statement replaces the signed statement. nil signs the statement of
	// domains, issued a minute ago and valid for an hour.
	statement func(publicHex, peerID string) string
	// badSignature signs the statement, then corrupts the signature.
	badSignature bool
}

// testDomainStatement composes a statement line by line from the layout in
// docs/domain-proofs.md, apart from the production builder.
func testDomainStatement(version string, domains []string, publicHex, peerID string, issued, expires int64) string {
	var statement strings.Builder
	statement.WriteString(version + "\n")
	for _, domain := range domains {
		statement.WriteString("domain=" + domain + "\n")
	}
	statement.WriteString("key=ed25519:" + publicHex + "\n")
	statement.WriteString("peerid=" + peerID + "\n")
	statement.WriteString(fmt.Sprintf("issued=%d\nexpires=%d\n", issued, expires))
	return statement.String()
}

// signedDomainProof is a spec with its statement signed.
type signedDomainProof struct {
	spec       domainProofSpec
	payloadHex string
	signature  string
}

// add writes the proof as a DomainProof table. Strings and vectors come first:
// the builder takes no nested objects while a table is open.
func (p signedDomainProof) add(b *flatbuffers.Builder, publicHex string) flatbuffers.UOffsetT {
	var domainsOffset, legacyOffset flatbuffers.UOffsetT
	if p.spec.domains != nil {
		names := make([]flatbuffers.UOffsetT, len(p.spec.domains))
		for i, domain := range p.spec.domains {
			names[i] = b.CreateString(domain)
		}
		standardsEPM.DomainProofStartDOMAINSVector(b, len(names))
		for i := len(names) - 1; i >= 0; i-- {
			b.PrependUOffsetT(names[i])
		}
		domainsOffset = b.EndVector(len(names))
	}
	if p.spec.legacyDomain != "" {
		legacyOffset = b.CreateString(p.spec.legacyDomain)
	}
	publicOffset := b.CreateString(publicHex)
	keyPathOffset := b.CreateString("m/44'/0'/0'/0'/0'")
	signatureOffset := b.CreateString(p.signature)
	payloadOffset := b.CreateString(p.payloadHex)
	algorithmOffset := b.CreateString("ed25519")
	encodingOffset := b.CreateString("raw-ed25519")
	standardsEPM.DomainProofStart(b)
	if legacyOffset != 0 {
		b.PrependUOffsetTSlot(0, legacyOffset, 0) // the retired slot has no generated adder
	}
	standardsEPM.DomainProofAddPUBLIC_KEY(b, publicOffset)
	standardsEPM.DomainProofAddKEY_PATH(b, keyPathOffset)
	standardsEPM.DomainProofAddSIGNATURE(b, signatureOffset)
	standardsEPM.DomainProofAddSIGNED_PAYLOAD(b, payloadOffset)
	standardsEPM.DomainProofAddALGORITHM(b, algorithmOffset)
	standardsEPM.DomainProofAddENCODING(b, encodingOffset)
	if domainsOffset != 0 {
		standardsEPM.DomainProofAddDOMAINS(b, domainsOffset)
	}
	return standardsEPM.DomainProofEnd(b)
}

// buildSignedPublisherEPMProfile signs a publisher profile with a legal name
// and/or a distinguished name; either may be empty. Each proof is signed by
// the profile's own signing key.
func buildSignedPublisherEPMProfile(t *testing.T, peerID, name, dn, chain, address string, proofs ...domainProofSpec) []byte {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	return buildSignedPublisherEPMProfileWithKey(t, publicKey, privateKey, peerID, name, dn, chain, address, proofs...)
}

func buildSignedPublisherEPMProfileWithKey(t *testing.T, publicKey ed25519.PublicKey, privateKey ed25519.PrivateKey, peerID, name, dn, chain, address string, proofs ...domainProofSpec) []byte {
	t.Helper()
	publicHex := hex.EncodeToString(publicKey)
	issued := time.Now().Unix() - 60
	expires := issued + 3600
	signedProofs := make([]signedDomainProof, len(proofs))
	for i, spec := range proofs {
		statement := testDomainStatement(domainProofVersionLine, spec.domains, publicHex, peerID, issued, expires)
		if spec.statement != nil {
			statement = spec.statement(publicHex, peerID)
		}
		signature := ed25519.Sign(privateKey, []byte(statement))
		if spec.badSignature {
			signature[0] ^= 0xff
		}
		signedProofs[i] = signedDomainProof{spec: spec, payloadHex: hex.EncodeToString([]byte(statement)), signature: hex.EncodeToString(signature)}
	}

	build := func(epmSignature string) []byte {
		b := flatbuffers.NewBuilder(1024)
		var nameOffset, dnOffset flatbuffers.UOffsetT
		if name != "" {
			nameOffset = b.CreateString(name)
		}
		if dn != "" {
			dnOffset = b.CreateString(dn)
		}
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
		if len(signedProofs) > 0 {
			proofOffsets := make([]flatbuffers.UOffsetT, len(signedProofs))
			for i, proof := range signedProofs {
				proofOffsets[i] = proof.add(b, publicHex)
			}
			standardsEPM.EPMStartDOMAIN_PROOFSVector(b, len(proofOffsets))
			for i := len(proofOffsets) - 1; i >= 0; i-- {
				b.PrependUOffsetT(proofOffsets[i])
			}
			domainProofsOffset = b.EndVector(len(proofOffsets))
		}

		var signatureOffset flatbuffers.UOffsetT
		if epmSignature != "" {
			signatureOffset = b.CreateString(epmSignature)
		}
		standardsEPM.EPMStart(b)
		if name != "" {
			standardsEPM.EPMAddLEGAL_NAME(b, nameOffset)
		}
		if dn != "" {
			standardsEPM.EPMAddDN(b, dnOffset)
		}
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
		domains        []string
		evaluator      *trust.Evaluator
		wantState      string
		wantDomainText bool
	}{
		{name: "source tag", peerID: "16UiuSOURCE", wantState: organizationStateSourceTag},
		{name: "signed", peerID: "16UiuSIGNED", wantState: organizationStateSigned},
		{name: "bonded", peerID: "16UiuBONDED", chain: "ethereum", address: bondAddress, evaluator: positiveBond, wantState: organizationStateBonded},
		{name: "domain proof is evidence only", peerID: "16UiuDOMAIN", domains: []string{"observatory.example"}, wantState: organizationStateSigned, wantDomainText: true},
		{name: "every domain of a proof is evidence", peerID: "16UiuDOMAINS", domains: []string{"observatory.example", "radio.observatory.example"}, wantState: organizationStateSigned, wantDomainText: true},
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
				upsertSignedPublisherFixture(t, store, tc.peerID, "State University Radio Observatory", tc.chain, tc.address, tc.domains...)
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
			for _, domain := range tc.domains {
				if tc.wantDomainText && !strings.Contains(joined, domain) {
					t.Fatalf("evidence for domain %q missing from %q", domain, joined)
				}
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

// domainProofRecord is a signed profile for peerID carrying the proofs, and its
// first DomainProof.
func domainProofRecord(t *testing.T, peerID string, proofs ...domainProofSpec) (*standardsEPM.EPM, *standardsEPM.DomainProof) {
	t.Helper()
	data := buildSignedPublisherEPMProfile(t, peerID, "State University Radio Observatory", "", "", "", proofs...)
	record := standardsEPM.GetSizePrefixedRootAsEPM(data, 0)
	proof := new(standardsEPM.DomainProof)
	if !record.DOMAIN_PROOFS(proof, 0) {
		t.Fatal("fixture carries no DomainProof")
	}
	return record, proof
}

func TestDomainProofMultiDomainVerifies(t *testing.T) {
	const peerID = "16UiuMULTIDOMAIN"
	domains := []string{"radio.observatory.example", "observatory.example", "telescope.example.org"}
	epmBytes := buildSignedPublisherEPMProfile(t, peerID, "State University Radio Observatory", "", "", "", domainProofSpec{domains: domains})
	record := standardsEPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
	proof := new(standardsEPM.DomainProof)
	if !record.DOMAIN_PROOFS(proof, 0) || proof.DOMAINSLength() != len(domains) {
		t.Fatalf("fixture proof carries %d domains, want %d", proof.DOMAINSLength(), len(domains))
	}

	proven, err := verifyPublisherDomainProof(record, proof, peerID, time.Now())
	if err != nil {
		t.Fatalf("a proof whose statement signs all %d names was refused: %v", len(domains), err)
	}
	if !reflect.DeepEqual(proven, domains) {
		t.Fatalf("proven = %v, want the DOMAINS vector in order %v", proven, domains)
	}
	sorted := append([]string(nil), domains...)
	sort.Strings(sorted)
	if got := verifiedPublisherDomains(record, peerID, time.Now()); !reflect.DeepEqual(got, sorted) {
		t.Fatalf("verified domains = %v, want every name, sorted: %v", got, sorted)
	}

	identity := publisherIdentity{epmBytes: func(string) []byte { return epmBytes }}
	org := resolvePublisherOrganization(nil, "lane", peerID, nil, identity)
	if org.State != organizationStateSigned {
		t.Fatalf("domain proofs changed the state to %q; they are evidence, not a rung", org.State)
	}
	evidence := strings.Join(org.Evidence, "\n")
	for _, domain := range domains {
		sentence := "A verified domain-control proof links this publisher to " + domain + "."
		if strings.Count(evidence, sentence) != 1 {
			t.Fatalf("evidence has no single sentence for %s:\n%s", domain, evidence)
		}
	}
}

// distinctDomains is n canonical names, none alike.
func distinctDomains(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("host-%02d.observatory.example", i)
	}
	return names
}

func TestDomainProofRefusals(t *testing.T) {
	const peerID = "16UiuREFUSED"
	one := []string{"observatory.example"}
	two := []string{"observatory.example", "radio.observatory.example"}
	overCap := distinctDomains(maxDomainsPerProof + 1)
	badFirstOverCap := append([]string{"Bad.Example"}, distinctDomains(maxDomainsPerProof)...)
	statement := func(version string, domains []string, issued, expires int64) func(publicHex, peerID string) string {
		return func(publicHex, peer string) string {
			return testDomainStatement(version, domains, publicHex, peer, issued, expires)
		}
	}
	now := time.Now().Unix()
	valid := func(version string, domains []string) func(publicHex, peerID string) string {
		return statement(version, domains, now-60, now+3600)
	}

	tests := []struct {
		name     string
		proof    domainProofSpec
		want     error  // matched with errors.Is when set
		wantText string // else a substring of the refusal
	}{
		{name: "empty DOMAINS vector, validly signed", proof: domainProofSpec{domains: []string{}}, want: errDomainProofNoDomains},
		{name: "no DOMAINS vector", proof: domainProofSpec{}, want: errDomainProofNoDomains},
		{name: "a name listed twice, side by side", proof: domainProofSpec{domains: []string{two[0], two[0]}}, want: errDomainProofRepeatedName},
		{name: "a name listed twice, apart", proof: domainProofSpec{domains: []string{two[0], two[1], two[0]}}, want: errDomainProofRepeatedName},
		{name: "one name more than the cap", proof: domainProofSpec{domains: overCap}, want: errDomainProofTooManyNames},
		{name: "over the cap, decided before any name is read", proof: domainProofSpec{domains: badFirstOverCap}, want: errDomainProofTooManyNames},
		{
			name:  "proof written under SDS 1.265.0: the retired slot is not read",
			proof: domainProofSpec{legacyDomain: "observatory.example", statement: valid("sdn-domain-proof/1", one)},
			want:  errDomainProofNoDomains,
		},
		{name: "sdn-domain-proof/1 statement", proof: domainProofSpec{domains: one, statement: valid("sdn-domain-proof/1", one)}, want: errDomainProofStatement},
		{name: "statement leaves out a name", proof: domainProofSpec{domains: two, statement: valid(domainProofVersionLine, two[:1])}, want: errDomainProofStatement},
		{name: "statement adds a name", proof: domainProofSpec{domains: one, statement: valid(domainProofVersionLine, two)}, want: errDomainProofStatement},
		{
			name:  "statement orders the names differently",
			proof: domainProofSpec{domains: two, statement: valid(domainProofVersionLine, []string{two[1], two[0]})},
			want:  errDomainProofStatement,
		},
		{
			name: "statement signed for another peer",
			proof: domainProofSpec{domains: two, statement: func(publicHex, _ string) string {
				return testDomainStatement(domainProofVersionLine, two, publicHex, "16UiuSOMEONEELSE", now-60, now+3600)
			}},
			want: errDomainProofStatement,
		},
		{
			name: "statement names another key",
			proof: domainProofSpec{domains: two, statement: func(publicHex, peer string) string {
				return testDomainStatement(domainProofVersionLine, two, "00"+publicHex[2:], peer, now-60, now+3600)
			}},
			want: errDomainProofStatement,
		},
		{name: "statement is not a number of seconds", proof: domainProofSpec{domains: one, statement: func(publicHex, peer string) string {
			return testDomainStatement(domainProofVersionLine, one, publicHex, peer, now-60, now+3600) + "trailing\n"
		}}, want: errDomainProofStatement},
		{name: "expired", proof: domainProofSpec{domains: two, statement: statement(domainProofVersionLine, two, now-7200, now-3600)}, wantText: "expired"},
		{name: "not valid yet", proof: domainProofSpec{domains: two, statement: statement(domainProofVersionLine, two, now+3600, now+7200)}, wantText: "not valid yet"},
		{name: "signature does not verify", proof: domainProofSpec{domains: two, badSignature: true}, wantText: "SIGNATURE does not verify"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record, proof := domainProofRecord(t, peerID, tc.proof)
			proven, err := verifyPublisherDomainProof(record, proof, peerID, time.Now())
			switch {
			case err == nil:
				t.Fatalf("proof was accepted, proving %v", proven)
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("refused with %q, want %q", err, tc.want)
			case tc.want == nil && !strings.Contains(err.Error(), tc.wantText):
				t.Fatalf("refused with %q, want it to say %q", err, tc.wantText)
			}
			if proven != nil {
				t.Fatalf("a refused proof still proved %v", proven)
			}
			if got := verifiedPublisherDomains(record, peerID, time.Now()); len(got) != 0 {
				t.Fatalf("a refused proof still yields evidence for %v", got)
			}
		})
	}
}

func TestDomainProofOneBadNameRefusesTheProof(t *testing.T) {
	const peerID = "16UiuBADNAME"
	badNames := []string{
		"Observatory.example",   // uppercase
		"bad_name.example",      // underscore
		"observatory.example.",  // trailing dot
		".observatory.example",  // empty first label
		" observatory.example",  // leading space
		"observatory.example\n", // line feed
		"m\u00fcnchen.example",  // IDN not in punycode
		"a..example",            // empty label
		"",                      // empty name
	}
	for _, bad := range badNames {
		for position := 0; position < 2; position++ {
			domains := []string{"good.example", "other.example"}
			domains[position] = bad
			t.Run(fmt.Sprintf("%q at %d", bad, position), func(t *testing.T) {
				// The statement signs exactly this vector, so only the name rule can refuse it.
				record, proof := domainProofRecord(t, peerID, domainProofSpec{domains: domains})
				_, err := verifyPublisherDomainProof(record, proof, peerID, time.Now())
				if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("DOMAINS[%d]", position)) {
					t.Fatalf("refusal = %v, want it to name DOMAINS[%d]", err, position)
				}
				if got := verifiedPublisherDomains(record, peerID, time.Now()); len(got) != 0 {
					t.Fatalf("the good name in a refused proof still yields evidence: %v", got)
				}
			})
		}
	}
}

func TestDomainProofsAreVerifiedIndependently(t *testing.T) {
	const peerID = "16UiuTHREEPROOFS"
	good := []string{"observatory.example", "radio.observatory.example"}
	data := buildSignedPublisherEPMProfile(t, peerID, "State University Radio Observatory", "", "", "",
		domainProofSpec{domains: []string{"refused.example", "Refused.Example"}},
		domainProofSpec{domains: good},
		domainProofSpec{legacyDomain: "legacy.example"},
	)
	record := standardsEPM.GetSizePrefixedRootAsEPM(data, 0)
	if record.DOMAIN_PROOFSLength() != 3 {
		t.Fatalf("fixture carries %d proofs, want 3", record.DOMAIN_PROOFSLength())
	}
	if got := verifiedPublisherDomains(record, peerID, time.Now()); !reflect.DeepEqual(got, good) {
		t.Fatalf("verified domains = %v, want only the names of the proof that verifies: %v", got, good)
	}
}

func TestDomainProofNameCap(t *testing.T) {
	const peerID = "16UiuNAMECAP"
	atCap := distinctDomains(maxDomainsPerProof)
	if maxDomainsPerProof != 32 {
		t.Fatalf("maxDomainsPerProof = %d; docs/domain-proofs.md says 32", maxDomainsPerProof)
	}

	record, proof := domainProofRecord(t, peerID, domainProofSpec{domains: atCap})
	proven, err := verifyPublisherDomainProof(record, proof, peerID, time.Now())
	if err != nil || !reflect.DeepEqual(proven, atCap) {
		t.Fatalf("a proof of exactly %d names: proven %d names, err %v", maxDomainsPerProof, len(proven), err)
	}

	for _, n := range []int{maxDomainsPerProof + 1, 2 * maxDomainsPerProof, 500} {
		record, proof := domainProofRecord(t, peerID, domainProofSpec{domains: distinctDomains(n)})
		if _, err := verifyPublisherDomainProof(record, proof, peerID, time.Now()); !errors.Is(err, errDomainProofTooManyNames) {
			t.Fatalf("a proof of %d names: refusal = %v, want %v", n, err, errDomainProofTooManyNames)
		}
	}

	// The cap is per proof: a record keeps the proofs that are within it.
	data := buildSignedPublisherEPMProfile(t, peerID, "State University Radio Observatory", "", "", "",
		domainProofSpec{domains: distinctDomains(maxDomainsPerProof + 1)},
		domainProofSpec{domains: []string{"observatory.example", "radio.observatory.example"}},
	)
	got := verifiedPublisherDomains(standardsEPM.GetSizePrefixedRootAsEPM(data, 0), peerID, time.Now())
	if want := []string{"observatory.example", "radio.observatory.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("verified domains = %v, want only those of the proof within the cap: %v", got, want)
	}
}

func TestDomainProofRepeatedNameIsJudgedInCanonicalForm(t *testing.T) {
	const peerID = "16UiuREPEATS"
	// Names must already be canonical, so a repeat is an exact repeat. A name
	// that repeats another only after lowercasing, trimming or dropping a
	// trailing dot is refused as not canonical, at the first such name.
	for _, tc := range []struct {
		name    string
		domains []string
		repeat  bool
	}{
		{"exact repeat", []string{"observatory.example", "observatory.example"}, true},
		{"uppercase twin", []string{"Observatory.example", "observatory.example"}, false},
		{"trailing-dot twin", []string{"observatory.example", "observatory.example."}, false},
		{"padded twin", []string{"observatory.example", " observatory.example"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, proof := domainProofRecord(t, peerID, domainProofSpec{domains: tc.domains})
			_, err := verifyPublisherDomainProof(record, proof, peerID, time.Now())
			if err == nil {
				t.Fatal("proof accepted")
			}
			if got := errors.Is(err, errDomainProofRepeatedName); got != tc.repeat {
				t.Fatalf("refusal = %q; repeated-name refusal = %v, want %v", err, got, tc.repeat)
			}
			if tc.repeat && !strings.Contains(err.Error(), "DOMAINS[1] repeats DOMAINS[0]") {
				t.Fatalf("refusal %q does not say which names repeat", err)
			}
		})
	}
}

func TestDomainProofSameNameInTwoProofsIsNotARepeat(t *testing.T) {
	const peerID = "16UiuTWOPROOFS"
	data := buildSignedPublisherEPMProfile(t, peerID, "State University Radio Observatory", "", "", "",
		domainProofSpec{domains: []string{"observatory.example", "radio.observatory.example"}},
		domainProofSpec{domains: []string{"observatory.example", "telescope.example.org"}},
	)
	got := verifiedPublisherDomains(standardsEPM.GetSizePrefixedRootAsEPM(data, 0), peerID, time.Now())
	want := []string{"observatory.example", "radio.observatory.example", "telescope.example.org"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verified domains = %v, want each name once: %v", got, want)
	}
}

// docs/domain-proofs.md states the cap and names both refusals as the node does.
func TestDomainProofLimitsAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "domain-proofs.md"))
	if err != nil {
		t.Fatalf("read docs/domain-proofs.md: %v", err)
	}
	for name, want := range map[string]string{
		"the cap":              fmt.Sprintf("at most %d names", maxDomainsPerProof),
		"the too-many refusal": errDomainProofTooManyNames.Error(),
		"the repeated refusal": errDomainProofRepeatedName.Error(),
		"the empty refusal":    errDomainProofNoDomains.Error(),
	} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("docs/domain-proofs.md does not carry %s: %q", name, want)
		}
	}
}

// The vector in docs/domain-proofs.md: a fixed key, peer and window, so an
// issuer can check its statement bytes against it and a verifier its code.
func TestDomainProofDocumentedVector(t *testing.T) {
	const (
		peerID       = "12D3KooWExampleNodeForDomainProofs1111111111111"
		issued       = int64(1790000000)
		expires      = int64(1800000000)
		publicHex    = "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8"
		payloadHex   = "73646e2d646f6d61696e2d70726f6f662f320a646f6d61696e3d6e6f64652e6578616d706c652e6f72670a646f6d61696e3d6e6f64652e6578616d706c652e636f6d0a6b65793d656432353531393a303361313037626666336365313062653164373064643138653734626330393936376534643633303962613530643566316464633836363431323535333162380a7065657269643d313244334b6f6f574578616d706c654e6f6465466f72446f6d61696e50726f6f6673313131313131313131313131310a6973737565643d313739303030303030300a657870697265733d313830303030303030300a"
		signatureHex = "27acb947ee522fb5538fc85c1318762d7de12aeee27c8504dbadf3350ecb9fb25fd5b444588b1254d38140903969a4a9b6f51142898ea526bfe986664712ea06"
	)
	domains := []string{"node.example.org", "node.example.com"}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if hex.EncodeToString(publicKey) != publicHex {
		t.Fatalf("vector public key = %x, want %s", publicKey, publicHex)
	}

	statement := domainProofStatement(domains, "ed25519", publicHex, peerID, issued, expires)
	if got := hex.EncodeToString(statement); got != payloadHex {
		t.Fatalf("statement bytes\n%q\nwant\n%q", statement, string(mustDecodeHex(t, payloadHex)))
	}
	if got := hex.EncodeToString(ed25519.Sign(privateKey, statement)); got != signatureHex {
		t.Fatalf("signature = %s, want the documented %s", got, signatureHex)
	}

	data := buildSignedPublisherEPMProfileWithKey(t, publicKey, privateKey, peerID, "Example Observatory", "", "", "",
		domainProofSpec{domains: domains, statement: func(string, string) string { return string(statement) }})
	record := standardsEPM.GetSizePrefixedRootAsEPM(data, 0)
	proof := new(standardsEPM.DomainProof)
	if !record.DOMAIN_PROOFS(proof, 0) || string(proof.SIGNED_PAYLOAD()) != payloadHex || string(proof.SIGNATURE()) != signatureHex {
		t.Fatal("fixture did not store the documented payload and signature")
	}
	proven, err := verifyPublisherDomainProof(record, proof, peerID, time.Unix(issued+1000, 0))
	if err != nil || !reflect.DeepEqual(proven, domains) {
		t.Fatalf("documented vector: proven %v, err %v; want %v", proven, err, domains)
	}
	if _, err := verifyPublisherDomainProof(record, proof, peerID, time.Unix(issued-301, 0)); err == nil {
		t.Fatal("vector accepted 301 s before issued")
	}
	if _, err := verifyPublisherDomainProof(record, proof, peerID, time.Unix(expires+301, 0)); err == nil {
		t.Fatal("vector accepted 301 s after expires")
	}
	if _, err := verifyPublisherDomainProof(record, proof, "12D3KooWAnotherPeer", time.Unix(issued+1000, 0)); !errors.Is(err, errDomainProofStatement) {
		t.Fatalf("vector verified for another peer: %v", err)
	}

	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "domain-proofs.md"))
	if err != nil {
		t.Fatalf("read docs/domain-proofs.md: %v", err)
	}
	for name, want := range map[string]string{
		"statement": string(statement), "public key": publicHex, "peer ID": peerID,
		"payload": payloadHex, "signature": signatureHex,
	} {
		if !strings.Contains(string(doc), want) {
			t.Fatalf("docs/domain-proofs.md does not carry the vector's %s", name)
		}
	}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
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
	upsertSignedPublisherFixture(t, store, "16UiuKNOWNPEER", "State University Radio Observatory", "", "")

	h := &DataQueryHandler{store: store}
	r := httptest.NewRequest("GET", "/api/v1/data/sources", nil)
	w := httptest.NewRecorder()
	h.handleResolvedSources(w, r)
	if w.Code != 200 {
		t.Fatalf("resolved sources -> %d: %s", w.Code, w.Body.String())
	}
	resp := decodeResolvedSourcesPage(t, w.Body.Bytes())

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

func TestResolvePublisherOrganizationFromHeldBytesAndSelf(t *testing.T) {
	store := newResolvedSourcesStore(t)
	const local = "16UiuLOCALNODE"
	const trusted = "16UiuTRUSTEDPEER"
	const placeholder = "16UiuPLACEHOLDER"
	held := map[string][]byte{
		local:       buildSignedPublisherEPMProfile(t, local, "Digital Arsenal", "sdn.spaceaware.io", "", ""),
		trusted:     buildSignedPublisherEPMProfile(t, trusted, "", "celestrak.eth", "", ""),
		placeholder: buildSignedPublisherEPMProfile(t, placeholder, "", "SDN Node <peer.ID 16*HOLDER>", "", ""),
	}
	identity := publisherIdentity{localPeerID: local, epmBytes: func(peerID string) []byte { return held[peerID] }}

	self := resolvePublisherOrganization(store, "account", local, nil, identity)
	if !self.Self || self.State != organizationStateSigned || self.Name != "Digital Arsenal" || self.EPMCID == "" || self.Unnamed {
		t.Fatalf("self = %+v, want signed, named from the legal name, marked self, with a CID", self)
	}
	peerOrg := resolvePublisherOrganization(store, "celestrak-gp", trusted, nil, identity)
	if peerOrg.Self || peerOrg.State != organizationStateSigned || peerOrg.Name != "celestrak.eth" || peerOrg.Unnamed {
		t.Fatalf("trusted peer = %+v, want signed, named from the dn, not self", peerOrg)
	}
	unnamed := resolvePublisherOrganization(store, "mlab-ndt-statistics", placeholder, nil, identity)
	if unnamed.State != organizationStateSigned || !unnamed.Unnamed || unnamed.Name != unnamedPublisherName {
		t.Fatalf("placeholder profile = %+v, want signed but unnamed", unnamed)
	}
	if strings.Contains(strings.Join(unnamed.Evidence, " "), "<peer.ID") {
		t.Fatalf("placeholder leaked into evidence: %v", unnamed.Evidence)
	}
	wrong := publisherIdentity{epmBytes: func(string) []byte { return held[trusted] }}
	if got := resolvePublisherOrganization(store, "lane", "16UiuSOMEONEELSE", nil, wrong); got.State != organizationStateSourceTag || got.Name != "lane" {
		t.Fatalf("bytes advertising another peer named a lane: %+v", got)
	}
	bare := resolvePublisherOrganization(store, "account", local, nil, publisherIdentity{localPeerID: local})
	if bare.State != organizationStateSourceTag || !bare.Self {
		t.Fatalf("without held bytes the local lane = %+v, want the source-tag floor still marked self", bare)
	}
	if got := resolveProducerOrganization(store, "lane", trusted, nil); got.State != organizationStateSourceTag {
		t.Fatalf("without identity the directory-only path = %+v", got)
	}
}

func TestResolvedSourcesMarksLocalLanesSelf(t *testing.T) {
	store := newResolvedSourcesStore(t)
	const local = "16UiuLOCALNODE"
	tags := storage.SourceTags{ProviderID: "account", SourceName: "596a40d90204321e", BatchID: "b-1", ContentKeyID: "public", ProducerPeerID: local}
	record := sds.NewOMMBuilder().WithNoradCatID(50001).WithObjectName("SELF-01").WithEpoch("2026-05-12T00:00:00Z").Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", record, "source:self", nil, tags); err != nil {
		t.Fatalf("store: %v", err)
	}
	localEPM := buildSignedPublisherEPMProfile(t, local, "Digital Arsenal", "", "", "")
	h := &DataQueryHandler{store: store}
	h.SetPublisherIdentity(local, func(peerID string) []byte {
		if peerID == local {
			return localEPM
		}
		return nil
	})
	r := httptest.NewRequest("GET", "/api/v1/data/sources", nil)
	w := httptest.NewRecorder()
	h.handleResolvedSources(w, r)
	if w.Code != 200 {
		t.Fatalf("resolved sources -> %d: %s", w.Code, w.Body.String())
	}
	resp := decodeResolvedSourcesPage(t, w.Body.Bytes())
	for _, row := range resp.Sources {
		if row.ProducerPeerID != local {
			continue
		}
		if row.Organization == nil || !row.Organization.Self || row.Organization.Name != "Digital Arsenal" || row.Organization.State != organizationStateSigned {
			t.Fatalf("local lane = %+v, want the node's own verified name marked self", row.Organization)
		}
		return
	}
	t.Fatalf("local lane missing from %+v", resp.Sources)
}

// decodeResolvedSourcesPage reads the sources $QRP page back into rows.
func decodeResolvedSourcesPage(t *testing.T, body []byte) struct{ Sources []resolvedSourceRow } {
	t.Helper()
	frames, err := SplitFrames(body)
	if err != nil || len(frames) != 1 {
		t.Fatalf("sources answered %d frames (%v), want one $QRP page", len(frames), err)
	}
	q, err := ParseQRP(frames[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := map[string]int{}
	var col QRP.QRPColumn
	for i := 0; i < q.COLUMNSLength(); i++ {
		q.COLUMNS(&col, i)
		idx[string(col.NAME())] = i
	}
	var out struct{ Sources []resolvedSourceRow }
	var row QRP.QRPRow
	var v QRP.QRPValue
	for r := 0; r < q.ROWSLength(); r++ {
		q.ROWS(&row, r)
		text := func(name string) string { row.CELLS(&v, idx[name]); return string(v.TEXT_VALUE()) }
		num := func(name string) int64 { row.CELLS(&v, idx[name]); return v.INT_VALUE() }
		flag := func(name string) bool { row.CELLS(&v, idx[name]); return v.BOOL_VALUE() }
		org := &resolvedOrganization{Name: text("organization_name"), State: text("organization_state"),
			PeerID: text("organization_peer_id"), EPMCID: text("organization_epm_cid"),
			Self: flag("organization_self"), Unnamed: flag("organization_unnamed")}
		if ev := text("organization_evidence"); ev != "" {
			org.Evidence = strings.Split(ev, ", ")
		}
		out.Sources = append(out.Sources, resolvedSourceRow{Schema: text("schema"), ProviderID: text("provider_id"),
			SourceName: text("source_name"), BatchID: text("batch_id"), ProducerPeerID: text("producer_peer_id"),
			Count: num("count"), TotalBytes: num("total_bytes"), Organization: org})
	}
	return out
}
