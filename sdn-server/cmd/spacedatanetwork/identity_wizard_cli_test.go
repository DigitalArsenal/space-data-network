package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/csv"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/cobra"

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
				"dn=CatalogFixture Provider",
				"legal_name=CatalogFixture",
				"given_name=Celes",
				"family_name=Trak",
				"entity_type=node",
				"email=ops@fixture.test",
				"telephone=+1-555-0100",
				"alternate_names=catalogfixture.eth,provider.sol",
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
	if payload["dn"] != "CatalogFixture Provider" || payload["legal_name"] != "CatalogFixture" {
		t.Fatalf("wizard json payload = %#v", payload)
	}
	if payload["given_name"] != "Celes" || payload["family_name"] != "Trak" || payload["entity_type"] != "node" {
		t.Fatalf("wizard json name/entity fields = %#v", payload)
	}

	localEPM, err := store.LoadLocalEPM(peerID.String())
	if err != nil {
		t.Fatalf("LoadLocalEPM failed: %v", err)
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(localEPM) {
		t.Fatalf("local EPM has invalid FlatBuffer identifier: %x", localEPM[:min(len(localEPM), 16)])
	}

	nodes, err := directory.NewService(store).SearchNodes("CatalogFixture", 10)
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
				"dn=CatalogFixture Provider",
				"legal_name=CatalogFixture",
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
	if records[0][1] != "dn" || records[1][1] != "CatalogFixture Provider" {
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

func TestIdentityDirectoryCommandIsRegisteredWithParitySubcommands(t *testing.T) {
	requireCommand(t, []string{"identity", "directory"}, "directory")
	for _, command := range []struct {
		args []string
		use  string
	}{
		{[]string{"identity", "directory", "list"}, "list [query]"},
		{[]string{"identity", "directory", "show"}, "show <peer-id>"},
		{[]string{"identity", "directory", "import"}, "import --file <path>"},
		{[]string{"identity", "directory", "download"}, "download <peer-id>"},
	} {
		requireCommand(t, command.args, command.use)
	}
}

func TestIdentityDirectoryImportListShowAndDownload(t *testing.T) {
	cfgPath, store, _, _ := newIdentityWizardTestStore(t)
	withSyncCLITestConfig(t, cfgPath)
	// The directory CLI verbs below open the store themselves; release the
	// seeding handle first (the v2 store is single-writer).
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	importPath := filepath.Join(t.TempDir(), "directory-node.json")
	if err := os.WriteFile(importPath, []byte(`{
		"kind": "node",
		"epm_json": {
			"peer_id": "16Uiu2DirectoryNode",
			"dn": "Directory Provider",
			"legal_name": "Directory Provider LLC",
			"bitcoin_address": "bc1qdirectory"
		},
		"epm_cid": "bafy-directory-node"
	}`), 0o600); err != nil {
		t.Fatalf("write import file: %v", err)
	}

	var importOut bytes.Buffer
	if err := runIdentityDirectoryImport(&importOut, identityDirectoryOptions{
		File:   importPath,
		Kind:   "node",
		Format: "json",
		Source: "test-import",
		Limit:  100,
	}); err != nil {
		t.Fatalf("runIdentityDirectoryImport failed: %v", err)
	}
	var imported searchResult
	if err := json.Unmarshal(importOut.Bytes(), &imported); err != nil {
		t.Fatalf("decode import JSON: %v\n%s", err, importOut.String())
	}
	if imported.Count != 1 || imported.Results[0]["peer_id"] != "16Uiu2DirectoryNode" {
		t.Fatalf("import result = %#v", imported)
	}

	var listOut bytes.Buffer
	if err := runIdentityDirectoryList(&listOut, identityDirectoryOptions{
		Kind:   "node",
		Format: "table",
		Limit:  100,
	}, "Provider"); err != nil {
		t.Fatalf("runIdentityDirectoryList failed: %v", err)
	}
	if output := listOut.String(); !strings.Contains(output, "Directory Provider") || !strings.Contains(output, "16Uiu2DirectoryNode") {
		t.Fatalf("directory list output missing imported node:\n%s", output)
	}

	var showOut bytes.Buffer
	if err := runIdentityDirectoryShow(&showOut, identityDirectoryOptions{
		Kind:   "node",
		Format: "csv",
		Limit:  100,
	}, "16Uiu2DirectoryNode"); err != nil {
		t.Fatalf("runIdentityDirectoryShow failed: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(showOut.String())).ReadAll()
	if err != nil {
		t.Fatalf("decode show CSV: %v\n%s", err, showOut.String())
	}
	if len(records) != 2 || records[0][1] != "peer_id" || records[1][1] != "16Uiu2DirectoryNode" {
		t.Fatalf("show CSV = %#v", records)
	}

	vcardPath := filepath.Join(t.TempDir(), "directory-node.vcf")
	if err := runIdentityDirectoryDownload(io.Discard, identityDirectoryOptions{
		Kind:       "node",
		Format:     "vcard",
		OutputPath: vcardPath,
		Limit:      100,
	}, "16Uiu2DirectoryNode"); err != nil {
		t.Fatalf("runIdentityDirectoryDownload failed: %v", err)
	}
	vcard, err := os.ReadFile(vcardPath)
	if err != nil {
		t.Fatalf("read downloaded vCard: %v", err)
	}
	if text := string(vcard); !strings.Contains(text, "BEGIN:VCARD") ||
		!strings.Contains(text, "FN:Directory Provider") ||
		!strings.Contains(text, "X-SDN-EPM-CID:bafy-directory-node") {
		t.Fatalf("downloaded vCard = %s", text)
	}
}

func TestExportLocalIdentityFlatBufferWritesOutputPath(t *testing.T) {
	_, store, peerID, dataDir := newIdentityWizardTestStore(t)
	if err := runIdentityWizardWithIO(
		strings.NewReader("y\n"),
		io.Discard,
		identityWizardOptions{
			Sets: []string{
				"dn=Local Export Provider",
				"legal_name=Local Export LLC",
			},
			Format: "json",
		},
		store,
		identityWizardNodeIdentity{PeerID: peerID},
		dataDir,
	); err != nil {
		t.Fatalf("runIdentityWizardWithIO failed: %v", err)
	}

	// exportLocalIdentity opens the store itself; release the wizard's
	// handle first (the v2 store is single-writer).
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	cfg := config.Default()
	cfg.Storage.Path = dataDir
	epmPath := filepath.Join(t.TempDir(), "local-epm.fbs")
	var out bytes.Buffer
	if err := exportLocalIdentity(t.Context(), &out, cfg, "flatbuffer", epmPath); err != nil {
		t.Fatalf("exportLocalIdentity failed: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("flatbuffer file export should not write stdout, got %q", out.String())
	}
	raw, err := os.ReadFile(epmPath)
	if err != nil {
		t.Fatalf("read flatbuffer output %s: %v", epmPath, err)
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(raw) {
		t.Fatalf("flatbuffer output has invalid EPM identifier: %x", raw[:min(len(raw), 16)])
	}
}

func TestRunIdentityExportFlatBufferUsesLocalFallbackOutputPathWhenDaemonUnavailable(t *testing.T) {
	cfgPath, store, peerID, dataDir := newIdentityWizardTestStore(t)
	if err := runIdentityWizardWithIO(
		strings.NewReader("y\n"),
		io.Discard,
		identityWizardOptions{
			Sets:   []string{"dn=Run Export Provider"},
			Format: "json",
		},
		store,
		identityWizardNodeIdentity{PeerID: peerID},
		dataDir,
	); err != nil {
		t.Fatalf("runIdentityWizardWithIO failed: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	closedAddr := reserveClosedLoopbackAddr(t)
	cfg.Admin.ListenAddr = closedAddr
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}

	oldConfigPath := configPath
	oldFormat := identityExportFormat
	oldOutput := identityExportOutput
	t.Cleanup(func() {
		configPath = oldConfigPath
		identityExportFormat = oldFormat
		identityExportOutput = oldOutput
	})
	configPath = cfgPath
	identityExportFormat = "flatbuffer"
	identityExportOutput = filepath.Join(t.TempDir(), "run-export.fbs")

	// The local fallback inside runIdentityExport opens the store itself;
	// release the wizard's handle first (the v2 store is single-writer).
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runIdentityExport(cmd, nil); err != nil {
		t.Fatalf("runIdentityExport failed: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("flatbuffer file export should not write stdout, got %q", out.String())
	}
	raw, err := os.ReadFile(identityExportOutput)
	if err != nil {
		t.Fatalf("read flatbuffer output %s: %v", identityExportOutput, err)
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(raw) {
		t.Fatalf("flatbuffer output has invalid EPM identifier: %x", raw[:min(len(raw), 16)])
	}
}

func TestIdentityExportDoesNotFallbackOnDaemonHTTPError(t *testing.T) {
	_, store, peerID, dataDir := newIdentityWizardTestStore(t)
	if err := runIdentityWizardWithIO(
		strings.NewReader("y\n"),
		io.Discard,
		identityWizardOptions{
			Sets:   []string{"dn=Stale Local Provider"},
			Format: "json",
		},
		store,
		identityWizardNodeIdentity{PeerID: peerID},
		dataDir,
	); err != nil {
		t.Fatalf("runIdentityWizardWithIO failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "daemon unhappy", http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Storage.Path = dataDir
	cfg.Admin.ListenAddr = strings.TrimPrefix(server.URL, "http://")
	var out bytes.Buffer
	err := exportIdentityWithLocalFallback(t.Context(), &out, cfg, "json", "")
	if err == nil {
		t.Fatal("exportIdentityWithLocalFallback succeeded, want daemon HTTP error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want daemon HTTP status", err)
	}
	if out.Len() != 0 {
		t.Fatalf("export wrote local fallback output despite daemon HTTP error: %s", out.String())
	}
}

func TestIdentityWizardPreservesIdentityBackedPublicKeys(t *testing.T) {
	_, store, _, dataDir := newIdentityWizardTestStore(t)
	identity, err := testProviderDerivedIdentity()
	if err != nil {
		t.Fatalf("testProviderDerivedIdentity failed: %v", err)
	}

	// A REAL account xpub, not the "xpub-provider" placeholder used elsewhere in
	// this file. This test asserts that a published SIGNING key carries the
	// xpub, and a key may only carry one when it is genuinely CKDpub-derivable
	// from it (task sdn-vcf-duplicate-sign-alias): the placeholder does not
	// parse, so the only key that ever carried it was the Ed25519 key, which was
	// stamped with it falsely. The other placeholder uses in this file assert
	// unrelated things and stay as they are.
	const xpub = "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm"
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

func TestIdentityWizardDaemonUpdatePreservesRuntimeAddress(t *testing.T) {
	_, store, _, dataDir := newIdentityWizardTestStore(t)
	identity, err := testProviderDerivedIdentity()
	if err != nil {
		t.Fatalf("testProviderDerivedIdentity failed: %v", err)
	}

	const (
		xpub         = "xpub-provider"
		runtimeAddr  = "http://runtime-node.onion"
		updatedName  = "Daemon Updated Provider"
		originalName = "Daemon Runtime Provider"
	)
	daemonService := epm.NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, xpub, dataDir)
	if err := daemonService.SetRuntimeAddresses([]string{runtimeAddr}); err != nil {
		t.Fatalf("SetRuntimeAddresses failed: %v", err)
	}
	if err := daemonService.UpdateProfile(&epm.Profile{DN: originalName}); err != nil {
		t.Fatalf("daemon seed UpdateProfile failed: %v", err)
	}
	sourceEPM := daemonService.GetNodeEPM()
	sourceProfile, err := epm.ProfileFromEPMBytes(sourceEPM)
	if err != nil {
		t.Fatalf("ProfileFromEPMBytes failed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/node/epm", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/x-flatbuffers")
			_, _ = w.Write(daemonService.GetNodeEPM())
		case http.MethodPut:
			cookie, err := r.Cookie("sdn_wallet_session")
			if err != nil || cookie.Value != "session-123" {
				http.Error(w, "missing session cookie", http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("X-Requested-With"); got != "spacedatanetwork-cli" {
				http.Error(w, "missing cli CSRF header", http.StatusForbidden)
				return
			}
			var profile epm.Profile
			if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := daemonService.UpdateProfile(&profile); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/x-flatbuffers")
			_, _ = w.Write(daemonService.GetNodeEPM())
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/node/epm/vcard", func(w http.ResponseWriter, r *http.Request) {
		vcard, err := daemonService.GetNodeVCard()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(vcard))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var out bytes.Buffer
	if err := runIdentityWizardWithProfile(
		t.Context(),
		strings.NewReader("y\n"),
		&out,
		identityWizardOptions{
			Sets:         []string{"dn=" + updatedName},
			Format:       "json",
			SessionToken: "session-123",
		},
		store,
		identityWizardNodeIdentity{
			Identity: identity,
			PeerID:   identity.PeerID,
			XPub:     xpub,
		},
		dataDir,
		server.URL,
		identityWizardProfileSource{
			Profile:      sourceProfile,
			SourceEPM:    sourceEPM,
			DaemonSource: true,
		},
	); err != nil {
		t.Fatalf("runIdentityWizardWithProfile failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("wizard json output is invalid JSON: %v\n%s", err, out.String())
	}
	if payload["dn"] != updatedName {
		t.Fatalf("payload dn = %v, want %q", payload["dn"], updatedName)
	}
	assertIdentityWizardJSONHasRuntimeAddress(t, payload, runtimeAddr)
}

func TestIdentityWizardDaemonProfileSourceReturnsHTTPError(t *testing.T) {
	_, _, peerID, _ := newIdentityWizardTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "daemon not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, ok, err := loadIdentityWizardDaemonProfileSource(t.Context(), server.URL, peerID)
	if err == nil {
		t.Fatal("loadIdentityWizardDaemonProfileSource succeeded, want daemon API error")
	}
	if !ok {
		t.Fatal("loadIdentityWizardDaemonProfileSource reported offline fallback for HTTP error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("error = %v, want HTTP 503 detail", err)
	}
}

func TestIdentityWizardOfflineUpdatePreservesStoredRuntimeAddress(t *testing.T) {
	_, store, _, dataDir := newIdentityWizardTestStore(t)
	identity, err := testProviderDerivedIdentity()
	if err != nil {
		t.Fatalf("testProviderDerivedIdentity failed: %v", err)
	}

	const (
		xpub        = "xpub-provider"
		runtimeAddr = "http://stored-runtime-node.onion"
	)
	seedService := epm.NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, xpub, dataDir)
	seedService.SetProfileStore(store)
	if err := seedService.SetRuntimeAddresses([]string{runtimeAddr}); err != nil {
		t.Fatalf("SetRuntimeAddresses failed: %v", err)
	}
	if err := seedService.UpdateProfile(&epm.Profile{DN: "Stored Runtime Provider"}); err != nil {
		t.Fatalf("seed UpdateProfile failed: %v", err)
	}

	var out bytes.Buffer
	if err := runIdentityWizardWithIO(
		strings.NewReader("y\n"),
		&out,
		identityWizardOptions{
			Sets:   []string{"dn=Offline Runtime Provider"},
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
	assertIdentityWizardJSONHasRuntimeAddress(t, payload, runtimeAddr)
}

func TestIdentityWizardOfflineRefusesUnrebuildableStoredKey(t *testing.T) {
	_, store, _, dataDir := newIdentityWizardTestStore(t)
	identity, err := testProviderDerivedIdentity()
	if err != nil {
		t.Fatalf("testProviderDerivedIdentity failed: %v", err)
	}
	_, runtimePriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	const xpub = "xpub-provider"
	seedService := epm.NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, xpub, dataDir)
	seedService.SetProfileStore(store)
	if err := seedService.SetRuntimeSigningKey(runtimePriv, "sdn/dataset-publication/v1"); err != nil {
		t.Fatalf("SetRuntimeSigningKey failed: %v", err)
	}
	if err := seedService.UpdateProfile(&epm.Profile{DN: "Runtime Key Provider"}); err != nil {
		t.Fatalf("seed UpdateProfile failed: %v", err)
	}

	err = runIdentityWizardWithIO(
		strings.NewReader("y\n"),
		io.Discard,
		identityWizardOptions{
			Sets:   []string{"dn=Should Not Drop Runtime Key"},
			Format: "json",
		},
		store,
		identityWizardNodeIdentity{
			Identity: identity,
			PeerID:   identity.PeerID,
			XPub:     xpub,
		},
		dataDir,
	)
	if err == nil {
		t.Fatal("runIdentityWizardWithIO succeeded, want unrebuildable key preservation error")
	}
	if !strings.Contains(err.Error(), "cannot be rebuilt offline") {
		t.Fatalf("error = %v, want unrebuildable key preservation error", err)
	}
}

func TestIdentityWizardDoesNotTreatUnknownXPubPathAsRebuildable(t *testing.T) {
	identity, err := testProviderDerivedIdentity()
	if err != nil {
		t.Fatalf("testProviderDerivedIdentity failed: %v", err)
	}

	if identityWizardCanRebuildKey(map[string]any{
		"xpub":        "xpub-provider",
		"public_key":  strings.Repeat("ab", 33),
		"key_address": "m/44'/0'/0'/99/0",
		"key_type":    "signing",
	}, identityWizardNodeIdentity{
		Identity: identity,
		PeerID:   identity.PeerID,
		XPub:     "xpub-provider",
	}) {
		t.Fatal("unknown xpub-derived path was treated as rebuildable")
	}
}

func TestIdentityWizardRefusesToDropExistingPublicIdentityMaterial(t *testing.T) {
	_, store, _, dataDir := newIdentityWizardTestStore(t)
	identity, err := testProviderDerivedIdentity()
	if err != nil {
		t.Fatalf("testProviderDerivedIdentity failed: %v", err)
	}

	seedService := epm.NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub-provider", dataDir)
	seedService.SetProfileStore(store)
	if err := seedService.Init(); err != nil {
		t.Fatalf("seed Init failed: %v", err)
	}
	if err := seedService.UpdateProfile(&epm.Profile{DN: "Identity Backed Provider"}); err != nil {
		t.Fatalf("seed UpdateProfile failed: %v", err)
	}

	var out bytes.Buffer
	err = runIdentityWizardWithIO(
		strings.NewReader("y\n"),
		&out,
		identityWizardOptions{
			Sets:   []string{"dn=Should Not Persist"},
			Format: "json",
		},
		store,
		identityWizardNodeIdentity{
			PeerID: identity.PeerID,
			XPub:   "xpub-provider",
		},
		dataDir,
	)
	if err == nil {
		t.Fatal("runIdentityWizardWithIO succeeded without derived identity, want preservation error")
	}
	if !strings.Contains(err.Error(), "refusing to update without derived identity/xpub") {
		t.Fatalf("error = %v, want identity preservation refusal", err)
	}
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

func assertIdentityWizardJSONHasRuntimeAddress(t *testing.T, payload map[string]any, runtimeAddr string) {
	t.Helper()

	addrs, ok := payload["multiformat_address"].([]any)
	if !ok || len(addrs) == 0 {
		t.Fatalf("payload multiformat_address missing or empty: %#v", payload["multiformat_address"])
	}
	for _, raw := range addrs {
		if raw == runtimeAddr {
			return
		}
	}
	t.Fatalf("payload multiformat_address does not include %q: %#v", runtimeAddr, addrs)
}

func reserveClosedLoopbackAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback address: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close reserved loopback listener: %v", err)
	}
	return addr
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
