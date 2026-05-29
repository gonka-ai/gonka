---
id: inference-chain-upgrade-handlers
ai_review: primer
type: implementation
path_filters:
  - "inference-chain/app/upgrades.go"
  - "inference-chain/app/upgrades_enabled.go"
  - "inference-chain/app/upgrade_tracking.go"
  - "inference-chain/app/upgrades/**.go"
---
Use `app.setTrackedUpgradeHandler(name, handler)` as the standard way to register full software upgrade handlers. Do not call `app.UpgradeKeeper.SetUpgradeHandler(...)` directly for new upgrades unless there is a deliberate, reviewed reason to bypass `LastUpgradeHeight` tracking. The helper applies the shared `withLastUpgradeHeight(...)` wrapper so successful full software upgrades record the applied height in one centralized place.

Historical upgrade registrations remain in `upgrades.go` for readability and for the normal Cosmos SDK upgrade registration pattern, but they are not expected to be re-executed by replaying chain history on a single modern binary. The intended operational model is live upgrade sequencing across historical binaries, where an old binary runs until its scheduled halt and the next binary resumes with the matching handler. Review changes here with that assumption in mind: preserve the registration map for clarity and continuity, but focus behavioral scrutiny on newly introduced upgrade handlers and the current tracked-registration convention.
