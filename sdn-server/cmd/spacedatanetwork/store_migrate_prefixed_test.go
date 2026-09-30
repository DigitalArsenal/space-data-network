package main

// store-migrate on a store holding records stored with their own size
// prefix (the dataset-publication PNMs and the local EPM, as SDN builds them
// with FinishSizePrefixed): every copy is accepted, verification finds no
// mismatch, format 2 is activated and serves the same bytes (flatsql
// PARTITION-STORE.md §38).

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

func TestStoreMigrateActivatesWithSizePrefixedRecords(t *testing.T) {
	requirePSEngine(t)
	dir := t.TempDir()
	buildLegacyStore(t, dir)
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewFlatSQLStore(dir, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string][]byte{} // cid -> bytes
	schemaOf := map[string]string{}
	for i := 0; i < 12; i++ {
		pnm, err := storage.BuildDatasetPublicationPNM(&storage.DatasetPublicationManifest{Path: fmt.Sprintf("shard-%d.dpm", i),
			CID:    "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku",
			FileID: fmt.Sprintf("OMM:space-data-network-02:celestrak-gp:gp-001:%d:1000", i*1000)},
			storage.DatasetPublicationPNMOptions{PublishedAt: time.Date(2026, 9, 29, 0, 0, i, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		peer := "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
		if i%2 == 1 {
			peer = "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"
		}
		cid, err := s.Store("PNM.fbs", pnm, peer, nil)
		if err != nil {
			t.Fatal(err)
		}
		stored[cid], schemaOf[cid] = pnm, "PNM.fbs"
	}
	b := flatbuffers.NewBuilder(256)
	dn, name := b.CreateString("CN=dev-node"), b.CreateString("Dev Node")
	EPM.EPMStart(b)
	EPM.EPMAddDN(b, dn)
	EPM.EPMAddLEGAL_NAME(b, name)
	EPM.FinishSizePrefixedEPMBuffer(b, EPM.EPMEnd(b))
	epm := append([]byte(nil), b.FinishedBytes()...)
	cid, err := s.Store("EPM.fbs", epm, "16Uiu2HAkuSSuf8u32gYjS24jmER35Le5GFYLJfJeaQNfPEWPsJuA", nil)
	if err != nil {
		t.Fatal(err)
	}
	stored[cid], schemaOf[cid] = epm, "EPM.fbs"
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	rep, err := migrateStore(context.Background(), migrateOptions{Store: dir, AOTCacheDir: migrateTestAOTDir(t),
		CompileOnMiss: true, PageRows: 64}, &log)
	if err != nil {
		t.Fatalf("migrate: %v\n%s\nverification %+v", err, log.String(), rep.Verification)
	}
	if len(rep.Rejected) != 0 {
		t.Fatalf("%d copies rejected (first %+v)", len(rep.Rejected), rep.Rejected[0])
	}
	if vr := rep.Verification; vr == nil || len(vr.Mismatches) != 0 || !vr.PartitionsEqual || !vr.LanesEqual || !vr.CIDSequences {
		t.Fatalf("verification %+v", rep.Verification)
	}
	if !rep.Activated {
		t.Fatal("not activated")
	}
	ps, err := format2.Open(format2.StoreConfig{Root: dir, AOTCacheDir: migrateTestAOTDir(t), CompileOnMiss: true,
		Topology: format2.Topology{Writers: 1, InteractiveLanes: 1, BulkLanes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	for cid, want := range stored {
		got, err := ps.GetRecord(context.Background(), schemaOf[cid], cid)
		if err != nil {
			t.Fatalf("%s %s: %v", schemaOf[cid], cid, err)
		}
		if !bytes.Equal(got.Data, want) {
			t.Fatalf("%s %s: %d bytes served, %d stored", schemaOf[cid], cid, len(got.Data), len(want))
		}
	}
	t.Logf("migrated %d records, 13 size-prefixed (12 PNM in 2 partitions, 1 EPM); activated; %s", rep.Records, rep.Took)
}
