//go:build !windows

package wasmrt

// A GO CRASH REPORT ALWAYS ENDS.
//
// Go prints a fatal report by walking every goroutine's stack. If one of them
// holds a return address that is not Go code, the walk dereferences gp.m of a
// goroutine that has no M (runtime/traceback.go:459, Go 1.26) and faults. A
// report printed inside a signal handler runs with every signal blocked, so
// that fault cannot be delivered: Linux kills the process, darwin re-executes
// the faulting load forever. The process then sits at 100% CPU on one thread
// with a partial report and never exits (the 54-minute storage test hang;
// sigstack.go has the chain that put the address there).
//
// The watchdog is a C thread that reads a copy of every crash report
// (runtime/debug.SetCrashOutput). Once a report has begun, if no byte of it
// arrives for crashReportStall while the process still lives, it prints the
// native state of every thread (darwin) and aborts with SIGABRT, which the
// system crash reporter records. A report that keeps printing is never cut
// short, and the thread costs nothing until a report begins. A later
// SetCrashOutput by anyone else closes the pipe and the watchdog exits.

/*
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <pthread.h>
#include <signal.h>
#include <stdint.h>
#include <string.h>
#include <unistd.h>
#if defined(__APPLE__)
#include <mach/mach.h>
#include <mach/mach_vm.h>
#include <mach-o/dyld.h>
#endif

static int sdn_watch_fd = -1;
static int sdn_watch_stall_ms;
static uintptr_t sdn_watch_image; // the executable's load address (darwin)

static void sdn_watch_write(const char *buf, size_t n) {
	while (n > 0) {
		ssize_t w = write(2, buf, n);
		if (w <= 0) {
			if (w < 0 && errno == EINTR) continue;
			return;
		}
		buf += w;
		n -= (size_t)w;
	}
}

typedef struct {
	char buf[1024];
	int n;
} sdn_watch_line;

static void sdn_watch_str(sdn_watch_line *l, const char *s) {
	while (*s && l->n < (int)sizeof l->buf - 1) l->buf[l->n++] = *s++;
}

static void sdn_watch_hex(sdn_watch_line *l, uintptr_t v) {
	char tmp[2 * sizeof(uintptr_t)];
	int k = 0;
	do {
		tmp[k++] = "0123456789abcdef"[v & 0xf];
		v >>= 4;
	} while (v != 0 && k < (int)sizeof tmp);
	sdn_watch_str(l, "0x");
	while (k > 0 && l->n < (int)sizeof l->buf - 1) l->buf[l->n++] = tmp[--k];
}

static void sdn_watch_dec(sdn_watch_line *l, unsigned v) {
	char tmp[12];
	int k = 0;
	do {
		tmp[k++] = (char)('0' + v % 10);
		v /= 10;
	} while (v != 0 && k < (int)sizeof tmp);
	while (k > 0 && l->n < (int)sizeof l->buf - 1) l->buf[l->n++] = tmp[--k];
}

static void sdn_watch_flush(sdn_watch_line *l) {
	l->buf[l->n++] = '\n';
	sdn_watch_write(l->buf, (size_t)l->n);
	l->n = 0;
}

#if defined(__APPLE__)
// sdn_watch_threads prints, for every other thread, its PC, SP, the address
// of its last fault and the return addresses of its frame-pointer chain.
static void sdn_watch_threads(void) {
	sdn_watch_line l = { .n = 0 };
	sdn_watch_str(&l, "[wasmrt]   executable loaded at ");
	sdn_watch_hex(&l, sdn_watch_image);
	sdn_watch_str(&l, " (atos -o <binary> -l <that> <pc>...)");
	sdn_watch_flush(&l);
	thread_act_array_t threads = NULL;
	mach_msg_type_number_t count = 0;
	if (task_threads(mach_task_self(), &threads, &count) != KERN_SUCCESS) {
		sdn_watch_str(&l, "[wasmrt]   task_threads failed");
		sdn_watch_flush(&l);
		return;
	}
	thread_t self = mach_thread_self();
	for (mach_msg_type_number_t i = 0; i < count; i++) {
		thread_t th = threads[i];
		if (th == self) continue;
		thread_suspend(th);
		uintptr_t pc = 0, sp = 0, fp = 0, fault = 0;
		mach_msg_type_number_t n;
#if defined(__aarch64__)
		arm_thread_state64_t st;
		n = ARM_THREAD_STATE64_COUNT;
		if (thread_get_state(th, ARM_THREAD_STATE64, (thread_state_t)&st, &n) != KERN_SUCCESS) continue;
		pc = (uintptr_t)arm_thread_state64_get_pc(st);
		sp = (uintptr_t)arm_thread_state64_get_sp(st);
		fp = (uintptr_t)arm_thread_state64_get_fp(st);
		arm_exception_state64_t es;
		n = ARM_EXCEPTION_STATE64_COUNT;
		if (thread_get_state(th, ARM_EXCEPTION_STATE64, (thread_state_t)&es, &n) == KERN_SUCCESS) fault = (uintptr_t)es.__far;
#elif defined(__x86_64__)
		x86_thread_state64_t st;
		n = x86_THREAD_STATE64_COUNT;
		if (thread_get_state(th, x86_THREAD_STATE64, (thread_state_t)&st, &n) != KERN_SUCCESS) continue;
		pc = (uintptr_t)st.__rip;
		sp = (uintptr_t)st.__rsp;
		fp = (uintptr_t)st.__rbp;
		x86_exception_state64_t es;
		n = x86_EXCEPTION_STATE64_COUNT;
		if (thread_get_state(th, x86_EXCEPTION_STATE64, (thread_state_t)&es, &n) == KERN_SUCCESS) fault = (uintptr_t)es.__faultvaddr;
#endif
		sdn_watch_str(&l, "[wasmrt]   thread ");
		sdn_watch_dec(&l, i);
		sdn_watch_str(&l, " pc ");
		sdn_watch_hex(&l, pc);
		sdn_watch_str(&l, " sp ");
		sdn_watch_hex(&l, sp);
		sdn_watch_str(&l, " last fault ");
		sdn_watch_hex(&l, fault);
		sdn_watch_str(&l, " frames");
		// Frame records are {previous fp, return address} on both
		// architectures; read through the kernel so a bad chain cannot fault.
		for (int k = 0; k < 24 && fp != 0 && (fp & 7) == 0; k++) {
			uintptr_t rec[2];
			mach_vm_size_t got = 0;
			if (mach_vm_read_overwrite(mach_task_self(), (mach_vm_address_t)fp, sizeof rec,
					(mach_vm_address_t)(uintptr_t)rec, &got) != KERN_SUCCESS || got != sizeof rec) break;
			if (rec[1] == 0) break;
			sdn_watch_str(&l, " ");
			sdn_watch_hex(&l, rec[1]);
			if (rec[0] <= fp) break;
			fp = rec[0];
		}
		sdn_watch_flush(&l);
	}
}
#endif

static void sdn_watch_fire(void) {
	sdn_watch_line l = { .n = 0 };
	sdn_watch_str(&l, "[wasmrt] FATAL the Go crash report has printed nothing for ");
	sdn_watch_dec(&l, (unsigned)(sdn_watch_stall_ms / 1000));
	sdn_watch_str(&l, "s and the process is still alive (a fault inside the report with signals blocked retries forever on darwin): native thread state follows, then SIGABRT");
	sdn_watch_flush(&l);
#if defined(__APPLE__)
	sdn_watch_threads();
#endif
	signal(SIGABRT, SIG_DFL);
	sigset_t s;
	sigemptyset(&s);
	sigaddset(&s, SIGABRT);
	pthread_sigmask(SIG_UNBLOCK, &s, NULL);
	raise(SIGABRT);
	_exit(134);
}

static void *sdn_watch_main(void *arg) {
	(void)arg;
	// Asynchronous signals go to Go's threads, never to this one: a handler
	// here would need the runtime this thread exists to outlive. A fault here
	// stays deliverable, so it ends the process instead of looping.
	sigset_t all;
	sigfillset(&all);
	sigdelset(&all, SIGSEGV);
	sigdelset(&all, SIGBUS);
	sigdelset(&all, SIGFPE);
	sigdelset(&all, SIGILL);
	sigdelset(&all, SIGTRAP);
	pthread_sigmask(SIG_BLOCK, &all, NULL);
	char buf[4096];
	int started = 0;
	for (;;) {
		struct pollfd p = { .fd = sdn_watch_fd, .events = POLLIN, .revents = 0 };
		int r = poll(&p, 1, started ? sdn_watch_stall_ms : -1);
		if (r < 0) {
			if (errno == EINTR) continue;
			break;
		}
		if (r == 0) sdn_watch_fire();
		ssize_t n = read(sdn_watch_fd, buf, sizeof buf);
		if (n > 0) {
			started = 1;
			continue;
		}
		if (n < 0 && (errno == EINTR || errno == EAGAIN)) continue;
		break; // the runtime closed its end: the crash output moved elsewhere
	}
	close(sdn_watch_fd);
	return NULL;
}

// sdn_watch_start starts the watchdog thread and returns the write end of its
// pipe, or -1.
static int sdn_watch_start(int stall_ms) {
	int fds[2];
	if (pipe(fds) != 0) return -1;
	fcntl(fds[0], F_SETFD, FD_CLOEXEC);
	fcntl(fds[1], F_SETFD, FD_CLOEXEC);
	sdn_watch_fd = fds[0];
	sdn_watch_stall_ms = stall_ms;
#if defined(__APPLE__)
	sdn_watch_image = (uintptr_t)_dyld_get_image_header(0);
#endif
	pthread_attr_t a;
	pthread_attr_init(&a);
	pthread_attr_setdetachstate(&a, PTHREAD_CREATE_DETACHED);
	pthread_t t;
	int rc = pthread_create(&t, &a, sdn_watch_main, NULL);
	pthread_attr_destroy(&a);
	if (rc != 0) {
		close(fds[0]);
		close(fds[1]);
		return -1;
	}
	return fds[1];
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"
)

// crashReportStall is how long a crash report may print nothing before the
// watchdog ends the process. Go's own GOTRACEBACK=crash relay waits up to 10 s
// between lines; a report that stalls longer is not going to finish.
var crashReportStall = 30 * time.Second

var (
	crashWatchdogOnce sync.Once
	crashWatchdogErr  error
)

// startCrashWatchdog makes a Go crash report that stops printing end the
// process (see the file comment). Idempotent; EnsureGoSignalHandling calls it.
func startCrashWatchdog() error {
	crashWatchdogOnce.Do(func() {
		fd := int(C.sdn_watch_start(C.int(crashReportStall / time.Millisecond)))
		if fd < 0 {
			crashWatchdogErr = errors.New("cannot start the crash-report watchdog thread")
			return
		}
		w := os.NewFile(uintptr(fd), "sdn-crash-watchdog")
		// SetCrashOutput keeps its own duplicate; closing ours leaves the
		// watchdog's pipe open until the runtime lets go of it.
		crashWatchdogErr = debug.SetCrashOutput(w, debug.CrashOptions{})
		w.Close()
		if crashWatchdogErr != nil {
			fmt.Fprintf(os.Stderr, "[wasmrt] WARN crash-report watchdog not armed (%v): a report that stalls is not ended\n", crashWatchdogErr)
		}
	})
	return crashWatchdogErr
}
