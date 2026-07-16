package flowcc

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"syscall"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// dispatch is the Go port of emshim2.c's cb(): the single host-function body
// switched on semantic id. mem is the guest's default linear memory for this
// call.
func (rs *runState) dispatch(sem int, cf *wasmedge.CallingFrame, in []interface{}) ([]interface{}, wasmedge.Result) {
	mem := cf.GetMemoryByIndex(0)

	switch sem {
	case hAbort:
		if rs.verbose {
			os.Stderr.WriteString("[flowcc] ABORT called\n")
		}
		return rs.trap()
	case hExit, hProcExit:
		rs.exitCode = int(i32(in[0]))
		rs.exited = true
		return rs.trap()

	case hEnvironSizesGet:
		wr32(mem, u32(in[0]), 0)
		wr32(mem, u32(in[1]), 0)
		return okI32, wasmedge.Result_Success
	case hEnvironGet:
		return okI32, wasmedge.Result_Success

	case hFdWrite:
		fd, iov, cnt, pn := i32(in[0]), u32(in[1]), u32(in[2]), u32(in[3])
		var total uint32
		for i := uint32(0); i < cnt; i++ {
			b := rd32(mem, iov+i*8)
			l := rd32(mem, iov+i*8+4)
			if l > 0 {
				if data, ok := memGet(mem, b, l); ok {
					rs.writeFD(fd, data)
				}
			}
			total += l
		}
		wr32(mem, pn, total)
		return okI32, wasmedge.Result_Success

	case hFdRead:
		fd, iov, cnt, pn := i32(in[0]), u32(in[1]), u32(in[2]), u32(in[3])
		var total uint32
		for i := uint32(0); i < cnt; i++ {
			b := rd32(mem, iov+i*8)
			l := rd32(mem, iov+i*8+4)
			if l == 0 {
				continue
			}
			n := rs.readFD(fd, mem, b, l, -1)
			if n > 0 {
				total += uint32(n)
			}
			if n < int(l) {
				break
			}
		}
		wr32(mem, pn, total)
		return okI32, wasmedge.Result_Success

	case hFdPread:
		fd, iov, cnt := i32(in[0]), u32(in[1]), u32(in[2])
		base := int64(i32(in[3]))
		pn := u32(in[5])
		var total uint32
		for i := uint32(0); i < cnt; i++ {
			b := rd32(mem, iov+i*8)
			l := rd32(mem, iov+i*8+4)
			if l == 0 {
				continue
			}
			n := rs.readFD(fd, mem, b, l, base+int64(total))
			if n > 0 {
				total += uint32(n)
			}
			if n < int(l) {
				break
			}
		}
		wr32(mem, pn, total)
		return okI32, wasmedge.Result_Success

	case hFdSeek:
		fd := i32(in[0])
		offlo := int64(i32(in[1]))
		whence := int(i32(in[3]))
		pnew := u32(in[4])
		if fd <= 2 {
			wr32(mem, pnew, 0)
			wr32(mem, pnew+4, 0)
			return okI32, wasmedge.Result_Success
		}
		r, err := syscall.Seek(int(fd), offlo, whence)
		if err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		wr32(mem, pnew, uint32(r&0xffffffff))
		wr32(mem, pnew+4, uint32(uint64(r)>>32))
		return okI32, wasmedge.Result_Success

	case hFdClose:
		fd := i32(in[0])
		if fd > 2 {
			syscall.Close(int(fd))
		}
		return okI32, wasmedge.Result_Success

	case hFdFdstatGet:
		buf := u32(in[1])
		fdstat := make([]byte, 24)
		fdstat[0] = 2 // __WASI_FILETYPE_CHARACTER_DEVICE
		memSet(mem, buf, fdstat)
		return okI32, wasmedge.Result_Success

	case hOpenat:
		pp := u32(in[1])
		mf := i32(in[2])
		hp := rs.hostPath(cstr(mem, pp))
		hf := mflagsToHost(mf)
		if hf&syscall.O_CREAT != 0 {
			_ = os.MkdirAll(filepath.Dir(hp), 0o755)
		}
		fd, err := syscall.Open(hp, hf, 0o644)
		if rs.verbose {
			os.Stderr.WriteString("[flowcc] openat " + hp + " -> " + itoa(fd) + "\n")
		}
		if err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return retI32(int32(fd)), wasmedge.Result_Success

	case hStat64, hLstat64:
		pp, buf := u32(in[0]), u32(in[1])
		hp := rs.hostPath(cstr(mem, pp))
		var st syscall.Stat_t
		var err error
		if sem == hLstat64 {
			err = syscall.Lstat(hp, &st)
		} else {
			err = syscall.Stat(hp, &st)
		}
		if err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		writeStat(mem, buf, &st)
		return okI32, wasmedge.Result_Success

	case hNewfstatat:
		pp, buf := u32(in[1]), u32(in[2])
		hp := rs.hostPath(cstr(mem, pp))
		var st syscall.Stat_t
		if err := syscall.Stat(hp, &st); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		writeStat(mem, buf, &st)
		return okI32, wasmedge.Result_Success

	case hFstat64:
		fd, buf := i32(in[0]), u32(in[1])
		if fd <= 2 {
			writeCharDevStat(mem, buf, fd)
			return okI32, wasmedge.Result_Success
		}
		var st syscall.Stat_t
		if err := syscall.Fstat(int(fd), &st); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		writeStat(mem, buf, &st)
		return okI32, wasmedge.Result_Success

	case hFaccessat:
		pp := u32(in[1])
		hp := rs.hostPath(cstr(mem, pp))
		if err := syscall.Access(hp, 0 /*F_OK*/); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return okI32, wasmedge.Result_Success

	case hReadlinkat:
		pp, buf, bs := u32(in[1]), u32(in[2]), u32(in[3])
		hp := rs.hostPath(cstr(mem, pp))
		lk := make([]byte, 1024)
		n, err := syscall.Readlink(hp, lk)
		if err != nil || n < 0 {
			return retI32(-22 /*-EINVAL*/), wasmedge.Result_Success
		}
		if uint32(n) > bs {
			n = int(bs)
		}
		memSet(mem, buf, lk[:n])
		return retI32(int32(n)), wasmedge.Result_Success

	case hGetcwd:
		buf := u32(in[0])
		memSet(mem, buf, []byte{'/', 0})
		return retI32(int32(buf)), wasmedge.Result_Success

	case hIoctl:
		return okI32, wasmedge.Result_Success

	case hMkdirat:
		pp := u32(in[1])
		hp := rs.hostPath(cstr(mem, pp))
		if err := syscall.Mkdir(hp, 0o755); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return okI32, wasmedge.Result_Success

	case hUnlinkat:
		pp := u32(in[1])
		hp := rs.hostPath(cstr(mem, pp))
		if err := syscall.Unlink(hp); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return okI32, wasmedge.Result_Success

	case hRenameat:
		p1, p2 := u32(in[1]), u32(in[3])
		a := rs.hostPath(cstr(mem, p1))
		b := rs.hostPath(cstr(mem, p2))
		if err := syscall.Rename(a, b); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return okI32, wasmedge.Result_Success

	case hRmdir:
		pp := u32(in[0])
		hp := rs.hostPath(cstr(mem, pp))
		if err := syscall.Rmdir(hp); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return okI32, wasmedge.Result_Success

	case hFtruncate64:
		fd := i32(in[0])
		length := int64(i32(in[1]))
		if err := syscall.Ftruncate(int(fd), length); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return okI32, wasmedge.Result_Success

	case hFcntl64:
		return okI32, wasmedge.Result_Success

	case hGetdents64:
		return retI32(-38 /*-ENOSYS*/), wasmedge.Result_Success

	case hMemcpyBig:
		d, s, n := u32(in[0]), u32(in[1]), u32(in[2])
		if data, ok := memGet(mem, s, n); ok {
			memSet(mem, d, data)
		}
		return nil, wasmedge.Result_Success

	case hResizeHeap:
		req := u32(in[0])
		if mem != nil {
			cur := uint32(mem.GetPageSize())
			need := (req + 65535) / 65536
			if need > cur {
				_ = mem.GrowPage(uint(need - cur))
			}
		}
		return retI32(1), wasmedge.Result_Success

	case hGetHeapMax:
		return retI32(2147418112), wasmedge.Result_Success

	case hGetentropy:
		b, n := u32(in[0]), u32(in[1])
		buf := make([]byte, n)
		for i := uint32(0); i < n; i++ {
			buf[i] = byte(i * 2654435761)
		}
		memSet(mem, b, buf)
		return okI32, wasmedge.Result_Success

	case hDateNow, hGetNow:
		return retF64(0.0), wasmedge.Result_Success
	case hMonotonic:
		return okI32, wasmedge.Result_Success
	case hTzset, hGmtime, hLocaltime:
		return nil, wasmedge.Result_Success
	case hStrftime, hStrftimeL:
		return okI32, wasmedge.Result_Success

	case hInvokeII:
		return rs.doInvoke(cf, in, 1, true)
	case hInvokeIIII:
		return rs.doInvoke(cf, in, 3, true)
	case hInvokeVI:
		return rs.doInvoke(cf, in, 1, false)
	case hInvokeVII:
		return rs.doInvoke(cf, in, 2, false)
	case hThrowLongjmp:
		rs.longjmp = true
		return nil, wasmedge.Result_Fail

	case hDlopen, hDlsym:
		return okI32, wasmedge.Result_Success
	case hDlinit, hCallSighandler:
		return nil, wasmedge.Result_Success

	case hStubI:
		return okI32, wasmedge.Result_Success
	case hStubEnoent:
		return retI32(-2 /*-ENOENT*/), wasmedge.Result_Success
	case hStubV:
		return nil, wasmedge.Result_Success
	}
	return rs.trap()
}

// writeFD routes a guest fd_write to captured stdout/stderr (fd 1/2) or a real
// host file (fd>2, opened via openat).
func (rs *runState) writeFD(fd int32, data []byte) {
	switch fd {
	case 1:
		rs.stdout = append(rs.stdout, data...)
	case 2:
		rs.stderr = append(rs.stderr, data...)
	default:
		if fd > 2 {
			_, _ = syscall.Write(int(fd), data)
		}
	}
}

// readFD reads up to len(l) bytes from a guest fd into guest memory at off.
// fd 0 (stdin) is always EOF. pos>=0 selects a positional (pread) read.
func (rs *runState) readFD(fd int32, mem *wasmedge.Memory, off, l uint32, pos int64) int {
	if fd == 0 {
		return 0
	}
	buf := make([]byte, l)
	var n int
	var err error
	if pos >= 0 {
		n, err = syscall.Pread(int(fd), buf, pos)
	} else {
		n, err = syscall.Read(int(fd), buf)
	}
	if err != nil || n <= 0 {
		return 0
	}
	memSet(mem, off, buf[:n])
	return n
}

// doInvoke is the Go port of emshim2.c's do_invoke: the emscripten SjLj
// trampoline. It fetches the target funcref from table "ia", saves the guest
// stack, re-enters the guest via the executor, and on a longjmp-flagged trap
// restores the stack + sets the throw flag and returns 0 (all other traps
// propagate).
func (rs *runState) doInvoke(cf *wasmedge.CallingFrame, in []interface{}, nargs int, hasResult bool) ([]interface{}, wasmedge.Result) {
	ex := cf.GetExecutor()
	mod := cf.GetModule()
	if ex == nil || mod == nil {
		return rs.trap()
	}
	rs.invokeCount++

	tab := mod.FindTable(expTable)
	if tab == nil {
		return rs.trap()
	}
	idx := uint(u32(in[0]))
	v, err := tab.GetData(idx)
	if err != nil {
		return rs.trap()
	}
	fref, ok := v.(wasmedge.FuncRef)
	if !ok {
		return rs.trap()
	}
	target := fref.GetRef()
	if target == nil {
		return rs.trap()
	}

	// stackSave -> sp
	var sp interface{} = int32(0)
	if ssf := mod.FindFunction(expStackSave); ssf != nil {
		if r, e := ex.Invoke(ssf); e == nil && len(r) > 0 {
			sp = r[0]
		}
	}

	args := make([]interface{}, nargs)
	for i := 0; i < nargs; i++ {
		args[i] = in[1+i]
	}

	rs.longjmp = false
	ret, invErr := ex.Invoke(target, args...)
	if rs.verbose && invErr != nil {
		os.Stderr.WriteString("[flowcc] invoke idx=" + itoa(int(u32(in[0]))) + " nargs=" + itoa(nargs) +
			" longjmp=" + boolStr(rs.longjmp) + "\n")
	}
	if invErr == nil {
		if hasResult {
			var rv int32
			if len(ret) > 0 {
				rv = i32(ret[0])
			}
			return retI32(rv), wasmedge.Result_Success
		}
		return nil, wasmedge.Result_Success
	}

	if rs.longjmp {
		rs.longjmpCount++
		if srf := mod.FindFunction(expStackRestor); srf != nil {
			_, _ = ex.Invoke(srf, sp)
		}
		if stf := mod.FindFunction(expSetThrew); stf != nil {
			_, _ = ex.Invoke(stf, int32(1), int32(0))
		}
		if hasResult {
			return retI32(0), wasmedge.Result_Success
		}
		return nil, wasmedge.Result_Success
	}

	// A real (non-longjmp) trap: propagate it to halt the guest.
	return nil, wasmedge.Result_Fail
}

// writeStat serializes a host stat into the 104-byte emscripten stat64 layout
// (identical field offsets to emshim2.c's write_stat).
func writeStat(mem *wasmedge.Memory, buf uint32, st *syscall.Stat_t) {
	b := make([]byte, 104)
	binary.LittleEndian.PutUint32(b[0:], uint32(st.Dev))
	binary.LittleEndian.PutUint32(b[8:], uint32(st.Ino))
	binary.LittleEndian.PutUint32(b[12:], uint32(st.Mode))
	binary.LittleEndian.PutUint32(b[16:], uint32(st.Nlink))
	binary.LittleEndian.PutUint32(b[20:], uint32(st.Uid))
	binary.LittleEndian.PutUint32(b[24:], uint32(st.Gid))
	binary.LittleEndian.PutUint32(b[28:], uint32(st.Rdev))
	binary.LittleEndian.PutUint32(b[40:], uint32(uint64(st.Size)&0xffffffff))
	binary.LittleEndian.PutUint32(b[44:], uint32(uint64(st.Size)>>32))
	binary.LittleEndian.PutUint32(b[48:], 4096)
	binary.LittleEndian.PutUint32(b[52:], uint32(st.Blocks))
	memSet(mem, buf, b)
}

// writeCharDevStat serializes a synthetic character-device stat for stdio fds
// (0/1/2), which are captured/redirected rather than backed by real files.
func writeCharDevStat(mem *wasmedge.Memory, buf uint32, fd int32) {
	b := make([]byte, 104)
	binary.LittleEndian.PutUint32(b[0:], 1)                              // dev
	binary.LittleEndian.PutUint32(b[8:], uint32(fd)+1)                   // ino
	binary.LittleEndian.PutUint32(b[12:], uint32(syscall.S_IFCHR)|0o666) // mode
	binary.LittleEndian.PutUint32(b[16:], 1)                             // nlink
	binary.LittleEndian.PutUint32(b[48:], 4096)                          // blksize
	memSet(mem, buf, b)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d [20]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		d[i] = '-'
	}
	return string(d[i:])
}
