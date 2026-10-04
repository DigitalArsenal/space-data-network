package main

// store-migrate --to 4 of sealed records: SDN's at-rest seal (contract C-25:
// KMF's KEY_BYTES sealed to the store's own key, the stored bytes an SDF1
// envelope, not a FlatBuffer). The migrated store holds format 1's envelopes,
// copies and tags; the store's identity carries over, so its key opens every
// envelope to the written record; and the daemon on the migrated store serves
// them as format 1 did, with the sealed standard off its SQL surface.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	KMFfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	flatbuffers "github.com/google/flatbuffers/go"
	"golang.org/x/crypto/curve25519"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// The store's field-encryption identity file and the envelopes' context, as
// the store writes and seals them (storage/field_encryption.go).
const (
	sealedIdentityFile = "field-encryption-identity.json"
	sealedContext      = "space-data-network/storage/field-encryption/v1"
)

func TestStoreMigrateFormat4SealedRecords(t *testing.T) {
	requireFormat4Engine(t)
	ctx := context.Background()
	dir := t.TempDir()

	// A throwaway identity, provisioned before the store's first open.
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := json.Marshal(map[string]string{"public_key_hex": hex.EncodeToString(pub), "private_key_hex": hex.EncodeToString(priv)})
	if err := os.WriteFile(filepath.Join(dir, sealedIdentityFile), identity, 0o600); err != nil {
		t.Fatal(err)
	}

	var recs, plainKeys [][]byte
	for i := 0; i < 12; i++ {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			t.Fatal(err)
		}
		b := flatbuffers.NewBuilder(256)
		id := b.CreateString(fmt.Sprintf("sealed-migrate-%02d", i))
		kb := b.CreateByteVector(key)
		KMFfb.KMFStart(b)
		KMFfb.KMFAddKEY_ID(b, id)
		KMFfb.KMFAddALGORITHM(b, 5)
		KMFfb.KMFAddKEY_BYTES(b, kb)
		KMFfb.KMFAddVERSION(b, uint32(i+1))
		KMFfb.FinishKMFBuffer(b, KMFfb.KMFEnd(b))
		recs = append(recs, append([]byte(nil), b.FinishedBytes()...))
		plainKeys = append(plainKeys, key)
	}
	cids := make([]string, len(recs))
	refs := make([]storage.RawRecordRef, len(recs))
	for i, d := range recs {
		cids[i] = storage.ComputeCID(d)
		refs[i] = storage.RawRecordRef{CID: cids[i]}
	}

	// Format 1: tagged, single, untagged and a second peer's copies.
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewFlatSQLStore(dir, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	tags := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "keys", BatchID: "k-1"}
	if n, err := s.StoreBatchWithSourceTags("KMF.fbs", recs[:8], "source:keys", nil, tags); err != nil || n != 8 {
		t.Fatalf("KMF batch: %d, %v", n, err)
	}
	if _, err := s.Store("KMF.fbs", recs[8], "source:keys", []byte{0xbe, 0xef}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreBatch("KMF.fbs", recs[9:], "source:keys", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreBatch("KMF.fbs", recs[10:], "source:keys-mirror", nil); err != nil {
		t.Fatal(err)
	}
	// A record's tags as GetSourceTags answers (an untagged record's: not
	// found).
	tagsOf := func(s *storage.FlatSQLStore, c string) string {
		tg, err := s.GetSourceTags("KMF.fbs", c)
		if err != nil {
			return "error: " + err.Error()
		}
		return fmt.Sprintf("%+v", tg)
	}
	f1Tags := map[string]string{}
	for _, c := range cids {
		f1Tags[c] = tagsOf(s, c)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	legacy := cloneStore(t, dir)

	rep, err := migrateStore4(ctx, engineOptions(t, dir), nil)
	if err != nil || !rep.Activated {
		t.Fatalf("store-migrate --to 4: %+v, %v", rep, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, sealedIdentityFile)); err != nil || !bytes.Equal(got, identity) {
		t.Fatalf("the migrated store's identity is not the store's: %v", err)
	}

	// The engine holds format 1's copies, tags and counters; every stored
	// envelope opens with the store's key to the written record.
	api, err := openFormat4Engine(ctx, format4.Options{DataRoot: dir, Create: format4.OpenExisting, AOTCacheDir: migrate4AOTDir(t),
		CompileOnMiss: true, Wasm: engineWasm(t)})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := migrate4TypeSpec("KMF.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if err := api.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	assertFormat4EqualsFormat1(t, api, legacy, 0)
	held, err := api.Get(ctx, "KMF", cids, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) < len(recs) {
		t.Fatalf("the migrated store holds %d KMF copies for %d records", len(held), len(recs))
	}
	for _, r := range held {
		plain, sealed, err := encfield.Open("KMF", r.Data, priv, sealedContext)
		if err != nil || !sealed || storage.ComputeCID(plain) != r.CID {
			t.Fatalf("migrated KMF %s: sealed=%v, opens with the store's key to its record: %v", r.CID, sealed, err)
		}
	}
	if err := api.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// The daemon on the migrated store: GET opens the envelopes, the refs
	// carry them, the tags are format 1's, and KMF is off the SQL surface.
	if _, _, err := flatsqlrt.PrewarmP4ThreadsAOT(storage.EngineAOTCacheDir()); err != nil {
		t.Fatalf("prewarm the format-4 engine: %v", err)
	}
	t.Setenv(format4.FormatEnv, "4")
	s4, err := storage.NewFlatSQLStore(dir, v)
	if err != nil {
		t.Fatalf("open the migrated store: %v", err)
	}
	defer s4.Close()
	for i, c := range cids {
		got, err := s4.GetRecord("KMF.fbs", c)
		if err != nil || !bytes.Equal(got.Data, recs[i]) {
			t.Fatalf("migrated GetRecord %s: %v", c, err)
		}
		if tg := tagsOf(s4, c); tg != f1Tags[c] {
			t.Fatalf("migrated tags of %s: %s; format 1 %s", c, tg, f1Tags[c])
		}
	}
	stored, err := s4.QueryRawRecordRefsByRefs("KMF.fbs", refs)
	if err != nil || len(stored) != len(refs) {
		t.Fatalf("migrated refs: %d, %v", len(stored), err)
	}
	for _, r := range stored {
		if plain, sealed, err := encfield.Open("KMF", r.Data, priv, sealedContext); err != nil || !sealed || storage.ComputeCID(plain) != r.CID {
			t.Fatalf("migrated ref %s: sealed=%v: %v", r.CID, sealed, err)
		}
	}
	if _, _, _, err := s4.QuerySandboxedJSON(`SELECT COUNT(*) FROM "KMF"`, flatsqlrt.SandboxCaps{Timeout: time.Minute}); err == nil {
		t.Fatal("the migrated store answers SQL over the sealed standard KMF")
	}
	if err := s4.Close(); err != nil {
		t.Fatal(err)
	}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, k := range plainKeys {
			if bytes.Contains(b, k) {
				t.Errorf("%s holds a plaintext KEY_BYTES", p)
			}
		}
		return nil
	})
}
