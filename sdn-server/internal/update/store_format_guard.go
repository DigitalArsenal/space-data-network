package update

// THE STORE-FORMAT GUARD (design A5 in docs/architecture/flatsql-partition-store.md;
// flatsql-ps-terabyte §3 "SDN Apply guard", task TB03s).
//
// Apply and Rollback swap in another build's binary, and that binary then opens
// the record store. If the store is at a format the binary's engines do not
// know, the new process refuses the store (a partition-store engine at a lower
// kFormatMax calls it corrupt) or does worse (a binary from before format 2
// recreates empty legacy tables beside an activated store). A rollback would
// then turn a bad update into a node that cannot serve at all.
//
// So every build stamps the highest store format it opens into its own bytes
// (versioninfo.MaxStoreFormat), and before a slot is activated this file reads:
//
//   - the store's on-disk format, from its markers (ReadStoreFormat), and
//   - the slot binary's stamp, by scanning the binary without running it; a
//     binary built before the stamp existed carries none and counts as
//     format 1, which is exactly what it opens.
//
// A slot whose stamp is below the store's format is refused: nothing is
// swapped, the ledger records nothing, and the running build stays.
//
// A FORWARD UPDATE ON TODAY'S FLEET IS NEVER REFUSED. Every store that is not
// an activated format-2 store or a format-4 store reads as format 1, every
// binary opens at least format 1, and for those stores the guard returns
// before it reads a single byte of the payload. The binary scan only runs on
// a format-2 or format-4 store.
//
// FORMAT 4 (stack design flatsql-sqlite-partitions.md §11 "Format guard";
// build-out contract §2.4) is read FIRST, through storage/format4/marker,
// which links no engine: any fsql4/MIGRATED or fsql4/STORE, whatever its
// content, makes the store format 4, so a format-4 store refuses every build
// stamped below 4 and every unstamped build from the moment its migration
// writes its first marker.

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	logging "github.com/ipfs/go-log/v2"
	"github.com/klauspost/compress/zstd"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacedatanetwork/sdn-server/internal/metrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

var log = logging.Logger("sdn-update")

// storeFormatRefusals counts the activations the guard refused, by action.
var storeFormatRefusals = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "sdn",
	Name:      "update_store_format_refusals_total",
	Help:      "Update applies and rollbacks refused because the slot's binary opens only store formats below the record store's on-disk format (A5).",
}, []string{"action"})

func init() {
	metrics.Registry().MustRegister(storeFormatRefusals)
	for _, action := range []string{"apply", "rollback"} {
		storeFormatRefusals.WithLabelValues(action)
	}
}

// The format-2 markers, as flatsql ps/format.h and internal/storage/format2
// write them. They are read here, not through storage/format2, because that
// package links the engine and this one runs inside the update helper.
const (
	storeEngineDir       = "fsql2"
	storeMarkerFile      = "STORE"
	storeRaiseFile       = "STORE.tmp" // the engine's level raise in flight (flatsql open.cpp ratchetStore)
	storeMigratedFile    = "MIGRATED"
	storeLegacyControlDB = "control.flatsqldb"
	storeFileMagic       = 0x32515346 // "FSQ2"
	storeFileLen         = 64
)

var storeFileCRCTable = crc32.MakeTable(crc32.Castagnoli)

// StoreFormat is the record store's on-disk format, read from its markers.
type StoreFormat struct {
	Root string
	// Format is 1 for a format-1 store (and for a directory holding no store
	// yet), else the STORE.format of an activated format-2 store.
	Format int
	// Evidence names the markers that decided Format, for the log line.
	Evidence string
}

// ReadStoreFormat reads the format of the record store rooted at root.
//
// Format 4 comes first: fsql4/MIGRATED or fsql4/STORE present (in any state:
// a migration in progress, an activation a crash cut short, a finished store)
// is format 4. The rules below do not run for it.
//
// A store is format 2 or above once it is ACTIVATED: fsql2/MIGRATED exists
// (store-migrate's last step, and a fresh format-2 store is created with it),
// or control.flatsqldb has been turned into a directory (activation step 2,
// which leaves a pre-format-2 binary unable to open the store). Its level is
// then fsql2/STORE's format field, which ratchets as engines add levels. A
// STORE without either is an unfinished migration: the legacy store is still
// the store, and it is format 1.
func ReadStoreFormat(root string) (StoreFormat, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return StoreFormat{}, errors.New("no store root")
	}
	sf := StoreFormat{Root: root, Format: versioninfo.Format1StoreFormat}
	if m, err := marker.Read(root); err != nil {
		return sf, err
	} else if m.Format4() {
		sf.Format = versioninfo.P4StoreFormat
		sf.Evidence = format4Evidence(m)
		return sf, nil
	}
	migrated, err := pathExists(filepath.Join(root, storeEngineDir, storeMigratedFile))
	if err != nil {
		return sf, err
	}
	controlIsDir := false
	if info, err := os.Stat(filepath.Join(root, storeLegacyControlDB)); err == nil {
		controlIsDir = info.IsDir()
	} else if !errors.Is(err, os.ErrNotExist) {
		return sf, err
	}
	if !migrated && !controlIsDir {
		sf.Evidence = "no activated format-2 store (format-1 layout)"
		return sf, nil
	}
	activated := storeEngineDir + "/" + storeMigratedFile
	if !migrated {
		activated = storeLegacyControlDB + " is a directory (activation begun)"
	}
	format, err := readStoreFileFormat(filepath.Join(root, storeEngineDir, storeMarkerFile))
	if err != nil {
		// The engine raises a store's level by writing STORE.tmp, then
		// rewriting STORE in place: a crash in that rewrite leaves STORE torn
		// and STORE.tmp whole, and the engine's next open finishes the raise.
		// The store is then at STORE.tmp's level (an engine below it refuses
		// the store: the finished raise is the only way on).
		if raised, rerr := readStoreFileFormat(filepath.Join(root, storeEngineDir, storeRaiseFile)); rerr == nil {
			sf.Format = max(raised, 2)
			sf.Evidence = fmt.Sprintf("%s, fsql2/STORE unreadable (%v), fsql2/STORE.tmp format %d (a level raise a crash cut short)",
				activated, err, raised)
			return sf, nil
		}
		// Activated, but STORE cannot be read: no engine opens this store as
		// it stands, and every format-2 build opens at least 2.
		sf.Format = 2
		sf.Evidence = fmt.Sprintf("%s, fsql2/STORE unreadable (%v); taken as format 2", activated, err)
		return sf, nil
	}
	sf.Format = format
	sf.Evidence = fmt.Sprintf("fsql2/STORE format %d, %s", format, activated)
	return sf, nil
}

// format4Evidence names the fsql4 markers that made a store format 4.
func format4Evidence(m marker.Markers) string {
	var present []string
	if m.MigratedPresent {
		present = append(present, marker.Dir+"/"+marker.MigratedFile)
	}
	if m.StorePresent {
		present = append(present, marker.Dir+"/"+marker.StoreFile)
	}
	state := "migration or activation in progress"
	switch {
	case m.Activated() && m.LegacyControlDir:
		state = "activated"
	case m.Activated():
		state = "activated, legacy control database not yet retired"
	}
	return strings.Join(present, " and ") + " present (" + state + ")"
}

// readStoreFileFormat reads the format field of fsql2/STORE: u32 magic "FSQ2",
// u16 format at 4, crc32c of bytes 0..56 at 56, 64 bytes in all.
func readStoreFileFormat(name string) (int, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return 0, err
	}
	if len(b) != storeFileLen || binary.LittleEndian.Uint32(b) != storeFileMagic ||
		binary.LittleEndian.Uint32(b[56:]) != crc32.Checksum(b[:56], storeFileCRCTable) {
		return 0, errors.New("not a valid STORE file")
	}
	format := int(binary.LittleEndian.Uint16(b[4:]))
	if format < 2 {
		return 0, fmt.Errorf("STORE format %d", format)
	}
	return format, nil
}

func pathExists(name string) (bool, error) {
	if _, err := os.Lstat(name); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, err
	}
}

// daemonBinaryPaths are where a bundle carries the daemon: runtime/sdn/ in the
// release layout (whose bin/spacedatanetwork is a launcher script), bin/ in the
// lean fleet layout and on Windows.
var daemonBinaryPaths = []string{
	"runtime/sdn/spacedatanetwork",
	"runtime/sdn/spacedatanetwork.exe",
	"bin/spacedatanetwork",
	"bin/spacedatanetwork.exe",
}

func isDaemonBinaryPath(rel string) bool {
	for _, p := range daemonBinaryPaths {
		if rel == p {
			return true
		}
	}
	return false
}

// slotStamp is what a payload's daemon binaries say about the store formats
// they open.
type slotStamp struct {
	// Format is the lowest stamp among the payload's daemon executables; 1
	// for an executable with no stamp, or for a payload with no executable.
	Format int
	// Binaries are the daemon executables that were scanned.
	Binaries []string
	// Unstamped are the scanned executables that carry no stamp.
	Unstamped []string
}

func (s *slotStamp) add(rel string, format int, found bool) {
	if !found {
		format = versioninfo.Format1StoreFormat
		s.Unstamped = append(s.Unstamped, rel)
	}
	if len(s.Binaries) == 0 || format < s.Format {
		s.Format = format
	}
	s.Binaries = append(s.Binaries, rel)
}

// describe says what the slot opens, for the refusal.
func (s slotStamp) describe() string {
	switch {
	case len(s.Binaries) == 0:
		return fmt.Sprintf("carries no daemon binary (none of %s), so it counts as max_store_format %d",
			strings.Join(daemonBinaryPaths, ", "), s.Format)
	case len(s.Unstamped) > 0:
		return fmt.Sprintf("has a binary with no max_store_format stamp (%s: built before the stamp existed), so it counts as max_store_format %d",
			strings.Join(s.Unstamped, ", "), s.Format)
	default:
		return fmt.Sprintf("has a binary stamped max_store_format %d (%s)", s.Format, strings.Join(s.Binaries, ", "))
	}
}

// executableMagic reports whether head starts an ELF, Mach-O or PE file. The
// release layout's bin/spacedatanetwork is a shell launcher: it is not the
// daemon and carries no stamp, so it must not be scanned as one.
func executableMagic(head []byte) bool {
	if len(head) < 4 {
		return false
	}
	switch binary.BigEndian.Uint32(head) {
	case 0x7f454c46, // ELF
		0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe, // Mach-O
		0xcafebabe: // Mach-O universal
		return true
	}
	return head[0] == 'M' && head[1] == 'Z' // PE
}

// scanExecutable adds r to s when it is an executable.
func (s *slotStamp) scanExecutable(rel string, r io.Reader) error {
	br := bufio.NewReaderSize(r, 64<<10)
	head, err := br.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if !executableMagic(head) {
		return nil
	}
	format, found, err := versioninfo.ReadStoreFormatStamp(br)
	if err != nil {
		return fmt.Errorf("scan %s: %w", rel, err)
	}
	s.add(rel, format, found)
	return nil
}

// stampOfDir reads the stamp of the payload laid out under dir (a rollback
// slot).
func stampOfDir(dir string) (slotStamp, error) {
	var s slotStamp
	for _, rel := range daemonBinaryPaths {
		name := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return s, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		f, err := os.Open(name)
		if err != nil {
			return s, err
		}
		err = s.scanExecutable(rel, f)
		f.Close()
		if err != nil {
			return s, err
		}
	}
	if len(s.Binaries) == 0 {
		s.Format = versioninfo.Format1StoreFormat
	}
	return s, nil
}

// stampOfArchive reads the stamp of a staged bundle archive by streaming it:
// nothing is extracted, so a refusal costs no disk and leaves no trace.
func stampOfArchive(archivePath, format string) (slotStamp, error) {
	var s slotStamp
	if format == "zip" {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return s, err
		}
		defer zr.Close()
		for _, file := range zr.File {
			rel, ok := bundleRelativePath(strings.ReplaceAll(file.Name, "\\", "/"))
			if !ok || !isDaemonBinaryPath(rel) || !file.FileInfo().Mode().IsRegular() {
				continue
			}
			r, err := file.Open()
			if err != nil {
				return s, err
			}
			err = s.scanExecutable(rel, r)
			r.Close()
			if err != nil {
				return s, err
			}
		}
	} else {
		f, err := os.Open(archivePath)
		if err != nil {
			return s, err
		}
		defer f.Close()
		var r io.Reader
		switch format {
		case "tar.gz":
			gz, err := gzip.NewReader(f)
			if err != nil {
				return s, err
			}
			defer gz.Close()
			r = gz
		case "tar.zst":
			zr, err := zstd.NewReader(f)
			if err != nil {
				return s, err
			}
			defer zr.Close()
			r = zr
		default:
			return s, fmt.Errorf("unsupported update bundle format: %s", format)
		}
		tr := tar.NewReader(r)
		for {
			header, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return s, err
			}
			rel, ok := bundleRelativePath(header.Name)
			if !ok || header.Typeflag != tar.TypeReg || !isDaemonBinaryPath(rel) {
				continue
			}
			if err := s.scanExecutable(rel, tr); err != nil {
				return s, err
			}
		}
	}
	if len(s.Binaries) == 0 {
		s.Format = versioninfo.Format1StoreFormat
	}
	return s, nil
}

// bundleRelativePath maps an archive entry to its path under the bundle root:
// archives hold either the bundle root itself or one wrapper directory
// (locateBundleRoot accepts both).
func bundleRelativePath(name string) (string, bool) {
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", false
	}
	if strings.HasPrefix(clean, "bin/") || strings.HasPrefix(clean, "runtime/") {
		return clean, true
	}
	if _, rest, ok := strings.Cut(clean, "/"); ok {
		return rest, true
	}
	return "", false
}

// StoreFormatRefusal is the guard's refusal: activating the slot would start a
// binary that does not open the store on disk.
type StoreFormatRefusal struct {
	Action  string // "apply" or "rollback"
	Target  string // the update id, or the slot's description
	Version string
	// SlotMaxStoreFormat is the highest store format the slot's binary opens.
	SlotMaxStoreFormat int
	Slot               string // what the slot's binaries say (slotStamp.describe)
	Store              StoreFormat
}

func (e *StoreFormatRefusal) Error() string {
	what := "apply update " + e.Target
	if e.Action == "rollback" {
		what = "roll back to " + e.Target
	}
	if v := strings.TrimSpace(e.Version); v != "" && !strings.Contains(e.Target, v) {
		what += " (version " + v + ")"
	}
	return fmt.Sprintf("store-format guard REFUSED to %s: the slot %s, but the record store at %s is format %d (%s). "+
		"That build would refuse or damage the store, so nothing was swapped and the running build stays. "+
		"Running a build that opens only format %d here needs the store restored from a snapshot taken before it reached format %d.",
		what, e.Slot, e.Store.Root, e.Store.Format, e.Store.Evidence, e.SlotMaxStoreFormat, e.Store.Format)
}

// storeToGuard reads the store's format and reports whether a slot has to be
// checked against it at all. A store without a root, one whose markers cannot
// be read, and every format-1 store need no check (every binary opens format
// 1), and for them nothing of the slot is read.
func storeToGuard(action, target, storeRoot string) (StoreFormat, bool) {
	if strings.TrimSpace(storeRoot) == "" {
		log.Warnf("store-format guard: no store root for %s %s; not checked", action, target)
		return StoreFormat{}, false
	}
	store, err := ReadStoreFormat(storeRoot)
	if err != nil {
		log.Warnf("store-format guard: cannot read the store format at %s (%v); %s %s not checked", storeRoot, err, action, target)
		return StoreFormat{}, false
	}
	return store, store.Format > versioninfo.Format1StoreFormat
}

// guardStoreFormat refuses to activate a slot whose binary opens only store
// formats below store's. applies=false from stamp means the slot does not
// replace the daemon binary (a module-targeted update without one), which the
// guard then leaves alone.
func guardStoreFormat(action, target, version string, store StoreFormat, stamp func() (s slotStamp, applies bool, err error)) error {
	s, applies, err := stamp()
	if err != nil {
		// The payload was verified by hash before this point, so an
		// unreadable binary is not a transient; it cannot be shown to open
		// the store, and it is treated as the unstamped binary it may be.
		s = slotStamp{Format: versioninfo.Format1StoreFormat, Unstamped: []string{fmt.Sprintf("unreadable: %v", err)}, Binaries: []string{"?"}}
		applies = true
	}
	if !applies || s.Format >= store.Format {
		return nil
	}
	refusal := &StoreFormatRefusal{
		Action:             action,
		Target:             target,
		Version:            version,
		SlotMaxStoreFormat: s.Format,
		Slot:               s.describe(),
		Store:              store,
	}
	storeFormatRefusals.WithLabelValues(action).Inc()
	log.Error(refusal.Error())
	return refusal
}

// guardCandidateStoreFormat is the Apply side: the staged bundle's binary.
func guardCandidateStoreFormat(storeRoot string, candidate *StagedUpdate) error {
	store, check := storeToGuard("apply", candidate.UpdateID, storeRoot)
	if !check {
		return nil
	}
	return guardStagedCandidate(store, candidate)
}

func guardStagedCandidate(store StoreFormat, candidate *StagedUpdate) error {
	version := ""
	if candidate.Result != nil {
		version = candidate.Result.Version
	}
	return guardStoreFormat("apply", candidate.UpdateID, version, store, func() (slotStamp, bool, error) {
		s, err := stampOfArchive(candidate.BundleFile, candidate.Manifest.Bundle.Format)
		if err != nil {
			return s, true, err
		}
		// A module-targeted update installs only its declared artifacts, so
		// it changes the daemon binary only if it ships one.
		if candidate.Manifest.IsModuleUpdate() && len(s.Binaries) == 0 {
			return s, false, nil
		}
		return s, true, nil
	})
}

// CheckStagedStoreFormat runs the Apply guard against a staged update without
// applying it. Callers that must stop the daemon before Apply (the update
// helper) and the daemon's own signal lane call it first, so a refused update
// never takes a serving node down. Apply runs the same check itself. On a
// format-1 store it reads nothing of the staged update.
func CheckStagedStoreFormat(paths Paths, updateID, storeRoot string) error {
	store, check := storeToGuard("apply", updateID, storeRoot)
	if !check {
		return nil
	}
	dir := filepath.Join(paths.Staged, updateID)
	raw, err := os.ReadFile(filepath.Join(dir, manifestFileName))
	if err != nil {
		return fmt.Errorf("read staged manifest: %w", err)
	}
	manifest, err := ParseManifest(raw)
	if err != nil {
		return err
	}
	bundleName, err := bundleFileName(manifest.Bundle.Format)
	if err != nil {
		return err
	}
	return guardStagedCandidate(store, &StagedUpdate{
		UpdateID:   updateID,
		Dir:        dir,
		Manifest:   manifest,
		BundleFile: filepath.Join(dir, bundleName),
		Result:     &VerifyResult{UpdateID: updateID, Version: manifest.Version},
	})
}
