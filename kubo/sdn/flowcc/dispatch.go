package flowcc

import (
	"encoding/binary"
	"os"
	"syscall"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// dirCursor is an open directory stream: the merged overlay entry list (built
// once at openat) plus a cursor into it that getdents64 advances.
type dirCursor struct {
	entries []dirEnt
	pos     int
}

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
		delete(rs.dirs, fd)
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
		guest := cstr(mem, pp)
		hf := mflagsToHost(mf)
		fd, err := rs.ov.open(guest, hf, 0o644)
		if rs.verbose {
			os.Stderr.WriteString("[flowcc] openat " + guest + " -> " + itoa(fd) + "\n")
		}
		if err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		// Track directory streams so getdents64 can serve a MERGED overlay
		// listing (both layers), independent of which layer the fd points at.
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
			rs.dirs[int32(fd)] = &dirCursor{entries: rs.ov.readMergedDir(guest)}
		}
		return retI32(int32(fd)), wasmedge.Result_Success

	case hStat64, hLstat64:
		pp, buf := u32(in[0]), u32(in[1])
		hp, _, ok := rs.ov.resolveExisting(cstr(mem, pp))
		if !ok {
			return retI32(-weENOENT), wasmedge.Result_Success
		}
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
		hp, _, ok := rs.ov.resolveExisting(cstr(mem, pp))
		if !ok {
			return retI32(-weENOENT), wasmedge.Result_Success
		}
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
		hp, _, ok := rs.ov.resolveExisting(cstr(mem, pp))
		if !ok {
			return retI32(-weENOENT), wasmedge.Result_Success
		}
		if err := syscall.Access(hp, 0 /*F_OK*/); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return okI32, wasmedge.Result_Success

	case hReadlinkat:
		pp, buf, bs := u32(in[1]), u32(in[2]), u32(in[3])
		hp, _, ok := rs.ov.resolveExisting(cstr(mem, pp))
		if !ok {
			return retI32(-weENOENT), wasmedge.Result_Success
		}
		lk := make([]byte, 1024)
		n, err := syscall.Readlink(hp, lk)
		if err != nil || n < 0 {
			return retI32(-weEINVAL), wasmedge.Result_Success
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
		hp, ok := rs.ov.scratchPath(cstr(mem, pp))
		if !ok {
			return retI32(-weEACCES), wasmedge.Result_Success
		}
		if err := syscall.Mkdir(hp, 0o755); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return okI32, wasmedge.Result_Success

	case hUnlinkat:
		// Mutations only ever affect the writable scratch; the sysroot is
		// read-only, so a path present only in the sysroot yields ENOENT here.
		pp := u32(in[1])
		hp, ok := rs.ov.scratchPath(cstr(mem, pp))
		if !ok {
			return retI32(-weEACCES), wasmedge.Result_Success
		}
		if err := syscall.Unlink(hp); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return okI32, wasmedge.Result_Success

	case hRenameat:
		p1, p2 := u32(in[1]), u32(in[3])
		a, ok1 := rs.ov.scratchPath(cstr(mem, p1))
		b, ok2 := rs.ov.scratchPath(cstr(mem, p2))
		if !ok1 || !ok2 {
			return retI32(-weEACCES), wasmedge.Result_Success
		}
		if err := syscall.Rename(a, b); err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		return okI32, wasmedge.Result_Success

	case hRmdir:
		pp := u32(in[0])
		hp, ok := rs.ov.scratchPath(cstr(mem, pp))
		if !ok {
			return retI32(-weEACCES), wasmedge.Result_Success
		}
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
		// Serve the merged directory stream built at openat, in emscripten's
		// fixed 280-byte dirent layout (d_ino@0, d_off@8, d_reclen@16=280,
		// d_type@18, d_name@19..). Returns bytes written; 0 at end of stream.
		fd := i32(in[0])
		dirp, count := u32(in[1]), u32(in[2])
		dc := rs.dirs[fd]
		if dc == nil {
			return retI32(0), wasmedge.Result_Success
		}
		const structSize = 280
		var pos uint32
		for dc.pos < len(dc.entries) && pos+structSize <= count {
			e := dc.entries[dc.pos]
			var rec [structSize]byte
			binary.LittleEndian.PutUint64(rec[0:], uint64(dc.pos+1))              // d_ino (synthetic, nonzero)
			binary.LittleEndian.PutUint64(rec[8:], uint64((dc.pos+1)*structSize)) // d_off
			binary.LittleEndian.PutUint16(rec[16:], structSize)                   // d_reclen
			rec[18] = e.dtype                                                     // d_type
			nb := []byte(e.name)
			if len(nb) > 255 {
				nb = nb[:255]
			}
			copy(rec[19:], nb) // d_name (NUL padding already zero)
			memSet(mem, dirp+pos, rec[:])
			pos += structSize
			dc.pos++
		}
		return retI32(int32(pos)), wasmedge.Result_Success

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

	case hMmapJs:
		return rs.doMmap(cf, mem, in)

	case hMunmapJs:
		// __munmap_js(addr, len, prot, flags, fd, offset). A writable mapping
		// (PROT_WRITE) is how wasm-ld/clang emit their OUTPUT: they mmap the
		// freshly ftruncated file, fill the buffer, then unmap expecting the
		// bytes to be flushed back. Mirror emscripten's doMsync and write the
		// dirty region to the file; a no-op here silently drops every output.
		addr, length, prot := u32(in[0]), u32(in[1]), i32(in[2])
		fd := i32(in[4])
		off := int64(i32(in[5]))
		if prot&2 != 0 && fd > 2 && length > 0 {
			if data, ok := memGet(mem, addr, length); ok {
				_, _ = syscall.Pwrite(int(fd), data, off)
			}
		}
		return okI32, wasmedge.Result_Success

	case hStubI:
		return okI32, wasmedge.Result_Success
	case hStubEnoent:
		return retI32(-weENOENT), wasmedge.Result_Success
	case hStubV:
		return nil, wasmedge.Result_Success
	}
	return rs.trap()
}

// doMmap is the Go port of emscripten's __mmap_js(len, prot, flags, fd, off,
// allocated, addr). clang maps large source/header files instead of read()ing
// them, AND wasm-ld/clang emit their OUTPUT through a writable mapping, so a
// working mmap is required for any real compile — a stub makes clang see empty
// headers and drops the linker output. It mirrors emscripten's MEMFS.mmap:
// allocate a guest buffer via the guest's own malloc, copy the file's bytes
// [off, off+len) into it, and hand back the pointer. Read mappings leak until
// the VM is torn down (emscripten leaks them too); writable mappings are
// flushed back to the file by the munmap handler (see hMunmapJs).
func (rs *runState) doMmap(cf *wasmedge.CallingFrame, mem *wasmedge.Memory, in []interface{}) ([]interface{}, wasmedge.Result) {
	length := u32(in[0])
	fd := i32(in[3])
	off := int64(i32(in[4]))
	allocatedPtr := u32(in[5])
	addrPtr := u32(in[6])
	if fd <= 2 || length == 0 {
		return retI32(-weENODEV), wasmedge.Result_Success
	}

	// Match emscripten's mmapAlloc: round the region up to a 64KiB page and
	// zero-fill it, then copy the file bytes into the front. The zero tail is
	// what gives clang's null-terminated MemoryBuffer a readable 0 just past the
	// file (clang loads that byte); a tight length-sized buffer instead faults
	// with an out-of-bounds read when the file ends exactly on a page boundary.
	allocLen := (length + 0xffff) &^ uint32(0xffff)
	data := make([]byte, allocLen) // zero-filled tail
	// Read the FULL region: a single pread may return a short count for large
	// files, and a truncated (silently zero-filled) mapping makes clang parse
	// garbage, so loop until length bytes or EOF.
	for got := 0; got < int(length); {
		n, err := syscall.Pread(int(fd), data[got:length], off+int64(got))
		if err != nil {
			return retI32(negErrno(err)), wasmedge.Result_Success
		}
		if n == 0 {
			break // EOF: shorter file, remainder stays zero
		}
		got += n
	}

	// Allocate the destination in the guest heap. This re-enters the guest and
	// can grow linear memory, so re-fetch the memory handle afterwards.
	ptr, ok := rs.guestMalloc(cf, allocLen)
	if !ok || ptr == 0 {
		return retI32(-weENOMEM), wasmedge.Result_Success
	}
	if m2 := cf.GetMemoryByIndex(0); m2 != nil {
		mem = m2
	}
	if !memSet(mem, ptr, data) {
		return retI32(-weEFAULT), wasmedge.Result_Success
	}
	wr32(mem, allocatedPtr, 1) // we allocated the region (munmap may free it)
	wr32(mem, addrPtr, ptr)
	return okI32, wasmedge.Result_Success
}

// guestMalloc invokes the guest's own malloc export from inside a host callback
// (same re-entrant executor mechanism the SjLj invoke_* trampolines use) and
// returns the resulting guest pointer.
func (rs *runState) guestMalloc(cf *wasmedge.CallingFrame, size uint32) (uint32, bool) {
	ex := cf.GetExecutor()
	mod := cf.GetModule()
	if ex == nil || mod == nil {
		return 0, false
	}
	fn := mod.FindFunction(expMalloc)
	if fn == nil {
		return 0, false
	}
	r, err := ex.Invoke(fn, int32(size))
	if err != nil || len(r) == 0 {
		return 0, false
	}
	return u32(r[0]), true
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

// writeStat serializes a host stat into emscripten's struct stat. The struct is
// 112 bytes (matching the glue's doStat), and CRUCIALLY the real ino_t st_ino
// lives at offset 104 — the 32-bit field at offset 8 is only musl's
// __st_ino_truncated. clang derives every file/directory's UniqueID from the
// real st_ino, so a struct that stops at 104 bytes (as an earlier version did)
// leaves st_ino reading uninitialized guest memory, collapsing every file to one
// identity: search-dir dedup then drops all but one include path and every
// header aliases the main source (#include nested too deeply). Writing offset
// 104/108 is what makes real, header-using compiles work.
func writeStat(mem *wasmedge.Memory, buf uint32, st *syscall.Stat_t) {
	b := make([]byte, 112)
	binary.LittleEndian.PutUint32(b[0:], uint32(st.Dev))
	binary.LittleEndian.PutUint32(b[8:], uint32(st.Ino)) // __st_ino_truncated
	binary.LittleEndian.PutUint32(b[12:], uint32(st.Mode))
	binary.LittleEndian.PutUint32(b[16:], uint32(st.Nlink))
	binary.LittleEndian.PutUint32(b[20:], uint32(st.Uid))
	binary.LittleEndian.PutUint32(b[24:], uint32(st.Gid))
	binary.LittleEndian.PutUint32(b[28:], uint32(st.Rdev))
	binary.LittleEndian.PutUint32(b[40:], uint32(uint64(st.Size)&0xffffffff))
	binary.LittleEndian.PutUint32(b[44:], uint32(uint64(st.Size)>>32))
	binary.LittleEndian.PutUint32(b[48:], 4096)
	binary.LittleEndian.PutUint32(b[52:], uint32(st.Blocks))
	binary.LittleEndian.PutUint32(b[104:], uint32(st.Ino)) // real st_ino (low)
	binary.LittleEndian.PutUint32(b[108:], uint32(uint64(st.Ino)>>32))
	memSet(mem, buf, b)
}

// writeCharDevStat serializes a synthetic character-device stat for stdio fds
// (0/1/2), which are captured/redirected rather than backed by real files.
func writeCharDevStat(mem *wasmedge.Memory, buf uint32, fd int32) {
	b := make([]byte, 112)
	binary.LittleEndian.PutUint32(b[0:], 1)                              // dev
	binary.LittleEndian.PutUint32(b[8:], uint32(fd)+1)                   // __st_ino_truncated
	binary.LittleEndian.PutUint32(b[104:], uint32(fd)+1)                 // real st_ino
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
