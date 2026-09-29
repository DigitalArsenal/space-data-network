;; Runtime substrate probe (design A21, A31). Embedded in the daemon and run by
;; `spacedatanetwork substrate-selftest` and before the first partition-store
;; instance opens. It measures the runtime's behaviour; it trusts no version
;; string and no patch manifest.
;;
;;   notify_without_store: a notify must wake a waiter even though the word
;;     never changes (the atomic-wait patch). Bounded: 0 = woken, 2 = timed out.
;;   spawn_spinners(n): n threads spin forever on their own counter at
;;     64 + 64*i (one cache line each), with no call out and no stop check:
;;     only an executor stop that reaches every thread ends them (the
;;     stop-token patch).
;;   memarg_offset_notify: a notify addressed through its memarg offset
;;     (`i32.const 0`, offset=32, as wasi-libc's thread-list lock is) must wake
;;     a waiter on address 32 (the atomic-memarg-offset patch; the AOT
;;     compiler dropped the offset). Bounded: 0 = woken, 2 = timed out.
;;   memarg_offset_wait: a wait addressed that way must compare the word at
;;     32 (7), not the word at 0 (0): waiting for 7 times out (2) where the
;;     wrong address returns not-equal (1) at once.
(module
 (import "env" "memory" (memory 1 1 shared))
 (import "wasi" "thread-spawn" (func $spawn (param i32) (result i32)))
 (func (export "wasi_thread_start") (param $tid i32) (param $arg i32)
   (if (i32.eq (local.get $arg) (i32.const 1024)) (then
     (loop $notify
       (br_if $notify (i32.eqz (memory.atomic.notify (i32.const 16) (i32.const 1)))))
     (return)))
   (if (i32.eq (local.get $arg) (i32.const 2048)) (then
     ;; Until it woke the waiter, or the waiter gave up (word 36).
     (block $out (loop $again
       (br_if $out (i32.atomic.load (i32.const 36)))
       (br_if $again (i32.eqz (memory.atomic.notify offset=32 (i32.const 0) (i32.const 1))))))
     (return)))
   (loop $spin
     (drop (i32.atomic.rmw.add
       (i32.add (i32.const 64) (i32.shl (local.get $arg) (i32.const 6)))
       (i32.const 1)))
     (br $spin)))
 (func (export "spawn_spinners") (param $n i32) (result i32)
   (local $i i32) (local $ok i32)
   (block $done (loop $next
     (br_if $done (i32.ge_u (local.get $i) (local.get $n)))
     (if (i32.ge_s (call $spawn (local.get $i)) (i32.const 1))
       (then (local.set $ok (i32.add (local.get $ok) (i32.const 1)))))
     (local.set $i (i32.add (local.get $i) (i32.const 1)))
     (br $next)))
   (local.get $ok))
 (func (export "memarg_offset_notify") (result i32)
   (local $r i32)
   (i32.atomic.store (i32.const 32) (i32.const 0))
   (i32.atomic.store (i32.const 36) (i32.const 0))
   (if (i32.lt_s (call $spawn (i32.const 2048)) (i32.const 1)) (then (return (i32.const -1))))
   (local.set $r (memory.atomic.wait32 (i32.const 32) (i32.const 0) (i64.const 500000000)))
   (i32.atomic.store (i32.const 36) (i32.const 1))
   (local.get $r))
 (func (export "memarg_offset_wait") (result i32)
   (i32.atomic.store (i32.const 0) (i32.const 0))
   (i32.atomic.store (i32.const 32) (i32.const 7))
   (memory.atomic.wait32 offset=32 (i32.const 0) (i32.const 7) (i64.const 50000000)))
 (func (export "notify_without_store") (result i32)
   (i32.atomic.store (i32.const 16) (i32.const 0))
   (if (i32.lt_s (call $spawn (i32.const 1024)) (i32.const 1)) (then (return (i32.const -1))))
   (memory.atomic.wait32 (i32.const 16) (i32.const 0) (i64.const 500000000)))
)
