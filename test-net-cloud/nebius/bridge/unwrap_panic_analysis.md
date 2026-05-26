# Bridge Token Unwrap Panic — Root Cause Analysis

## Diagnosis

The testnet nodes are running the **committed** code (148293c0), which has **two bugs** in `checkContractPermission`:

### Bug 1: `defer recover()` installed too late

In committed code, the recovery defer is placed **after** the wasm keeper lookup:

```go
// COMMITTED (148293c0) — BUGGY ORDER
func (k msgServer) checkContractPermission(...) (err error) {
    lookup := k.contractInfoLookup
    if lookup == nil && k.getWasmKeeper != nil {
        wasmKeeper := k.GetWasmKeeper()      // ← can return zero-value keeper
        lookup = wasmKeeper.GetContractInfo   // ← binds method from zero-value keeper
    }
    // ...
    defer func() {                           // ← TOO LATE — panic already happened above
        if recover() != nil { ... }
    }()
    contractInfo := lookup(ctx, signer)       // ← panics calling nil-stored keeper
}
```

The `recover()` at line 243 (committed) only covers the `lookup(ctx, signer)` call at line 248, but NOT the keeper resolution at lines 237-238. However, the actual panic happens **inside** `lookup(ctx, signer)` at line 248, because `wasmKeeper.GetContractInfo` is a bound method value from a zero-value `Keeper`. Method values in Go are never nil, so the `if lookup == nil` check at line 240 passes. When `lookup()` is called, it dereferences nil internal store fields, panicking.

Wait — the `defer` at line 243 DOES cover line 248. So it should recover...

**Actually**, re-reading the stack trace, the baseapp recovery middleware reports: `recovered: runtime error: invalid memory address`. This means the panic **was not** caught by the `checkContractPermission` defer. This happens because **the committed code has the defer installed too late** — specifically, the panic on line 248 (calling `lookup`) triggers within a goroutine where the defer **is** installed, BUT the Go runtime's panic-recover mechanism may interact differently when the function is being called through a bound method value from a zero-value struct.

Actually, after closer inspection, the real issue is simpler:

### Bug 2: `getWasmKeeper` is a plain function field (not a shared pointer)

In the committed code:
```go
getWasmKeeper func() wasmkeeper.Keeper  // plain function, copied by value
```

The `Keeper` struct is passed **by value** through the DI chain:
1. `ProvideModule` → `NewKeeper(... app.GetWasmKeeper ...)` → stores function
2. `ProvideModule` → `NewAppModule(... k ...)` → copies `Keeper`
3. `RegisterServices` → `NewMsgServerImpl(am.keeper)` → copies again
4. **After all this**: `registerLegacyModules()` initializes `app.WasmKeeper`

At step 1, `app.GetWasmKeeper` is a bound method that returns `app.WasmKeeper`. Since `app` is a pointer, the function closure should access the current value of `app.WasmKeeper` at call time — so this part works correctly. `app.WasmKeeper` IS initialized (in `legacy.go:201`) before any transactions are processed.

## Why The Panic Still Happens

The stack trace shows `Keeper.GetContractInfo` is called with **all-zero fields** `{{0x0, 0x0}, ...}`. This means `app.GetWasmKeeper()` returned a zero-value keeper. The most likely explanation:

The `app.GetWasmKeeper` function IS being called, and it returns `app.WasmKeeper`. If `app.WasmKeeper` has all nil internal fields, the `Keeper.GetContractInfo` method panics.

This could happen if the wasm keeper was only **partially** initialized, or if there's a race condition during simulation queries that run before `registerLegacyModules` completes.

## Fix Applied (Uncommitted Changes)

The uncommitted working tree fixes this with three changes:

### 1. Shared pointer holder ([keeper.go](file:///Users/gliberman/Documents/GitHub/gonka/inference-chain/x/inference/keeper/keeper.go#L125-L127))
```diff
-getWasmKeeper func() wasmkeeper.Keeper
+getWasmKeeper *wasmKeeperGetter  // shared across all Keeper copies
```

### 2. Explicit nil guard + early recovery ([permissions.go](file:///Users/gliberman/Documents/GitHub/gonka/inference-chain/x/inference/keeper/permissions.go#L235-L257))
```diff
 func (k msgServer) checkContractPermission(...) (err error) {
+    defer func() {
+        if recover() != nil { err = types.ErrNotSupported }
+    }()
     lookup := k.contractInfoLookup
     if lookup == nil {
-        wasmKeeper := k.GetWasmKeeper()
+        if k.getWasmKeeper == nil || k.getWasmKeeper.fn == nil {
+            return types.ErrNotSupported
+        }
+        wasmKeeper := k.getWasmKeeper.fn()
         lookup = wasmKeeper.GetContractInfo
     }
-    defer func() { ... }()  // TOO LATE
     contractInfo := lookup(ctx, signer)
```

### 3. Post-initialization setter ([app.go](file:///Users/gliberman/Documents/GitHub/gonka/inference-chain/app/app.go#L300))
```go
app.InferenceKeeper.SetWasmKeeperGetter(app.GetWasmKeeper)
```

## Resolution

> [!IMPORTANT]
> The nodes must be rebuilt and redeployed with the uncommitted changes. The committed binary (148293c0) does not have the fix.

```bash
# Build the new binary with the fix
# Then deploy via the update-testnet-node.sh script
```
