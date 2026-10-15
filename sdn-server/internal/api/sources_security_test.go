package api

// Security audit regressions for the resolved-sources provenance ladder:
// SEC-013 (a bond needs a verified ChainProof) and SEC-037 (a domain proof
// is only a claim until DNS confirms it).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	standardsEPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	flatbuffers "github.com/google/flatbuffers/go"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/chainsig"
	"github.com/spacedatanetwork/sdn-server/internal/dnsproof"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

// staticTXT is a hermetic DNS: the records it holds, NXDOMAIN otherwise.
type staticTXT map[string][]string

func (s staticTXT) LookupTXT(_ context.Context, name string) ([]string, error) {
	if values, ok := s[name]; ok {
		return values, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func init() {
	// No test in this package may reach real DNS.
	publisherDomainDNS = dnsproof.NewVerifier(dnsproof.Options{}, staticTXT{})
}

func withPublisherDNS(t *testing.T, records staticTXT) *dnsproof.Verifier {
	t.Helper()
	previous := publisherDomainDNS
	verifier := dnsproof.NewVerifier(dnsproof.Options{}, records)
	publisherDomainDNS = verifier
	t.Cleanup(func() { publisherDomainDNS = previous })
	return verifier
}

func domainProofKey(t *testing.T, epmBytes []byte) string {
	t.Helper()
	record := standardsEPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
	proof := new(standardsEPM.DomainProof)
	if record.DOMAIN_PROOFSLength() != 1 || !record.DOMAIN_PROOFS(proof, 0) {
		t.Fatal("fixture carries no domain proof")
	}
	return string(proof.PUBLIC_KEY())
}

func storePublisherEPM(t *testing.T, store *storage.FlatSQLStore, peerID string, epmBytes []byte) {
	t.Helper()
	stored, err := json.Marshal(map[string]string{"epm_base64": base64.StdEncoding.EncodeToString(epmBytes)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind: "node", PeerID: peerID, EPMJSON: string(stored), Source: "test",
		EPMCID: "bafkrei" + strings.ToLower(hex.EncodeToString([]byte(peerID))[:20]),
	}); err != nil {
		t.Fatalf("UpsertDirectoryRecord: %v", err)
	}
}

// TestSEC037ForgedDomainProofIsOnlyAClaim is the audit repro: an attacker's
// EPM named "NOAA NESDIS" carries a self-signed DomainProof for noaa.gov.
// With no DNS record backing it, nothing may call it verified.
func TestSEC037ForgedDomainProofIsOnlyAClaim(t *testing.T) {
	store := newResolvedSourcesStore(t)
	upsertSignedPublisherFixture(t, store, "16UiuATTACKER", "NOAA NESDIS", "", "", "noaa.gov")
	withPublisherDNS(t, staticTXT{})

	got := resolveProducerOrganization(store, "noaa-feed", "16UiuATTACKER", nil)
	joined := strings.Join(got.Evidence, " ")
	if strings.Contains(joined, "verified domain-control proof") || strings.Contains(joined, "DNS confirms") {
		t.Fatalf("a self-signed domain proof reads as verified: %q", joined)
	}
	if !strings.Contains(joined, "claims the domain noaa.gov") || !strings.Contains(joined, "not DNS-verified") {
		t.Fatalf("the claim is not shown as an unverified claim: %q", joined)
	}
	if got.State != organizationStateSigned {
		t.Fatalf("state = %q, want signed", got.State)
	}
}

// TestDomainProofVerifiedWhenDNSNamesTheKey: the domain's TXT record names
// the proof key and the peer, so the claim is confirmed.
func TestDomainProofVerifiedWhenDNSNamesTheKey(t *testing.T) {
	store := newResolvedSourcesStore(t)
	const peerID = "16UiuOBSERVATORY"
	epmBytes := buildSignedPublisherEPM(t, peerID, "State University Radio Observatory", "", "", "observatory.example")
	storePublisherEPM(t, store, peerID, epmBytes)
	key := domainProofKey(t, epmBytes)

	verifier := withPublisherDNS(t, staticTXT{
		"_sdnkey.observatory.example": {dnsproof.TXTValue("ed25519", key, peerID)},
	})
	// Fill the cache the way the background refresh would.
	if status := verifier.Check(context.Background(), "observatory.example", "ed25519", key, peerID); status != dnsproof.StatusVerified {
		t.Fatalf("DNS check = %v, want verified", status)
	}
	got := resolveProducerOrganization(store, "observatory", peerID, nil)
	joined := strings.Join(got.Evidence, " ")
	if !strings.Contains(joined, "DNS confirms") || !strings.Contains(joined, "observatory.example") {
		t.Fatalf("DNS-confirmed proof not shown as verified: %q", joined)
	}

	// The same TXT record does not vouch for a different peer's claim.
	other := buildSignedPublisherEPM(t, "16UiuIMPOSTOR", "Impostor", "", "", "observatory.example")
	storePublisherEPM(t, store, "16UiuIMPOSTOR", other)
	got = resolveProducerOrganization(store, "impostor", "16UiuIMPOSTOR", nil)
	if joined := strings.Join(got.Evidence, " "); strings.Contains(joined, "DNS confirms") {
		t.Fatalf("a TXT record for another key and peer verified the impostor: %q", joined)
	}
}

// attestedPublisherEPM signs a publisher profile whose Ethereum ChainProof
// really verifies: the chain key signs an attestation naming the profile's
// signing key and the peer's identity key.
func attestedPublisherEPM(t *testing.T, name string) (peerID string, epmBytes []byte, ethAddress string) {
	t.Helper()
	identityPriv, _, err := libp2pcrypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(identityPriv)
	if err != nil {
		t.Fatal(err)
	}
	identityRaw, err := identityPriv.GetPublic().Raw()
	if err != nil {
		t.Fatal(err)
	}
	signingPub, signingPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ethKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	probe := []byte("probe")
	recovered, err := chainsig.VerifyPersonalSign(probe, ecdsa.SignCompact(ethKey, chainsig.PersonalSignHash(probe), true), chainsig.SignatureEncodingCompactVRS)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"version":             "1",
		"identity_pubkey_hex": hex.EncodeToString(identityRaw),
		"signing_pubkey_hex":  hex.EncodeToString(signingPub),
		"ethereum_address":    recovered.EthereumAddress,
		"issued_at":           time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	chainSig := ecdsa.SignCompact(ethKey, chainsig.PersonalSignHash(payload), true)
	ts := time.Now().Unix()

	build := func(signatureHex string) []byte {
		b := flatbuffers.NewBuilder(1024)
		nameOff := b.CreateString(name)
		chain, addr := b.CreateString("ethereum"), b.CreateString(recovered.EthereumAddress)
		pub := b.CreateString(hex.EncodeToString(ethKey.PubKey().SerializeCompressed()))
		path := b.CreateString("m/44'/60'/0'/0/0")
		sig, signed := b.CreateString(hex.EncodeToString(chainSig)), b.CreateString(hex.EncodeToString(payload))
		alg, enc := b.CreateString("secp256k1-compact-ethereum"), b.CreateString("compact")
		standardsEPM.ChainProofStart(b)
		standardsEPM.ChainProofAddCHAIN(b, chain)
		standardsEPM.ChainProofAddADDRESS(b, addr)
		standardsEPM.ChainProofAddPUBLIC_KEY(b, pub)
		standardsEPM.ChainProofAddKEY_PATH(b, path)
		standardsEPM.ChainProofAddSIGNATURE(b, sig)
		standardsEPM.ChainProofAddSIGNED_PAYLOAD(b, signed)
		standardsEPM.ChainProofAddALGORITHM(b, alg)
		standardsEPM.ChainProofAddENCODING(b, enc)
		proof := standardsEPM.ChainProofEnd(b)
		standardsEPM.EPMStartCHAIN_PROOFSVector(b, 1)
		b.PrependUOffsetT(proof)
		proofs := b.EndVector(1)

		keyOff, algOff := b.CreateString(hex.EncodeToString(signingPub)), b.CreateString("ed25519")
		standardsEPM.CryptoKeyStart(b)
		standardsEPM.CryptoKeyAddPUBLIC_KEY(b, keyOff)
		standardsEPM.CryptoKeyAddALGORITHM(b, algOff)
		standardsEPM.CryptoKeyAddKEY_TYPE(b, standardsEPM.KeyTypeSigning)
		key := standardsEPM.CryptoKeyEnd(b)
		standardsEPM.EPMStartKEYSVector(b, 1)
		b.PrependUOffsetT(key)
		keys := b.EndVector(1)

		p2p := b.CreateString("/p2p/" + pid.String())
		standardsEPM.EPMStartMULTIFORMAT_ADDRESSVector(b, 1)
		b.PrependUOffsetT(p2p)
		addrs := b.EndVector(1)
		var sigOff flatbuffers.UOffsetT
		if signatureHex != "" {
			sigOff = b.CreateString(signatureHex)
		}
		standardsEPM.EPMStart(b)
		standardsEPM.EPMAddLEGAL_NAME(b, nameOff)
		standardsEPM.EPMAddKEYS(b, keys)
		standardsEPM.EPMAddMULTIFORMAT_ADDRESS(b, addrs)
		standardsEPM.EPMAddCHAIN_PROOFS(b, proofs)
		standardsEPM.EPMAddENTITY_TYPE(b, standardsEPM.EntityTypeNode)
		standardsEPM.EPMAddSIGNATURE_TIMESTAMP(b, ts)
		if signatureHex != "" {
			standardsEPM.EPMAddSIGNATURE(b, sigOff)
		}
		standardsEPM.FinishSizePrefixedEPMBuffer(b, standardsEPM.EPMEnd(b))
		return append([]byte(nil), b.FinishedBytes()...)
	}
	preimage, err := epm.EPMSigningPayload(build(""))
	if err != nil {
		t.Fatal(err)
	}
	epmBytes = build(hex.EncodeToString(ed25519.Sign(signingPriv, preimage)))
	if err := epm.VerifyEPMSignature(epmBytes); err != nil {
		t.Fatalf("attested fixture does not verify: %v", err)
	}
	return pid.String(), epmBytes, recovered.EthereumAddress
}

// TestBondedRungNeedsAVerifiedChainProof: the audit's "bonded" rung only
// fires for an address whose ChainProof verifies; a profile naming a funded
// address it cannot prove stays "signed".
func TestBondedRungNeedsAVerifiedChainProof(t *testing.T) {
	store := newResolvedSourcesStore(t)

	peerID, epmBytes, ethAddress := attestedPublisherEPM(t, "Bonded Observatory")
	storePublisherEPM(t, store, peerID, epmBytes)
	funded := trust.NewEvaluator(trust.NewGraph(), trust.MemoryFundsProvider{}, trust.MemoryBondSource{
		ChainID: trust.ChainEthereum,
		Amounts: map[string]float64{ethAddress: 2500, "0xwhalewallet": 2500},
	})
	if got := resolveProducerOrganization(store, "bonded", peerID, funded); got.State != organizationStateBonded {
		t.Fatalf("verified proof with a positive bond: state %q, want bonded (%+v)", got.State, got)
	}

	// The audit repro: a self-signed profile whose ChainProof names a funded
	// wallet with no chain signature at all.
	upsertSignedPublisherFixture(t, store, "16UiuUNPROVEN", "Unproven Observatory", "ethereum", "0xwhalewallet", "")
	if got := resolveProducerOrganization(store, "unproven", "16UiuUNPROVEN", funded); got.State != organizationStateSigned {
		t.Fatalf("unverified chain proof on a funded address: state %q, want signed", got.State)
	}
}
