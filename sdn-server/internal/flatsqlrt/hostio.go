package flatsqlrt

// hostio.go — the Go host's satisfaction of FlatSQL's SEVEN-import file
// contract (module "env"). This is the whole filesystem the engine has.
//
// WHY THIS FILE EXISTS AT ALL. Until flatsql v1.4.x the engine had NO file I/O
// (measured: the shipped artifact imported six WASI functions, none of them
// path_open), so the Go host opened every database `:memory:` and rebuilt the
// control tables from a journal on every boot — the 5-minute host-01 start.
// flatsql now brings its OWN sqlite3_vfs whose methods call out through seven
// explicit, offset-addressed imports that BOTH lanes satisfy identically: the
// browser shim backs them with chunked key->bytes pages, this file backs them
// with real files. See flatsql docs/STORAGE-DURABILITY.md §3.5 and §6.1.
//
// THE PIN IS A HARD GATE. The engine imports these seven unconditionally, so a
// host that does not register them CANNOT INSTANTIATE the artifact. The
// embedded wasm bump and this wiring land in the same commit or not at all.
//
// SIGNATURE LAW (do not "simplify" this): offsets and sizes cross as f64, never
// i64. Emscripten legalizes i64 at the JS boundary for the browser target and
// not for STANDALONE_WASM, so an i64 would give the same import genuinely
// different signatures in the two lanes. f64 is exact to 2^53 (9 PB).
//
// xAccess and xDelete ride on open FLAGS (PROBE / UNLINK) instead of becoming
// imports eight and nine; both are pure path->status questions that allocate no
// handle.
//
// CONNECTORS ONLY. Nothing here knows what a record, a schema or a catalog is.
// It opens, reads, writes, truncates, syncs, sizes and closes files inside one
// directory, and refuses everything else.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/second-state/WasmEdge-go/wasmedge"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// HostIOModule is the wasm import-module name the seven functions live under.
// It is "env" because that is what the engine's import section says; nothing
// about that is negotiable from this side.
const HostIOModule = "env"

// Open flags — must stay byte-identical to flatsql cpp/include/flatsql/flatsql_io.h.
const (
	ioFlagRead          int32 = 0x0001
	ioFlagWrite         int32 = 0x0002
	ioFlagCreate        int32 = 0x0004
	ioFlagExcl          int32 = 0x0008
	ioFlagTrunc         int32 = 0x0010
	ioFlagDeleteOnClose int32 = 0x0020
	ioFlagProbe         int32 = 0x0040
	ioFlagUnlink        int32 = 0x0080
)

// Status codes — must stay byte-identical to flatsql_io.h. Every one is
// negative; a caller can never confuse a status with a byte count.
const (
	ioErrGeneric    int32 = -1
	ioErrNoEnt      int32 = -2
	ioErrAccess     int32 = -3
	ioErrIO         int32 = -4
	ioErrNoSpace    int32 = -5
	ioErrBadHandle  int32 = -6
	ioStatusSuccess int32 = 0
)

// maxHostIOHandles caps concurrently open guest files. SQLite needs a handful
// (main db, rollback journal, transient index files); a runaway handle leak is
// a guest defect and must surface as an error, not as an exhausted host fd
// table shared with libp2p.
const maxHostIOHandles = 256

// maxHostIOPathLen mirrors the VFS's kMaxPathLen guard; anything longer is a
// guest defect.
const maxHostIOPathLen = 4096

// maxHostIOTransfer bounds one read/write in bytes. The engine chunks at 1 MiB;
// this is the defensive ceiling on a single guest-supplied length so a bogus
// value cannot make the host allocate arbitrarily.
const maxHostIOTransfer = 64 << 20

// hostIOFile is one open guest file.
type hostIOFile struct {
	f             *os.File
	path          string
	deleteOnClose bool
	inUse         bool
}

// HostIO is the rooted file layer behind the seven imports.
//
// FAIL-CLOSED CONFINEMENT. Every path the guest hands us is resolved inside
// `root` and refused if it lands outside. The engine treats paths as opaque
// host-namespace strings (flatsql cpp/src/flatsql_vfs.cpp fsFullPathname is the
// identity function), so the host — and only the host — decides what a path may
// reach. A guest that asks for /etc/shadow gets FLATSQL_IO_ERR_ACCESS.
type HostIO struct {
	// root is the canonical (symlink-resolved) directory every path must stay
	// inside. rawRoot is the caller's spelling of the same directory, kept
	// because macOS hands out /var/folders/... paths whose real location is
	// /private/var/folders/... — the store passes the raw spelling to the
	// engine, and the engine hands that exact string back to us.
	root    string
	rawRoot string

	mu    sync.Mutex
	files []hostIOFile

	// Counters exist so a boot can be explained rather than guessed at: an
	// unexpectedly slow open is either many small reads or a few large ones,
	// and those have different fixes.
	opens   atomic.Int64
	reads   atomic.Int64
	writes  atomic.Int64
	syncs   atomic.Int64
	bytesIn atomic.Int64
	bytesOu atomic.Int64
}

// HostIOStats is a snapshot of the file layer's counters.
type HostIOStats struct {
	Opens      int64
	Reads      int64
	Writes     int64
	Syncs      int64
	BytesRead  int64
	BytesWrote int64
}

// Stats snapshots the counters (lock-free; for logging only).
func (h *HostIO) Stats() HostIOStats {
	if h == nil {
		return HostIOStats{}
	}
	return HostIOStats{
		Opens:      h.opens.Load(),
		Reads:      h.reads.Load(),
		Writes:     h.writes.Load(),
		Syncs:      h.syncs.Load(),
		BytesRead:  h.bytesIn.Load(),
		BytesWrote: h.bytesOu.Load(),
	}
}

// NewHostIO roots a file layer at dir. dir must already exist: creating it here
// would let a typo in a config silently produce a second, empty store.
func NewHostIO(dir string) (*HostIO, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("flatsqlrt: host I/O root is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("flatsqlrt: host I/O root %q: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("flatsqlrt: host I/O root %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("flatsqlrt: host I/O root %q is not a directory", dir)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		canonical = abs
	}
	return &HostIO{root: filepath.Clean(canonical), rawRoot: filepath.Clean(abs)}, nil
}

// Root returns the canonical directory this layer is confined to.
func (h *HostIO) Root() string { return h.root }

// Resolve maps a guest path onto a host path, refusing anything that escapes
// the root. Exported so the store can build the exact string it must hand the
// engine and be sure the host will accept it back.
func (h *HostIO) Resolve(guestPath string) (string, int32) {
	if h == nil {
		return "", ioErrAccess
	}
	if guestPath == "" || len(guestPath) > maxHostIOPathLen {
		return "", ioErrGeneric
	}
	if strings.ContainsRune(guestPath, 0) {
		return "", ioErrGeneric
	}
	candidate := guestPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(h.rawRoot, candidate)
	}
	candidate = filepath.Clean(candidate)

	// Containment is checked against BOTH spellings of the root, then against
	// the canonical location of the deepest existing ancestor. The last check is
	// what stops a symlink planted inside the store from pointing at /etc: a
	// path is only accepted if the directory it will really be created in is
	// inside the root.
	if !withinRoot(candidate, h.rawRoot) && !withinRoot(candidate, h.root) {
		return "", ioErrAccess
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err == nil {
		realDir = filepath.Clean(realDir)
		if !withinRoot(realDir, h.root) && !withinRoot(realDir, h.rawRoot) &&
			realDir != h.root && realDir != h.rawRoot {
			return "", ioErrAccess
		}
	}
	return candidate, ioStatusSuccess
}

// withinRoot reports whether p is root itself or lives beneath it.
func withinRoot(p, root string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(os.PathSeparator))
}

func hostIOStatusFor(err error) int32 {
	switch {
	case err == nil:
		return ioStatusSuccess
	case errors.Is(err, os.ErrNotExist):
		return ioErrNoEnt
	case errors.Is(err, os.ErrPermission):
		return ioErrAccess
	case errors.Is(err, os.ErrExist):
		return ioErrGeneric
	}
	if strings.Contains(err.Error(), "no space left") {
		return ioErrNoSpace
	}
	return ioErrIO
}

// Open implements flatsql_io_open, including the PROBE (xAccess) and UNLINK
// (xDelete) namespace operations that ride on flags.
func (h *HostIO) Open(guestPath string, flags int32) int32 {
	full, status := h.Resolve(guestPath)
	if status != ioStatusSuccess {
		return status
	}

	if flags&ioFlagProbe != 0 {
		if _, err := os.Stat(full); err != nil {
			return ioErrNoEnt
		}
		return ioStatusSuccess
	}
	if flags&ioFlagUnlink != 0 {
		if err := os.Remove(full); err != nil {
			return hostIOStatusFor(err)
		}
		return ioStatusSuccess
	}

	oflags := os.O_RDONLY
	switch {
	case flags&ioFlagWrite != 0 && flags&ioFlagRead != 0:
		oflags = os.O_RDWR
	case flags&ioFlagWrite != 0:
		oflags = os.O_WRONLY
	}
	if flags&ioFlagCreate != 0 {
		oflags |= os.O_CREATE
	}
	if flags&ioFlagExcl != 0 {
		oflags |= os.O_EXCL
	}
	if flags&ioFlagTrunc != 0 {
		oflags |= os.O_TRUNC
	}

	// 0600: the store directory is 0700 and nothing outside this process has
	// any business reading a database page.
	f, err := os.OpenFile(full, oflags, 0o600)
	if err != nil {
		return hostIOStatusFor(err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	slot := hostIOFile{f: f, path: full, deleteOnClose: flags&ioFlagDeleteOnClose != 0, inUse: true}
	for i := range h.files {
		if !h.files[i].inUse {
			h.files[i] = slot
			h.opens.Add(1)
			return int32(i)
		}
	}
	if len(h.files) >= maxHostIOHandles {
		f.Close()
		return ioErrGeneric
	}
	h.files = append(h.files, slot)
	h.opens.Add(1)
	return int32(len(h.files) - 1)
}

func (h *HostIO) slot(handle int32) *hostIOFile {
	if handle < 0 || int(handle) >= len(h.files) {
		return nil
	}
	s := &h.files[handle]
	if !s.inUse {
		return nil
	}
	return s
}

// ReadAt implements flatsql_io_read: offset-addressed, never moves a cursor,
// and a short read at EOF is a SUCCESS returning the byte count (the VFS
// zero-fills the remainder and reports SQLITE_IOERR_SHORT_READ itself).
func (h *HostIO) ReadAt(handle int32, dst []byte, offset int64) int32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.slot(handle)
	if s == nil {
		return ioErrBadHandle
	}
	n, err := s.f.ReadAt(dst, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return hostIOStatusFor(err)
	}
	h.reads.Add(1)
	h.bytesIn.Add(int64(n))
	return int32(n)
}

// WriteAt implements flatsql_io_write. Writing past EOF extends the file and
// the gap reads as zeroes — POSIX pwrite semantics, which is what the browser
// chunked backend emulates and therefore what the engine may assume.
func (h *HostIO) WriteAt(handle int32, src []byte, offset int64) int32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.slot(handle)
	if s == nil {
		return ioErrBadHandle
	}
	n, err := s.f.WriteAt(src, offset)
	if err != nil {
		return hostIOStatusFor(err)
	}
	h.writes.Add(1)
	h.bytesOu.Add(int64(n))
	return int32(n)
}

// Truncate implements flatsql_io_truncate (grow or shrink to exactly size).
func (h *HostIO) Truncate(handle int32, size int64) int32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.slot(handle)
	if s == nil {
		return ioErrBadHandle
	}
	if size < 0 {
		return ioErrGeneric
	}
	if err := s.f.Truncate(size); err != nil {
		return hostIOStatusFor(err)
	}
	return ioStatusSuccess
}

// Sync implements flatsql_io_sync — a real fsync. This lane, unlike the
// browser's chunked backend, can and does give the engine the durability
// barrier it asks for; the mark-after-stream ordering inside the engine is only
// meaningful because this returns 0 exclusively when bytes are on the platter.
func (h *HostIO) Sync(handle int32) int32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.slot(handle)
	if s == nil {
		return ioErrBadHandle
	}
	if err := s.f.Sync(); err != nil {
		return hostIOStatusFor(err)
	}
	h.syncs.Add(1)
	return ioStatusSuccess
}

// Size implements flatsql_io_size. Returns a negative STATUS as a float64 on
// failure, which is why the contract's return type is f64 rather than a
// separate out-parameter.
func (h *HostIO) Size(handle int32) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.slot(handle)
	if s == nil {
		return float64(ioErrBadHandle)
	}
	info, err := s.f.Stat()
	if err != nil {
		return float64(hostIOStatusFor(err))
	}
	return float64(info.Size())
}

// Close implements flatsql_io_close, honouring DELETE_ON_CLOSE. The engine's
// VFS also unlinks such files itself (fsClose), so the removal here is
// belt-and-braces and a missing file is not an error.
func (h *HostIO) Close(handle int32) int32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.slot(handle)
	if s == nil {
		return ioErrBadHandle
	}
	err := s.f.Close()
	path, del := s.path, s.deleteOnClose
	*s = hostIOFile{}
	if del && path != "" {
		_ = os.Remove(path)
	}
	if err != nil {
		return hostIOStatusFor(err)
	}
	return ioStatusSuccess
}

// CloseAll releases every handle still open. Called when the runtime is torn
// down so a discarded engine cannot keep the store's files open.
func (h *HostIO) CloseAll() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.files {
		if !h.files[i].inUse {
			continue
		}
		_ = h.files[i].f.Close()
		if h.files[i].deleteOnClose && h.files[i].path != "" {
			_ = os.Remove(h.files[i].path)
		}
		h.files[i] = hostIOFile{}
	}
}

// hostFuncs returns the seven imports as wasmrt host functions. Names and
// signatures are the contract; a divergence here is not a bug that degrades
// performance, it is a module that will not instantiate.
func (h *HostIO) hostFuncs() []wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	f64 := func() *wasmedge.ValType { return wasmedge.NewValTypeF64() }

	// guestBytes reads len bytes at ptr out of the calling frame's memory.
	guestBytes := func(cf *wasmedge.CallingFrame, ptr uint32, length uint32) ([]byte, bool) {
		mem := cf.GetMemoryByIndex(0)
		if mem == nil {
			return nil, false
		}
		data, err := mem.GetData(uint(ptr), uint(length))
		if err != nil {
			return nil, false
		}
		return data, true
	}

	return []wasmrt.HostFunc{
		{
			Name: "flatsql_io_open",
			Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				ptr := uint32(params[0].(int32))
				pathLen := params[1].(int32)
				flags := params[2].(int32)
				if pathLen <= 0 || pathLen > maxHostIOPathLen {
					return []interface{}{ioErrGeneric}, wasmedge.Result_Success
				}
				raw, ok := guestBytes(cf, ptr, uint32(pathLen))
				if !ok {
					return []interface{}{ioErrGeneric}, wasmedge.Result_Success
				}
				// GetData may hand back a view into linear memory; copy before
				// anything can grow it.
				path := string(append([]byte(nil), raw...))
				return []interface{}{h.Open(path, flags)}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32(), i32(), i32()},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "flatsql_io_read",
			Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				handle := params[0].(int32)
				ptr := uint32(params[1].(int32))
				length := params[2].(int32)
				offset := params[3].(float64)
				if length < 0 || length > maxHostIOTransfer || offset < 0 {
					return []interface{}{ioErrGeneric}, wasmedge.Result_Success
				}
				if length == 0 {
					return []interface{}{ioStatusSuccess}, wasmedge.Result_Success
				}
				buf := make([]byte, length)
				n := h.ReadAt(handle, buf, int64(offset))
				if n <= 0 {
					return []interface{}{n}, wasmedge.Result_Success
				}
				mem := cf.GetMemoryByIndex(0)
				if mem == nil {
					return []interface{}{ioErrIO}, wasmedge.Result_Success
				}
				if err := mem.SetData(buf[:n], uint(ptr), uint(n)); err != nil {
					return []interface{}{ioErrIO}, wasmedge.Result_Success
				}
				return []interface{}{n}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32(), i32(), i32(), f64()},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "flatsql_io_write",
			Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				handle := params[0].(int32)
				ptr := uint32(params[1].(int32))
				length := params[2].(int32)
				offset := params[3].(float64)
				if length < 0 || length > maxHostIOTransfer || offset < 0 {
					return []interface{}{ioErrGeneric}, wasmedge.Result_Success
				}
				if length == 0 {
					return []interface{}{ioStatusSuccess}, wasmedge.Result_Success
				}
				raw, ok := guestBytes(cf, ptr, uint32(length))
				if !ok {
					return []interface{}{ioErrGeneric}, wasmedge.Result_Success
				}
				return []interface{}{h.WriteAt(handle, raw, int64(offset))}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32(), i32(), i32(), f64()},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "flatsql_io_truncate",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				handle := params[0].(int32)
				size := params[1].(float64)
				if size < 0 {
					return []interface{}{ioErrGeneric}, wasmedge.Result_Success
				}
				return []interface{}{h.Truncate(handle, int64(size))}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32(), f64()},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "flatsql_io_sync",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				return []interface{}{h.Sync(params[0].(int32))}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32()},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "flatsql_io_size",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				return []interface{}{h.Size(params[0].(int32))}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32()},
			Returns: []*wasmedge.ValType{f64()},
		},
		{
			Name: "flatsql_io_close",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				return []interface{}{h.Close(params[0].(int32))}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32()},
			Returns: []*wasmedge.ValType{i32()},
		},
	}
}

// refusingHostIO is the file layer a runtime created WITHOUT a root gets: the
// seven imports still exist (or the module cannot instantiate at all), but
// every one of them refuses.
//
// This is the fail-closed half of the design. An ephemeral engine — a query
// sandbox, a test, a flow VM — has no business touching the store directory,
// and the old failure mode this whole task exists to remove was I/O that
// SUCCEEDED against something that was not durable. Refusing is the only answer
// that cannot be mistaken for durability: a disk-backed open simply fails, and
// the caller falls back to :memory: knowingly.
func refusingHostFuncs() []wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	f64 := func() *wasmedge.ValType { return wasmedge.NewValTypeF64() }
	refuseI32 := func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
		return []interface{}{ioErrAccess}, wasmedge.Result_Success
	}
	refuseF64 := func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
		return []interface{}{float64(ioErrAccess)}, wasmedge.Result_Success
	}
	return []wasmrt.HostFunc{
		{Name: "flatsql_io_open", Func: refuseI32, Params: []*wasmedge.ValType{i32(), i32(), i32()}, Returns: []*wasmedge.ValType{i32()}},
		{Name: "flatsql_io_read", Func: refuseI32, Params: []*wasmedge.ValType{i32(), i32(), i32(), f64()}, Returns: []*wasmedge.ValType{i32()}},
		{Name: "flatsql_io_write", Func: refuseI32, Params: []*wasmedge.ValType{i32(), i32(), i32(), f64()}, Returns: []*wasmedge.ValType{i32()}},
		{Name: "flatsql_io_truncate", Func: refuseI32, Params: []*wasmedge.ValType{i32(), f64()}, Returns: []*wasmedge.ValType{i32()}},
		{Name: "flatsql_io_sync", Func: refuseI32, Params: []*wasmedge.ValType{i32()}, Returns: []*wasmedge.ValType{i32()}},
		{Name: "flatsql_io_size", Func: refuseF64, Params: []*wasmedge.ValType{i32()}, Returns: []*wasmedge.ValType{f64()}},
		{Name: "flatsql_io_close", Func: refuseI32, Params: []*wasmedge.ValType{i32()}, Returns: []*wasmedge.ValType{i32()}},
	}
}
