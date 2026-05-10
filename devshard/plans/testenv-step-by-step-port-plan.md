# devshard testenv — step-by-step port plan

This document contains the extracted implementation sequence that was
previously in `testenv.md` section 6.

## Phase list

0. Branch and skeleton.
1. Create reusable `devshard/blockoracle` package.
2. Port and wire `mock-chain` container.
3. Port and wire `height-sync` container.
4. Adapt bridge (`testenv/bridge/grpc.go`) to current `MainnetBridge`.
5. Inject `BlockOracle` seam into `host.Host`.
6. Implement `testenv/mockdapi` library.
7. Implement testenv stub engines and deferred extensions.
8. Implement `devshardd-testenv` wiring and tests.
9. Integrate `devshardctl` with testenv env/flags.
10. Implement `gencompose` and config/compose generation flow.
11. Port service Dockerfiles and pin contracts by tests.
12. Add dev overlay (`air` + `dlv`) and related smoke contracts.
13. Add observability stack (metrics/logs/traces roadmap).
14. Finalize Makefile/operator docs.
15. Wire CI targets and integration automation.

## Notes

- The phase-level detailed narrative is now maintained outside the
  main architecture doc to keep `testenv.md` focused on current
  behavior, interfaces, and validation contracts.
- If you need to restore the full expanded per-phase text, recover it
  from git history for `testenv.md` before this extraction commit.
