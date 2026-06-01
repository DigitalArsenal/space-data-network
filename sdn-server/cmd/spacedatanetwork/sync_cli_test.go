package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestNormalizeSyncSchemaNameAcceptsThreeLetterNames(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"OMM":     "OMM.fbs",
		"omm":     "OMM.fbs",
		"OMM.fbs": "OMM.fbs",
		"omm.fbs": "OMM.fbs",
		"cat":     "CAT.fbs",
	}
	for input, want := range tests {
		got, err := normalizeSyncSchemaName(input)
		if err != nil {
			t.Fatalf("normalizeSyncSchemaName(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeSyncSchemaName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestClassifySyncProviderIdentifier(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"bafkreigh2akiscaildcagqrb7hf7vsgkl2kpdx3obxxm2pvshpwrsp7m2a": "ipfs-cid",
		"ipns://k51qzi5uqu5dlprovider":                                "ipns",
		"xpub661MyMwAqRbcFexample":                                    "xpub",
		"bc1qspacedatanetwork000000000000000000000000":                "bitcoin-address",
		"0x000000000000000000000000000000000000dEaD":                  "ethereum-address",
		"So11111111111111111111111111111111111111112":                 "solana-address",
		"celestrak.eth":         "ens-domain",
		"provider.sol":          "sns-domain",
		"space-data-network-02": "provider-id",
	}
	for input, want := range tests {
		if got := classifySyncProviderIdentifier(input); got != want {
			t.Fatalf("classifySyncProviderIdentifier(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSyncStatusReportsLocalReplicaWithoutSourceName(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSyncStatusWithOptions(context.Background(), &out, syncStatusOptions{
		Schema:       "OMM",
		ProviderID:   "space-data-network-02",
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("runSyncStatusWithOptions failed: %v", err)
	}

	body := out.String()
	for _, want := range []string{
		"schema=OMM.fbs",
		"provider_identifier=space-data-network-02",
		"provider_identifier_kind=provider-id",
		"provider_id=space-data-network-02",
		"source_name=celestrak-gp",
		"status=synced",
		"local_rows=1",
		"pinned_rows=1",
		"last_synced_at=2026-05-25T06:08:54Z",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("sync status output missing %q:\n%s", want, body)
		}
	}
}

func TestSyncStatusResolvesProviderAliasFromDirectory(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSyncStatusWithOptions(context.Background(), &out, syncStatusOptions{
		Schema:       "OMM.fbs",
		ProviderID:   "celestrak.eth",
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("runSyncStatusWithOptions failed: %v", err)
	}

	body := out.String()
	for _, want := range []string{
		"provider_identifier=celestrak.eth",
		"provider_identifier_kind=ens-domain",
		"provider_identifier_match=directory",
		"provider_peer_id=16Uiu2HCelesTrak",
		"source_name=celestrak-gp",
		"status=synced",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("sync status output missing %q:\n%s", want, body)
		}
	}
}

func TestSyncStatusResolvesProviderDomainFromSourcePrefix(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestReplica(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSyncStatusWithOptions(context.Background(), &out, syncStatusOptions{
		Schema:       "OMM",
		ProviderID:   "celestrak.eth",
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("runSyncStatusWithOptions failed: %v", err)
	}

	body := out.String()
	for _, want := range []string{
		"provider_identifier=celestrak.eth",
		"provider_identifier_kind=ens-domain",
		"provider_identifier_match=source",
		"provider_id=space-data-network-02",
		"source_name=celestrak-gp",
		"status=synced",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("sync status output missing %q:\n%s", want, body)
		}
	}
}

func TestSyncStatusJSONOutput(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSyncStatusWithOptions(context.Background(), &out, syncStatusOptions{
		Schema:       "omm",
		ProviderID:   "celestrak.eth",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		JSON:         true,
	})
	if err != nil {
		t.Fatalf("runSyncStatusWithOptions failed: %v", err)
	}

	var body struct {
		Count   int `json:"count"`
		Results []struct {
			SchemaName              string `json:"schema_name"`
			ProviderIdentifierKind  string `json:"provider_identifier_kind"`
			ProviderIdentifierMatch string `json:"provider_identifier_match"`
			Status                  string `json:"status"`
			LocalRows               int64  `json:"local_rows"`
			PinnedRows              int64  `json:"pinned_rows"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode sync status JSON failed: %v\n%s", err, out.String())
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("unexpected sync status JSON count: %+v", body)
	}
	got := body.Results[0]
	if got.SchemaName != "OMM.fbs" || got.ProviderIdentifierKind != "ens-domain" || got.ProviderIdentifierMatch != "directory" || got.Status != "synced" || got.LocalRows != 1 || got.PinnedRows != 1 {
		t.Fatalf("unexpected sync status JSON result: %+v", got)
	}
}

func newSyncCLITestStore(t *testing.T) (string, *storage.FlatSQLStore) {
	t.Helper()

	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(tmpDir, "data")
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config failed: %v", err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("create validator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return cfgPath, store
}

func withSyncCLITestConfig(t *testing.T, cfgPath string) {
	t.Helper()

	oldConfigPath := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = oldConfigPath })
}

func seedSyncCLITestData(t *testing.T, store *storage.FlatSQLStore) {
	t.Helper()

	seedSyncCLITestReplica(t, store)
	verifiedAt := time.Date(2026, 5, 25, 6, 8, 54, 0, time.UTC)
	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:           "node",
		PeerID:         "16Uiu2HCelesTrak",
		DN:             "CelesTrak",
		LegalName:      "CelesTrak",
		BitcoinAddress: "bc1qspacedatanetwork000000000000000000000000",
		EPMCID:         "bafkreigh2akiscaildcagqrb7hf7vsgkl2kpdx3obxxm2pvshpwrsp7m2a",
		Source:         "test",
		EPMJSON: `{
			"xpub": "xpub661MyMwAqRbcFexample",
			"ethereum_address": "0x000000000000000000000000000000000000dEaD",
			"solana_address": "So11111111111111111111111111111111111111112",
			"ens_names": ["celestrak.eth"],
			"sns_names": ["provider.sol"],
			"ipns": "ipns://k51qzi5uqu5dlprovider"
		}`,
		UpdatedAt: verifiedAt.Unix(),
	}); err != nil {
		t.Fatalf("upsert directory record failed: %v", err)
	}
}

func seedSyncCLITestReplica(t *testing.T, store *storage.FlatSQLStore) {
	t.Helper()

	payload := sds.NewOMMBuilder().
		WithNoradCatID(56775).
		WithObjectName("STARLINK-6292").
		WithEpoch("2026-05-25T06:08:54Z").
		Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", payload, "16Uiu2HCelesTrak", nil, storage.SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		SourceURL:         "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
		BatchID:           "test-batch",
		ProducerPeerID:    "16Uiu2HCelesTrak",
		ProducerPublicKey: "provider-public-key",
	}); err != nil {
		t.Fatalf("store OMM failed: %v", err)
	}

	verifiedAt := time.Date(2026, 5, 25, 6, 8, 54, 0, time.UTC)
	if err := store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
		CID:               "bafkshard-omm",
		SchemaName:        "OMM.fbs",
		ProviderPeerID:    "16Uiu2HCelesTrak",
		ProviderPublicKey: "provider-public-key",
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		BatchID:           "test-batch",
		QueryProfile:      storage.DatasetPublicationQueryProfile,
		SnapshotID:        "head-2",
		Head:              "head-2",
		HighWaterMark:     "published-feed-v1:1779689334:1:1:1024",
		ByteHash:          "sha256:shard",
		Role:              "shard",
		RowCount:          1,
		ByteCount:         1024,
		VerificationState: "verified",
		VerifiedAt:        verifiedAt,
	}); err != nil {
		t.Fatalf("upsert pin ledger entry failed: %v", err)
	}
}
