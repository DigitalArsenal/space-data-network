// sigstack_native.h: see sigstack_native.c.
#ifndef SDN_SIGSTACK_NATIVE_H
#define SDN_SIGSTACK_NATIVE_H

// Clears a stale "running on the alternate signal stack" mark on the calling
// thread. Call it on a Go thread after native code that may have left
// WasmEdge by a trap jump, before Go code runs on a goroutine stack again.
// Returns 0 when the thread was not marked, 1 when a stale mark was cleared,
// 2 when the thread really is on its signal stack (left alone), -1 when a
// stale mark could not be cleared. Always 0 off darwin.
int sdn_signal_stack_repair(void);

// 1 when the calling thread is marked as running on its alternate signal
// stack, 0 when not, -1 when unknown (tests).
int sdn_signal_stack_marked(void);

// Marks the calling thread as running on its alternate signal stack, the
// state darwin/arm64's longjmp leaves when the jmp_buf word setjmp never
// writes has bit 0 set (tests). With quiet it first blocks SIGURG and SIGPROF
// on the thread, so no signal lands on the wrong stack while the mark stands;
// sdn_signal_stack_unquiet unblocks them. Returns 1 when the mark took, 0 off
// darwin (where nothing is blocked).
int sdn_signal_stack_mark(int quiet);
void sdn_signal_stack_unquiet(void);

#endif
