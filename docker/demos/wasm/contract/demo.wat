(module
  ;; Host functions provided by the Zenon WASM runtime.
  (import "env" "state_read"  (func $state_read  (param i32 i32 i32 i32) (result i32)))
  (import "env" "state_write" (func $state_write (param i32 i32 i32 i32) (result i32)))
  (import "env" "emit_event"  (func $emit_event  (param i32 i32 i32 i32) (result i32)))

  ;; 1 page of linear memory (64 KiB), exported.
  (memory (export "memory") 1)

  ;; --- execute(args_ptr, args_len) -> i32 ---
  ;; Reads "count" from state, increments, writes back, emits an event.
  ;; Returns 0 on success.
  (func (export "execute") (param $args_ptr i32) (param $args_len i32) (result i32)
    (local $count i64)
    (local $new_count i64)

    ;; Read current count (8 bytes LE) from key "count"
    (call $state_read
      (i32.const 0)    ;; key_ptr: "count" at offset 0
      (i32.const 5)    ;; key_len
      (i32.const 100)  ;; result_ptr
      (i32.const 8)    ;; result_max_len
    )
    drop

    ;; Load the 8-byte LE value into $count
    (local.set $count (i64.load (i32.const 100)))

    ;; Increment
    (local.set $new_count (i64.add (local.get $count) (i64.const 1)))

    ;; Store new count at offset 200 (8 bytes LE)
    (i64.store (i32.const 200) (local.get $new_count))

    ;; Write state: key "count" -> new value.
    ;; state_write returns 0 on success, 1 if the contract lacks the spendable
    ;; QSR to lock this key's storage deposit. Trap on failure instead of
    ;; dropping the error: a silently-ignored write makes the contract appear to
    ;; succeed (it still emits an event and returns 0) while persisting nothing.
    (if (call $state_write
          (i32.const 0)    ;; key_ptr
          (i32.const 5)    ;; key_len
          (i32.const 200)  ;; val_ptr
          (i32.const 8))   ;; val_len
      (then unreachable))

    ;; Emit event: topic at 300 (32 bytes), data at 400 (8 bytes), indexed=1
    ;; Zero-fill the 32-byte topic at offset 300
    (call $emit_event
      (i32.const 300)  ;; topic_ptr
      (i32.const 200)  ;; data_ptr (the new count bytes)
      (i32.const 8)    ;; data_len
      (i32.const 1)    ;; indexed
    )
    drop

    (i32.const 0) ;; success
  )

  ;; --- view(args_ptr, args_len) -> i32 ---
  ;; Returns a pointer to [u32 LE length][8 bytes LE count].
  (func (export "view") (param $args_ptr i32) (param $args_len i32) (result i32)
    ;; Read current count
    (call $state_read
      (i32.const 0)
      (i32.const 5)
      (i32.const 100)
      (i32.const 8)
    )
    drop

    ;; Lay out [u32 LE length = 8][8 bytes LE value] at offset 500
    (i32.store (i32.const 500) (i32.const 8))  ;; length prefix
    (i64.store (i32.const 504) (i64.load (i32.const 100))) ;; value

    (i32.const 500) ;; return pointer
  )

  ;; --- Data section: key "count" at offset 0 ---
  (data (i32.const 0) "count")
)
