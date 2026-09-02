package storage

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// rawFrames appends n synthetic frames ([u32 len][u32 crc][payload]) to path
// and returns the file length afterwards.
func rawFrames(t *testing.T, path string, n int, seed byte) int64 {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := 0; i < n; i++ {
		payload := make([]byte, 16+i)
		for k := range payload {
			payload[k] = seed + byte(i) + byte(k)
		}
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[0:], uint32(len(payload)))
		binary.LittleEndian.PutUint32(hdr[4:], crc32.ChecksumIEEE(payload))
		if _, err := f.Write(hdr[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func openJournalForTest(t *testing.T, path string) *recordCatalogJournal {
	t.Helper()
	j, err := openRecordCatalogJournal(path, false)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	return j
}

// A checkpoint writes the prefix sidecar; the next open trusts the prefix,
// CRC-scans only the tail, and resumes the running digest without walking
// the prefix again.
func TestJournalOpenTrustsTheCheckpointedPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), recordCatalogJournalFileName)
	end := rawFrames(t, path, 5, 1)

	j := openJournalForTest(t, path)
	if j.scanStart != 0 {
		t.Fatalf("first open scanStart = %d, want 0 (no sidecar yet)", j.scanStart)
	}
	digest, err := j.digestPrefix(end)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.writePrefixMark(end, digest); err != nil {
		t.Fatalf("writePrefixMark: %v", err)
	}
	j.Close()

	// More frames after the checkpoint, plus a torn tail.
	grown := rawFrames(t, path, 3, 9)
	if err := os.WriteFile(path+".torn", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	f.Write([]byte{7, 0, 0})
	f.Close()

	j2 := openJournalForTest(t, path)
	defer j2.Close()
	if j2.scanStart != end {
		t.Fatalf("reopen scanStart = %d, want the checkpointed %d", j2.scanStart, end)
	}
	if got := j2.validLength(); got != grown {
		t.Fatalf("validLength = %d, want %d (torn tail dropped)", got, grown)
	}
	// The restored digest state must agree with a from-scratch walk.
	fresh := openJournalForTest(t, path)
	defer fresh.Close()
	fresh.digest = nil
	fresh.digestOffset = 0
	want, err := fresh.digestPrefix(grown)
	if err != nil {
		t.Fatal(err)
	}
	got, err := j2.digestPrefix(grown)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("digest over the tail from the restored state = %s, want %s", got, want)
	}
	if again, _ := j2.digestPrefix(end); again != digest {
		t.Logf("note: digestPrefix(end) after extending restarts the walk; got %s", again)
	}
}

// A sidecar whose boundary no longer matches the file (a byte flipped in the
// last checkpointed frame, or a journal shorter than the mark) is ignored and
// the whole file is scanned, exactly as before the sidecar existed.
func TestJournalOpenFallsBackToAFullScanWhenThePrefixDoesNotVerify(t *testing.T) {
	path := filepath.Join(t.TempDir(), recordCatalogJournalFileName)
	end := rawFrames(t, path, 4, 3)
	j := openJournalForTest(t, path)
	digest, err := j.digestPrefix(end)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.writePrefixMark(end, digest); err != nil {
		t.Fatal(err)
	}
	j.Close()

	// Flip a payload byte inside the last checkpointed frame.
	f, _ := os.OpenFile(path, os.O_RDWR, 0o644)
	f.WriteAt([]byte{0xFF}, end-1)
	f.Close()
	j2 := openJournalForTest(t, path)
	if j2.scanStart != 0 {
		t.Fatalf("corrupt boundary: scanStart = %d, want 0", j2.scanStart)
	}
	if got := j2.validLength(); got >= end {
		t.Fatalf("corrupt last frame must not be valid: validLength = %d, end %d", got, end)
	}
	j2.Close()

	// Shorter than the mark.
	if err := os.Truncate(path, end/2); err != nil {
		t.Fatal(err)
	}
	j3 := openJournalForTest(t, path)
	defer j3.Close()
	if j3.scanStart != 0 {
		t.Fatalf("short journal: scanStart = %d, want 0", j3.scanStart)
	}
}

// A clean shutdown checkpoints; the next boot must not re-read the
// checkpointed journal prefix at all — the scan starts at the mark.
func TestWarmBootScansOnlyTheJournalTail(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	if !store.BootReplay().Durable {
		t.Skip("engine has no filesystem on this host — the persisted-state lane is inert")
	}
	for i, norad := range []uint32{25544, 43013, 48274} {
		if _, err := store.Store("OMM", buildEngineOMM(t, norad, "SAT", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatalf("store OMM %d: %v", norad, err)
		}
	}
	journalEnd := store.recordCatalog.validLength()
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(recordCatalogPrefixMarkPath(filepath.Join(basePath, recordCatalogJournalFileName))); err != nil {
		t.Fatalf("clean shutdown wrote no prefix mark: %v", err)
	}

	warm := reopenDeferred(t, basePath)
	defer warm.Close()
	if !warm.BootReplay().Warm {
		t.Fatalf("restart was not warm: %+v", warm.BootReplay())
	}
	if warm.recordCatalog.scanStart != journalEnd {
		t.Fatalf("boot scanned the journal from %d, want only the tail past the checkpoint at %d", warm.recordCatalog.scanStart, journalEnd)
	}
}

// A journal rewritten with DIFFERENT frames of the SAME lengths keeps every
// frame boundary; the sidecar must still be refused (and dropped), because
// the last frame's payload CRC no longer matches.
func TestJournalOpenRefusesASidecarFromARewrittenJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), recordCatalogJournalFileName)
	end := rawFrames(t, path, 4, 3)
	j := openJournalForTest(t, path)
	digest, err := j.digestPrefix(end)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.writePrefixMark(end, digest); err != nil {
		t.Fatal(err)
	}
	j.Close()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := rawFrames(t, path, 4, 200); got != end { // same lengths, other payloads
		t.Fatalf("rewritten length %d != %d", got, end)
	}
	j2 := openJournalForTest(t, path)
	defer j2.Close()
	if j2.scanStart != 0 {
		t.Fatalf("rewritten journal: scanStart = %d, want 0", j2.scanStart)
	}
	if _, err := os.Stat(recordCatalogPrefixMarkPath(path)); !os.IsNotExist(err) {
		t.Fatalf("stale sidecar was not dropped: %v", err)
	}
	fresh, err := j2.digestPrefix(end)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == digest {
		t.Fatal("digest of the rewritten journal equals the old one; the test rewrite is not adversarial")
	}
}
