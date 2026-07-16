package flowcc

import (
	"encoding/binary"
	"syscall"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// Semantic ids for the host imports (1:1 with emshim2.c's H_* enum).
const (
	hAbort = iota
	hExit
	hProcExit
	hFdWrite
	hFdRead
	hFdPread
	hFdSeek
	hFdClose
	hFdFdstatGet
	hEnvironGet
	hEnvironSizesGet
	hMemcpyBig
	hResizeHeap
	hGetHeapMax
	hGetentropy
	hStat64
	hLstat64
	hFstat64
	hNewfstatat
	hOpenat
	hFaccessat
	hReadlinkat
	hGetcwd
	hIoctl
	hGetdents64
	hMkdirat
	hUnlinkat
	hRenameat
	hRmdir
	hFtruncate64
	hFcntl64
	hDateNow
	hGetNow
	hMonotonic
	hTzset
	hGmtime
	hLocaltime
	hStrftime
	hStrftimeL
	hInvokeII
	hInvokeIIII
	hInvokeVI
	hInvokeVII
	hThrowLongjmp
	hDlopen
	hDlsym
	hDlinit
	hCallSighandler
	hStubI      // present, returns 0
	hStubEnoent // present, returns -ENOENT (-2)
	hStubV      // present, void no-op
)

// impDef mirrors emshim2.c's ImpDef: minified name, param count, result kind
// (0=void, 1=i32, 2=f64), and semantic id.
type impDef struct {
	name    string
	nparams int
	reskind int
	sem     int
}

// impTable is the exact 58-entry g_imps[] from emshim2.c, in order.
var impTable = []impDef{
	{"a", 0, 0, hAbort}, {"b", 1, 0, hExit}, {"c", 3, 1, hFcntl64},
	{"d", 1, 1, hFdClose}, {"e", 2, 1, hInvokeII}, {"f", 4, 1, hFdWrite},
	{"g", 0, 2, hGetNow}, {"h", 3, 1, hUnlinkat}, {"i", 4, 1, hInvokeIIII},
	{"j", 4, 1, hFdRead}, {"k", 4, 1, hOpenat}, {"l", 2, 1, hFdFdstatGet},
	{"m", 0, 2, hDateNow}, {"n", 4, 1, hStrftime}, {"o", 1, 0, hProcExit},
	{"p", 2, 1, hEnvironGet}, {"q", 2, 1, hEnvironSizesGet}, {"r", 2, 0, hInvokeVI},
	{"s", 6, 1, hFdPread}, {"t", 5, 1, hFdSeek}, {"u", 3, 1, hFtruncate64},
	{"v", 2, 1, hGetentropy}, {"w", 5, 1, hStrftimeL}, {"x", 3, 1, hIoctl},
	{"y", 0, 0, hThrowLongjmp}, {"z", 1, 1, hResizeHeap}, {"A", 3, 0, hInvokeVII},
	{"B", 4, 1, hStubI /*utimensat*/}, {"C", 0, 1, hGetHeapMax}, {"D", 2, 1, hStubEnoent /*symlink*/},
	{"E", 3, 1, hStubEnoent /*statfs64*/}, {"F", 4, 1, hRenameat}, {"G", 1, 1, hRmdir},
	{"H", 4, 1, hReadlinkat}, {"I", 3, 1, hGetdents64}, {"J", 2, 0, hCallSighandler},
	{"K", 7, 1, hStubEnoent /*mmap_js*/}, {"L", 6, 1, hStubI /*munmap_js*/}, {"M", 3, 1, hMkdirat},
	{"N", 2, 1, hGetcwd}, {"O", 2, 1, hLstat64}, {"P", 4, 1, hNewfstatat},
	{"Q", 2, 1, hStat64}, {"R", 2, 1, hFstat64}, {"S", 3, 1, hStubI /*fchown32*/},
	{"T", 2, 1, hStubI /*fchmod*/}, {"U", 0, 1, hMonotonic}, {"V", 2, 0, hGmtime},
	{"W", 2, 0, hLocaltime}, {"X", 3, 0, hTzset}, {"Y", 3, 0, hMemcpyBig},
	{"Z", 1, 0, hDlinit}, {"_", 2, 1, hDlsym}, {"$", 1, 1, hDlopen},
	{"aa", 3, 1, hStubEnoent /*dup3*/}, {"ba", 2, 1, hStubI /*chmod*/}, {"ca", 1, 1, hStubI /*chdir*/},
	{"da", 4, 1, hFaccessat},
}

// buildHostModule creates the emscripten import module "a" with all 58 host
// functions, each closing over rs.
func buildHostModule(rs *runState) *wasmedge.Module {
	mod := wasmedge.NewModule(hostModuleName)
	for _, def := range impTable {
		params := make([]*wasmedge.ValType, def.nparams)
		for i := range params {
			params[i] = wasmedge.NewValTypeI32()
		}
		var returns []*wasmedge.ValType
		switch def.reskind {
		case 1:
			returns = []*wasmedge.ValType{wasmedge.NewValTypeI32()}
		case 2:
			returns = []*wasmedge.ValType{wasmedge.NewValTypeF64()}
		}
		ft := wasmedge.NewFunctionType(params, returns)
		sem := def.sem
		fn := wasmedge.NewFunction(ft, func(_ interface{}, cf *wasmedge.CallingFrame, in []interface{}) ([]interface{}, wasmedge.Result) {
			return rs.dispatch(sem, cf, in)
		}, nil, 0)
		ft.Release()
		mod.AddFunction(def.name, fn)
	}
	return mod
}

// ---- linear-memory helpers (operate on the callframe's default memory) ----

func memGet(mem *wasmedge.Memory, off, n uint32) ([]byte, bool) {
	if mem == nil || n == 0 {
		return nil, mem != nil
	}
	d, err := mem.GetData(uint(off), uint(n))
	if err != nil || d == nil {
		return nil, false
	}
	out := make([]byte, n)
	copy(out, d)
	return out, true
}

func memSet(mem *wasmedge.Memory, off uint32, b []byte) bool {
	if mem == nil {
		return false
	}
	if len(b) == 0 {
		return true
	}
	return mem.SetData(b, uint(off), uint(len(b))) == nil
}

func rd32(mem *wasmedge.Memory, off uint32) uint32 {
	b, ok := memGet(mem, off, 4)
	if !ok {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func wr32(mem *wasmedge.Memory, off, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	memSet(mem, off, b[:])
}

// cstr reads a NUL-terminated string from guest memory (bounded).
func cstr(mem *wasmedge.Memory, off uint32) string {
	const cap = 4096
	buf := make([]byte, 0, 256)
	for i := uint32(0); i < cap; i++ {
		b, ok := memGet(mem, off+i, 1)
		if !ok || b[0] == 0 {
			break
		}
		buf = append(buf, b[0])
	}
	return string(buf)
}

func i32(v interface{}) int32  { return wasmrtInt32(v) }
func u32(v interface{}) uint32 { return uint32(wasmrtInt32(v)) }

// negErrno maps a Go syscall errno to the negative-errno the guest expects.
func negErrno(err error) int32 {
	if errno, ok := err.(syscall.Errno); ok {
		return -int32(errno)
	}
	return -int32(syscall.EINVAL)
}

// retI32 / retVoid / retF64 build host-callback return slices per reskind.
func retI32(v int32) []interface{}   { return []interface{}{v} }
func retF64(v float64) []interface{} { return []interface{}{v} }

var okI32 = retI32(0)

// trap returns a guest-halting failure result.
func (rs *runState) trap() ([]interface{}, wasmedge.Result) {
	return nil, wasmedge.Result_Fail
}
