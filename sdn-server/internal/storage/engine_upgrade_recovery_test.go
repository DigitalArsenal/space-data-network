package storage

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

func TestEngineUpgradeRecoversStreamAboveFormerDiscardLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "upgrade.db")
	const schema = `table CAT { OBJECT_NAME:string; OBJECT_ID:string; NORAD_CAT_ID:uint; } root_type CAT; file_identifier "$CAT";`
	open := func(definition string) (*flatsqlrt.Runtime, *flatsqlrt.Database) {
		rt, err := flatsqlrt.New(flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()), flatsqlrt.WithFileIORoot(root))
		if err != nil {
			t.Fatal(err)
		}
		db, err := rt.OpenDatabase(definition, "upgrade", path, flatsqlrt.JournalTruncate)
		if err != nil {
			rt.Close()
			t.Fatal(err)
		}
		if err = db.RegisterFileID("$CAT", "CAT"); err != nil {
			db.Destroy()
			rt.Close()
			t.Fatal(err)
		}
		return rt, db
	}
	rt, db := open(schema)
	if _, err := db.Query("CREATE TABLE control_checkpoint(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Query("INSERT INTO control_checkpoint VALUES('must survive upgrade')"); err != nil {
		t.Fatal(err)
	}
	// Large payloads cross the old 32 MiB discard boundary without millions
	// of fixture rows. The original record journal is not needed for recovery.
	name := strings.Repeat("x", 512*1024)
	for i := 0; i < 66; i++ {
		frame := licenceTestCATRecord(uint32(i+1), name)
		if int(binary.LittleEndian.Uint32(frame[:4])) != len(frame)-4 {
			t.Fatal("expected size-prefixed CAT fixture")
		}
		if _, err := db.Ingest(frame); err != nil {
			t.Fatal(err)
		}
	}
	beforeRows, beforeErr := db.Query("SELECT COUNT(*) FROM CAT")
	if beforeErr != nil || len(beforeRows.Rows) != 1 || beforeRows.Rows[0][0] != int64(66) {
		t.Fatalf("fixture records missing: %#v %v", beforeRows, beforeErr)
	}
	if err := db.FlushIndex(); err != nil {
		t.Fatal(err)
	}
	db.Destroy()
	rt.Close()
	info, err := os.Stat(path + ".fsdata")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= engineReindexStreamCeiling {
		t.Fatal("fixture did not exceed former discard limit")
	}
	rt, db = open(strings.Replace(schema, "OBJECT_NAME:string", "OBJECT_NAME:[ubyte]", 1))
	if _, err = db.OpenState(); !errors.Is(err, flatsqlrt.ErrStateVersionMismatch) {
		t.Fatalf("expected schema mismatch: %v", err)
	}
	_, err = openEngineRecordState(db, path)
	if !errors.Is(err, errEngineStateReindexed) {
		t.Fatalf("expected committed incremental recovery, got %v", err)
	}
	db.Destroy()
	rt.Close()
	rt, db = open(strings.Replace(schema, "OBJECT_NAME:string", "OBJECT_NAME:[ubyte]", 1))
	defer rt.Close()
	defer db.Destroy()
	state, err := openEngineRecordState(db, path)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Warm || state.Records != 66 {
		t.Fatalf("recovery state=%+v", state)
	}
	rows, err := db.Query("SELECT value FROM control_checkpoint")
	if err != nil || len(rows.Rows) != 1 || rows.Rows[0][0] != "must survive upgrade" {
		t.Fatalf("control checkpoint lost: %#v %v", rows, err)
	}
	rows, err = db.Query("SELECT COUNT(*) FROM CAT")
	if err != nil || len(rows.Rows) != 1 || rows.Rows[0][0] != int64(66) {
		t.Fatalf("records lost: %#v %v", rows, err)
	}
}

func TestEngineFormatUpgradeKeepsJournalResumeAndServesExactlyOnce(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	base := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, base)
	for i := 0; i < 4; i++ {
		if _, err := store.Store("OMM", buildEngineOMM(t, uint32(30000+i), "Upgrade", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.engineDB.Query("UPDATE _flatsql_state SET v='old-format' WHERE k='format_version'"); err != nil {
		t.Fatal(err)
	}
	// Preserve the old-format checkpoint exactly as an older binary leaves it.
	simulateCrash(t, store)
	warm := reopenDeferred(t, base)
	defer warm.Close()
	stats := warm.BootReplay()
	if !stats.Warm || !stats.EngineWarm || stats.EngineRecords != 4 || stats.FramesApplied != 0 {
		t.Fatalf("upgrade lost warm resume: %+v", stats)
	}
	if got := engineFrameCount(t, warm, "OMM"); got != 4 {
		t.Fatalf("upgrade serves %d rows, want exactly 4", got)
	}
	if loaded, err := warm.HydrateEngineHotWindowFromRecordCatalog(); err != nil || loaded != 0 {
		t.Fatalf("upgrade reingested %d rows: %v", loaded, err)
	}
	if got := engineFrameCount(t, warm, "OMM"); got != 4 {
		t.Fatalf("post-hydration duplicate rows: %d", got)
	}
}
