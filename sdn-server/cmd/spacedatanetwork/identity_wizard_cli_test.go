package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestIdentityWizardSetValuesWritesLocalEPMAndJSON(t *testing.T) {
	_, store, peerID, dataDir := newIdentityWizardTestStore(t)

	var out bytes.Buffer
	err := runIdentityWizardWithIO(
		strings.NewReader("y\n"),
		&out,
		identityWizardOptions{
			Sets: []string{
				"dn=CelesTrak Provider",
				"legal_name=CelesTrak",
				"email=ops@celestrak.test",
				"telephone=+1-555-0100",
				"alternate_names=celestrak.eth,provider.sol",
			},
			Format: "json",
		},
		store,
		identityWizardNodeIdentity{PeerID: peerID},
		dataDir,
	)
	if err != nil {
		t.Fatalf("runIdentityWizardWithIO failed: %v", err)
	}

	output := out.String()
	for _, forbidden := range []string{"mnemonic", "xpriv", "private_key"} {
		if strings.Contains(strings.ToLower(output), forbidden) {
			t.Fatalf("wizard output leaked private material marker %q: %s", forbidden, output)
		}
	}

	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("wizard json output is invalid JSON: %v\n%s", err, output)
	}
	if payload["dn"] != "CelesTrak Provider" || payload["legal_name"] != "CelesTrak" {
		t.Fatalf("wizard json payload = %#v", payload)
	}

	localEPM, err := store.LoadLocalEPM(peerID.String())
	if err != nil {
		t.Fatalf("LoadLocalEPM failed: %v", err)
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(localEPM) {
		t.Fatalf("local EPM has invalid FlatBuffer identifier: %x", localEPM[:min(len(localEPM), 16)])
	}

	nodes, err := directory.NewService(store).SearchNodes("CelesTrak", 10)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("SearchNodes returned %d records, want 1: %#v", len(nodes), nodes)
	}
	if nodes[0].PeerID != peerID.String() {
		t.Fatalf("directory peer_id = %q, want %q", nodes[0].PeerID, peerID.String())
	}
}

func TestIdentityWizardCSVAndFlatBufferOutputs(t *testing.T) {
	_, store, peerID, dataDir := newIdentityWizardTestStore(t)

	var csvOut bytes.Buffer
	if err := runIdentityWizardWithIO(
		strings.NewReader("y\n"),
		&csvOut,
		identityWizardOptions{
			Sets: []string{
				"dn=CelesTrak Provider",
				"legal_name=CelesTrak",
			},
			Format: "csv",
		},
		store,
		identityWizardNodeIdentity{PeerID: peerID},
		dataDir,
	); err != nil {
		t.Fatalf("runIdentityWizardWithIO csv failed: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(csvOut.String())).ReadAll()
	if err != nil {
		t.Fatalf("wizard csv output is invalid CSV: %v\n%s", err, csvOut.String())
	}
	if len(records) != 2 {
		t.Fatalf("wizard csv records len = %d, want 2: %#v", len(records), records)
	}
	if records[0][0] != "peer_id" || records[1][0] != peerID.String() {
		t.Fatalf("wizard csv peer_id column = %#v", records)
	}
	if records[0][1] != "dn" || records[1][1] != "CelesTrak Provider" {
		t.Fatalf("wizard csv dn column = %#v", records)
	}

	epmPath := filepath.Join(t.TempDir(), "epm.fbs")
	var flatOut bytes.Buffer
	if err := runIdentityWizardWithIO(
		strings.NewReader("y\n"),
		&flatOut,
		identityWizardOptions{
			Sets: []string{
				"dn=FlatBuffer Wizard",
				"legal_name=FlatBuffer Wizard LLC",
			},
			Format:     "flatbuffer",
			OutputPath: epmPath,
		},
		store,
		identityWizardNodeIdentity{PeerID: peerID},
		dataDir,
	); err != nil {
		t.Fatalf("runIdentityWizardWithIO flatbuffer failed: %v", err)
	}
	raw, err := os.ReadFile(epmPath)
	if err != nil {
		t.Fatalf("read flatbuffer output %s: %v", epmPath, err)
	}
	if len(raw) == 0 {
		t.Fatal("flatbuffer output file is empty")
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(raw) {
		t.Fatalf("flatbuffer output has invalid EPM identifier: %x", raw[:min(len(raw), 16)])
	}
	if flatOut.Len() != 0 {
		t.Fatalf("flatbuffer file output should not also write stdout, got %q", flatOut.String())
	}
}

func TestIdentityWizardPreservesIdentityBackedPublicKeys(t *testing.T) {
	_, store, _, dataDir := newIdentityWizardTestStore(t)
	identity, err := testProviderDerivedIdentity()
	if err != nil {
		t.Fatalf("testProviderDerivedIdentity failed: %v", err)
	}

	const xpub = "xpub-provider"
	seedService := epm.NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, xpub, dataDir)
	seedService.SetProfileStore(store)
	if err := seedService.Init(); err != nil {
		t.Fatalf("seed Init failed: %v", err)
	}
	if err := seedService.UpdateProfile(&epm.Profile{
		DN:        "Identity Backed Provider",
		LegalName: "Identity Backed LLC",
	}); err != nil {
		t.Fatalf("seed UpdateProfile failed: %v", err)
	}

	var out bytes.Buffer
	if err := runIdentityWizardWithIO(
		strings.NewReader("y\n"),
		&out,
		identityWizardOptions{
			Sets:   []string{"dn=Updated Identity Provider"},
			Format: "json",
		},
		store,
		identityWizardNodeIdentity{
			Identity: identity,
			PeerID:   identity.PeerID,
			XPub:     xpub,
		},
		dataDir,
	); err != nil {
		t.Fatalf("runIdentityWizardWithIO failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("wizard json output is invalid JSON: %v\n%s", err, out.String())
	}
	assertIdentityWizardJSONHasPublicIdentity(t, payload, xpub)

	records, err := store.QueryDirectory(storage.DirectoryQuery{
		Kind:   directory.KindNode,
		PeerID: identity.PeerID.String(),
		Limit:  1,
	})
	if err != nil {
		t.Fatalf("QueryDirectory failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("directory records len = %d, want 1: %#v", len(records), records)
	}
	var directoryJSON map[string]any
	if err := json.Unmarshal([]byte(records[0].EPMJSON), &directoryJSON); err != nil {
		t.Fatalf("directory EPMJSON is invalid JSON: %v\n%s", err, records[0].EPMJSON)
	}
	assertIdentityWizardJSONHasPublicIdentity(t, directoryJSON, xpub)
}

func assertIdentityWizardJSONHasPublicIdentity(t *testing.T, payload map[string]any, xpub string) {
	t.Helper()

	keys, ok := payload["keys"].([]any)
	if !ok || len(keys) == 0 {
		t.Fatalf("payload keys missing or empty: %#v", payload["keys"])
	}
	for _, rawKey := range keys {
		key, ok := rawKey.(map[string]any)
		if !ok {
			t.Fatalf("payload key has type %T: %#v", rawKey, rawKey)
		}
		if key["xpub"] == xpub && key["public_key"] != "" && key["key_type"] == "signing" {
			return
		}
	}
	t.Fatalf("payload keys do not include signing key with xpub %q: %#v", xpub, keys)
}

func newIdentityWizardTestStore(t *testing.T) (string, *storage.FlatSQLStore, peer.ID, string) {
	t.Helper()

	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(tmpDir, "data")
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("sds.NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		t.Fatalf("storage.NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	peerID, err := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("peer.Decode failed: %v", err)
	}
	return cfgPath, store, peerID, cfg.Storage.Path
}
