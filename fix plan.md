
### Phase 1: Make it Compile

These must be done first — the project cannot build without them.

| # | Task | File(s) | Description |
|---|------|---------|-------------|
| P0-1 | Fix `fmtInt()` zero bug | `admin/onboarding_state_manager.go:99-108` | `fmtInt(0)` returns `""` instead of `"0"`. The loop `for n > 0` never executes when v==0. Fix: add `if v == 0 { return "0" }` or replace with `strconv.FormatInt(v, 10)` |
| P0-2 | Fix broken imports | `internal/event_listener/new_block_dispatcher.go` | Cherry-pick conflict left stale imports: `"decentralized-api/internal/poc"` (doesn't exist) and `"decentralized-api/poc"` (duplicate name). Remove stale imports, keep only the one actually used. |
| P0-3 | Fix MockClient interface | `mlnodeclient/mock.go` + `poc/validator_v1_test.go` | JF's last commit removed old PoC v1 methods, but main added new ones (e.g. `GenerateV2`). `MockClient` no longer satisfies `MLNodeClient` interface. Add stub implementations for all missing methods. Check `interface.go` for the full method list. Additionally, `poc/validator_v1_test.go` has two fake clients (`fakeNodeClient`, `failingNodeClient`) that also need a `SetNodeState` stub to satisfy the updated interface. |

**Checkpoint: `go build ./...` should pass after Phase 1**

---

### Phase 2: Fix Logic Bugs

| # | Task | File(s) | Description |
|---|------|---------|-------------|
| P1-1 | Fix `isTesting` always false | `admin/node_handlers.go:47-51` | `isTesting := false` is never set to `true`, so `MLNodeStatus()` never returns `TESTING` state. Users will never see "Running pre-PoC validation testing". Fix: add a `testingNodes map[string]bool` to `Server` struct, set it when auto-test starts, clear when finished. |
| P1-2 | Fix `testFailed` detection | `admin/node_handlers.go:48` | `testFailed := state.FailureReason != ""` treats ANY failure as test failure (could be PoC failure). Fix: check `state.MLNodeOnboardingState == string(MLNodeState_TEST_FAILED)` instead. |
| P1-4 | Add HTTP timeout | `broker/node_admin_commands.go:507` + `admin/mlnode_testing_orchestrator.go:121` | `http.Post()` uses default client with NO timeout — hangs forever if MLnode unresponsive. Fix: use `&http.Client{Timeout: 30 * time.Second}` |

**Checkpoint: core logic should work correctly after Phase 2**

---

### Phase 3: Consolidate & Clean Up

| # | Task | File(s) | Description |
|---|------|---------|-------------|
| P1-3 | Merge duplicated auto-test logic | `broker/node_admin_commands.go:432-528` + `admin/mlnode_testing_orchestrator.go:59-150` | Nearly identical ~100-line test logic exists in both files. Keep `MLnodeTestingOrchestrator` as single source of truth, remove `autoTestNodeIfTimeAllows()` from broker. Have broker call orchestrator instead. |
| P1-5 | Extract hardcoded block time | `apiconfig/constants.go` + 4 files | `6.0` seconds is hardcoded in 4 places. Define `const DefaultBlockTimeSeconds = 6.0` in `apiconfig/constants.go`, replace all occurrences. Files: `onboarding_state_manager.go:33`, `mlnode_testing_orchestrator.go:52`, `node_admin_commands.go:446`, `commands.go:71` |
| P1-6 | Extract hardcoded thresholds | `apiconfig/constants.go` + 4 files | Define `AutoTestMinSecondsBeforePoC = 3600` and `OnlineAlertLeadSeconds = 600`. Replace in: `node_admin_commands.go:448`, `mlnode_testing_orchestrator.go:56`, `status_reporter.go:44,23`, `onboarding_state_manager.go:34-35`, `commands.go:124` |
| P2-1 | Remove dead code | `admin/onboarding_state_manager.go:39-51` | Delete the commented-out old `MLNodeStatus` implementation |
| P2-2 | Wire up or remove `BuildNoModelGuidance()` | `admin/status_reporter.go:43-48` | Method exists but never called. Proposal requires showing "MLnode will be tested automatically when there is more than 1 hour until next PoC". Either call it in `getNodes` handler or remove. |

**Checkpoint: no code duplication, no dead code, all constants unified**

---

### Phase 4: Improve Robustness

| # | Task | File(s) | Description |
|---|------|---------|-------------|
| P1-7 | Suppress "no model" error at source | TBD (likely `broker/broker.go` reconciliation) | Proposal: "Don't show confusing error messages if participant isn't active yet". The `getNodes` handler avoids setting `userMsg` when inactive, but the original error log that prints "there is no model for ml node" still fires at the source. Find it and add inactive check. |
| P2-3 | Goroutine lifecycle for auto-tests | `broker/node_admin_commands.go` | Auto-tests run in fire-and-forget goroutines with no tracking or cancellation. Add `sync.WaitGroup` or channel-based test runner. |
| P2-4 | Stop silently ignoring errors | `broker/node_admin_commands.go` + `admin/node_handlers.go` | Multiple `_ = b.QueueMessage(cmd)` ignore failures. `IsParticipantActiveOnChain()` error ignored in `node_handlers.go:25-28`. At minimum, log all errors. |
| P2-6 | Add updateNode auto-test trigger | `admin/node_handlers.go` | `addNode()` has auto-test trigger (lines 242-269) but `updateNode()` doesn't. Add consistent behavior. |

**Checkpoint: robust error handling, no goroutine leaks**
