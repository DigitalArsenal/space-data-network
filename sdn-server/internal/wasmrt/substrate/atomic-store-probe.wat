;; Each store consumes its address and value, leaving 100 for subtraction.
(module (memory 1 1 shared)
  (func (export "i32.atomic.store") (result i32)
    i32.const 100 i32.const 0 i32.const 7 i32.atomic.store
    i32.const 2 i32.sub)
  (func (export "i64.atomic.store") (result i32)
    i32.const 100 i32.const 0 i64.const 7 i64.atomic.store
    i32.const 2 i32.sub)
  (func (export "i32.atomic.store8") (result i32)
    i32.const 100 i32.const 0 i32.const 7 i32.atomic.store8
    i32.const 2 i32.sub)
  (func (export "i32.atomic.store16") (result i32)
    i32.const 100 i32.const 0 i32.const 7 i32.atomic.store16
    i32.const 2 i32.sub)
  (func (export "i64.atomic.store8") (result i32)
    i32.const 100 i32.const 0 i64.const 7 i64.atomic.store8
    i32.const 2 i32.sub)
  (func (export "i64.atomic.store16") (result i32)
    i32.const 100 i32.const 0 i64.const 7 i64.atomic.store16
    i32.const 2 i32.sub)
  (func (export "i64.atomic.store32") (result i32)
    i32.const 100 i32.const 0 i64.const 7 i64.atomic.store32
    i32.const 2 i32.sub)
)
