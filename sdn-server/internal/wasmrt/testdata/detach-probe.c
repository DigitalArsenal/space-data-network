// Preloaded into a child test process. Once SDN_DETACH_PROBE_ARMED is set in
// the environment, any pthread_detach call ends the process with exit status
// 97 and names itself; before that it detaches as usual. Detaching a running
// thread races that thread's exit on glibc before 2.34 (sourceware bug
// 19951): pthread_detach marks the thread detached, the thread exits and
// frees its stack, and pthread_detach then reads the thread descriptor inside
// that stack. A runtime that starts its threads detached never calls it.
#define _GNU_SOURCE
#include <dlfcn.h>
#include <pthread.h>
#include <stdlib.h>
#include <unistd.h>

int pthread_detach(pthread_t thread) {
  if (getenv("SDN_DETACH_PROBE_ARMED") != NULL) {
    static const char msg[] = "DETACH-PROBE pthread_detach called\n";
    (void)!write(2, msg, sizeof msg - 1);
    _exit(97);
  }
  int (*next)(pthread_t) = (int (*)(pthread_t))dlsym(RTLD_NEXT, "pthread_detach");
  if (next == NULL) {
    static const char msg[] = "DETACH-PROBE no pthread_detach to forward to\n";
    (void)!write(2, msg, sizeof msg - 1);
    _exit(98);
  }
  return next(thread);
}
