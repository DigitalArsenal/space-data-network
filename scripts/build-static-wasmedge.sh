#!/usr/bin/env bash
# Build WasmEdge (default 0.16.4, override WASMEDGE_VERSION) as a STATIC
# library WITH LLVM linked in, and static-link the spacedatanetwork daemon into
# a single self-contained binary — the way kubo ships one file.
#
# The result depends only on base system libraries (libstdc++, libm, libgcc_s,
# libc). No ~/.wasmedge install, no WASMEDGE_DIR, no LD_LIBRARY_PATH, and
# nothing per-host that can drift from the version the binary was built against.
# That drift is not hypothetical: the AOT cache key includes the libwasmedge
# runtime version, a miss does not fail, and production has twice fallen back to
# the roughly 100x slower interpreter because a host's install did not match.
#
# AOT IS RETAINED, and that is the whole point of linking LLVM statically.
# An earlier revision of this script built with WASMEDGE_USE_LLVM=OFF on the
# premise that "the server runs modules interpreted". That is wrong for the
# FlatSQL engine, which loads a precompiled artifact and is ~100x slower
# without one. In 0.16.4 WASMEDGE_BUILD_AOT_RUNTIME is DEPRECATED AND ALIASED
# to WASMEDGE_USE_LLVM (CMakeLists.txt:69-72), so there is no load-only mode:
# turning LLVM off removes the ability to load an AOT artifact, not just to
# compile one. Verified on the built binary: `prewarm-aot` compiles both
# artifacts in-process and reports we0.16.4, the same cache key the fleet uses.
#
# Cost: the binary is ~200 MB because LLVM is inside it. The upstream shared
# library is 175 MB for exactly the same reason.
#
# Built with clang, not gcc: WasmEdge adds -Werror unconditionally
# (cmake/Helper.cmake:43-48) and gcc-12 fails its own sources on
# -Wmaybe-uninitialized (ast/component/component_type.cpp) and -Warray-bounds
# (executor/coredump.cpp). Upstream builds with clang.
#
# Produces: $OUT (default: sdn-server/spacedatanetwork-static).
#
# Requires: git, cmake, ninja, clang, and the STATIC LLVM archives —
# on Debian/Ubuntu: llvm-16-dev liblld-16-dev libpolly-16-dev libzstd-dev
# zlib1g-dev clang-16. libPolly.a ships separately and the final archive merge
# fails without it. On macOS: Xcode (Apple clang) plus `brew install cmake
# ninja llvm@18 zstd`, and `. scripts/wasmedge-static-env.sh` first; see
# sdn-server/internal/wasmrt/SUBSTRATE.md for the darwin path.
set -euo pipefail

WASMEDGE_VERSION="${WASMEDGE_VERSION:-0.16.4}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${WASMEDGE_STATIC_WORK:-${ROOT}/.wasmedge-static-build}"
SRC="${WORK}/WasmEdge"
STATIC="${WORK}/prefix"          # holds lib/*.a + include/wasmedge/*.h
OUT="${OUT:-${ROOT}/sdn-server/spacedatanetwork-static}"

mkdir -p "$WORK"

# 1. WasmEdge source
if [[ ! -d "$SRC" ]]; then
  git clone --branch "$WASMEDGE_VERSION" --depth 1 --recursive \
    https://github.com/WasmEdge/WasmEdge.git "$SRC"
fi

# THE SDN RUNTIME PATCHES (design A31, A21). Every binary this script links
# carries them, so a host gets the fixed runtime by running the release binary;
# there is no separate per-host library install to forget.
#
#   01-atomic-wait  compare, register and sleep under one mutex, and let a
#                   notify wake a waiter without a store. Upstream loses wakeups
#                   (sdn-server/internal/wasmrt/testdata/wasmedge-atomic-wait.md).
#   02-stop-token   a stop is sticky for every invocation on the executor, and
#                   is read (never consumed) by the interpreter and by
#                   Interruptible AOT code. Upstream's exchange(0) stopped exactly
#                   one thread per cancel, and put an RMW on one shared cache line
#                   at every block of every thread.
#   03-fault-jmp    the fault handler jumps with _setjmp/_longjmp, which carry
#                   no signal state. darwin's longjmp sets or clears the
#                   thread's on-signal-stack flag from a jmp_buf word setjmp
#                   never writes; after a trap, half the time, Go's next signal
#                   on that thread landed on a goroutine stack and the runtime
#                   threw (sdn-server/internal/wasmrt/signals.go).
#   04-atomic-memarg-offset
#                   AOT memory.atomic.notify / wait32 / wait64 pass the runtime
#                   the operand PLUS the instruction's memarg offset, as the
#                   interpreter does. The upstream AOT compiler dropped the
#                   offset: wasi-libc's thread-list-lock wakeups
#                   (`i32.const 0; memory.atomic.notify offset=<lock>`) went to
#                   address 0, so thread exit and join hung (a plain
#                   spawn/join guest hung 11 of 20 runs), and flatsql's
#                   sleepNs waited on the wrong stack word and never slept.
#   05-loop-stop-checks
#                   Interruptible AOT code checks the stop token at each loop
#                   header and function entry, not at every block. Only a loop
#                   or a call can run code again, so a stop still reaches every
#                   thread; the check at each block put 182 checks in front of
#                   every SQLite VDBE opcode dispatch and made the engine 2-3x
#                   slower than with no checks.
#   06-call-indirect
#                   AOT call_indirect reads the calling module's type list
#                   without its shared_mutex, and a function of the same module
#                   with the expected type index matches without the type
#                   matcher (16-19 ns -> ~5 ns per call; SQLite calls through
#                   function pointers in every B-tree seek and page fetch).
#
# THE PATCH TEXT LIVES IN THIS FILE, deliberately. The CI prefix cache key and
# the Dockerfile's static layer are both keyed on this file's bytes, so a patch
# change can never reuse a prefix built without it. The copies in
# sdn-server/internal/wasmrt/testdata are the regression-test inputs;
# TestStaticBuildCarriesTheRuntimePatches fails if the two ever differ.
#
# The patches are written against one exact source commit. A different
# WASMEDGE_VERSION has no series here and refuses to build unless the caller
# says WASMEDGE_ALLOW_UNPATCHED=1, because an unpatched runtime loses wakeups
# and cannot stop a threaded instance.
write_sdn_patch_01_atomic_wait() {
  cat <<'SDN_WASMEDGE_PATCH_EOF'
diff --git a/include/executor/engine/atomic.ipp b/include/executor/engine/atomic.ipp
index 8568e24..8414705 100644
--- a/include/executor/engine/atomic.ipp
+++ b/include/executor/engine/atomic.ipp
@@ -478,40 +478,32 @@ Executor::atomicWait(Runtime::Instance::MemoryInstance &MemInst,
   auto *AtomicObj = MemInst.getPointer<std::atomic<T> *>(Address);
   assuming(AtomicObj);
 
+  // Compare, register, and sleep under the notifier's mutex. Otherwise a
+  // store+notify between the comparison and wait can be lost forever.
+  std::unique_lock<decltype(WaiterMapMutex)> Locker(WaiterMapMutex);
   if (AtomicObj->load() != Expected.le()) {
     return UINT32_C(1); // NotEqual
   }
-
-  decltype(WaiterMap)::iterator WaiterIterator;
-  {
-    std::unique_lock<decltype(WaiterMapMutex)> Locker(WaiterMapMutex);
-    WaiterIterator = WaiterMap.emplace(Address, &MemInst);
-  }
-
+  auto WaiterIterator = WaiterMap.emplace(Address, &MemInst);
   cxx20::scope_exit ScopeExitHolder([&]() noexcept {
-    std::unique_lock<decltype(WaiterMapMutex)> Locker(WaiterMapMutex);
     WaiterMap.erase(WaiterIterator);
   });
-
-  while (true) {
-    std::unique_lock<decltype(WaiterIterator->second.Mutex)> Locker(
-        WaiterIterator->second.Mutex);
-    std::cv_status WaitResult = std::cv_status::no_timeout;
-    if (!Until) {
-      WaiterIterator->second.Cond.wait(Locker);
-    } else {
-      WaitResult = WaiterIterator->second.Cond.wait_until(Locker, *Until);
-    }
-    if (unlikely(StopToken.load(std::memory_order_relaxed) != 0)) {
-      return Unexpect(ErrCode::Value::Interrupted);
-    }
-    if (likely(AtomicObj->load() != Expected.le())) {
-      return UINT32_C(0); // ok
-    }
-    if (WaitResult == std::cv_status::timeout) {
-      return UINT32_C(2); // Timed-out
-    }
+  auto Ready = [&]() {
+    return WaiterIterator->second.Notified ||
+           StopToken.load(std::memory_order_relaxed) != 0;
+  };
+  bool Woken = true;
+  if (!Until) {
+    WaiterIterator->second.Cond.wait(Locker, Ready);
+  } else {
+    Woken = WaiterIterator->second.Cond.wait_until(Locker, *Until, Ready);
+  }
+  if (unlikely(StopToken.load(std::memory_order_relaxed) != 0)) {
+    return Unexpect(ErrCode::Value::Interrupted);
   }
+  // A notification succeeds even when the memory value has not changed.
+  return Woken ? UINT32_C(0) : UINT32_C(2);
+
 }
 
 } // namespace Executor
diff --git a/include/executor/executor.h b/include/executor/executor.h
index fbcc6ac..6683ee9 100644
--- a/include/executor/executor.h
+++ b/include/executor/executor.h
@@ -28,6 +28,7 @@
 #include "runtime/stackmgr.h"
 #include "runtime/storemgr.h"
 
+#include <map>
 #include <atomic>
 #include <condition_variable>
 #include <csignal>
@@ -1103,7 +1104,7 @@ private:
 
   /// Waiter struct for atomic instructions
   struct Waiter {
-    std::mutex Mutex;
+    bool Notified = false;
     std::condition_variable Cond;
     Runtime::Instance::MemoryInstance *MemInst;
     Waiter(Runtime::Instance::MemoryInstance *Inst) noexcept : MemInst(Inst) {}
@@ -1111,7 +1112,7 @@ private:
   /// Waiter map mutex
   std::mutex WaiterMapMutex;
   /// Waiter multimap
-  std::unordered_multimap<uint32_t, Waiter> WaiterMap;
+  std::multimap<uint32_t, Waiter> WaiterMap;
 
   /// WasmEdge configuration
   const Configure Conf;
diff --git a/lib/executor/engine/threadInstr.cpp b/lib/executor/engine/threadInstr.cpp
index 0d1ca70..5331e95 100644
--- a/lib/executor/engine/threadInstr.cpp
+++ b/lib/executor/engine/threadInstr.cpp
@@ -67,8 +67,10 @@ Executor::atomicNotify(Runtime::Instance::MemoryInstance &MemInst,
   auto Range = WaiterMap.equal_range(Address);
   for (auto Iterator = Range.first; Total < Count && Iterator != Range.second;
        ++Iterator) {
-    if (likely(&MemInst == Iterator->second.MemInst)) {
-      Iterator->second.Cond.notify_all();
+    if (likely(&MemInst == Iterator->second.MemInst) &&
+        !Iterator->second.Notified) {
+      Iterator->second.Notified = true;
+      Iterator->second.Cond.notify_one();
       ++Total;
     }
   }
SDN_WASMEDGE_PATCH_EOF
}

write_sdn_patch_02_stop_token() {
  cat <<'SDN_WASMEDGE_PATCH_EOF'
diff --git a/include/executor/executor.h b/include/executor/executor.h
index 6683ee9..6bdccea 100644
--- a/include/executor/executor.h
+++ b/include/executor/executor.h
@@ -201,9 +201,15 @@ public:
   asyncInvoke(const Runtime::Instance::FunctionInstance *FuncInst,
               Span<const ValVariant> Params, Span<const ValType> ParamTypes);
 
-  /// Stop execution
+  /// Stop execution.
+  ///
+  /// SDN patch (stop-token): a stop is STICKY for every invocation running on
+  /// this executor. Upstream consumed the token with exchange(0), so one stop
+  /// interrupted exactly one thread and every other wasi-thread on the same
+  /// executor kept running. The token is cleared only when the next
+  /// invocation starts on an idle executor (see invoke()).
   void stop() noexcept {
-    StopToken.store(1, std::memory_order_relaxed);
+    StopToken.store(1, std::memory_order_seq_cst);
     atomicNotifyAll();
   }
 
@@ -1120,6 +1126,8 @@ private:
   Statistics::Statistics *Stat;
   /// Stop Execution
   std::atomic_uint32_t StopToken = 0;
+  /// Invocations currently running on this executor (SDN stop-token patch).
+  std::atomic_uint32_t ActiveInvocations = 0;
   /// Executor Host Function Handler
   HostFuncHandler HostFuncHelper = {};
 };
diff --git a/lib/executor/engine/controlInstr.cpp b/lib/executor/engine/controlInstr.cpp
index 935992a..55e08d7 100644
--- a/lib/executor/engine/controlInstr.cpp
+++ b/lib/executor/engine/controlInstr.cpp
@@ -134,7 +134,7 @@ Expect<void> Executor::runBrOnCastOp(Runtime::StackManager &StackMgr,
 Expect<void> Executor::runReturnOp(Runtime::StackManager &StackMgr,
                                    AST::InstrView::iterator &PC) noexcept {
   // Check stop token
-  if (unlikely(StopToken.exchange(0, std::memory_order_relaxed))) {
+  if (unlikely(StopToken.load(std::memory_order_relaxed))) {
     spdlog::error(ErrCode::Value::Interrupted);
     return Unexpect(ErrCode::Value::Interrupted);
   }
diff --git a/lib/executor/executor.cpp b/lib/executor/executor.cpp
index 137131b..68ef4fe 100644
--- a/lib/executor/executor.cpp
+++ b/lib/executor/executor.cpp
@@ -140,6 +140,16 @@ Executor::invoke(const Runtime::Instance::FunctionInstance *FuncInst,
     }
   }
 
+  // SDN patch (stop-token): a stop issued while invocations run stays set
+  // until all of them have returned; the first invocation on an idle executor
+  // clears a token left over from an earlier stop.
+  if (ActiveInvocations.fetch_add(1, std::memory_order_acq_rel) == 0) {
+    StopToken.store(0, std::memory_order_seq_cst);
+  }
+  cxx20::scope_exit ActiveInvocationGuard([this]() noexcept {
+    ActiveInvocations.fetch_sub(1, std::memory_order_acq_rel);
+  });
+
   Runtime::StackManager StackMgr;
 
   // Call runFunction.
diff --git a/lib/executor/helper.cpp b/lib/executor/helper.cpp
index 96205cc..2ace419 100644
--- a/lib/executor/helper.cpp
+++ b/lib/executor/helper.cpp
@@ -51,7 +51,7 @@ Executor::enterFunction(Runtime::StackManager &StackMgr,
   // RetIt: the return position when the entered function returns.
 
   // Check if the interruption occurs.
-  if (unlikely(StopToken.exchange(0, std::memory_order_relaxed))) {
+  if (unlikely(StopToken.load(std::memory_order_relaxed))) {
     spdlog::error(ErrCode::Value::Interrupted);
     return Unexpect(ErrCode::Value::Interrupted);
   }
@@ -251,7 +251,7 @@ Executor::branchToLabel(Runtime::StackManager &StackMgr,
                         const AST::Instruction::JumpDescriptor &JumpDesc,
                         AST::InstrView::iterator &PC) noexcept {
   // Check the stop token.
-  if (unlikely(StopToken.exchange(0, std::memory_order_relaxed))) {
+  if (unlikely(StopToken.load(std::memory_order_relaxed))) {
     spdlog::error(ErrCode::Value::Interrupted);
     return Unexpect(ErrCode::Value::Interrupted);
   }
diff --git a/lib/llvm/compiler.cpp b/lib/llvm/compiler.cpp
index 8db12e3..f765db6 100644
--- a/lib/llvm/compiler.cpp
+++ b/lib/llvm/compiler.cpp
@@ -5670,12 +5670,13 @@ private:
       return;
     }
     auto NotStopBB = LLVM::BasicBlock::create(LLContext, F.Fn, "NotStop");
-    auto StopToken = Builder.createAtomicRMW(
-        LLVMAtomicRMWBinOpXchg, Context.getStopToken(Builder, ExecCtx),
-        LLContext.getInt32(0), LLVMAtomicOrderingMonotonic);
-#if LLVM_VERSION_MAJOR >= 13
-    StopToken.setAlignment(32);
-#endif
+    // SDN patch (stop-token): read the token, never consume it. An exchange
+    // let one thread swallow a stop meant for all of them, and it made every
+    // block of every thread an RMW on one shared cache line.
+    auto StopToken = Builder.createLoad(
+        LLContext.getInt32Ty(), Context.getStopToken(Builder, ExecCtx), true);
+    StopToken.setOrdering(LLVMAtomicOrderingMonotonic);
+    StopToken.setAlignment(4);
     auto NotStop = Builder.createLikely(
         Builder.createICmpEQ(StopToken, LLContext.getInt32(0)));
     Builder.createCondBr(NotStop, NotStopBB,
SDN_WASMEDGE_PATCH_EOF
}

write_sdn_patch_03_fault_jmp() {
  cat <<'SDN_WASMEDGE_PATCH_EOF'
diff --git a/include/system/fault.h b/include/system/fault.h
index 1f57d78..71dd6ea 100644
--- a/include/system/fault.h
+++ b/include/system/fault.h
@@ -43,4 +43,14 @@ private:
 
 } // namespace WasmEdge
 
+// _setjmp/_longjmp carry no signal state. darwin's longjmp resets or sets the
+// thread's alternate-signal-stack flag from a jmp_buf word setjmp never
+// writes, so a trap could leave a thread marked as running on its signal stack
+// and the embedder's next signal (Go's preemption) lands on the wrong stack.
+// The fault handler unblocks its own signal before it jumps, so nothing needs
+// restoring.
+#if defined(_WIN32)
 #define PREPARE_FAULT(f) (static_cast<uint32_t>(setjmp((f).buffer())))
+#else
+#define PREPARE_FAULT(f) (static_cast<uint32_t>(_setjmp((f).buffer())))
+#endif
diff --git a/lib/system/fault.cpp b/lib/system/fault.cpp
index e435d4e..c49f5ae 100644
--- a/lib/system/fault.cpp
+++ b/lib/system/fault.cpp
@@ -118,7 +118,11 @@ Fault::~Fault() noexcept {
   assuming(localHandler != nullptr);
   auto Buffer = stackTrace(localHandler->StackTraceBuffer);
   localHandler->StackTraceSize = Buffer.size();
+#if defined(_WIN32)
   longjmp(localHandler->Buffer, static_cast<int>(Error.operator uint32_t()));
+#else
+  _longjmp(localHandler->Buffer, static_cast<int>(Error.operator uint32_t()));
+#endif
 }
 
 } // namespace WasmEdge
SDN_WASMEDGE_PATCH_EOF
}

write_sdn_patch_04_atomic_memarg_offset() {
  cat <<'SDN_WASMEDGE_PATCH_EOF'
diff --git a/lib/llvm/compiler.cpp b/lib/llvm/compiler.cpp
index f765db6..5b1bfc3 100644
--- a/lib/llvm/compiler.cpp
+++ b/lib/llvm/compiler.cpp
@@ -3952,6 +3952,21 @@ public:
     Builder.positionAtEnd(OkBB);
   }
 
+  // memory.atomic.notify / wait32 / wait64 call the runtime with the
+  // effective address: the operand plus the memarg offset (as the interpreter
+  // does, threadInstr.cpp). Passing the bare operand notified and waited on
+  // the wrong word whenever the offset was not 0 (a global such as wasi-libc's
+  // __thread_list_lock at `i32.const 0; memory.atomic.notify offset=N`): lost
+  // wakeups. An address past 4 GiB is out of bounds.
+  LLVM::Value compileAtomicEffectiveAddress(LLVM::Value Addr) noexcept {
+    auto OkBB = LLVM::BasicBlock::create(LLContext, F.Fn, "address_in_bounds");
+    auto InBounds = Builder.createLikely(
+        Builder.createICmpULE(Addr, LLContext.getInt64(UINT32_MAX)));
+    Builder.createCondBr(InBounds, OkBB,
+                         getTrapBB(ErrCode::Value::MemoryOutOfBounds));
+    Builder.positionAtEnd(OkBB);
+    return Builder.createTrunc(Addr, Context.Int32Ty);
+  }
   void compileMemoryFence() noexcept {
     Builder.createFence(LLVMAtomicOrderingSequentiallyConsistent);
   }
@@ -3963,7 +3978,8 @@ public:
       Addr = Builder.createAdd(Addr, LLContext.getInt64(MemoryOffset));
     }
     compileAtomicCheckOffsetAlignment(Addr, Context.Int32Ty);
-    auto Offset = stackPop();
+    stackPop();
+    auto Offset = compileAtomicEffectiveAddress(Addr);
 
     stackPush(Builder.createCall(
         Context.getIntrinsic(
@@ -3982,7 +3998,8 @@ public:
       Addr = Builder.createAdd(Addr, LLContext.getInt64(MemoryOffset));
     }
     compileAtomicCheckOffsetAlignment(Addr, TargetType);
-    auto Offset = stackPop();
+    stackPop();
+    auto Offset = compileAtomicEffectiveAddress(Addr);
 
     stackPush(Builder.createCall(
         Context.getIntrinsic(
SDN_WASMEDGE_PATCH_EOF
}

write_sdn_patch_05_loop_stop_checks() {
  cat <<'SDN_WASMEDGE_PATCH_EOF'
diff --git a/lib/llvm/compiler.cpp b/lib/llvm/compiler.cpp
index 5b1bfc3..4350f7a 100644
--- a/lib/llvm/compiler.cpp
+++ b/lib/llvm/compiler.cpp
@@ -581,6 +581,9 @@ public:
     auto RetBB = LLVM::BasicBlock::create(LLContext, F.Fn, "ret");
     Type.first.clear();
     enterBlock(RetBB, {}, {}, {}, std::move(Type));
+    // SDN patch (loop-stop-checks): every function entry checks the stop
+    // token, so deep recursion stops as a loop does.
+    checkStop();
     EXPECTED_TRY(compile(Code.getExpr().getInstrs()));
     assuming(ControlStack.empty());
     compileReturn();
@@ -622,7 +625,11 @@ public:
           }
         }
         enterBlock(EndBlock, {}, {}, std::move(Args), std::move(Type));
-        checkStop();
+        // SDN patch (loop-stop-checks): no stop check at a block entry. Only a
+        // loop can run code again, and a loop header and every function entry
+        // still check, so a stop reaches any guest that keeps running. The
+        // check at every block made a run of 182 checks in front of each
+        // SQLite VDBE opcode dispatch (181 nested blocks before its br_table).
         updateGas();
         return {};
       }
SDN_WASMEDGE_PATCH_EOF
}

write_sdn_patch_06_call_indirect() {
  cat <<'SDN_WASMEDGE_PATCH_EOF'
diff --git a/lib/executor/engine/proxy.cpp b/lib/executor/engine/proxy.cpp
index b67238b..d77b063 100644
--- a/lib/executor/engine/proxy.cpp
+++ b/lib/executor/engine/proxy.cpp
@@ -600,9 +600,23 @@ Expect<void *> Executor::proxyTableGetFuncSymbol(
 
   const auto *ModInst = StackMgr.getModule();
   assuming(ModInst);
-  const auto &ExpDefType = **ModInst->getType(FuncTypeIdx);
+  // SDN patch (call-indirect): a module's type list is fixed once it is
+  // instantiated, and validation bounded FuncTypeIdx, so it is read without
+  // the module's shared_mutex, as getTabInstByIdx reads the table list. The
+  // lock cost four pthread mutex operations on every AOT call_indirect.
+  const auto &ExpDefType = *ModInst->unsafeGetType(FuncTypeIdx);
   const auto *FuncInst = retrieveFuncRef(*Ref);
   assuming(FuncInst);
+  // SDN patch (call-indirect): a function of the calling module with the
+  // expected type index matches; the matcher below would compare that type
+  // with itself.
+  if (likely(FuncInst->getModule() == ModInst &&
+             FuncInst->getTypeIndex() == *ExpDefType.getTypeIndex())) {
+    if (unlikely(!FuncInst->isCompiledFunction())) {
+      return nullptr;
+    }
+    return FuncInst->getSymbol().get();
+  }
   bool IsMatch = false;
   if (FuncInst->getModule()) {
     IsMatch = AST::TypeMatcher::matchType(
SDN_WASMEDGE_PATCH_EOF
}

SDN_PATCH_DIR="${WORK}/sdn-patches"
SDN_PATCH_STAMP=""
if [[ "$WASMEDGE_VERSION" == "0.16.4" ]]; then
  sdn_expected_commit=be85c2fbba68318f103b4a766728f6946e65abf8
  sdn_actual_commit="$(git -C "$SRC" rev-parse HEAD)"
  if [[ "$sdn_actual_commit" != "$sdn_expected_commit" ]]; then
    echo "WasmEdge 0.16.4 source is at ${sdn_actual_commit}; the SDN patches are written against ${sdn_expected_commit}" >&2
    exit 1
  fi
  rm -rf "$SDN_PATCH_DIR"
  mkdir -p "$SDN_PATCH_DIR"
  write_sdn_patch_01_atomic_wait > "$SDN_PATCH_DIR/01-atomic-wait.patch"
  write_sdn_patch_02_stop_token > "$SDN_PATCH_DIR/02-stop-token.patch"
  write_sdn_patch_03_fault_jmp > "$SDN_PATCH_DIR/03-fault-jmp.patch"
  write_sdn_patch_04_atomic_memarg_offset > "$SDN_PATCH_DIR/04-atomic-memarg-offset.patch"
  write_sdn_patch_05_loop_stop_checks > "$SDN_PATCH_DIR/05-loop-stop-checks.patch"
  write_sdn_patch_06_call_indirect > "$SDN_PATCH_DIR/06-call-indirect.patch"
  for sdn_patch in "$SDN_PATCH_DIR"/*.patch; do
    if git -C "$SRC" apply --reverse --check "$sdn_patch" >/dev/null 2>&1; then
      echo "WasmEdge patch already applied: $(basename "$sdn_patch")"
      continue
    fi
    git -C "$SRC" apply --check "$sdn_patch"
    git -C "$SRC" apply "$sdn_patch"
    echo "WasmEdge patch applied: $(basename "$sdn_patch")"
  done
  SDN_PATCH_STAMP="$(cat "$SDN_PATCH_DIR"/*.patch | git hash-object --stdin)"
elif [[ -z "${WASMEDGE_ALLOW_UNPATCHED:-}" ]]; then
  echo "no SDN patch series for WasmEdge ${WASMEDGE_VERSION}; refusing to build an unpatched runtime (set WASMEDGE_ALLOW_UNPATCHED=1 to override)" >&2
  exit 1
fi

# DROP UPSTREAM'S -Werror. WasmEdge adds it unconditionally
# (cmake/Helper.cmake) and then fails its OWN sources under every compiler it
# did not test: gcc rejects component_type.cpp on -Wmaybe-uninitialized and
# coredump.cpp on -Warray-bounds, MSYS2's clang rejects the CRTP in
# host/wasi/inode.h. We consume this dependency rather than develop it, so its
# warnings must not gate our build — and without this, each new toolchain
# breaks the build again for reasons that are never ours.
if [[ -f "$SRC/cmake/Helper.cmake" ]]; then
  perl -0pi -e 's/^\s*-Werror\s*$//mg' "$SRC/cmake/Helper.cmake"
fi

# GRANT THE CRTP BASE FRIENDSHIP (Windows WASI only).
#
# FindHolderBase<T>::Proxy reaches T's PROTECTED members by writing
# `&T::doReset`, which names a protected member through T rather than through
# the accessing class. [class.protected] forbids that; MSVC accepts it as an
# extension, so this Windows-only header has never been compiled by anything
# else — gcc rejects it ("declared protected here") and clang rejects it
# ("must name member"). Upstream ships a non-conforming file, and there is no
# compiler choice that avoids it, so the base is made a friend instead.
if [[ -f "$SRC/include/host/wasi/inode.h" ]]; then
  perl -0pi -e 's/(class FindHolder : public FindHolderBase<FindHolder> \{)/$1\n  friend class FindHolderBase<FindHolder>;/g' \
    "$SRC/include/host/wasi/inode.h"
fi

# SANITISE THE STATIC-MERGE SCRATCH DIRECTORIES (Windows).
#
# The merge extracts each component into objs/<target>, and one target is
# literally named `fmt::fmt`. A colon cannot appear in a Windows path, so the
# merge dies with `Error creating directory "objs/fmt::fmt": Invalid argument`
# after every object has already compiled. The target name still has to reach
# $<TARGET_FILE:...> intact, so only the DIRECTORY is renamed.
if [[ -f "$SRC/lib/api/CMakeLists.txt" ]]; then
  perl -0pi -e 's/(function\(wasmedge_add_static_lib_component_command target\)\n)/$1  string(REPLACE "::" "_" _sanitized_target "${target}")\n/' \
    "$SRC/lib/api/CMakeLists.txt"
  perl -0pi -e 's/objs\/\$\{target\}/objs\/\$\{_sanitized_target\}/g' \
    "$SRC/lib/api/CMakeLists.txt"
  # SKIP COMPONENTS THAT ARE NOT REAL FILES. LLVM's component list can name a
  # dependency without a path (zstd on MinGW), and the merge then runs
  # `ar -x /libzstd.a` — an absolute path at the drive root — failing at 131/131
  # with everything already compiled. We link zstd explicitly from the archive
  # found above, so dropping it from the MERGE loses nothing.
  perl -0pi -e 's/(function\(wasmedge_add_libs_component_command target_path\)\n)/$1  if(NOT EXISTS "\${target_path}")\n    return()\n  endif()\n/' \
    "$SRC/lib/api/CMakeLists.txt"
  # EXPAND THE OBJECT GLOB IN A SHELL. The merge ends with
  #   ar -qcs libwasmedge.a <CAPI objects> objs/*/*.o
  # and relies on a SHELL expanding `objs/*/*.o`. CMake runs it through cmd.exe
  # on Windows, which does not glob, so ar received the pattern literally and
  # failed "objs/*/*.o: Invalid argument" at 131/131. Splitting it in two keeps
  # the generator expression (a CMake list) out of the quoted shell command,
  # which would otherwise be re-split into separate arguments.
  # TOLERATE AN EMPTY objs/. The merge extracts each component into objs/<t> and
  # then re-archives `objs/*/*.o`, relying on a SHELL to expand it. cmd.exe does
  # not glob, and on Windows the extraction leaves objs/ empty anyway, so ar got
  # the literal pattern and failed "Invalid argument" at 131/131 — every object
  # compiled, dying on the last command.
  #
  # The merged archive only has to carry the CAPI objects: the link line names
  # every component archive individually (see GRP below), so nothing is lost if
  # objs/ is empty. The second command adds them when they exist and is a no-op
  # when they do not.
  perl -0pi -e 's{COMMAND \$\{CMAKE_AR\} -qcs libwasmedge\.a \$<TARGET_OBJECTS:wasmedgeCAPI> objs/\*/\*\.o}
                 {COMMAND \${CMAKE_AR} -qcs libwasmedge.a \$<TARGET_OBJECTS:wasmedgeCAPI>\n      COMMAND sh -c "\${CMAKE_AR} -qcs libwasmedge.a objs/*/*.o || true"}g' \
    "$SRC/lib/api/CMakeLists.txt"
fi

# 2. Configure + build the static library WITH LLVM.
#
# NOT optional, and the opposite of what this comment used to claim. In 0.16.4
# WASMEDGE_BUILD_AOT_RUNTIME is deprecated and aliased to WASMEDGE_USE_LLVM
# (CMakeLists.txt:69-72), so there is no load-only AOT mode: turning LLVM off
# removes the ability to LOAD a precompiled artifact, not just to produce one,
# and every query silently falls back to the interpreter at ~100x.
#
# A build directory left by an earlier run is reused incrementally, but only
# when it was built from the same patch series: the stamp is the git hash of
# the series, and a mismatch (or no stamp) rebuilds. Without that check a
# workspace built before the patches existed would stage an unpatched runtime.
SDN_BUILD_STAMP="$SRC/build/.sdn-patch-series"
sdn_build_needed=0
if [[ ! -f "$SRC/build/lib/api/libwasmedge.a" ]]; then
  sdn_build_needed=1
elif [[ "$(cat "$SDN_BUILD_STAMP" 2>/dev/null || true)" != "$SDN_PATCH_STAMP" ]]; then
  sdn_build_needed=1
fi
if [[ "$sdn_build_needed" == 1 ]]; then
  # LIBC++ NOW REFUSES WASMEDGE'S std::is_class SPECIALIZATION.
  #
  # include/common/int128.h:545 specializes std::is_class for its uint128, and
  # the libc++ in the Xcode 26 SDK marks that template
  # _LIBCPP_NO_SPECIALIZATIONS. Apple clang 21 then stops lib/llvm/compiler.cpp
  # and lib/plugin/plugin.cpp on -Winvalid-specialization, which is an ERROR by
  # default: dropping -Werror above does not reach it. The specialization only
  # restates that WasmEdge::uint128 is a class, so the diagnostic is silenced
  # rather than the header patched.
  #
  # ONLY WHERE THE COMPILER KNOWS THE WARNING. Older clang rejects an unknown
  # -Wno- option under -Werror (measured: clang 16, the Linux and Docker
  # compiler, and clang 18 both do), so the flag is added only when a probe
  # compiles cleanly with it. The probe names the POSITIVE form as well: gcc
  # accepts ANY unknown -Wno- option in silence, so a -Wno- probe alone passes
  # on MinGW's g++ and would change the Windows build for nothing. Everywhere
  # the probe fails, the configure line is exactly what it was.
  #
  # Passed as -DCMAKE_CXX_FLAGS, not exported as CXXFLAGS: cmake reads the
  # environment only on a FIRST configure, and a build directory reused after
  # the patch series changed would otherwise keep a cached value without it.
  # The caller's CXXFLAGS (Windows sets -DLLVM_BUILD_STATIC there) stay in front.
  SDN_CMAKE_CXX_FLAGS=""
  if printf '' | "${CXX:-clang++-16}" -x c++ -Werror -Winvalid-specialization \
       -Wno-invalid-specialization -fsyntax-only - >/dev/null 2>&1; then
    SDN_CMAKE_CXX_FLAGS="${CXXFLAGS:+${CXXFLAGS} }-Wno-invalid-specialization"
    echo "compiler knows -Winvalid-specialization: building with -Wno-invalid-specialization"
  fi
  CC="${CC:-clang-16}" CXX="${CXX:-clang++-16}" \
  cmake -S "$SRC" -B "$SRC/build" -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DWASMEDGE_USE_LLVM=ON \
    -DWASMEDGE_LINK_LLVM_STATIC=ON \
    -DWASMEDGE_BUILD_SHARED_LIB=OFF \
    -DWASMEDGE_BUILD_STATIC_LIB=ON \
    -DWASMEDGE_BUILD_TOOLS=OFF \
    -DWASMEDGE_BUILD_PLUGINS=OFF \
    -DLLVM_DIR="${LLVM_DIR:-/usr/lib/llvm-16/lib/cmake/llvm}" \
    -DLLD_DIR="${LLD_DIR:-/usr/lib/llvm-16/lib/cmake/lld}" \
    ${CMAKE_AR:+-DCMAKE_AR="$CMAKE_AR"} \
    ${CMAKE_PREFIX_PATH:+-DCMAKE_PREFIX_PATH="$CMAKE_PREFIX_PATH"} \
    ${SDN_CMAKE_CXX_FLAGS:+-DCMAKE_CXX_FLAGS="$SDN_CMAKE_CXX_FLAGS"}
  JOBS="${WASMEDGE_BUILD_JOBS:-$( (command -v nproc >/dev/null && nproc) || sysctl -n hw.ncpu 2>/dev/null || echo 4 )}"
  cmake --build "$SRC/build" -j"$JOBS"
  printf '%s' "$SDN_PATCH_STAMP" > "$SDN_BUILD_STAMP"
fi

# 3. Stage archives + headers. The merged libwasmedge.a is INCOMPLETE (the
#    component-model loader lives in the sublibraries), so we link the full set
#    inside a --start-group; ld pulls only the members it needs, no duplicates.
rm -rf "$STATIC"; mkdir -p "$STATIC/lib" "$STATIC/include"
find "$SRC/build" -name "libwasmedge*.a" -exec cp {} "$STATIC/lib/" \;
cp "$SRC"/build/lib/host/wasi/*.a             "$STATIC/lib/" 2>/dev/null || true
cp "$SRC"/build/_deps/fmt-build/libfmt.a      "$STATIC/lib/"
cp "$SRC"/build/_deps/spdlog-build/libspdlog.a "$STATIC/lib/"
cp -r "$SRC"/include/api/wasmedge             "$STATIC/include/"
cp -r "$SRC"/build/include/api/wasmedge/*     "$STATIC/include/wasmedge/" 2>/dev/null || true
# Say which runtime patches this prefix carries. Operators and the daemon's
# `substrate-selftest` report read it; the self-test still measures behaviour
# rather than trusting this file.
{
  echo "wasmedge ${WASMEDGE_VERSION}"
  echo "series ${SDN_PATCH_STAMP:-none}"
  for sdn_patch in "$SDN_PATCH_DIR"/*.patch; do
    [[ -f "$sdn_patch" ]] || continue
    echo "patch $(basename "$sdn_patch") $(git hash-object "$sdn_patch")"
  done
} > "$STATIC/sdn-runtime-patches.txt"

# WINDOWS: make the staged header describe the STATIC library it sits beside.
#
# wasmedge_basic.h has exactly two cases on Windows — WASMEDGE_COMPILE_LIBRARY
# means __declspec(dllexport), anything else means __declspec(dllimport) — and
# no third case for a consumer linking the static archive, which is what we are.
# So every C API call compiled against it referenced the DLL import thunk and
# the link died with "undefined reference to `__imp_WasmEdge_ASTModuleDelete'"
# once per function, against archives that hold the plain symbols.
#
# Patched HERE rather than by defining a macro in the consumer, for the same
# reason link.flags lives here: the prefix has to be self-describing. A consumer
# that has to know a magic define is a consumer that will one day forget it.
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    for header in "$STATIC"/include/wasmedge/*.h; do
      [[ -f "$header" ]] || continue
      perl -0pi -e 's/#define\s+(WASMEDGE_CAPI_(?:PLUGIN_)?EXPORT)\s+__declspec\(dllimport\)/#define $1/g' "$header"
    done
    if grep -rq 'dllimport' "$STATIC/include/wasmedge/"; then
      echo "static prefix: a WasmEdge header still declares dllimport" >&2
      grep -rn 'dllimport' "$STATIC/include/wasmedge/" >&2
      exit 1
    fi
    echo "patched staged headers for static linkage (no dllimport)"
    ;;
esac

# EVERY staged WasmEdge archive, not a hand-kept list. The list silently omitted
# libwasmedgePluginWasiLogging.a, which left
# WasmEdge::Host::WasiLoggingModule::PluginDescriptor undefined — a component
# added upstream would fail the same way, and the failure is a link error a long
# way from its cause.
GRP="$STATIC/lib/libwasmedge.a"
for archive in "$STATIC"/lib/libwasmedge*.a; do
  [[ -f "$archive" && "$archive" != "$STATIC/lib/libwasmedge.a" ]] && GRP="$GRP $archive"
done
GRP="$GRP $STATIC/lib/libspdlog.a $STATIC/lib/libfmt.a"

# LLVM's own archives, in the order llvm-config gives them.
LLVM_CONFIG="${LLVM_CONFIG:-llvm-config-16}"
LLVMLIBS="$($LLVM_CONFIG --link-static --libfiles | tr '\n' ' ')"

# LLD, EXPLICITLY. WasmEdge's AOT links its output through lld, but
# `llvm-config --libfiles` never lists lld's archives — they only reached the
# binary because the merge swept them into libwasmedge.a. Once the merge started
# skipping components it could not resolve, the link failed on
# `lld::CommonLinkerContext::destroy()`. Naming them here does not depend on how
# the merge behaves.
LLVM_LIBDIR="$($LLVM_CONFIG --libdir 2>/dev/null || true)"
if [[ -n "$LLVM_LIBDIR" && -d "$LLVM_LIBDIR" ]]; then
  for lld in "$LLVM_LIBDIR"/liblldCommon.a "$LLVM_LIBDIR"/liblldELF.a \
             "$LLVM_LIBDIR"/liblldCOFF.a "$LLVM_LIBDIR"/liblldMachO.a \
             "$LLVM_LIBDIR"/liblldMinGW.a "$LLVM_LIBDIR"/liblldWasm.a; do
    [[ -f "$lld" ]] && LLD_LIBS="${LLD_LIBS:-} $lld"
  done
fi
# lld BEFORE LLVM. A static archive must precede the archives it depends on, and
# lld calls into LLVM (llvm::parallelFor out of Support). Appending it left those
# undefined even though the archive was present.
LLVMLIBS="${LLD_LIBS:-} $LLVMLIBS"


# FIND THE STATIC ZSTD. LLVM's Support library calls ZSTD_decompress, so the
# archive must come with us or the link fails with undefined references to it.
# Discovered rather than required from the caller: only macOS was setting
# ZSTD_STATIC, which left Linux linking nothing at all for zstd once the
# hand-written -lzstd came off the link line.
if [[ -z "${ZSTD_STATIC:-}" ]]; then
  for cand in \
    /usr/lib/x86_64-linux-gnu/libzstd.a \
    /usr/lib/aarch64-linux-gnu/libzstd.a \
    /usr/lib/libzstd.a \
    /mingw64/lib/libzstd.a; do
    if [[ -f "$cand" ]]; then ZSTD_STATIC="$cand"; break; fi
  done
fi
# Plain ifs, not `[[ ]] && ...`: under `set -e` a trailing test that fails is
# the script's exit status, so a no-match on the LAST candidate killed the build
# outright. Linux matched its first candidate and never showed it; macOS, which
# is handed ZSTD_STATIC and skips the loop entirely, died on the report line.
if [[ -n "${ZSTD_STATIC:-}" ]]; then
  echo "static zstd: $ZSTD_STATIC"
else
  echo "no static zstd found; relying on the link line" >&2
fi

# FIND THE STATIC libxml2 AND zlib, ON WINDOWS ONLY.
#
# MSYS2's prebuilt LLVM is what supplies our archives, and two of its components
# call out of the toolchain: WindowsManifest into libxml2, Support into zlib.
# Naming them as -lxml2 -lz on the link line takes MSYS2's IMPORT libraries, and
# the produced .exe then listed
#   DLL Name: libxml2-16.dll
#   DLL Name: zlib1.dll
# — two files that exist only where MSYS2 is installed. That is the same defect
# as the libstdc++-6.dll one directly above and it survived the first fix,
# because the guard named specific runtime libraries instead of asking which
# imports are NOT part of Windows. Both are now staged as archives, like zstd.
XML2_STATIC=""
ZLIB_STATIC=""
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    for cand in /mingw64/lib/libxml2.a; do
      if [[ -f "$cand" ]]; then XML2_STATIC="$cand"; break; fi
    done
    for cand in /mingw64/lib/libz.a; do
      if [[ -f "$cand" ]]; then ZLIB_STATIC="$cand"; break; fi
    done
    if [[ -n "$XML2_STATIC" ]]; then echo "static libxml2: $XML2_STATIC"; else
      echo "no static libxml2 in /mingw64/lib; the .exe will import libxml2-*.dll" >&2
    fi
    if [[ -n "$ZLIB_STATIC" ]]; then echo "static zlib: $ZLIB_STATIC"; else
      echo "no static zlib in /mingw64/lib; the .exe will import zlib1.dll" >&2
    fi
    ;;
esac

# Make the prefix SELF-CONTAINED and RELOCATABLE.
#
# LLVM's archives are copied in beside WasmEdge's, and link.flags names every
# archive through a @PREFIX@ placeholder that the consumer substitutes. Both
# matter: a prefix that points at /usr/lib/llvm-16 or at the absolute path it
# happened to be built in cannot be handed to another machine, another job, or
# another checkout — which is exactly what a cached CI artifact is. Measured the
# hard way: link.flags with absolute paths failed every downstream job with
# "cannot find .../libwasmedgeAOT.a".
for archive in $LLVMLIBS; do
  [[ -f "$archive" ]] && cp -n "$archive" "$STATIC/lib/" 2>/dev/null || true
done
[[ -n "${ZSTD_STATIC:-}" && -f "${ZSTD_STATIC}" ]] && cp -n "$ZSTD_STATIC" "$STATIC/lib/" 2>/dev/null || true
# The Windows pair, for the same reason and by the same rule: link.flags names
# every archive as @PREFIX@/lib/<basename>, so discovering one without copying
# it produces a prefix that names a file it does not contain — which is exactly
# how this failed the first time, with "cannot find .../libxml2.a" at the end of
# a Go build.
[[ -n "${XML2_STATIC:-}" && -f "${XML2_STATIC}" ]] && cp -n "$XML2_STATIC" "$STATIC/lib/" 2>/dev/null || true
[[ -n "${ZLIB_STATIC:-}" && -f "${ZLIB_STATIC}" ]] && cp -n "$ZLIB_STATIC" "$STATIC/lib/" 2>/dev/null || true

# libstdc++, INTO the prefix, on ELF platforms.
#
# The binary was carrying libstdc++.so.6 and libgcc_s.so.1 as external
# dependencies. They are present on most Linux hosts, but "most" is not the
# contract — the owner's is "no dependencies outside the binary, no need for
# homebrew or any other package manager" — and a host with an older libstdc++
# than the build machine's fails at exec, not at install.
#
# macOS keeps libc++ dynamic: it ships with the OS, is versioned as part of it,
# and Apple does not support statically linking it. Windows does NOT need a
# staged libstdc++.a — MinGW ships one — but it does need to be ASKED for it;
# see the -static-libstdc++ in link.flags below. The claim that once stood here,
# that MinGW links libstdc++ statically by default, is false: objdump on the
# produced spacedatanetwork.exe listed "DLL Name: libstdc++-6.dll".
STDCXX_STATIC=""
case "$(uname -s)" in
  Darwin|MINGW*|MSYS*|CYGWIN*) : ;;
  *)
    _cxx_probe="${CXX:-${CC:-cc}}"
    STDCXX_STATIC="$("$_cxx_probe" -print-file-name=libstdc++.a 2>/dev/null || true)"
    if [[ -n "$STDCXX_STATIC" && -f "$STDCXX_STATIC" ]]; then
      cp -n "$STDCXX_STATIC" "$STATIC/lib/" 2>/dev/null || true
      echo "static prefix: staged $(basename "$STDCXX_STATIC")"
    else
      echo "static prefix: no libstdc++.a from $_cxx_probe; the binary will need libstdc++.so" >&2
      STDCXX_STATIC=""
    fi
    unset _cxx_probe
    ;;
esac

# lld has gone missing from this prefix once before, and the symptom was 26
# undefined lld:: symbols at the far end of a Go build on another machine.
# Check it here, where the answer is one line.
# A plain `if`, NOT `[[ -f ]] && count=…`: under `set -e` a false test on the
# last iteration makes the loop return non-zero and kills the script before the
# diagnosis below can print. That exact shape has bitten this file once already.
lld_staged=0
for archive in "$STATIC"/lib/liblld*.a; do
  if [[ -f "$archive" ]]; then
    lld_staged=$((lld_staged + 1))
  fi
done
if [[ "$lld_staged" -eq 0 ]]; then
  echo "static prefix: no lld archives staged — WasmEdge's AOT links through lld and will not resolve" >&2
  echo "  LLVM_CONFIG=$LLVM_CONFIG libdir=${LLVM_LIBDIR:-<unset>}" >&2
  ls "${LLVM_LIBDIR:-/nonexistent}"/liblld*.a 2>&1 | head -5 >&2 || true
  exit 1
fi
echo "static prefix: $lld_staged lld archive(s) staged"

# PE IMPORT SLOTS FOR STATICALLY LINKED LLVM.
#
# MSYS2 ships lld as a PREBUILT package, compiled against LLVM as a DLL. Every
# symbol LLVM annotates with LLVM_ABI therefore reaches lld's objects as an
# import slot, `__imp_<mangled>`, and GNU ld will not synthesise an import slot
# from a static definition. Setting -DLLVM_BUILD_STATIC (see
# scripts/wasmedge-static-env.sh) fixes the sources WE compile; it cannot reach
# an archive somebody else already built.
#
# Measured: liblldCOFF.a(Symbols.cpp.obj) wants
#   __imp__ZN4llvm8demangleB5cxx11ESt17basic_string_viewIcSt11char_traitsIcEE
# for lld::maybeDemangleSymbol, while libLLVMDemangle.a defines the plain
# symbol. Five other archives referenced the same function WITHOUT the prefix
# and linked fine, which is what pins this on the annotation rather than on
# archive order.
#
# So synthesise the slots: an import slot is just a pointer-sized datum holding
# the address of the real symbol. Derived from nm rather than hardcoded to the
# one symbol that happens to fail today, because which symbols carry LLVM_ABI
# changes between LLVM releases.
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    _shim_dir="$STATIC/lib"
    _shim_s="$(mktemp -t impshim.XXXXXX.s)"
    _undef="$(mktemp)"
    _defined="$(mktemp)"

    {
      for _a in "$STATIC"/lib/*.a; do
        nm --undefined-only --format=posix "$_a" 2>/dev/null | awk '{print $1}'
      done
    } 2>/dev/null | grep '^__imp_' | sort -u > "$_undef" || true

    {
      for _a in "$STATIC"/lib/*.a; do
        nm --defined-only --format=posix "$_a" 2>/dev/null | awk 'NF >= 2 && $2 != "" {print $1}'
      done
    } 2>/dev/null | sort -u > "$_defined" || true

    printf '\t.section .rdata,"dr"\n' > "$_shim_s"
    _shim_n=0
    while IFS= read -r _imp; do
      [[ -n "$_imp" ]] || continue
      _real="${_imp#__imp_}"
      # Only bridge slots whose target is actually defined in this prefix;
      # anything else is a genuinely missing library, and hiding that behind a
      # dangling pointer would turn a link error into a crash at runtime.
      if grep -qxF "$_real" "$_defined"; then
        printf '\t.globl %s\n\t.p2align 3\n%s:\n\t.quad %s\n' "$_imp" "$_imp" "$_real" >> "$_shim_s"
        _shim_n=$((_shim_n + 1))
      fi
    done < "$_undef"

    if [[ "$_shim_n" -gt 0 ]]; then
      "${CC:-gcc}" -c "$_shim_s" -o "$_shim_dir/sdn-imp-shim.o"
      "${AR:-ar}" rcs "$_shim_dir/libsdnimpshim.a" "$_shim_dir/sdn-imp-shim.o"
      rm -f "$_shim_dir/sdn-imp-shim.o"
      IMPSHIM_STATIC="$_shim_dir/libsdnimpshim.a"
      echo "static prefix: bridged $_shim_n PE import slot(s) to static definitions"
      sed -n 's/^\t\.globl //p' "$_shim_s" | sed 's/^/  slot: /'
    else
      echo "static prefix: no PE import slots needed"
    fi
    rm -f "$_shim_s" "$_undef" "$_defined"
    ;;
esac

# THE SYSTEM LIBRARIES LLVM NEEDS ON DARWIN, FROM LLVM ITSELF.
#
# The hand-kept list (-lm -lz -lncurses) had no libxml2, and Homebrew's
# llvm@18 is built with it: libLLVMWindowsManifest.a calls xmlAddChild and
# friends, and lld's COFF driver (swept into the merged libwasmedge.a) calls
# the merger. With Xcode 26's linker the Go link died on _xmlAddChild.
# `llvm-config --link-static --system-libs` is the authority on what the
# archives were built against, so the extras are derived from it (brew llvm@18:
# -lm -lz -lzstd -lcurses -lxml2) rather than grown one link error at a time.
#
# Only -l<name> reaches link.flags. llvm-config can report a library as a
# full path (Linux does, for libz3.so), and a path would break the prefix's
# relocatability; the name resolves against the SDK instead, where libxml2,
# libz and libncurses are part of macOS. Skipped: what the line already
# carries, curses (the same library as ncurses on macOS), and zstd when its
# archive is named in the prefix. Linux and Windows keep their literal lists.
DARWIN_SYSTEM_LIBS="-lc++ -lc++abi -lm -lz -lncurses"
if [[ "$(uname -s)" == Darwin ]]; then
  for sys_lib in $($LLVM_CONFIG --link-static --system-libs 2>/dev/null || true); do
    case "$sys_lib" in
      -l*) sys_name="${sys_lib#-l}" ;;
      */lib*)
        sys_name="$(basename "$sys_lib")"
        sys_name="${sys_name#lib}"
        sys_name="${sys_name%%.*}"
        ;;
      *) continue ;;
    esac
    case "$sys_name" in
      ''|curses) continue ;;
      zstd)
        if [[ -n "${ZSTD_STATIC:-}" ]]; then
          continue
        fi
        ;;
    esac
    case " $DARWIN_SYSTEM_LIBS " in
      *" -l${sys_name} "*) continue ;;
    esac
    DARWIN_SYSTEM_LIBS="$DARWIN_SYSTEM_LIBS -l${sys_name}"
  done
  echo "static prefix: darwin system libraries: $DARWIN_SYSTEM_LIBS"
fi

{
  # --start-group wherever the linker is GNU ld: these archives depend on each
  # other BOTH ways (lld calls LLVM, lld's own ELF/Common halves call each
  # other), and a single-pass linker cannot resolve that from any fixed order.
  #
  # MinGW BELONGS IN THIS GROUP and was wrongly excluded with macOS. Only ld64
  # re-scans archives on its own; MinGW links with GNU ld, which does not, so
  # the Windows build was left with 26 undefined lld:: symbols plus
  # llvm::demangle and three WasmEdge ones — every one of them a cycle. It hid
  # behind the larger toolchain mismatch until that was fixed.
  case "$(uname -s)" in Darwin) : ;; *) printf -- '-Wl,--start-group ' ;; esac
  for archive in $GRP $LLVMLIBS ${ZSTD_STATIC:-} ${STDCXX_STATIC:-} ${XML2_STATIC:-} ${ZLIB_STATIC:-} ${IMPSHIM_STATIC:-}; do
    [[ -n "$archive" ]] || continue
    printf '@PREFIX@/lib/%s ' "$(basename "$archive")"
  done
  case "$(uname -s)" in Darwin) : ;; *) printf -- '-Wl,--end-group ' ;; esac
  case "$(uname -s)" in
    # -lc++abi as well as -lc++: libc++'s exception ABI lives in libc++abi on
    # macOS, and without it the link dies on ___cxa_init_primary_exception.
    # The rest is what llvm-config reports (DARWIN_SYSTEM_LIBS above).
    Darwin) printf -- '%s\n' "$DARWIN_SYSTEM_LIBS" ;;
    # MinGW/clang on Windows: the C++ runtime and the sockets/crypto libraries
    # the runtime calls into. MSVC's lib.exe cannot do the `ar -x` extraction
    # WasmEdge's static merge performs, so CMAKE_AR must be llvm-ar there.
    # Every one of these was a name in the link error, not a guess:
    #   ntdll   NtQueryInformationFile, NtSetInformationFile,
    #           NtQueryTimerResolution, RtlGetLastNtStatus (WASI inode-win.cpp)
    #   ktmw32  CreateTransaction, CommitTransaction (transactional file ops)
    #   dbghelp SymInitializeW, SymGetModuleBase64, SymSetOptions … (LLVM's
    #           Windows backtrace support)
    #   xml2    xmlReadMemory, xmlDocDumpFormatMemoryEnc … (LLVM's
    #           WindowsManifest merger, libLLVMWindowsManifest.a)
    #   z       compress2, compressBound, crc32
    #
    # -static-libstdc++ -static-libgcc ARE THE WHOLE POINT ON THIS PLATFORM.
    # MinGW's gcc links the IMPORT library for libstdc++ unless told otherwise,
    # so -lstdc++ alone produced an .exe importing libstdc++-6.dll — a binary
    # that cannot start on a machine without MSYS2 installed, which is the exact
    # failure the static prefix exists to prevent. It went unseen because the
    # Windows leg carried continue-on-error: the job reported success while the
    # step failed, and the release simply shipped no Windows archive at all.
    # -Bstatic around winpthread covers the third GCC runtime DLL
    # (libwinpthread-1.dll); the Windows system import libraries after it must
    # stay dynamic, which is what -Bdynamic restores.
    # No -lxml2 -lz: both are staged into the prefix above and named as
    # archives, so the linker cannot prefer MSYS2's import library for either.
    # What is left after -Bdynamic is Windows' own system import libraries,
    # which are supplied by the OS and must stay dynamic. -llzma and -liconv
    # are libxml2.a's own dependencies, which an import library used to satisfy
    # for us.
    MINGW*|MSYS*|CYGWIN*) printf -- '-static-libstdc++ -static-libgcc -Wl,-Bstatic -lstdc++ -lwinpthread -llzma -liconv -Wl,-Bdynamic -lm -lws2_32 -lbcrypt -lole32 -luuid -lntdll -lktmw32 -ldbghelp\n' ;;
    # No -lstdc++: it is staged into the prefix above and named as an archive,
    # so the linker cannot prefer a shared one. -static-libgcc removes
    # libgcc_s.so.1 the same way. What is left is glibc, which stays dynamic —
    # a statically linked glibc breaks NSS and dlopen.
    #
    # -lrt is NOT redundant. glibc merged librt into libc in 2.34, so on a
    # modern build host the POSIX timer calls WasmEdge's WASI layer makes
    # (timer_create/timer_settime/timer_delete, host/wasi/inode-linux.cpp)
    # resolve out of libc and nobody notices. Building against 2.31 — which is
    # the point, so the binary runs on Debian 12 and RHEL 9 — puts them back in
    # a separate library, and the link fails on `timer_delete'. Naming it is
    # harmless on newer glibc, where librt is kept as an empty stub.
    *)      printf -- '-static-libgcc -lm -ldl -lpthread -lrt -lz -ltinfo\n' ;;
  esac
} > "$STATIC/link.flags"

# EVERY ARCHIVE link.flags NAMES MUST BE IN THE PREFIX.
#
# The file names archives as @PREFIX@/lib/<basename>, which is a promise about
# the prefix's contents that nothing here checked. Discovering a library and
# forgetting to copy it therefore produced a prefix that names a file it does
# not contain, and the only symptom was "cannot find .../libxml2.a" at the far
# end of a Go build on another machine, minutes later and with no hint that the
# prefix was at fault. Checking it here costs one pass and fails where the
# answer is.
missing_named=""
for named in $(tr ' ' '\n' < "$STATIC/link.flags" | grep '^@PREFIX@/lib/'); do
  if [[ ! -f "$STATIC/lib/${named#@PREFIX@/lib/}" ]]; then
    missing_named="${missing_named} ${named#@PREFIX@/lib/}"
  fi
done
if [[ -n "$missing_named" ]]; then
  echo "static prefix: link.flags names archive(s) the prefix does not contain:${missing_named}" >&2
  exit 1
fi
echo "static prefix: link.flags names $(tr ' ' '\n' < "$STATIC/link.flags" | grep -c '^@PREFIX@/lib/') archive(s), all present"

# Stop here when the caller only wants the staged archives. A Docker layer that
# builds LLVM + WasmEdge is expensive and depends ONLY on the version, so the
# image builds it once and rebuilds it only when WASMEDGE_VERSION changes; the
# Go build then lives in a later, cheap layer.
if [[ -n "${WASMEDGE_STATIC_PREFIX_ONLY:-}" ]]; then
  echo "staged static WasmEdge prefix: $STATIC"
  exit 0
fi

# 4. Build the daemon. -extldflags places the C++ runtime + WasmEdge archives
#    LAST on the external link line (after libwasmedge.a from the binding's
#    -lwasmedge), so static symbols resolve. libstdc++/libc stay dynamic — they
#    are base system libraries present on every Ubuntu host.
#
#    DARWIN LINKS THROUGH link.flags. The line below is GNU ld's (--start-group,
#    -lstdc++, -ltinfo) and Apple's ld rejects it outright, so on macOS the
#    daemon is linked the way every other macOS build links it:
#    go-with-wasmedge.sh's static branch, which substitutes the prefix into
#    link.flags. That is also the only line the CI darwin legs ever link with,
#    so this script now proves the same one. GOCACHE stays the caller's (the
#    wrapper would otherwise default it to a cold <repo>/.gocache).
cd "$ROOT/sdn-server"
case "$(uname -s)" in
  Darwin)
    CGO_ENABLED=1 \
    GOCACHE="${GOCACHE:-$(go env GOCACHE)}" \
    WASMEDGE_DIR="$STATIC" \
    bash "$ROOT/scripts/go-with-wasmedge.sh" build -o "$OUT" ./cmd/spacedatanetwork
    ;;
  *)
    CGO_ENABLED=1 \
    CGO_CFLAGS="-I$STATIC/include" \
    CGO_LDFLAGS="-L$STATIC/lib" \
    go build -ldflags "-linkmode external -extldflags \"-Wl,--start-group $GRP $LLVMLIBS -lstdc++ -Wl,--end-group -lm -ldl -lpthread -lz -lzstd -ltinfo\"" \
      -o "$OUT" ./cmd/spacedatanetwork
    ;;
esac

echo "built: $OUT"
DEPS_TOOL="$( (command -v ldd >/dev/null && echo ldd) || (command -v otool >/dev/null && echo 'otool -L') || echo '' )"
if [[ -n "$DEPS_TOOL" ]] && $DEPS_TOOL "$OUT" 2>/dev/null | grep -qi wasmedge; then
  echo "WARNING: binary still references libwasmedge (not self-contained)" >&2
  exit 1
fi
echo "self-contained OK (no libwasmedge dependency):"
[[ -n "$DEPS_TOOL" ]] && $DEPS_TOOL "$OUT" || true
