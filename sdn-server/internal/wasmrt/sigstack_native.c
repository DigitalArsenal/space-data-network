// sigstack_native.c: the calling thread's "on the alternate signal stack" mark.
//
// The kernel keeps, per thread, whether the thread is running on its
// alternate signal stack. While the mark is set, a signal whose handler asks
// for that stack (Go's all do) is delivered on the CURRENT stack instead.
// darwin's longjmp sets or clears the mark from a jmp_buf word (byte offset
// 0xBC on arm64) that setjmp never writes, so it takes whatever the stack held
// there: bit 0 set means marked. WasmEdge 0.16.4 leaves every trap with
// longjmp (lib/system/fault.cpp, Fault::emitFault); the static build's patch
// 03-fault-jmp makes it _longjmp, which carries no signal state. With the
// upstream library the word holds the same value at every trap of a process,
// so a process marks its thread on every trap or on none: measured on
// darwin/arm64, 3 processes of 5, 50 traps of 50 each. The next preemption
// signal then lands on a goroutine stack (signals.go).
//
// Clearing makes the call libc's own longjmp makes (libplatform's
// _sigunaltstack): sigreturn with a null context and the RESET_ALT_STACK
// style, which changes only the mark. The result is read back, never assumed.

#include "sigstack_native.h"

#if defined(__APPLE__)
#include <pthread.h>
#include <signal.h>
#include <stddef.h>
#include <stdint.h>

// libsystem_kernel's sigreturn trap (exported, not declared in the SDK).
extern int __sigreturn(void *uctx, int infostyle, void *token);

#define SDN_UC_SET_ALT_STACK 0x40000000
#define SDN_UC_RESET_ALT_STACK ((int)0x80000000u)

int sdn_signal_stack_marked(void) {
  stack_t ss;
  if (sigaltstack(NULL, &ss) != 0) return -1;
  return (ss.ss_flags & SS_ONSTACK) != 0;
}

int sdn_signal_stack_repair(void) {
  stack_t ss;
  if (sigaltstack(NULL, &ss) != 0) return -1;
  if ((ss.ss_flags & SS_ONSTACK) == 0) return 0;
  // A thread that really runs on its signal stack keeps the mark.
  uintptr_t sp = (uintptr_t)__builtin_frame_address(0);
  uintptr_t lo = (uintptr_t)ss.ss_sp;
  if ((ss.ss_flags & SS_DISABLE) == 0 && sp >= lo && sp < lo + ss.ss_size) return 2;
  (void)__sigreturn(NULL, SDN_UC_RESET_ALT_STACK, NULL);
  return sdn_signal_stack_marked() == 0 ? 1 : -1;
}

static void sdn_signal_stack_async(int how) {
  sigset_t s;
  sigemptyset(&s);
  sigaddset(&s, SIGURG);
  sigaddset(&s, SIGPROF);
  pthread_sigmask(how, &s, NULL);
}

int sdn_signal_stack_mark(int quiet) {
  if (quiet) sdn_signal_stack_async(SIG_BLOCK);
  (void)__sigreturn(NULL, SDN_UC_SET_ALT_STACK, NULL);
  return sdn_signal_stack_marked() == 1;
}

void sdn_signal_stack_unquiet(void) { sdn_signal_stack_async(SIG_UNBLOCK); }

#else

// glibc's longjmp keeps no such state. Nothing is cleared off darwin.
int sdn_signal_stack_marked(void) { return 0; }
int sdn_signal_stack_repair(void) { return 0; }
int sdn_signal_stack_mark(int quiet) {
  (void)quiet;
  return 0;
}
void sdn_signal_stack_unquiet(void) {}

#endif
