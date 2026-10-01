// Package marker reads what a data directory says about store format 4
// (stack design docs/architecture/flatsql-sqlite-partitions.md §1, §11;
// build-out contract §2, §5.1).
//
// It imports no engine. The update helper's store-format guard
// (internal/update) and the format-4 binding (internal/storage/format4) both
// read markers through it, and the guard must never link the engine.
//
// Only the engine writes the markers, through its host I/O: MIGRATED, then
// STORE, each created, written and synced (contract §2.2). Go reads them, and
// finishes an activation (FinishActivation) once both are valid.
package marker

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
)

const (
	Dir           = "fsql4"
	StoreFile     = "STORE"
	MigratedFile  = "MIGRATED"
	LegacyControl = "control.flatsqldb"
	PreFormat4Dir = "pre-format4"
	StoreFormat   = 4
)

// Marker byte layouts (contract §2.2; little-endian, crc32c = Castagnoli).
const (
	storeMagic    = 0x34515346 // "FSQ4"
	migratedMagic = 0x4D515346 // "FSQM"
	storeLen      = 64
	migratedLen   = 40
	storeLayout   = 1
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Markers is what <data> says about format 4 (§2).
type Markers struct {
	StorePresent, StoreValid       bool // fsql4/STORE exists / has length, magic and crc right
	MigratedPresent, MigratedValid bool
	Format, Layout                 uint16
	UUID                           [16]byte
	CreatedMs                      int64
	GseqFloor                      uint64
	MigratedFrom                   uint32
	LegacyControlFile              bool // <data>/control.flatsqldb is a regular file
	LegacyControlDir               bool // ... is a directory

	// MIGRATED's own fields: Activated needs them to agree with STORE's.
	migratedFormat uint16
	migratedUUID   [16]byte
}

// Read reads the markers under dataRoot. It never writes; a missing fsql4 is
// not an error. Format, Layout, UUID, CreatedMs, GseqFloor and MigratedFrom
// come from a valid STORE; without one, UUID and Format come from a valid
// MIGRATED.
func Read(dataRoot string) (Markers, error) {
	var m Markers
	dataRoot = strings.TrimSpace(dataRoot)
	if dataRoot == "" {
		return m, errors.New("marker: no data root")
	}
	dir := filepath.Join(dataRoot, Dir)
	b, present, err := readFile(filepath.Join(dir, MigratedFile))
	if err != nil {
		return m, err
	}
	m.MigratedPresent = present
	if present && len(b) == migratedLen && binary.LittleEndian.Uint32(b) == migratedMagic &&
		binary.LittleEndian.Uint32(b[32:]) == crc32.Checksum(b[:32], castagnoli) {
		m.MigratedValid = true
		m.migratedFormat = binary.LittleEndian.Uint16(b[4:])
		copy(m.migratedUUID[:], b[8:24])
		m.Format, m.UUID = m.migratedFormat, m.migratedUUID
	}
	b, present, err = readFile(filepath.Join(dir, StoreFile))
	if err != nil {
		return m, err
	}
	m.StorePresent = present
	if present && len(b) == storeLen && binary.LittleEndian.Uint32(b) == storeMagic &&
		binary.LittleEndian.Uint32(b[56:]) == crc32.Checksum(b[:56], castagnoli) {
		m.StoreValid = true
		m.Format = binary.LittleEndian.Uint16(b[4:])
		m.Layout = binary.LittleEndian.Uint16(b[6:])
		copy(m.UUID[:], b[8:24])
		m.CreatedMs = int64(binary.LittleEndian.Uint64(b[24:]))
		m.GseqFloor = binary.LittleEndian.Uint64(b[32:])
		m.MigratedFrom = binary.LittleEndian.Uint32(b[40:])
	}
	switch info, err := os.Stat(filepath.Join(dataRoot, LegacyControl)); {
	case err == nil:
		m.LegacyControlDir = info.IsDir()
		m.LegacyControlFile = info.Mode().IsRegular()
	case !errors.Is(err, os.ErrNotExist):
		return m, err
	}
	return m, nil
}

// readFile reads a marker; present is false when it does not exist.
func readFile(name string) (b []byte, present bool, err error) {
	b, err = os.ReadFile(name)
	switch {
	case err == nil:
		return b, true, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("marker: %w", err)
	}
}

// Format4 reports whether the directory holds a format-4 store, whatever its
// state: the guard's rule (§2.4).
func (m Markers) Format4() bool { return m.StorePresent || m.MigratedPresent }

// Activated reports a store whose activation's engine step is complete: a
// valid MIGRATED and a valid STORE of one store, at format 4.
func (m Markers) Activated() bool {
	return m.StoreValid && m.MigratedValid && m.UUID == m.migratedUUID &&
		m.Format == StoreFormat && m.migratedFormat == StoreFormat
}

// NeedsFinish reports an activated store whose Go steps (§2.3 steps 2-3) are
// not done yet.
func (m Markers) NeedsFinish() bool { return m.Activated() && !m.LegacyControlDir }

// FinishActivation runs §2.3 steps 2-3: every root entry whose name starts
// with control.flatsqldb (the database and every sidecar) moves into
// pre-format4/, then the directory control.flatsqldb/ is created, so a
// pre-format-4 binary fails loudly instead of opening an empty store. Every
// step is synced and the whole is idempotent: a rerun after a crash finishes
// what is left. It refuses a store whose engine step is not complete, and it
// never overwrites an entry already kept in pre-format4/.
func FinishActivation(dataRoot string) error {
	m, err := Read(dataRoot)
	if err != nil {
		return err
	}
	if !m.Activated() {
		return fmt.Errorf("marker: %s is not an activated format-4 store (STORE valid %v, MIGRATED valid %v, format %d)",
			dataRoot, m.StoreValid, m.MigratedValid, m.Format)
	}
	entries, err := os.ReadDir(dataRoot)
	if err != nil {
		return fmt.Errorf("marker: %w", err)
	}
	pre := filepath.Join(dataRoot, PreFormat4Dir)
	moved := false
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, LegacyControl) || (name == LegacyControl && e.IsDir()) {
			continue
		}
		if !moved {
			if err := os.MkdirAll(pre, 0o700); err != nil {
				return fmt.Errorf("marker: %w", err)
			}
			moved = true
		}
		dst := filepath.Join(pre, name)
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("marker: %s and %s both exist; refusing to overwrite the kept copy", filepath.Join(dataRoot, name), dst)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("marker: %w", err)
		}
		if err := os.Rename(filepath.Join(dataRoot, name), dst); err != nil {
			return fmt.Errorf("marker: %w", err)
		}
	}
	if moved {
		if err := syncDir(pre); err != nil {
			return err
		}
		if err := syncDir(dataRoot); err != nil {
			return err
		}
	}
	if err := os.Mkdir(filepath.Join(dataRoot, LegacyControl), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("marker: %w", err)
	}
	if info, err := os.Stat(filepath.Join(dataRoot, LegacyControl)); err != nil || !info.IsDir() {
		return fmt.Errorf("marker: %s is not a directory after activation (%v)", filepath.Join(dataRoot, LegacyControl), err)
	}
	return syncDir(dataRoot)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("marker: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("marker: sync %s: %w", dir, err)
	}
	return nil
}

// StoreBytes encodes a STORE marker (§2.2): format 4, layout 1. Only the
// engine writes markers; this is the golden reference for tests and test
// doubles.
func StoreBytes(uuid [16]byte, createdMs int64, gseqFloor uint64, migratedFrom uint32) []byte {
	b := make([]byte, storeLen)
	binary.LittleEndian.PutUint32(b[0:], storeMagic)
	binary.LittleEndian.PutUint16(b[4:], StoreFormat)
	binary.LittleEndian.PutUint16(b[6:], storeLayout)
	copy(b[8:24], uuid[:])
	binary.LittleEndian.PutUint64(b[24:], uint64(createdMs))
	binary.LittleEndian.PutUint64(b[32:], gseqFloor)
	binary.LittleEndian.PutUint32(b[40:], migratedFrom)
	binary.LittleEndian.PutUint32(b[56:], crc32.Checksum(b[:56], castagnoli))
	return b
}

// MigratedBytes encodes a MIGRATED marker (§2.2), as StoreBytes.
func MigratedBytes(uuid [16]byte, writtenMs int64) []byte {
	b := make([]byte, migratedLen)
	binary.LittleEndian.PutUint32(b[0:], migratedMagic)
	binary.LittleEndian.PutUint16(b[4:], StoreFormat)
	copy(b[8:24], uuid[:])
	binary.LittleEndian.PutUint64(b[24:], uint64(writtenMs))
	binary.LittleEndian.PutUint32(b[32:], crc32.Checksum(b[:32], castagnoli))
	return b
}
