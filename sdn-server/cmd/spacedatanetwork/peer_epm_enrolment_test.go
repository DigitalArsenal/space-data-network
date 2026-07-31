package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/peer"

	sdnepm "github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

const enrolmentTestPeerID = "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"

// buildEnrolmentEPM assembles a size-prefixed $EPM advertising peerID via a
// /p2p/ multiformat address, signed (or not) with the given secp256k1 hex sig.
func buildEnrolmentEPM(pubHex, sigHex, peerID string, ts int64) []byte {
	b := flatbuffers.NewBuilder(512)
	pub := b.CreateString(pubHex)
	at := b.CreateString("secp256k1")
	EPM.CryptoKeyStart(b)
	EPM.CryptoKeyAddPUBLIC_KEY(b, pub)
	EPM.CryptoKeyAddADDRESS_TYPE(b, at)
	EPM.CryptoKeyAddKEY_TYPE(b, EPM.KeyTypeSigning)
	key := EPM.CryptoKeyEnd(b)

	EPM.EPMStartKEYSVector(b, 1)
	b.PrependUOffsetT(key)
	keys := b.EndVector(1)

	addrOff := b.CreateString("/ip4/10.0.0.1/tcp/4001/p2p/" + peerID)
	EPM.EPMStartMULTIFORMAT_ADDRESSVector(b, 1)
	b.PrependUOffsetT(addrOff)
	addrs := b.EndVector(1)

	var sig flatbuffers.UOffsetT
	if sigHex != "" {
		sig = b.CreateString(sigHex)
	}
	EPM.EPMStart(b)
	EPM.EPMAddKEYS(b, keys)
	EPM.EPMAddMULTIFORMAT_ADDRESS(b, addrs)
	EPM.EPMAddENTITY_TYPE(b, EPM.EntityTypeUser)
	EPM.EPMAddSIGNATURE_TIMESTAMP(b, ts)
	if sigHex != "" {
		EPM.EPMAddSIGNATURE(b, sig)
	}
	epmOff := EPM.EPMEnd(b)
	b.FinishSizePrefixedWithFileIdentifier(epmOff, []byte("$EPM"))
	return b.FinishedBytes()
}

func signedEnrolmentEPM(t *testing.T, peerID string) []byte {
	t.Helper()
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pubHex := hex.EncodeToString(priv.PubKey().SerializeCompressed())
	const ts = int64(1785500000)
	unsigned := buildEnrolmentEPM(pubHex, "", peerID, ts)
	payload, err := sdnepm.EPMSigningPayload(unsigned)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	digest := sha256.Sum256(payload)
	sigHex := hex.EncodeToString(ecdsa.Sign(priv, digest[:]).Serialize())
	return buildEnrolmentEPM(pubHex, sigHex, peerID, ts)
}

// OWNER DIRECTIVE 2026-07-31 ("on instantiation, once the keys are
// generated, you have all the info you need to get this"): operator-enrolled
// signed EPM files load into the registry at boot, so a fleet peer's full
// crypto identity is held even while the peer is offline.
func TestLoadEnrolledPeerEPMs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "retriever.epm"), signedEnrolmentEPM(t, enrolmentTestPeerID), 0o600); err != nil {
		t.Fatal(err)
	}
	// Unsigned record must be refused.
	if err := os.WriteFile(filepath.Join(dir, "unsigned.epm"), buildEnrolmentEPM("deadbeef", "", enrolmentTestPeerID, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	// Non-EPM noise must be skipped.
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("not an epm"), 0o600); err != nil {
		t.Fatal(err)
	}

	registry := peers.NewRegistry(false, nil)
	if got := loadEnrolledPeerEPMs(dir, registry); got != 1 {
		t.Fatalf("loaded = %d, want 1 (signed only)", got)
	}

	pid, err := peer.Decode(enrolmentTestPeerID)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := registry.GetPeer(pid)
	if err != nil {
		t.Fatalf("enrolled peer missing from registry: %v", err)
	}
	if len(tp.EPMData) == 0 {
		t.Error("enrolled peer has no EPMData")
	}
	if tp.VCardData == "" {
		t.Error("enrolled peer has no VCardData")
	}
}

func TestLoadEnrolledPeerEPMsEmptyOrMissingDir(t *testing.T) {
	registry := peers.NewRegistry(false, nil)
	if got := loadEnrolledPeerEPMs("", registry); got != 0 {
		t.Errorf("empty dir string loaded %d", got)
	}
	if got := loadEnrolledPeerEPMs("/nonexistent/peer-epms", registry); got != 0 {
		t.Errorf("missing dir loaded %d", got)
	}
}
