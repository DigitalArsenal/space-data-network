package node

import (
	"encoding/hex"
	"os"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestComputeModuleDeliveryDiscoveryCID(t *testing.T) {
	t.Parallel()

	pubKey, err := hex.DecodeString("021111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("hex.DecodeString failed: %v", err)
	}

	discoveryCID, err := computeModuleDeliveryDiscoveryCID(pubKey)
	if err != nil {
		t.Fatalf("computeModuleDeliveryDiscoveryCID failed: %v", err)
	}

	const expected = "bafkreicfbpehrnn2ynqbs7tf7cu4rj57tsw222kkna33ytyfaz6v37oniu"
	if got := discoveryCID.String(); got != expected {
		t.Fatalf("discovery CID = %q, want %q", got, expected)
	}
}

func TestComputeModuleDeliveryDiscoveryCIDRejectsNonCompressedKey(t *testing.T) {
	t.Parallel()

	if _, err := computeModuleDeliveryDiscoveryCID(make([]byte, 32)); err == nil {
		t.Fatal("expected error for non-compressed provider key")
	}
}

func TestModuleDeliveryDiscoveryTargetsUseProviderIdentityOnly(t *testing.T) {
	t.Parallel()

	providerPubKey, err := hex.DecodeString("021111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("hex.DecodeString failed: %v", err)
	}
	providerDiscoveryCID, err := computeModuleDeliveryDiscoveryCID(providerPubKey)
	if err != nil {
		t.Fatalf("computeModuleDeliveryDiscoveryCID failed: %v", err)
	}

	targets := moduleDeliveryDiscoveryTargets(providerDiscoveryCID)
	if len(targets) != 1 {
		t.Fatalf("discovery target count = %d, want 1", len(targets))
	}
	if got := targets[0]; got != providerDiscoveryCID {
		t.Fatalf("discovery target = %s, want %s", got, providerDiscoveryCID)
	}
	for _, target := range targets {
		if target == cid.Undef {
			t.Fatal("discovery target must be defined")
		}
	}
}

func TestSDNAdvertisementDiscoveryTargetsUseSupportedFlagWindow(t *testing.T) {
	t.Parallel()

	announce, discover, err := sdnAdvertisementDiscoveryTargets(
		"spacedatanetwork/1.2.0",
		[]string{
			"spacedatanetwork/1.2.0",
			"spacedatanetwork/1.1.0",
			"spacedatanetwork/1.0.0",
			"spacedatanetwork/1.2.0",
		},
	)
	if err != nil {
		t.Fatalf("sdnAdvertisementDiscoveryTargets failed: %v", err)
	}

	if announce.Flag != "spacedatanetwork/1.2.0" {
		t.Fatalf("announce flag = %q, want current flag", announce.Flag)
	}
	if announce.CID == cid.Undef {
		t.Fatal("announce CID must be defined")
	}

	if len(discover) != 3 {
		t.Fatalf("discover target count = %d, want 3 unique flags", len(discover))
	}
	if discover[0].Flag != "spacedatanetwork/1.2.0" {
		t.Fatalf("discover[0] flag = %q, want current flag first", discover[0].Flag)
	}
	if discover[1].Flag != "spacedatanetwork/1.1.0" {
		t.Fatalf("discover[1] flag = %q, want previous supported flag", discover[1].Flag)
	}
	if discover[2].Flag != "spacedatanetwork/1.0.0" {
		t.Fatalf("discover[2] flag = %q, want oldest supported flag", discover[2].Flag)
	}
	for _, target := range discover {
		if target.CID == cid.Undef {
			t.Fatalf("discover target %q must have a defined CID", target.Flag)
		}
	}
}

func TestRecordCurrentSDNAdvertisementDiscoveryUsesAnnouncedFlag(t *testing.T) {
	n := &Node{
		sdnAdvertisementTarget: sdnAdvertisementDiscoveryTarget{Flag: "spacedatanetwork/1.2.3"},
	}

	pid, err := peer.Decode("12D3KooWJQvxYjnF8UARVq8hdD2WmT9N4xJm9kMumZ5qX6Ch12yv")
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}

	n.recordCurrentSDNAdvertisementDiscovery(pid)

	flagsByPeer := n.SDNAdvertisementFlagsByPeer()
	flags := flagsByPeer[pid.String()]
	if len(flags) != 1 || flags[0] != "spacedatanetwork/1.2.3" {
		t.Fatalf("recorded advertisement flags = %v, want [spacedatanetwork/1.2.3]", flags)
	}
}

func TestIndexKnownDiscoveredNodeEPMStoresDirectoryRecord(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "node-directory-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	dirSvc := directory.NewService(store)
	registry := peers.NewRegistry(false, nil)

	peerID, err := peer.Decode("12D3KooWJQvxYjnF8UARVq8hdD2WmT9N4xJm9kMumZ5qX6Ch12yv")
	if err != nil {
		t.Fatalf("peer.Decode failed: %v", err)
	}

	epmBytes := buildDiscoveredEPMFixture(t, "Discovery Node", "Discovery Node LLC", "bc1qdiscoverwallet0000000000000000000000000")

	if err := registry.AddPeer(&peers.TrustedPeer{
		ID:      peerID,
		EPMData: epmBytes,
	}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	n := &Node{
		directorySvc: dirSvc,
		peerRegistry: registry,
	}

	n.indexFetchedDiscoveredNodeEPM(peerID, "dht-discovery", epmBytes)

	nodes, err := dirSvc.SearchNodes("Discovery Node", 10)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("SearchNodes returned %d records, want 1", len(nodes))
	}
	got := nodes[0]
	if got.PeerID != peerID.String() {
		t.Fatalf("PeerID = %q, want %q", got.PeerID, peerID.String())
	}
	if got.DN != "Discovery Node" {
		t.Fatalf("DN = %q, want %q", got.DN, "Discovery Node")
	}
	if got.LegalName != "Discovery Node LLC" {
		t.Fatalf("LegalName = %q, want %q", got.LegalName, "Discovery Node LLC")
	}
	if got.Source != "dht-discovery" {
		t.Fatalf("Source = %q, want %q", got.Source, "dht-discovery")
	}
	if got.BitcoinAddress != "bc1qdiscoverwallet0000000000000000000000000" {
		t.Fatalf("BitcoinAddress = %q, want %q", got.BitcoinAddress, "bc1qdiscoverwallet0000000000000000000000000")
	}
	nodesByAddress, err := dirSvc.SearchNodes("bc1qdiscoverwallet0000000000000000000000000", 10)
	if err != nil {
		t.Fatalf("SearchNodes by bitcoin address failed: %v", err)
	}
	if len(nodesByAddress) != 1 {
		t.Fatalf("SearchNodes by bitcoin address returned %d records, want 1", len(nodesByAddress))
	}
}

func TestIndexFetchedDiscoveredNodeEPMSkipsStaleRegistryDataWhenFetchReturnsNoContent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "node-directory-stale-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	dirSvc := directory.NewService(store)
	registry := peers.NewRegistry(false, nil)

	peerID, err := peer.Decode("12D3KooWJQvxYjnF8UARVq8hdD2WmT9N4xJm9kMumZ5qX6Ch12yv")
	if err != nil {
		t.Fatalf("peer.Decode failed: %v", err)
	}

	staleEPM := buildDiscoveredEPMFixture(t, "Stale Node", "Stale Node LLC", "bc1qstalewallet00000000000000000000000000")
	if err := registry.AddPeer(&peers.TrustedPeer{
		ID:      peerID,
		EPMData: staleEPM,
	}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	n := &Node{
		directorySvc: dirSvc,
		peerRegistry: registry,
	}

	n.indexFetchedDiscoveredNodeEPM(peerID, "dht-discovery", nil)

	nodes, err := dirSvc.SearchNodes("Stale Node", 10)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("SearchNodes returned %d records, want 0 when fetch returned no content", len(nodes))
	}
}

func buildDiscoveredEPMFixture(t *testing.T, dn, legalName, bitcoinAddress string) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(256)

	dnOffset := builder.CreateString(dn)
	legalNameOffset := builder.CreateString(legalName)
	chainOffset := builder.CreateString("bitcoin")
	addressOffset := builder.CreateString(bitcoinAddress)
	publicKeyOffset := builder.CreateString("021111111111111111111111111111111111111111111111111111111111111111")
	keyPathOffset := builder.CreateString("m/44'/0'/0'/0/0")
	signatureOffset := builder.CreateString("00")
	payloadOffset := builder.CreateString("00")
	algorithmOffset := builder.CreateString("secp256k1-compact-bitcoin")
	encodingOffset := builder.CreateString("compact")

	EPM.ChainProofStart(builder)
	EPM.ChainProofAddCHAIN(builder, chainOffset)
	EPM.ChainProofAddADDRESS(builder, addressOffset)
	EPM.ChainProofAddPUBLIC_KEY(builder, publicKeyOffset)
	EPM.ChainProofAddKEY_PATH(builder, keyPathOffset)
	EPM.ChainProofAddSIGNATURE(builder, signatureOffset)
	EPM.ChainProofAddSIGNED_PAYLOAD(builder, payloadOffset)
	EPM.ChainProofAddALGORITHM(builder, algorithmOffset)
	EPM.ChainProofAddENCODING(builder, encodingOffset)
	chainProofOffset := EPM.ChainProofEnd(builder)

	EPM.EPMStartCHAIN_PROOFSVector(builder, 1)
	builder.PrependUOffsetT(chainProofOffset)
	chainProofsOffset := builder.EndVector(1)

	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddLEGAL_NAME(builder, legalNameOffset)
	EPM.EPMAddCHAIN_PROOFS(builder, chainProofsOffset)
	epmOffset := EPM.EPMEnd(builder)
	EPM.FinishSizePrefixedEPMBuffer(builder, epmOffset)

	return append([]byte(nil), builder.FinishedBytes()...)
}
