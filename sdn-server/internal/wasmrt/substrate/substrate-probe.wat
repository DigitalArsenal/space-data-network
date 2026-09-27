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
(module
 (import "env" "memory" (memory 1 1 shared))
 (import "wasi" "thread-spawn" (func $spawn (param i32) (result i32)))
 (func (export "wasi_thread_start") (param $tid i32) (param $arg i32)
   (if (i32.eq (local.get $arg) (i32.const 1024)) (then
     (loop $notify
       (br_if $notify (i32.eqz (memory.atomic.notify (i32.const 16) (i32.const 1)))))
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
 (func (export "notify_without_store") (result i32)
   (i32.atomic.store (i32.const 16) (i32.const 0))
   (if (i32.lt_s (call $spawn (i32.const 1024)) (i32.const 1)) (then (return (i32.const -1))))
   (memory.atomic.wait32 (i32.const 16) (i32.const 0) (i64.const 500000000)))
)
