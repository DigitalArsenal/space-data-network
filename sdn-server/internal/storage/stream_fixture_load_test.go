package storage

// stream_fixture_load_test.go — a REHEARSAL LOADER, skipped unless asked.
//
// It fills a store from the size-prefixed FlatBuffer frame files an earlier
// build wrote under flatsql-streams/ (one <TABLE>.flatsql per standard), by
// storing each frame through the ordinary batch write path — the same path a
// publisher's records take. It exists so a boot-time rehearsal
// (TestOpenExistingStoreRehearsal) can be run against a store of production
// scale without waiting for the network to deliver one.
//
//	SDN_STORE_LOAD_STREAMS=/path/to/flatsql-streams \
//	SDN_STORE_LOAD_TARGET=/path/to/new-store \
//	SDN_STORE_LOAD_SCHEMAS=OMM,CAT,MPE \
//	go test ./internal/storage/ -run TestLoadStreamFixtureRehearsal -v -timeout 6h

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadStreamFixtureRehearsal(t *testing.T) {
	streamsDir := os.Getenv("SDN_STORE_LOAD_STREAMS")
	target := os.Getenv("SDN_STORE_LOAD_TARGET")
	if streamsDir == "" || target == "" {
		t.Skip("set SDN_STORE_LOAD_STREAMS (a flatsql-streams directory) and SDN_STORE_LOAD_TARGET (the store to fill)")
	}
	only := map[string]bool{}
	for _, name := range strings.Split(os.Getenv("SDN_STORE_LOAD_SCHEMAS"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			only[strings.TrimSuffix(name, ".fbs")] = true
		}
	}
	files, err := filepath.Glob(filepath.Join(streamsDir, "*.flatsql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no *.flatsql files under %s (err=%v)", streamsDir, err)
	}
	t.Setenv(checkpointIntervalEnv, "30s")
	if _, err := os.Stat(filepath.Join(target, flatSQLControlDBName)); err == nil {
		t.Logf("target store already exists; records already stored are skipped as repeat CIDs")
	}
	store, err := NewFlatSQLStore(target, bootTestValidator(t), WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("open target store: %v", err)
	}
	defer func() {
		closeStart := time.Now()
		if err := store.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		t.Logf("close (final checkpoint) took %s", time.Since(closeStart).Round(time.Millisecond))
	}()
	if _, err := store.HydrateEngineHotWindow(); err != nil {
		t.Fatalf("hydrate empty store: %v", err)
	}

	// One control transaction per batch: the ingest rate is not what this
	// rehearsal measures, the boot after it is, and a 64-record production
	// chunk pays four fsyncs per 64 records on a laptop SSD.
	const batch = 2048
	grandTotal := 0
	grandStart := time.Now()
	for _, path := range files {
		table := strings.TrimSuffix(filepath.Base(path), ".flatsql")
		if len(only) > 0 && !only[table] {
			continue
		}
		schemaName := table + ".fbs"
		if _, err := s.validatorSchemaTable(store, schemaName); err != nil {
			t.Logf("skip %s: %v", path, err)
			continue
		}
		tags := SourceTags{ProviderID: "rehearsal", SourceName: strings.ToLower(table) + "-replay", BatchID: "streams"}
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		start := time.Now()
		stored, frames := 0, 0
		var pending [][]byte
		flush := func() {
			if len(pending) == 0 {
				return
			}
			n, err := store.storeBatchChunk(schemaName, pending, "rehearsal-peer", nil, &tags)
			if err != nil {
				t.Fatalf("store %s batch: %v", schemaName, err)
			}
			stored += n
			pending = pending[:0]
		}
		var hdr [4]byte
		for {
			if _, err := io.ReadFull(f, hdr[:]); err != nil {
				break
			}
			n := binary.LittleEndian.Uint32(hdr[:])
			data := make([]byte, n)
			if _, err := io.ReadFull(f, data); err != nil {
				break
			}
			frames++
			pending = append(pending, data)
			if len(pending) >= batch {
				flush()
				if frames%(batch*40) == 0 {
					t.Logf("%s: %d frames read, %d stored, %.0f rec/s", table, frames, stored, float64(frames)/time.Since(start).Seconds())
				}
			}
		}
		flush()
		f.Close()
		grandTotal += stored
		t.Logf("%s: %d frames read, %d stored (%d superseded or repeated) in %s", table, frames, stored, frames-stored, time.Since(start).Round(time.Millisecond))
	}
	t.Logf("TOTAL %d records stored in %s", grandTotal, time.Since(grandStart).Round(time.Millisecond))
}

type schemaTableProbe struct{}

var s schemaTableProbe

// validatorSchemaTable reports whether the store's validator knows the schema.
func (schemaTableProbe) validatorSchemaTable(store *FlatSQLStore, schemaName string) (string, error) {
	for _, known := range store.validator.Schemas() {
		if known == schemaName {
			return schemaName, nil
		}
	}
	return "", os.ErrNotExist
}
