//go:build !windows

package wasmrt

// GO PANICS STAY RECOVERABLE AFTER WASMEDGE HAS RUN.
//
// WasmEdge 0.16.4 turns a fault in AOT code into a trap with a process-wide
// signal handler (lib/system/fault.cpp). It installs that handler when the
// first compiled function starts (a refcount goes 0 -> 1) with a plain
// sigaction: no SA_ONSTACK and no chaining to the handler it replaced. When the
// last compiled call returns (1 -> 0) it does not restore that handler either:
// it calls signal(SIGSEGV/SIGBUS/SIGFPE, SIG_DFL). So once the FlatSQL engine
// has answered one query, Go's own handler is gone:
//
//   - a nil dereference anywhere in Go, between engine calls, is SIG_DFL: the
//     process dies with no traceback, and recover() never runs (the stress
//     campaign's read-after-Close, host-02's four SEGV exits);
//   - during an engine call, a Go fault on another thread reaches WasmEdge's
//     handler, which dereferences the thread's (null) fault context and faults
//     again inside the handler.
//
// The fix keeps Go's handler in charge and hands WasmEdge only the faults that
// are WasmEdge's:
//
//  1. Before the Go runtime starts, a C constructor installs sdn_fault_forward
//     for the three signals. Go's initsig records whatever handler it finds as
//     the one to forward to (runtime.fwdSig), then installs its own. Go
//     forwards exactly the faults that are not Go's: a fault in C code (a
//     goroutine inside a cgo call, which is where AOT code runs) or on a thread
//     Go did not create (WasmEdge's async and wasi-threads workers). A fault in
//     Go code stays Go's and becomes a panic.
//  2. At init, the Go handler is saved.
//  3. EnsureGoSignalHandling runs one compiled function that never returns: a
//     tiny AOT module whose export calls a host function that blocks forever on
//     a dedicated thread. While it is inside that call WasmEdge's refcount can
//     never reach 0 again, so WasmEdge never resets the handler and never
//     re-installs its own. Inside the call the host function reads WasmEdge's
//     handler (sdn_fault_forward's target) and puts Go's back.
//
// sdn_fault_forward does not call WasmEdge's handler from inside the signal
// handler. It rewrites the interrupted context so that, once the kernel returns
// from the signal, the faulting thread resumes in sdn_fault_redirect on its own
// stack, and WasmEdge's handler jumps from there into the executor: the call
// returns a trap error and no signal frame is abandoned. Jumping out of Go's
// handler instead would leave the thread's state to longjmp: glibc's restores
// no signal mask (Go's handler runs with every signal blocked), and darwin's
// sets the thread's on-signal-stack flag from a jmp_buf word setjmp never
// writes. The last is also why darwin needs WasmEdge patch 03-fault-jmp
// (scripts/build-static-wasmedge.sh) even for this path: WasmEdge's jump from
// sdn_fault_redirect, and its jump for every trap that is not a signal
// (lib/executor/engine/proxy.cpp, emitFault), is the same longjmp. Without the
// patch, synchronous calls clear the flag afterwards (sigstack.go).
//
// A fault inside the image that holds WasmEdge's own code is never a guest's
// AOT code (that lives in anonymous mappings); neither is native code in the
// image holding this file. Such a fault is named on stderr and takes the
// signal's default action. So does a fault on the thread's own stack (guest
// recursion that exhausted it): there is no room to resume in, and on x86-64
// the redirect's store of the return address, made inside this handler with
// every signal blocked, would fault again where darwin retries it forever.
//
// If a Go crash report still stalls, crash_watchdog.go ends the process.

/*
#define _GNU_SOURCE
#include <signal.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <pthread.h>
#include <dlfcn.h>
#include <sys/mman.h>
#if defined(__linux__)
#include <ucontext.h>
#include <link.h>
#endif
#if defined(__APPLE__)
#include <mach-o/loader.h>
#endif

#define SDN_FAULT_NSIG 3
static const int sdn_fault_sigs[SDN_FAULT_NSIG] = { SIGSEGV, SIGBUS, SIGFPE };

typedef void (*sdn_fault_fn)(int, siginfo_t *, void *);

// Go's handler, saved at package init.
static struct sigaction sdn_go_action[SDN_FAULT_NSIG];
static int sdn_go_saved[SDN_FAULT_NSIG];
// WasmEdge's handler, read while a compiled call is live.
static _Atomic(uintptr_t) sdn_wasmedge_fn[SDN_FAULT_NSIG];
// The constructor installed sdn_fault_forward (so Go forwards to it).
static int sdn_forwarder_installed[SDN_FAULT_NSIG];
static atomic_int sdn_fault_notes;

// The executable text of the image that holds WasmEdge's handler. A fault
// there is WasmEdge's own C++ (or, in a static build, any native code linked
// beside it) — never a guest's AOT code, which lives in anonymous mappings.
#define SDN_NATIVE_MAX 16
static uintptr_t sdn_native_lo[SDN_NATIVE_MAX], sdn_native_hi[SDN_NATIVE_MAX];
static atomic_int sdn_native_n;

static int sdn_fault_index(int sig) {
	for (int i = 0; i < SDN_FAULT_NSIG; i++) {
		if (sdn_fault_sigs[i] == sig) {
			return i;
		}
	}
	return -1;
}

static void sdn_fault_puthex(char *buf, int *n, uintptr_t v) {
	static const char digits[] = "0123456789abcdef";
	char tmp[2 * sizeof(uintptr_t)];
	int k = 0;
	do {
		tmp[k++] = digits[v & 0xf];
		v >>= 4;
	} while (v != 0 && k < (int)sizeof tmp);
	buf[(*n)++] = '0';
	buf[(*n)++] = 'x';
	while (k > 0) {
		buf[(*n)++] = tmp[--k];
	}
}

static void sdn_fault_note(int sig, siginfo_t *info, uintptr_t pc, const char *what) {
	// Async-signal-safe: write(2) of a buffer built by hand. Bounded, so a
	// fault loop cannot flood the journal.
	if (atomic_fetch_add(&sdn_fault_notes, 1) >= 32) {
		return;
	}
	char buf[320];
	int n = 0;
	const char *head = "[wasmrt] FATAL signal ";
	for (const char *p = head; *p; p++) buf[n++] = *p;
	buf[n++] = (char)('0' + (sig / 10) % 10);
	buf[n++] = (char)('0' + sig % 10);
	const char *at = " pc ";
	for (const char *p = at; *p; p++) buf[n++] = *p;
	sdn_fault_puthex(buf, &n, pc);
	const char *mid = " addr ";
	for (const char *p = mid; *p; p++) buf[n++] = *p;
	sdn_fault_puthex(buf, &n, info ? (uintptr_t)info->si_addr : 0);
	buf[n++] = ' ';
	for (const char *p = what; *p && n < (int)sizeof buf - 2; p++) buf[n++] = *p;
	buf[n++] = '\n';
	ssize_t w = write(2, buf, (size_t)n);
	(void)w;
}

static int sdn_pc_is_native(uintptr_t pc) {
	int n = atomic_load(&sdn_native_n);
	for (int i = 0; i < n; i++) {
		if (pc >= sdn_native_lo[i] && pc < sdn_native_hi[i]) {
			return 1;
		}
	}
	return 0;
}

// sdn_fault_redirect is where a redirected wasm fault resumes: on the faulting
// thread's own stack, outside any signal handler. WasmEdge's handler jumps from
// here into the executor exactly as it would from a kernel delivery; it never
// returns.
__attribute__((noinline)) static void sdn_fault_redirect(long sig, long code, uintptr_t addr) {
	siginfo_t si;
	memset(&si, 0, sizeof si);
	si.si_signo = (int)sig;
	si.si_code = (int)code;
	si.si_addr = (void *)addr;
	int i = sdn_fault_index((int)sig);
	sdn_fault_fn fn = i < 0 ? NULL : (sdn_fault_fn)atomic_load(&sdn_wasmedge_fn[i]);
	if (fn != NULL) {
		fn((int)sig, &si, NULL);
	}
	abort();
}

// sdn_redirect_context reports the interrupted PC and SP and, with apply,
// makes the interrupted context resume in sdn_fault_redirect(sig, code, addr)
// as if the faulting instruction had called it: below the red zone, 16-byte
// aligned, with the faulting PC as the return address so a backtrace reads
// through. Returns 0 on an architecture it does not know.
static int sdn_redirect_context(void *uctx, uintptr_t *pcOut, uintptr_t *spOut, long sig, long code, uintptr_t addr, int apply) {
	ucontext_t *uc = (ucontext_t *)uctx;
#if defined(__APPLE__) && defined(__aarch64__)
	uintptr_t pc = (uintptr_t)__darwin_arm_thread_state64_get_pc(uc->uc_mcontext->__ss);
	*pcOut = pc;
	*spOut = (uintptr_t)__darwin_arm_thread_state64_get_sp(uc->uc_mcontext->__ss);
	if (apply) {
		uintptr_t sp = (*spOut - 128) & ~(uintptr_t)15;
		uc->uc_mcontext->__ss.__x[0] = (uint64_t)sig;
		uc->uc_mcontext->__ss.__x[1] = (uint64_t)code;
		uc->uc_mcontext->__ss.__x[2] = (uint64_t)addr;
		__darwin_arm_thread_state64_set_sp(uc->uc_mcontext->__ss, sp);
		__darwin_arm_thread_state64_set_lr_fptr(uc->uc_mcontext->__ss, (void *)pc);
		__darwin_arm_thread_state64_set_pc_fptr(uc->uc_mcontext->__ss, (void *)sdn_fault_redirect);
	}
	return 1;
#elif defined(__APPLE__) && defined(__x86_64__)
	uintptr_t pc = (uintptr_t)uc->uc_mcontext->__ss.__rip;
	*pcOut = pc;
	*spOut = (uintptr_t)uc->uc_mcontext->__ss.__rsp;
	if (apply) {
		uintptr_t sp = ((*spOut - 128) & ~(uintptr_t)15) - 8;
		*(uintptr_t *)sp = pc;
		uc->uc_mcontext->__ss.__rsp = sp;
		uc->uc_mcontext->__ss.__rdi = (uint64_t)sig;
		uc->uc_mcontext->__ss.__rsi = (uint64_t)code;
		uc->uc_mcontext->__ss.__rdx = (uint64_t)addr;
		uc->uc_mcontext->__ss.__rip = (uint64_t)(uintptr_t)sdn_fault_redirect;
	}
	return 1;
#elif defined(__linux__) && defined(__x86_64__)
	greg_t *g = uc->uc_mcontext.gregs;
	uintptr_t pc = (uintptr_t)g[REG_RIP];
	*pcOut = pc;
	*spOut = (uintptr_t)g[REG_RSP];
	if (apply) {
		uintptr_t sp = ((*spOut - 128) & ~(uintptr_t)15) - 8;
		*(uintptr_t *)sp = pc;
		g[REG_RSP] = (greg_t)sp;
		g[REG_RDI] = (greg_t)sig;
		g[REG_RSI] = (greg_t)code;
		g[REG_RDX] = (greg_t)addr;
		g[REG_RIP] = (greg_t)(uintptr_t)sdn_fault_redirect;
	}
	return 1;
#elif defined(__linux__) && defined(__aarch64__)
	uintptr_t pc = (uintptr_t)uc->uc_mcontext.pc;
	*pcOut = pc;
	*spOut = (uintptr_t)uc->uc_mcontext.sp;
	if (apply) {
		uintptr_t sp = (*spOut - 128) & ~(uintptr_t)15;
		uc->uc_mcontext.regs[0] = (uint64_t)sig;
		uc->uc_mcontext.regs[1] = (uint64_t)code;
		uc->uc_mcontext.regs[2] = (uint64_t)addr;
		uc->uc_mcontext.regs[30] = (uint64_t)pc;
		uc->uc_mcontext.sp = (uint64_t)sp;
		uc->uc_mcontext.pc = (uint64_t)(uintptr_t)sdn_fault_redirect;
	}
	return 1;
#else
	(void)uc; (void)sig; (void)code; (void)addr; (void)apply;
	*pcOut = 0;
	*spOut = 0;
	return 0;
#endif
}

// sdn_fault_on_stack reports whether a fault address lies within 256 KiB of
// the interrupted SP: an access to the thread's own stack. A guest's linear
// memory never does; WasmEdge reserves it in mappings of its own.
static int sdn_fault_on_stack(uintptr_t addr, uintptr_t sp) {
	const uintptr_t window = (uintptr_t)256 * 1024;
	return sp != 0 && (addr >= sp ? addr - sp : sp - addr) < window;
}

// sdn_fault_forward receives the faults Go does not own: a fault in native
// code, or on a thread Go did not create. A fault in a guest's AOT code is
// handed to WasmEdge by resuming the thread in sdn_fault_redirect (see the file
// comment). Anything else is a crash; it is named on stderr and takes the
// default action.
static void sdn_fault_forward(int sig, siginfo_t *info, void *uctx) {
	int i = sdn_fault_index(sig);
	sdn_fault_fn fn = i < 0 ? NULL : (sdn_fault_fn)atomic_load(&sdn_wasmedge_fn[i]);
	uintptr_t pc = 0, sp = 0;
	uintptr_t addr = info ? (uintptr_t)info->si_addr : 0;
	int canRedirect = uctx != NULL && sdn_redirect_context(uctx, &pc, &sp, 0, 0, 0, 0);
	if (fn == NULL || sdn_pc_is_native(pc)) {
		sdn_fault_note(sig, info, pc, fn == NULL
			? "in native code, before WasmEdge's handler was adopted: default action"
			: "in native code (not guest AOT code): default action");
		signal(sig, SIG_DFL);
		return; // the faulting instruction runs again and takes the default action
	}
	if (sig != SIGFPE && sdn_fault_on_stack(addr, sp)) {
		// No room to resume in, and the x86-64 redirect would store to this
		// stack from inside the handler (see the file comment).
		sdn_fault_note(sig, info, pc, "on the thread's own stack (guest recursion exhausted it): default action");
		signal(sig, SIG_DFL);
		return;
	}
	if (canRedirect) {
		sdn_redirect_context(uctx, &pc, &sp, sig, info ? info->si_code : 0, addr, 1);
		return; // the thread resumes in sdn_fault_redirect
	}
	// An architecture without a redirect: WasmEdge's own way, a jump out of
	// this handler that leaves the thread's signal mask and signal-stack state
	// to that architecture's longjmp. None ships (darwin and Linux on arm64 and
	// x86-64 all redirect).
	fn(sig, info, uctx);
}

__attribute__((constructor)) static void sdn_install_fault_forwarder(void) {
	for (int i = 0; i < SDN_FAULT_NSIG; i++) {
		struct sigaction cur;
		if (sigaction(sdn_fault_sigs[i], NULL, &cur) != 0) {
			continue;
		}
		// The handler word alone decides: after an exec from a Go parent,
		// darwin keeps SA_SIGINFO in the flags of a disposition reset to
		// SIG_DFL.
		if ((uintptr_t)cur.sa_handler != (uintptr_t)SIG_DFL) {
			continue; // somebody else's handler: leave it for Go to forward to
		}
		struct sigaction sa;
		memset(&sa, 0, sizeof sa);
		sa.sa_sigaction = sdn_fault_forward;
		sa.sa_flags = SA_SIGINFO | SA_ONSTACK;
		sigemptyset(&sa.sa_mask);
		if (sigaction(sdn_fault_sigs[i], &sa, NULL) == 0) {
			sdn_forwarder_installed[i] = 1;
		}
	}
}

static uintptr_t sdn_fault_handler_of(struct sigaction *sa) {
	if ((sa->sa_flags & SA_SIGINFO) != 0) {
		return (uintptr_t)sa->sa_sigaction;
	}
	return (uintptr_t)sa->sa_handler;
}

// sdn_save_go_actions records the handlers installed now (Go's, when called
// from package init). Returns how many are Go's own (not the forwarder).
static int sdn_save_go_actions(void) {
	int saved = 0;
	for (int i = 0; i < SDN_FAULT_NSIG; i++) {
		struct sigaction cur;
		if (sigaction(sdn_fault_sigs[i], NULL, &cur) != 0) {
			continue;
		}
		uintptr_t h = sdn_fault_handler_of(&cur);
		if (h == (uintptr_t)SIG_DFL || h == (uintptr_t)SIG_IGN || h == (uintptr_t)sdn_fault_forward) {
			continue;
		}
		sdn_go_action[i] = cur;
		sdn_go_saved[i] = 1;
		saved++;
	}
	return saved;
}

static int sdn_forwarders_installed(void) {
	int n = 0;
	for (int i = 0; i < SDN_FAULT_NSIG; i++) {
		n += sdn_forwarder_installed[i];
	}
	return n;
}

// sdn_add_native_range appends one range, once. Only the pin (before any
// redirect can read the table) writes it.
static void sdn_add_native_range(uintptr_t lo, uintptr_t hi) {
	int n = atomic_load(&sdn_native_n);
	for (int i = 0; i < n; i++) {
		if (sdn_native_lo[i] == lo && sdn_native_hi[i] == hi) {
			return;
		}
	}
	if (n >= SDN_NATIVE_MAX) {
		return;
	}
	sdn_native_lo[n] = lo;
	sdn_native_hi[n] = hi;
	atomic_store(&sdn_native_n, n + 1);
}

#if defined(__linux__)
struct sdn_image_probe {
	uintptr_t addr;
	int found;
};

static int sdn_image_cb(struct dl_phdr_info *info, size_t size, void *data) {
	(void)size;
	struct sdn_image_probe *probe = (struct sdn_image_probe *)data;
	int holds = 0;
	for (int j = 0; j < info->dlpi_phnum; j++) {
		const ElfW(Phdr) *ph = &info->dlpi_phdr[j];
		if (ph->p_type != PT_LOAD) {
			continue;
		}
		uintptr_t lo = (uintptr_t)info->dlpi_addr + (uintptr_t)ph->p_vaddr;
		if (probe->addr >= lo && probe->addr < lo + (uintptr_t)ph->p_memsz) {
			holds = 1;
		}
	}
	if (!holds) {
		return 0;
	}
	for (int j = 0; j < info->dlpi_phnum; j++) {
		const ElfW(Phdr) *ph = &info->dlpi_phdr[j];
		if (ph->p_type == PT_LOAD && (ph->p_flags & PF_X) != 0) {
			uintptr_t lo = (uintptr_t)info->dlpi_addr + (uintptr_t)ph->p_vaddr;
			sdn_add_native_range(lo, lo + (uintptr_t)ph->p_memsz);
		}
	}
	probe->found = 1;
	return 1;
}
#endif

// sdn_note_native_image records the executable text of the image holding
// addr (WasmEdge's handler). Returns how many ranges it recorded.
static int sdn_note_native_image(uintptr_t addr) {
#if defined(__linux__)
	struct sdn_image_probe probe = { addr, 0 };
	dl_iterate_phdr(sdn_image_cb, &probe);
	return atomic_load(&sdn_native_n);
#elif defined(__APPLE__)
	Dl_info info;
	if (dladdr((const void *)addr, &info) == 0 || info.dli_fbase == NULL) {
		return 0;
	}
	const struct mach_header_64 *mh = (const struct mach_header_64 *)info.dli_fbase;
	if (mh->magic != MH_MAGIC_64) {
		return 0;
	}
	const struct load_command *lc = (const struct load_command *)(mh + 1);
	uintptr_t slide = 0;
	int haveSlide = 0;
	// The slide is the header's address minus __TEXT's vmaddr; every
	// executable segment is recorded at vmaddr + slide.
	for (uint32_t k = 0; k < mh->ncmds; k++) {
		if (lc->cmd == LC_SEGMENT_64) {
			const struct segment_command_64 *seg = (const struct segment_command_64 *)lc;
			if (strncmp(seg->segname, "__TEXT", 16) == 0) {
				slide = (uintptr_t)mh - (uintptr_t)seg->vmaddr;
				haveSlide = 1;
			}
		}
		lc = (const struct load_command *)((const char *)lc + lc->cmdsize);
	}
	if (!haveSlide) {
		return 0;
	}
	lc = (const struct load_command *)(mh + 1);
	for (uint32_t k = 0; k < mh->ncmds; k++) {
		if (lc->cmd == LC_SEGMENT_64) {
			const struct segment_command_64 *seg = (const struct segment_command_64 *)lc;
			if ((seg->initprot & VM_PROT_EXECUTE) != 0 && seg->vmsize > 0) {
				uintptr_t lo = (uintptr_t)seg->vmaddr + slide;
				sdn_add_native_range(lo, lo + (uintptr_t)seg->vmsize);
			}
		}
		lc = (const struct load_command *)((const char *)lc + lc->cmdsize);
	}
	return atomic_load(&sdn_native_n);
#else
	(void)addr;
	return 0;
#endif
}

// sdn_adopt_wasmedge_handlers runs INSIDE a compiled WasmEdge call, where
// WasmEdge's handler is installed: it records that handler as the forwarder's
// target and puts Go's handler back. Returns how many signals it adopted.
static int sdn_adopt_wasmedge_handlers(void) {
	int adopted = 0;
	for (int i = 0; i < SDN_FAULT_NSIG; i++) {
		if (!sdn_go_saved[i]) {
			continue;
		}
		struct sigaction cur;
		if (sigaction(sdn_fault_sigs[i], NULL, &cur) != 0) {
			continue;
		}
		uintptr_t h = sdn_fault_handler_of(&cur);
		if (h == (uintptr_t)SIG_DFL || h == (uintptr_t)SIG_IGN || h == sdn_fault_handler_of(&sdn_go_action[i]) ||
			h == (uintptr_t)sdn_fault_forward) {
			continue; // WasmEdge did not install one for this signal
		}
		if (atomic_load(&sdn_native_n) == 0) {
			sdn_note_native_image(h);
			sdn_note_native_image((uintptr_t)sdn_fault_forward);
		}
		atomic_store(&sdn_wasmedge_fn[i], h);
		if (sigaction(sdn_fault_sigs[i], &sdn_go_action[i], NULL) == 0) {
			adopted++;
		}
	}
	return adopted;
}

// sdn_fault_handler_is_go reports whether Go's saved handler is the one
// installed for sig now (tests and the start-up log).
static int sdn_fault_handler_is_go(int sig) {
	int i = sdn_fault_index(sig);
	if (i < 0 || !sdn_go_saved[i]) {
		return 0;
	}
	struct sigaction cur;
	if (sigaction(sig, NULL, &cur) != 0) {
		return 0;
	}
	return sdn_fault_handler_of(&cur) == sdn_fault_handler_of(&sdn_go_action[i]);
}

static int sdn_native_ranges(void) { return atomic_load(&sdn_native_n); }

// sdn_native_bad_store faults in native code (tests: a native crash must not
// be taken for a guest trap). It stores to a PROT_NONE page rather than to
// NULL, which an x86-64 emulator (Rosetta) lets through.
__attribute__((noinline)) static void sdn_native_bad_store(void) {
	volatile int *p = (volatile int *)mmap(NULL, 4096, PROT_NONE, MAP_PRIVATE | MAP_ANON, -1, 0);
	if ((void *)p == MAP_FAILED) {
		abort();
	}
	*p = 1;
}

// sdn_thread_blocks reports whether the calling thread blocks sig.
static int sdn_thread_blocks(int sig) {
	sigset_t cur;
	if (pthread_sigmask(SIG_BLOCK, NULL, &cur) != 0) {
		return -1;
	}
	return sigismember(&cur, sig);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// signalPinWasm is the module whose one export never returns:
//
//	(module
//	  (import "sdn_signal_pin" "hold" (func $hold))
//	  (func (export "pin") call $hold))
var signalPinWasm = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, 0x01, 0x04, 0x01, 0x60,
	0x00, 0x00, 0x02, 0x17, 0x01, 0x0e, 0x73, 0x64, 0x6e, 0x5f, 0x73, 0x69,
	0x67, 0x6e, 0x61, 0x6c, 0x5f, 0x70, 0x69, 0x6e, 0x04, 0x68, 0x6f, 0x6c,
	0x64, 0x00, 0x00, 0x03, 0x02, 0x01, 0x00, 0x07, 0x07, 0x01, 0x03, 0x70,
	0x69, 0x6e, 0x00, 0x01, 0x0a, 0x06, 0x01, 0x04, 0x00, 0x10, 0x00, 0x0b,
}

// goSignalsSaved is how many of SIGSEGV/SIGBUS/SIGFPE had a Go handler at
// package init.
var goSignalsSaved = int(C.sdn_save_go_actions())

var (
	signalPinOnce sync.Once
	signalPinErr  error
	// signalPinHold is never closed: the pin's host call waits on it forever.
	signalPinHold = make(chan struct{})
)

// EnsureGoSignalHandling makes Go's fault handler the process's for good,
// before any WasmEdge AOT code runs (see the file comment). NewModule calls it;
// it is idempotent and cheap after the first call. It costs one parked thread
// and one tiny AOT compile per process. An error means the process still runs
// with WasmEdge's handling, which is what every build before this one did.
// It also arms the crash-report watchdog (crash_watchdog.go).
func EnsureGoSignalHandling() error {
	_ = startCrashWatchdog()
	if signalPinDisabled {
		return errors.New("disabled")
	}
	signalPinOnce.Do(func() {
		started := time.Now()
		signalPinErr = pinFaultHandler()
		if signalPinErr != nil {
			fmt.Fprintf(os.Stderr, "[wasmrt] WARN Go fault handling not pinned (%v): a Go nil dereference after an engine call ends the process without a traceback\n", signalPinErr)
			return
		}
		signalPinTook = time.Since(started)
	})
	return signalPinErr
}

// signalPinDisabled keeps a test child on WasmEdge's own handling (the
// negative control that shows what the pin prevents).
var signalPinDisabled bool

// threadBlocksSignal reports whether the calling OS thread blocks sig; the
// caller must be locked to its thread.
func threadBlocksSignal(sig syscall.Signal) bool {
	return C.sdn_thread_blocks(C.int(sig)) == 1
}

// crashInNativeCode faults in C code (tests).
func crashInNativeCode() { C.sdn_native_bad_store() }

// signalNativeRanges is how many executable ranges of WasmEdge's own image
// the forwarder excludes from redirection.
var signalNativeRanges int

// signalPinTook is how long the pin took (tests report it).
var signalPinTook time.Duration

// GoFaultHandlerInstalled reports whether Go's own handler is the installed
// SIGSEGV handler right now.
func GoFaultHandlerInstalled() bool {
	return C.sdn_fault_handler_is_go(C.int(syscall.SIGSEGV)) != 0
}

func pinFaultHandler() error {
	if goSignalsSaved == 0 {
		return errors.New("no Go fault handler was installed at init")
	}
	if C.sdn_forwarders_installed() == 0 {
		// Go's runtime found some other handler first and forwards to it; a
		// wasm fault in AOT code would not reach WasmEdge through Go.
		return errors.New("the fault forwarder was not installed before the Go runtime started")
	}
	aot, err := compileAOTBytes(signalPinWasm)
	if err != nil {
		return fmt.Errorf("compile the pin module: %w", err)
	}

	vm := wasmedge.NewVM()
	if vm == nil {
		return errors.New("cannot create a WasmEdge VM")
	}
	host := wasmedge.NewModule("sdn_signal_pin")
	if host == nil {
		vm.Release()
		return errors.New("cannot create the pin host module")
	}
	ready := make(chan error, 2)
	hold := func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
		// A compiled call is live on this thread, so WasmEdge's handler is
		// installed and its refcount is at least one until this returns —
		// which it never does.
		if n := int(C.sdn_adopt_wasmedge_handlers()); n == 0 {
			ready <- errors.New("no WasmEdge fault handler was installed inside a compiled call (is the pin module running as AOT?)")
			return nil, wasmedge.Result_Fail
		}
		signalNativeRanges = int(C.sdn_native_ranges())
		ready <- nil
		<-signalPinHold
		return nil, wasmedge.Result_Success
	}
	ft := wasmedge.NewFunctionType(nil, nil)
	fn := wasmedge.NewFunction(ft, hold, nil, 0)
	ft.Release()
	host.AddFunction("hold", fn)
	if err := vm.RegisterModule(host); err != nil {
		host.Release()
		vm.Release()
		return fmt.Errorf("register the pin host module: %w", err)
	}
	if err := vm.LoadWasmBuffer(aot); err != nil {
		vm.Release()
		return fmt.Errorf("load the pin module: %w", err)
	}
	if err := vm.Validate(); err != nil {
		vm.Release()
		return fmt.Errorf("validate the pin module: %w", err)
	}
	if err := vm.Instantiate(); err != nil {
		vm.Release()
		return fmt.Errorf("instantiate the pin module: %w", err)
	}
	go func() {
		// The fault context lives on this thread for the life of the process.
		runtime.LockOSThread()
		_, err := vm.Execute("pin")
		ready <- fmt.Errorf("the pin call returned (%v)", err)
	}()
	select {
	case err := <-ready:
		return err
	case <-time.After(30 * time.Second):
		return errors.New("the pin call did not start within 30s")
	}
}

// compileAOTBytes compiles a module to AOT code in a universal wasm file and
// returns its bytes (WasmEdge installs its fault handler only around compiled
// functions, so the pin must be one).
func compileAOTBytes(wasm []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "sdn-signal-pin-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "module.aot.wasm")
	conf := wasmedge.NewConfigure()
	if conf == nil {
		return nil, errors.New("cannot create a WasmEdge configuration")
	}
	defer conf.Release()
	compiler := wasmedge.NewCompilerWithConfig(conf)
	if compiler == nil {
		return nil, errors.New("WasmEdge AOT compiler unavailable")
	}
	defer compiler.Release()
	if err := compiler.CompileBuffer(wasm, path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
