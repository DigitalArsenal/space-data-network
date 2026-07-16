package flowcc

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// overlay is a two-layer guest filesystem: a per-Run writable scratch (the
// "upper" layer) stacked over an optional shared read-only sysroot (the "lower"
// layer). Every guest path resolves scratch-first: if it exists in the scratch
// it is served there, otherwise, if it falls under the sysroot mount point, it
// reads through to the sysroot. The scratch therefore SHADOWS the sysroot, and
// creates/writes/removes always land in the scratch so the sysroot is never
// mutated. Because the sysroot is only ever opened O_RDONLY, one sysroot
// directory safely backs many concurrent overlays (one scratch per Run).
//
// The sysroot is mounted at the guest prefix sysrootMount ("/sysroot"), so the
// compiler is driven with --sysroot=/sysroot. This keeps guest paths clean
// single-slash strings; driving with --sysroot=/ instead makes the compiler
// build "//include/..." double-slash paths, which its own filename-keyed header
// cache handles inconsistently (re-searching / mis-erroring on system headers).
// The mount point does not affect output bytes — only the files' contents do —
// so byte-for-byte parity with emception is unchanged.
type overlay struct {
	scratch string // host path of the per-Run writable root (required)
	sysroot string // host path of the shared read-only sysroot ("" = none)
}

// sysrootMount is the guest path the read-only sysroot is mounted at.
const sysrootMount = "/sysroot"

// oAccMode masks the POSIX access-mode bits (O_RDONLY/O_WRONLY/O_RDWR) out of a
// set of open flags. It is 3 on every POSIX platform.
const oAccMode = 0x3

// contain joins base + guest so the result can never escape base. The guest
// path is first made absolute and cleaned, which collapses any ".." that would
// climb above the guest root (e.g. "/../etc/passwd" -> "/etc/passwd"), then it
// is joined under base. ok is false only if the defense-in-depth prefix check
// still trips (it should not, given the clean).
func contain(base, guest string) (host string, ok bool) {
	clean := filepath.Clean("/" + guest)
	joined := filepath.Join(base, clean)
	if joined != base && !strings.HasPrefix(joined, base+string(os.PathSeparator)) {
		return "", false
	}
	return joined, true
}

// scratchPath maps a guest path into the writable scratch layer.
func (o *overlay) scratchPath(guest string) (string, bool) {
	return contain(o.scratch, guest)
}

// sysrootPath maps a guest path under the sysrootMount prefix into the
// read-only sysroot layer. Paths outside the mount point never resolve to the
// sysroot (they belong to the scratch alone).
func (o *overlay) sysrootPath(guest string) (string, bool) {
	if o.sysroot == "" {
		return "", false
	}
	clean := filepath.Clean("/" + guest)
	if clean != sysrootMount && !strings.HasPrefix(clean, sysrootMount+"/") {
		return "", false
	}
	return contain(o.sysroot, strings.TrimPrefix(clean, sysrootMount))
}

// resolveExisting returns the host path of the FIRST layer in which guest
// exists (scratch shadows sysroot), whether that layer is writable, and ok.
// Existence is probed with Lstat so that even a (hypothetical) dangling symlink
// counts as present and is served from the layer that holds it.
func (o *overlay) resolveExisting(guest string) (host string, writable, ok bool) {
	if sp, c := o.scratchPath(guest); c {
		if _, err := os.Lstat(sp); err == nil {
			return sp, true, true
		}
	}
	if yp, c := o.sysrootPath(guest); c {
		if _, err := os.Lstat(yp); err == nil {
			return yp, false, true
		}
	}
	return "", false, false
}

// dirEnt is one merged directory entry: a name and its Linux d_type
// (DT_DIR=4, DT_CHR=2, DT_REG=8, DT_LNK=10).
type dirEnt struct {
	name  string
	dtype byte
}

// readMergedDir returns the union of the entries of guest in both layers, with
// the scratch shadowing the sysroot (a name present in both appears once, typed
// from the scratch). "." and ".." lead the list, matching the kernel/emscripten
// getdents64 stream. It is used by getdents64 so that a directory that exists
// only in (or is extended by) the read-only sysroot still enumerates correctly
// — which is how clang discovers the libc++ "v1" include subdirectory.
func (o *overlay) readMergedDir(guest string) []dirEnt {
	seen := map[string]bool{".": true, "..": true}
	entries := []dirEnt{{".", 4}, {"..", 4}}
	add := func(hostDir string) {
		des, err := os.ReadDir(hostDir)
		if err != nil {
			return
		}
		for _, de := range des {
			if seen[de.Name()] {
				continue
			}
			seen[de.Name()] = true
			entries = append(entries, dirEnt{de.Name(), direntType(de.Type())})
		}
	}
	if sp, ok := o.scratchPath(guest); ok { // upper layer shadows lower
		add(sp)
	}
	if yp, ok := o.sysrootPath(guest); ok {
		add(yp)
	}
	return entries
}

// direntType maps a Go FileMode to the Linux getdents64 d_type byte.
func direntType(m os.FileMode) byte {
	switch {
	case m&os.ModeDir != 0:
		return 4 // DT_DIR
	case m&os.ModeSymlink != 0:
		return 10 // DT_LNK
	case m&os.ModeCharDevice != 0:
		return 2 // DT_CHR
	default:
		return 8 // DT_REG
	}
}

// open applies overlay precedence to an openat: any open that can create or
// mutate (O_CREAT, or a non-RDONLY access mode) targets the scratch, creating
// parent directories as needed; a pure read-only open resolves scratch-first
// then reads through to the sysroot. The returned fd is a real host fd.
func (o *overlay) open(guest string, hostFlags int, mode uint32) (int, error) {
	writing := hostFlags&syscall.O_CREAT != 0 || hostFlags&oAccMode != 0
	if writing {
		sp, ok := o.scratchPath(guest)
		if !ok {
			return -1, syscall.EACCES
		}
		if hostFlags&syscall.O_CREAT != 0 {
			_ = os.MkdirAll(filepath.Dir(sp), 0o755)
		}
		return syscall.Open(sp, hostFlags, mode)
	}
	host, _, ok := o.resolveExisting(guest)
	if !ok {
		return -1, syscall.ENOENT
	}
	return syscall.Open(host, hostFlags, mode)
}
