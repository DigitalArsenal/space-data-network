// Preloaded into a child test process. It names itself when it loads, and any
// pthread_detach call ends the process with exit status 97 and names itself.
// Detaching a running thread races that thread's exit in glibc through at
// least 2.42 (sourceware bug 19951, fixed on glibc master, December 2025):
// pthread_detach marks the thread detached, the thread exits and frees its
// stack, and pthread_detach then reads the thread descriptor inside that
// stack. A runtime that starts its threads detached never calls it.
#define _GNU_SOURCE
#include <pthread.h>
#include <unistd.h>

__attribute__((constructor)) static void detach_probe_loaded(void) {
  static const char msg[] = "DETACH-PROBE-LOADED\n";
  (void)!write(2, msg, sizeof msg - 1);
}

int pthread_detach(pthread_t thread) {
  (void)thread;
  static const char msg[] = "DETACH-PROBE-CALLED pthread_detach\n";
  (void)!write(2, msg, sizeof msg - 1);
  _exit(97);
}
