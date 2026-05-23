# Suggested commit message

feat(devshard): runtime config via dapi long-poll; lean Testermint e2e

Push governance and epoch params through dapi instead of periodic devshardd
chain queries. The event listener updates ConfigManager; devshardd and
embedded devshard consume snapshots via NodeManager GetRuntimeConfig
long-polling (shared proto under devshard/nodemanager).

- dapi: runtime cache, notifier, GetRuntimeConfig RPC; remove devshard
  AvailabilityTracker wiring from the block dispatcher
- devshardd: drop chainParamsProvider; runtimeconfig provider only
- bind: grace defaults from runtime snapshot; single GetEscrow per bind
- Testermint: RuntimeConfigTests (dapi gRPC); DevsharddRuntimeConfigTests
  (versiond + host devshardd); genesis-only cluster (joinCount=0) with
  versiond overlay for fewer containers
- local/CI: Apple Silicon BLST_PORTABLE builds; versiond image linux/amd64;
  two-phase genesis boot for versiond compose; devshardd-build in Makefile
- docs: params-dataflow.md
