;; Independent WASI threads contract fixture: child writes 42 into the shared
;; word and wakes the parent. No orbital code and no self-generated oracle.
(module
 (import "env" "memory" (memory 1 2 shared))
 (import "wasi" "thread-spawn" (func $spawn (param i32) (result i32)))
 (func (export "wasi_thread_start") (param $tid i32) (param $arg i32)
   (if (i32.eq (local.get $arg) (i32.const 8)) (then unreachable))
   (if (i32.eq (local.get $arg) (i32.const 16)) (then
     (loop $notify
       (br_if $notify (i32.eqz (memory.atomic.notify (i32.const 16) (i32.const 1)))))
     (return)))
   (if (i32.eq (local.get $arg) (i32.const 4)) (then
     (block $released (loop $wait
       (br_if $released (i32.ne (i32.atomic.load (i32.const 4)) (i32.const 0)))
       (drop (memory.atomic.wait32 (i32.const 4) (i32.const 0) (i64.const 10000000)))
       (br $wait)))
     (return)))
   (i32.atomic.store (local.get $arg) (i32.const 42))
   (drop (memory.atomic.notify (local.get $arg) (i32.const 1))))
 (func (export "run") (result i32)
   (i32.atomic.store (i32.const 0) (i32.const 0))
   (if (i32.lt_s (call $spawn (i32.const 0)) (i32.const 1)) (then unreachable))
   (block $done (loop $wait
     (br_if $done (i32.eq (i32.atomic.load (i32.const 0)) (i32.const 42)))
     (drop (memory.atomic.wait32 (i32.const 0) (i32.const 0) (i64.const 100000000)))
     (br $wait)))
   (i32.atomic.load (i32.const 0)))
 (func (export "trap_worker") (result i32) (call $spawn (i32.const 8)))
 (func (export "capacity") (result i32) (local $i i32) (local $failed i32)
  (i32.atomic.store (i32.const 4) (i32.const 0))
  (loop $spawn_loop
   (if (i32.lt_s (call $spawn (i32.const 4)) (i32.const 0))
    (then (local.set $failed (i32.add (local.get $failed) (i32.const 1)))))
   (local.set $i (i32.add (local.get $i) (i32.const 1)))
   (br_if $spawn_loop (i32.lt_s (local.get $i) (i32.const 33))))
  (i32.atomic.store (i32.const 4) (i32.const 1))
  (drop (memory.atomic.notify (i32.const 4) (i32.const 32)))
  (local.get $failed))
 (func (export "join_trapped_worker")
  (drop (call $spawn (i32.const 8)))
  (loop $forever (br $forever)))
 (func (export "unbounded_atomic_wait")
  (i32.atomic.store (i32.const 12) (i32.const 0))
  (drop (memory.atomic.wait32 (i32.const 12) (i32.const 0) (i64.const -1))))
 (func (export "notify_without_store") (result i32)
  (i32.atomic.store (i32.const 16) (i32.const 0))
  (drop (call $spawn (i32.const 16)))
  (memory.atomic.wait32 (i32.const 16) (i32.const 0) (i64.const -1)))
)
