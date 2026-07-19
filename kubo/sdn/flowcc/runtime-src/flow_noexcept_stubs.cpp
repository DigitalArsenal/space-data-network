// flow_noexcept_stubs.cpp — C++ exception-ABI stubs for the wasi-threads
// (wasm32-wasip1-threads) composed flow runtime.
//
// The wasi-sdk threads libc++ throw paths (std::vector/std::string growth,
// std::thread failure, etc.) reference the C++ exception runtime
// (__cxa_allocate_exception/__cxa_throw/personality). The composed flow is
// EH-free (-fignore-exceptions) and those paths are error paths that never fire
// on the data path, so routing them to std::abort() — a clean, host-visible trap
// — keeps the linked runtime self-sufficient (no undefined env import) without a
// real exception runtime. WEAK so guest-link objects that carry the SAME stubs
// (the OD/provider modules do) compose with no duplicate-symbol conflict, and any
// stronger real definition still wins. This TU is folded into the PREBUILT native
// flow_runtime.threads.o only; the single-thread emscripten runtime is unaffected.
#include <cstdlib>

#define FLOW_WEAK __attribute__((weak))
extern "C" {
FLOW_WEAK void* __cxa_allocate_exception(unsigned long) { std::abort(); }
FLOW_WEAK void __cxa_free_exception(void*) {}
FLOW_WEAK void __cxa_throw(void*, void*, void*) { std::abort(); }
FLOW_WEAK void __cxa_rethrow() { std::abort(); }
FLOW_WEAK void* __cxa_begin_catch(void*) { std::abort(); }
FLOW_WEAK void __cxa_end_catch() {}
FLOW_WEAK void __cxa_call_unexpected(void*) { std::abort(); }
FLOW_WEAK void __cxa_pure_virtual() { std::abort(); }
FLOW_WEAK int __gxx_personality_v0(int, int, unsigned long long, void*, void*) { std::abort(); }
FLOW_WEAK void _Unwind_Resume(void*) { std::abort(); }
}
