# app

The composition root: it builds every layer package into one gateway process, starts it in the boot order and stops it in the shutdown order. What each file is for, the wiring order and the shutdown contract live in the gateway's [`README.md`](../README.md), "The composition root", and in [`docs/operations.md`](../docs/operations.md), "Boot" and "Shutdown".

The binary is [`../main.go`](../main.go): it copies the version the build stamps into `main.Version` to `app.Version` and calls `app.Main`. The stamp stays on `main.Version` because every build file sets `-X main.Version=…`, and `-X` on a symbol that does not exist is silently ignored.

## The harness surface

`composed.go` exports what the [`scenarios`](../scenarios/) harness needs: compose the real gateway over a fake chain and in-process hosts (`Compose`, `Sources`), boot it one step at a time inside a `testing/synctest` bubble (`BootSteps`, `BootStep`), read its parts (`Handler`, `Config`, `Store`, `Observer`, `Escrows`, `Manager`, `Races`, `Nonces`, `Journal`, `Telemetry`), act as the operator (`Deactivate`, `Settle`) and stop it (`Shutdown`, `ShutdownGracePeriod`). Nothing in the binary calls it. It holds adapters only: a change of behaviour belongs in the file the adapter reaches, never here.
