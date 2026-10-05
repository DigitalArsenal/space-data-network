package versioninfo

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

// ReleaseTag is the release this binary was cut as (for example
// "v1.0.4-beta.18"), stamped by the release runner through
//
//	-ldflags "-X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=v1.0.4-beta.18"
//
// A development build leaves it empty and reports the suite version.
var ReleaseTag string

// Version is the one version string every surface reports (VER-01): the
// release tag without its "v" when the binary was cut as a release, else the
// suite version from suite.versions.json.
func Version() string {
	if tag := strings.TrimSpace(ReleaseTag); tag != "" {
		return strings.TrimPrefix(tag, "v")
	}
	return SuiteVersion
}

// IsRelease reports whether this binary carries a release tag.
func IsRelease() bool { return strings.TrimSpace(ReleaseTag) != "" }

// STORE FORMAT STAMP (design A5 in docs/architecture/flatsql-partition-store.md;
// flatsql-ps-terabyte §3 "SDN Apply guard", task TB03s; format 4:
// flatsql-sqlite-partitions.md §11 "Format guard").
//
// A rollback, or an install of an older build, must never start a binary whose
// engines cannot open the record store on disk. A partition-store engine
// refuses a STORE.format above its own kFormatMax, and a binary from before
// format 2 recreates empty legacy tables beside an activated store. So every
// build carries, in its own bytes, the highest store format it opens, and
// update Apply and Rollback read that stamp out of the slot's binary, without
// running it, before they activate the slot (internal/update/store_format_guard.go).
// A binary without the stamp was built before it existed and counts as
// Format1StoreFormat.
const (
	// Format1StoreFormat is the legacy store: control.flatsqldb, opened by the
	// embedded flatsql-wasi-noeh.wasm engine.
	Format1StoreFormat = 1

	// PSEngineSHA256 names the partition-store engine PSEngineStoreFormatMax
	// describes: the sha256 of the embedded flatsql-ps-threads.wasm
	// (flatsqlrt.PSThreadsSHA256). Repinning the engine changes that constant,
	// and TestEmbeddedEngineStoreFormatIsTheBuildStamp (internal/storage/format2)
	// fails until this pair is re-derived from the new engine.
	PSEngineSHA256 = "aa1ed58de773347c478215a763328f2aca9bca795c0944a85247e53b2433736a"

	// PSEngineStoreFormatMax is that engine's kFormatMax: the highest
	// fsql2/STORE format it opens and the level it writes and raises stores to
	// (flatsql format_level.h; 3 since flatsql 3.6.0, TB03).
	// It is held to the engine, not typed from memory: the storage/format2 test
	// runs the embedded engine and requires that a fresh store is written at
	// exactly this format, that SDN's own STORE reader accepts it, and that the
	// engine refuses a store one level above it.
	PSEngineStoreFormatMax = 3

	// P4EngineSHA256 is the release pin of the format-4 engine (store format
	// 4, the stream engine: per source feed a pure FlatBuffer stream plus a
	// SQLite index file): the sha256 of the published flatsql-p4-threads.wasm
	// (flatsql@3.7.1, its package's wasm/integrity.json), which flatsqlrt
	// embeds. Empty means no format-4 engine is embedded. flatsqlrt refuses to
	// start a binary whose embedded bytes are not this engine (empty bytes
	// with no pin, else bytes with exactly this sha256).
	P4EngineSHA256 = "7ae31439a7e79f9063c9c77ad2df5402d9efec063934b6a34d7837e4e56f0b7e"

	// P4StoreFormat is format 4's fsql4/STORE format: the format a format-4
	// store is at, and the one the format-4 engine opens.
	P4StoreFormat = 4

	// p4Pinned is 1 when this build pins (and so embeds) a format-4 engine,
	// else 0.
	p4Pinned = min(len(P4EngineSHA256), 1)

	// MaxStoreFormat is the highest on-disk store format this build opens: the
	// highest of its embedded engines. Format 4 counts only when the build
	// pins a format-4 engine; without one the stamp stays at the format-2
	// engine's level, so the update guard refuses this build on a format-4
	// store instead of letting it fail at open (rollback, review 2).
	MaxStoreFormat = max(Format1StoreFormat, PSEngineStoreFormatMax, P4StoreFormat*p4Pinned)
)

const (
	storeFormatStampVersion   = 1
	storeFormatStampHeaderLen = 22
)

// storeFormatStamp is MaxStoreFormat as it sits in the binary:
//
//	NUL "SDN.MAX_STORE_FORMAT" NUL | stamp version (1) | format | ^format
//
// A package-level array of constants is laid out verbatim in the binary's data
// section, and init reads it, so the linker keeps it in every binary that links
// this package. The scanner takes its needle from this array, so no second copy
// of the header exists in the binary to be mistaken for a stamp.
var storeFormatStamp = [...]byte{
	0x00, 'S', 'D', 'N', '.', 'M', 'A', 'X', '_', 'S', 'T', 'O', 'R', 'E', '_', 'F', 'O', 'R', 'M', 'A', 'T', 0x00,
	storeFormatStampVersion,
	byte(MaxStoreFormat),
	^byte(MaxStoreFormat),
}

func init() {
	// Reading the stamp at run time is what keeps it in the binary; checking it
	// catches an edit that lets the array and MaxStoreFormat disagree.
	if got := storeFormatStamp[storeFormatStampHeaderLen+1]; int(got) != MaxStoreFormat {
		panic(fmt.Sprintf("versioninfo: the store-format stamp says %d, MaxStoreFormat is %d", got, MaxStoreFormat))
	}
}

// StoreFormatStamp returns the stamp bytes of a build that opens store formats
// up to format. StoreFormatStamp(MaxStoreFormat) is this build's own; tests and
// tooling use other values to stand in for other builds.
func StoreFormatStamp(format int) []byte {
	if format < 1 || format > 255 {
		panic(fmt.Sprintf("versioninfo: store format %d is outside 1..255", format))
	}
	out := append([]byte(nil), storeFormatStamp[:storeFormatStampHeaderLen]...)
	return append(out, storeFormatStampVersion, byte(format), ^byte(format))
}

// ReadStoreFormatStamp scans a binary for the store-format stamp. found is
// false for a binary built before the stamp existed. When the bytes carry more
// than one valid stamp, the lowest format wins.
func ReadStoreFormatStamp(r io.Reader) (format int, found bool, err error) {
	header := storeFormatStamp[:storeFormatStampHeaderLen]
	need := len(storeFormatStamp)
	buf := make([]byte, 1<<20+need)
	keep := 0
	for {
		n, readErr := io.ReadFull(r, buf[keep:])
		data := buf[:keep+n]
		for i := 0; ; {
			j := bytes.Index(data[i:], header)
			if j < 0 {
				break
			}
			at := i + j
			if at+need > len(data) {
				break // completed by the next read, which carries these bytes over
			}
			tail := data[at+storeFormatStampHeaderLen : at+need]
			if tail[0] == storeFormatStampVersion && tail[1] >= 1 && tail[2] == ^tail[1] {
				if !found || int(tail[1]) < format {
					format = int(tail[1])
				}
				found = true
			}
			i = at + 1
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			return format, found, nil
		}
		if readErr != nil {
			return 0, false, readErr
		}
		// A stamp cut by the read boundary has at most need-1 bytes on this
		// side of it; carry those over. A complete stamp never fits in them, so
		// nothing is counted twice.
		keep = min(need-1, len(data))
		copy(buf, data[len(data)-keep:])
	}
}

// ReadStoreFormatStampFile is ReadStoreFormatStamp over a file.
func ReadStoreFormatStampFile(path string) (format int, found bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	return ReadStoreFormatStamp(f)
}
