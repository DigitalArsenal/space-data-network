package epm

// SEC-013 regression tests: an EPM's ChainProofs count only when the chain
// signature verifies, the address derives from the proof key, and the
// attestation is bound to this record's signing key and this peer.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/mr-tron/base58"

	"github.com/spacedatanetwork/sdn-server/internal/chainsig"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// A public exchange cold wallet: the address the audit repro claimed.
const whaleBitcoinAddress = "bc1qvictimwhaleaddressnotcontrolledbyattacker000"

// attestedTestIdentity is testDerivedIdentity with chain addresses that really
// derive from its chain keys, so the node's own attestation builder emits
// proofs that verify.
func attestedTestIdentity(t *testing.T, seed byte) *wasm.DerivedIdentity {
	t.Helper()
	identityPriv, _, err := crypto.GenerateSecp256k1Key(bytes.NewReader(bytes.Repeat([]byte{seed}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	signingPriv, signingPub, err := crypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{seed + 1}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	peerID, err := peer.IDFromPublicKey(identityPriv.GetPublic())
	if err != nil {
		t.Fatal(err)
	}
	btcKey := bytes.Repeat([]byte{seed + 2}, 32)
	ethKey := bytes.Repeat([]byte{seed + 3}, 32)
	solSeed := bytes.Repeat([]byte{seed + 4}, 32)

	btcAddress, err := bitcoinAddressFromCompressedPublicKey(secp256k1.PrivKeyFromBytes(btcKey).PubKey().SerializeCompressed())
	if err != nil {
		t.Fatal(err)
	}
	probe := []byte("address probe")
	recovered, err := chainsig.VerifyPersonalSign(probe, ecdsa.SignCompact(secp256k1.PrivKeyFromBytes(ethKey), chainsig.PersonalSignHash(probe), true), chainsig.SignatureEncodingCompactVRS)
	if err != nil {
		t.Fatal(err)
	}
	solAddress := base58.Encode(ed25519.NewKeyFromSeed(solSeed).Public().(ed25519.PublicKey))

	return &wasm.DerivedIdentity{
		IdentityPrivKey:    identityPriv,
		IdentityPubKey:     identityPriv.GetPublic(),
		SigningPrivKey:     signingPriv,
		SigningPubKey:      signingPub,
		EncryptionKey:      bytes.Repeat([]byte{0x33}, 32),
		EncryptionPub:      bytes.Repeat([]byte{0x44}, 32),
		PeerID:             peerID,
		IdentityKeyPath:    "m/44'/0'/0'",
		SigningKeyPath:     "m/44'/0'/0'/0'/0'",
		EncryptionKeyPath:  "m/44'/0'/0'/1'/0'",
		BitcoinKeyPath:     "m/44'/0'/0'/0/0",
		BitcoinPrivateKey:  btcKey,
		EthereumKeyPath:    "m/44'/60'/0'/0/0",
		EthereumPrivateKey: ethKey,
		SolanaKeyPath:      "m/44'/501'/0'/0'",
		SolanaPrivateKey:   solSeed,
		Addresses: &wasm.CoinAddresses{
			Bitcoin:  &wasm.CoinAddress{Address: btcAddress, Path: "m/44'/0'/0'/0/0"},
			Ethereum: &wasm.CoinAddress{Address: recovered.EthereumAddress, Path: "m/44'/60'/0'/0/0"},
			Solana:   &wasm.CoinAddress{Address: solAddress, Path: "m/44'/501'/0'/0'"},
		},
	}
}

func attestedTestService(t *testing.T, seed byte) (*Service, *wasm.DerivedIdentity) {
	t.Helper()
	identity := attestedTestIdentity(t, seed)
	svc := NewService(identity, nil, identity.PeerID, validTestXPub, t.TempDir())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if att := svc.GetIdentityAttestation(); att == nil || len(att.ChainProofs) != 3 {
		t.Fatalf("test service built no identity attestation")
	}
	return svc, identity
}

func signingKeyOf(t *testing.T, identity *wasm.DerivedIdentity) ed25519.PrivateKey {
	t.Helper()
	raw, err := identity.SigningPrivKey.Raw()
	if err != nil {
		t.Fatal(err)
	}
	return ed25519.PrivateKey(raw)
}

// buildChainProofTestEPM builds and signs a minimal node EPM: one Ed25519
// signing key, the peer's /ipns address, and the given chain proofs.
func buildChainProofTestEPM(t *testing.T, signer ed25519.PrivateKey, peerID string, proofs []IdentityAttestationChainProof) []byte {
	t.Helper()
	build := func(signatureHex string, ts int64) []byte {
		b := flatbuffers.NewBuilder(1024)
		proofOffsets := make([]flatbuffers.UOffsetT, len(proofs))
		for i, p := range proofs {
			chain, addr, pub := b.CreateString(p.Chain), b.CreateString(p.Address), b.CreateString(p.PublicKeyHex)
			path, sig, payload := b.CreateString(p.KeyPath), b.CreateString(p.Signature), b.CreateString(p.SignedPayloadHex)
			alg, enc := b.CreateString(p.SignatureAlgorithm), b.CreateString(p.SignatureEncoding)
			EPM.ChainProofStart(b)
			EPM.ChainProofAddCHAIN(b, chain)
			EPM.ChainProofAddADDRESS(b, addr)
			EPM.ChainProofAddPUBLIC_KEY(b, pub)
			EPM.ChainProofAddKEY_PATH(b, path)
			EPM.ChainProofAddSIGNATURE(b, sig)
			EPM.ChainProofAddSIGNED_PAYLOAD(b, payload)
			EPM.ChainProofAddALGORITHM(b, alg)
			EPM.ChainProofAddENCODING(b, enc)
			proofOffsets[i] = EPM.ChainProofEnd(b)
		}
		EPM.EPMStartCHAIN_PROOFSVector(b, len(proofOffsets))
		for i := len(proofOffsets) - 1; i >= 0; i-- {
			b.PrependUOffsetT(proofOffsets[i])
		}
		proofsVec := b.EndVector(len(proofOffsets))

		pubOff := b.CreateString(hex.EncodeToString(signer.Public().(ed25519.PublicKey)))
		typeOff := b.CreateString("ed25519")
		EPM.CryptoKeyStart(b)
		EPM.CryptoKeyAddPUBLIC_KEY(b, pubOff)
		EPM.CryptoKeyAddADDRESS_TYPE(b, typeOff)
		EPM.CryptoKeyAddKEY_TYPE(b, EPM.KeyTypeSigning)
		key := EPM.CryptoKeyEnd(b)
		EPM.EPMStartKEYSVector(b, 1)
		b.PrependUOffsetT(key)
		keys := b.EndVector(1)

		addr := b.CreateString("/ipns/" + peerID)
		EPM.EPMStartMULTIFORMAT_ADDRESSVector(b, 1)
		b.PrependUOffsetT(addr)
		addrs := b.EndVector(1)

		dn := b.CreateString("Chain Proof Test Node")
		var sigOff flatbuffers.UOffsetT
		if signatureHex != "" {
			sigOff = b.CreateString(signatureHex)
		}
		EPM.EPMStart(b)
		EPM.EPMAddDN(b, dn)
		EPM.EPMAddKEYS(b, keys)
		EPM.EPMAddMULTIFORMAT_ADDRESS(b, addrs)
		EPM.EPMAddCHAIN_PROOFS(b, proofsVec)
		EPM.EPMAddENTITY_TYPE(b, EPM.EntityTypeNode)
		EPM.EPMAddSIGNATURE_TIMESTAMP(b, ts)
		if signatureHex != "" {
			EPM.EPMAddSIGNATURE(b, sigOff)
		}
		b.FinishSizePrefixedWithFileIdentifier(EPM.EPMEnd(b), []byte("$EPM"))
		return b.FinishedBytes()
	}
	ts := time.Now().Unix()
	payload, err := EPMSigningPayload(build("", ts))
	if err != nil {
		t.Fatal(err)
	}
	signed := build(hex.EncodeToString(ed25519.Sign(signer, payload)), ts)
	if err := VerifyEPMSignature(signed); err != nil {
		t.Fatalf("test EPM does not verify: %v", err)
	}
	return signed
}

func TestVerifiedEPMChainProofsAcceptsTheNodesOwnAttestation(t *testing.T) {
	svc, identity := attestedTestService(t, 0x51)
	verified, rejected, err := VerifiedEPMChainProofs(svc.GetNodeEPM(), identity.PeerID.String(), time.Now())
	if err != nil {
		t.Fatalf("VerifiedEPMChainProofs: %v", err)
	}
	if len(rejected) != 0 || len(verified) != 3 {
		t.Fatalf("verified=%d rejected=%v, want 3 verified and none rejected", len(verified), rejected)
	}
	want := map[string]string{
		"bitcoin":  identity.Addresses.Bitcoin.Address,
		"ethereum": identity.Addresses.Ethereum.Address,
		"solana":   identity.Addresses.Solana.Address,
	}
	for _, proof := range verified {
		if want[proof.Chain] != proof.Address {
			t.Fatalf("%s address = %q, want %q", proof.Chain, proof.Address, want[proof.Chain])
		}
	}
}

func TestVerifiedEPMChainProofsRefusesJunkSignature(t *testing.T) {
	// The audit repro: a self-signed EPM naming someone else's wallet with
	// SIGNATURE "00" and SIGNED_PAYLOAD "00".
	_, attacker := attestedTestService(t, 0x61)
	epmBytes := buildChainProofTestEPM(t, signingKeyOf(t, attacker), attacker.PeerID.String(), []IdentityAttestationChainProof{{
		Chain: "bitcoin", Address: whaleBitcoinAddress, KeyPath: "m/44'/0'/0'/0/0",
		PublicKeyHex: "02" + strings.Repeat("11", 32), Signature: "00", SignedPayloadHex: "00",
		SignatureAlgorithm: identityAttestationAlgorithmBitcoin, SignatureEncoding: identityAttestationBitcoinSigEncoding,
	}})
	verified, rejected, err := VerifiedEPMChainProofs(epmBytes, attacker.PeerID.String(), time.Now())
	if err != nil {
		t.Fatalf("the EPM itself verifies, got %v", err)
	}
	if len(verified) != 0 || len(rejected) != 1 {
		t.Fatalf("verified=%v rejected=%v, want the junk proof refused", verified, rejected)
	}
	info := map[string]any{}
	ApplyVerifiedChainProofs(info, epmBytes, attacker.PeerID.String(), time.Now())
	if _, ok := info["bitcoin_address"]; ok {
		t.Fatalf("projection kept an unverified address: %v", info)
	}
	if _, ok := info["chain_proofs"]; ok {
		t.Fatalf("projection kept an unverified chain proof: %v", info)
	}
}

func TestVerifiedEPMChainProofsRefusesAddressNotDerivedFromKey(t *testing.T) {
	svc, identity := attestedTestService(t, 0x71)
	proofs := append([]IdentityAttestationChainProof(nil), svc.GetIdentityAttestation().ChainProofs...)
	for i := range proofs {
		if proofs[i].Chain == "bitcoin" {
			proofs[i].Address = whaleBitcoinAddress
		}
	}
	epmBytes := buildChainProofTestEPM(t, signingKeyOf(t, identity), identity.PeerID.String(), proofs)
	verified, _, err := VerifiedEPMChainProofs(epmBytes, identity.PeerID.String(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, proof := range verified {
		if proof.Chain == "bitcoin" {
			t.Fatalf("a bitcoin address that does not derive from the proof key verified: %+v", proof)
		}
	}
	if len(verified) != 2 {
		t.Fatalf("verified %d proofs, want the untouched ethereum and solana proofs only", len(verified))
	}
}

func TestVerifiedEPMChainProofsRefusesProofReplayedFromAnotherRecord(t *testing.T) {
	victimSvc, _ := attestedTestService(t, 0x81)
	_, attacker := attestedTestService(t, 0x91)
	// Every victim proof is genuine, but it names the victim's signing and
	// identity keys, not the attacker record that now carries it.
	epmBytes := buildChainProofTestEPM(t, signingKeyOf(t, attacker), attacker.PeerID.String(), victimSvc.GetIdentityAttestation().ChainProofs)
	verified, rejected, err := VerifiedEPMChainProofs(epmBytes, attacker.PeerID.String(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 0 || len(rejected) != 3 {
		t.Fatalf("verified=%v rejected=%v, want every replayed proof refused", verified, rejected)
	}
}

func TestVerifiedEPMChainProofsRefusesWrongPeer(t *testing.T) {
	svc, _ := attestedTestService(t, 0xa1)
	_, other := attestedTestService(t, 0xb1)
	verified, _, err := VerifiedEPMChainProofs(svc.GetNodeEPM(), other.PeerID.String(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 0 {
		t.Fatalf("proofs bound to another peer verified: %v", verified)
	}
}

func TestVerifiedEPMChainProofsRefusesFutureIssuedAt(t *testing.T) {
	svc, identity := attestedTestService(t, 0xc1)
	att := svc.GetIdentityAttestation()
	future := identityAttestationPayload{
		Version: att.Version, XPub: att.XPub, IdentityPubKeyHex: att.IdentityPubKeyHex,
		IdentityKeyPath: att.IdentityKeyPath, SigningPubKeyHex: att.SigningPubKeyHex,
		SigningKeyPath: att.SigningKeyPath, BitcoinAddress: att.BitcoinAddress,
		BitcoinKeyPath: att.BitcoinKeyPath, EthereumAddress: att.EthereumAddress,
		EthereumKeyPath: att.EthereumKeyPath, SolanaAddress: att.SolanaAddress,
		SolanaKeyPath: att.SolanaKeyPath, IssuedAt: time.Now().Add(time.Hour).Unix(),
	}
	payload, err := json.Marshal(future)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := svc.buildSolanaAttestationProof(payload, future)
	if err != nil {
		t.Fatal(err)
	}
	epmBytes := buildChainProofTestEPM(t, signingKeyOf(t, identity), identity.PeerID.String(), []IdentityAttestationChainProof{proof})
	verified, rejected, err := VerifiedEPMChainProofs(epmBytes, identity.PeerID.String(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 0 || len(rejected) != 1 || !strings.Contains(rejected[0].Error(), "future") {
		t.Fatalf("verified=%v rejected=%v, want the future-dated proof refused", verified, rejected)
	}
}

func TestVerifiedEPMChainProofsRefusesUnsignedRecord(t *testing.T) {
	svc, identity := attestedTestService(t, 0xd1)
	epmBytes := append([]byte(nil), svc.GetNodeEPM()...)
	// Change one character of the signed Solana address: the record
	// signature no longer verifies, so no proof inside it may be honoured.
	idx := bytes.Index(epmBytes, []byte(identity.Addresses.Solana.Address))
	if idx < 0 {
		t.Fatal("service EPM does not carry its solana address")
	}
	epmBytes[idx+5] ^= 0x01
	if _, _, err := VerifiedEPMChainProofs(epmBytes, identity.PeerID.String(), time.Now()); err == nil {
		t.Fatal("a record whose own signature fails still yielded chain proofs")
	}
}
