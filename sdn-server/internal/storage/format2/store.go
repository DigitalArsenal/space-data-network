package format2

// Store-level files and the format switch.
//
// SDN_STORE_FORMAT=2 selects format 2. Without it (format 4 is the default,
// format 1 the opt-out) nothing in this package runs. A5: format 2 ships dark
// for at least five releases before any box activates it, and an existing
// store becomes format 2 only through store-migrate in a per-host ops task;
// the daemon never migrates to format 2 by itself.
//
// The engine's own markers live in <root>/fsql2/: STORE and MIGRATED
// (flatsql ps/format.h StoreFile, MigratedFile). store-migrate writes both
// itself, so the store it builds carries the migration's gseq floor (22.3a-2)
// and migratedFrom = 1.
//
// STORE's format is the store's level (flatsql format_level.h, terabyte
// design §3): 2 to the embedded engine's kFormatMax
// (versioninfo.PSEngineStoreFormatMax). The engine raises it at open once the
// registry is non-empty, to the level it writes (WriteFormatEnv pins that
// lower), through fsql2/STORE.tmp: it writes STORE.tmp, then rewrites STORE in
// place, then removes STORE.tmp. STORE is otherwise written once. MIGRATED and
// the partition heads keep format 2.

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// FormatEnv is the environment switch for the store format.
const FormatEnv = "SDN_STORE_FORMAT"

// Selected reports whether this process runs store format 2.
func Selected() bool { return strings.TrimSpace(os.Getenv(FormatEnv)) == "2" }

// WriteFormatEnv pins the level the partition-store engine writes and raises
// stores to (flatsql writer TLV 31): unset or 0 is the embedded engine's
// kFormatMax. A host held at 2 keeps a store the 3.5.1 engine still opens
// (terabyte design O5, a staged rollout); a store never goes back down.
const WriteFormatEnv = "SDN_F2_WRITE_FORMAT"

// WriteFormat is the level WriteFormatEnv pins, 0 when unset. A value outside
// 2 to versioninfo.PSEngineStoreFormatMax is an error: a pin the engine would
// clamp silently is a misconfigured host.
func WriteFormat() (uint16, error) {
	raw := strings.TrimSpace(os.Getenv(WriteFormatEnv))
	if raw == "" || raw == "0" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < storeFormatMin || v > versioninfo.PSEngineStoreFormatMax {
		return 0, fmt.Errorf("format2: %s=%q: want %d to %d", WriteFormatEnv, raw, storeFormatMin,
			versioninfo.PSEngineStoreFormatMax)
	}
	return uint16(v), nil
}

// StoreLevel is the level a new store is written at: the WriteFormatEnv pin,
// else the embedded engine's kFormatMax.
func StoreLevel() (uint16, error) {
	wf, err := WriteFormat()
	if err != nil || wf != 0 {
		return wf, err
	}
	return versioninfo.PSEngineStoreFormatMax, nil
}

// Dir is the engine directory under a store root.
const Dir = "fsql2"

const (
	magicStore     = 0x32515346 // "FSQ2"
	magicMigrated  = 0x4d515346 // "FSQM"
	storeFormat    = 2          // MIGRATED's format (and the partition heads')
	storeFormatMin = 2          // the lowest STORE level
	storeFileLen   = 64
	migratedLen    = 40
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// StoreFile is fsql2/STORE.
type StoreFile struct {
	Format       uint16 // the store's level (0 when written: StoreLevel)
	UUID         [16]byte
	CreatedMs    int64
	GseqFloor    uint64
	MigratedFrom uint32 // 0 = fresh store, 1 = legacy flatsql
}

func (s StoreFile) encode() []byte {
	b := make([]byte, storeFileLen)
	binary.LittleEndian.PutUint32(b[0:], magicStore)
	binary.LittleEndian.PutUint16(b[4:], s.Format)
	copy(b[8:24], s.UUID[:])
	binary.LittleEndian.PutUint64(b[24:], uint64(s.CreatedMs))
	binary.LittleEndian.PutUint64(b[32:], s.GseqFloor)
	binary.LittleEndian.PutUint32(b[40:], s.MigratedFrom)
	binary.LittleEndian.PutUint32(b[56:], crc32.Checksum(b[:56], castagnoli))
	return b
}

// errStoreCorrupt is a STORE whose length, magic or CRC is wrong.
var errStoreCorrupt = errors.New("format2: fsql2/STORE is corrupt")

// ReadStoreFile reads and checks fsql2/STORE under root, at any level from 2
// to the embedded engine's kFormatMax. A STORE above that is refused: this
// build's engine refuses the store. A torn STORE is read through a whole
// fsql2/STORE.tmp, the one state a crash inside the engine's level raise
// leaves (it rewrites STORE in place after STORE.tmp is durable); the engine
// finishes the raise from it at its next open, as this reads it.
func ReadStoreFile(root string) (StoreFile, error) {
	b, err := os.ReadFile(filepath.Join(root, Dir, "STORE"))
	if err != nil {
		return StoreFile{}, err
	}
	s, err := decodeStoreFile(b)
	if errors.Is(err, errStoreCorrupt) {
		if tb, terr := os.ReadFile(filepath.Join(root, Dir, "STORE.tmp")); terr == nil {
			if ts, terr := decodeStoreFile(tb); terr == nil {
				return ts, nil
			} else if !errors.Is(terr, errStoreCorrupt) {
				return StoreFile{}, terr // a raise to a level this build does not open
			}
		}
	}
	return s, err
}

func decodeStoreFile(b []byte) (StoreFile, error) {
	if len(b) != storeFileLen || binary.LittleEndian.Uint32(b) != magicStore ||
		binary.LittleEndian.Uint32(b[56:]) != crc32.Checksum(b[:56], castagnoli) {
		return StoreFile{}, errStoreCorrupt
	}
	var s StoreFile
	s.Format = binary.LittleEndian.Uint16(b[4:])
	if s.Format < storeFormatMin || int(s.Format) > versioninfo.PSEngineStoreFormatMax {
		return StoreFile{}, fmt.Errorf("format2: fsql2/STORE is at level %d; this build opens %d to %d",
			s.Format, storeFormatMin, versioninfo.PSEngineStoreFormatMax)
	}
	copy(s.UUID[:], b[8:24])
	s.CreatedMs = int64(binary.LittleEndian.Uint64(b[24:]))
	s.GseqFloor = binary.LittleEndian.Uint64(b[32:])
	s.MigratedFrom = binary.LittleEndian.Uint32(b[40:])
	return s, nil
}

// NewStoreFile returns a STORE for a new store with a random UUID, at the
// level this build writes (StoreLevel).
func NewStoreFile(gseqFloor uint64, migratedFrom uint32) (StoreFile, error) {
	var s StoreFile
	level, err := StoreLevel()
	if err != nil {
		return s, err
	}
	s.Format = level
	if _, err := rand.Read(s.UUID[:]); err != nil {
		return s, err
	}
	s.CreatedMs = time.Now().UnixMilli()
	if gseqFloor == 0 {
		gseqFloor = 1
	}
	s.GseqFloor = gseqFloor
	s.MigratedFrom = migratedFrom
	return s, nil
}

// WriteStoreFile writes fsql2/STORE once (A5: temp file, fsync, rename,
// fsync the directory), at s.Format (0: StoreLevel). It refuses to replace an
// existing STORE.
func WriteStoreFile(root string, s StoreFile) error {
	if s.Format == 0 {
		level, err := StoreLevel()
		if err != nil {
			return err
		}
		s.Format = level
	}
	dir := filepath.Join(root, Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "STORE")); err == nil {
		return errors.New("format2: fsql2/STORE already exists (it is written once)")
	}
	if err := writeDurable(dir, "STORE", s.encode()); err != nil {
		return err
	}
	return syncDir(root)
}

// WriteMigrated writes fsql2/MIGRATED for the store's UUID, durably.
func WriteMigrated(root string, uuid [16]byte) error {
	b := make([]byte, migratedLen)
	binary.LittleEndian.PutUint32(b[0:], magicMigrated)
	binary.LittleEndian.PutUint16(b[4:], storeFormat)
	copy(b[8:24], uuid[:])
	binary.LittleEndian.PutUint64(b[24:], uint64(time.Now().UnixMilli()))
	binary.LittleEndian.PutUint32(b[32:], crc32.Checksum(b[:32], castagnoli))
	return writeDurable(filepath.Join(root, Dir), "MIGRATED", b)
}

// Migrated reports whether root holds a format-2 store marked MIGRATED for
// its own STORE.
//
// It is also true for a store whose creation a crash cut short inside
// STORE's own write. The engine creates a fresh store in this order: the
// registry files, MIGRATED, then STORE, and STORE is the commit point (flatsql
// ps/open.cpp, A5). A torn STORE beside a whole MIGRATED and an empty
// registry is therefore that one state, with nothing registered and no data;
// the engine finishes STORE from MIGRATED at its next open. Before STORE
// exists the store reads as absent and is created again. So after a crash at
// any instruction of a creation the store is either absent or MIGRATED, and
// Open opens both.
func Migrated(root string) (bool, error) {
	s, err := ReadStoreFile(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		if unfinishedStore(root) {
			return true, nil
		}
		return false, err
	}
	uuid, err := readMigrated(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if uuid != s.UUID {
		return false, fmt.Errorf("format2: fsql2/MIGRATED names another store")
	}
	return true, nil
}

// readMigrated reads and checks fsql2/MIGRATED, returning the store UUID it
// names.
func readMigrated(root string) ([16]byte, error) {
	var uuid [16]byte
	b, err := os.ReadFile(filepath.Join(root, Dir, "MIGRATED"))
	if err != nil {
		return uuid, err
	}
	if len(b) != migratedLen || binary.LittleEndian.Uint32(b) != magicMigrated ||
		binary.LittleEndian.Uint32(b[32:]) != crc32.Checksum(b[:32], castagnoli) {
		return uuid, errors.New("format2: fsql2/MIGRATED is corrupt")
	}
	copy(uuid[:], b[8:24])
	return uuid, nil
}

// unfinishedStore reports the one torn state a fresh store's creation can
// leave (see Migrated): STORE unreadable, MIGRATED whole, and a registry with
// no frames, so nothing was ever registered.
func unfinishedStore(root string) bool {
	if _, err := readMigrated(root); err != nil {
		return false
	}
	fi, err := os.Stat(filepath.Join(root, Dir, "registry.fsl"))
	return errors.Is(err, os.ErrNotExist) || (err == nil && fi.Mode().IsRegular() && fi.Size() == 0)
}

// writeDurable writes dir/name through a temp file: write, fsync, rename,
// fsync the directory. A kill at any step leaves the file absent (or as it
// was) or whole, never torn; a leftover temp file is overwritten by the next
// write.
func writeDurable(dir, name string, data []byte) error {
	tmp := filepath.Join(dir, "."+name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if crashAfter("created") {
		f.Close()
		return errSimulatedCrash
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if crashAfter("written") {
		f.Close()
		return errSimulatedCrash
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if crashAfter("synced") {
		return errSimulatedCrash
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	if crashAfter("renamed") {
		return errSimulatedCrash
	}
	return syncDir(dir)
}

// crashStep, set only by this package's tests, ends a durable write after the
// named step as a kill there would: the steps taken stay on disk and nothing
// after them runs.
var crashStep string

var errSimulatedCrash = errors.New("format2: simulated crash")

func crashAfter(step string) bool { return crashStep != "" && crashStep == step }

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
