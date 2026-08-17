---
name: upgrade-bootstrap
description: Bootstrap a new Gonka full automatic upgrade train for inference-chain and decentralized-api only. Use when the user wants to start a new upgrade branch from the latest main, scaffold the matching chain upgrade handler, make the initial bootstrap commit, and open the canonical draft PR against main. Do not use for MLNode-only or devshard-only work, release rollout, governance voting, or post-upgrade merge steps.
---

# Upgrade Bootstrap

This skill bootstraps an upgrade train. It does not finish one.

Use it only for the special full automatic upgrade flow that covers:

- `inference-chain`
- `decentralized-api`

Do not use this skill for:

- MLNode upgrades
- devshard upgrades
- ad hoc API-only rollouts
- release publishing
- governance voting
- testnet rollout
- merge or post-upgrade cleanup

## Required input

The caller must provide the target version in exact `vX.Y.Z` form.

Everything else is derived from that one string:

- branch: `upgrade-vX.Y.Z`
- upgrade package dir: `inference-chain/app/upgrades/vX_Y_Z/`
- Go package: `vX_Y_Z`
- upgrade constant: `UpgradeName = "vX.Y.Z"`
- draft PR title: `Upgrade vX.Y.Z`

If the caller gives a partial version, a branch name without the `v`, or any
other approximate form, normalize only after confirming the exact target
version.

## Goal

Produce the canonical starting point for the upgrade train:

1. branch from the latest `main`
2. scaffold the minimal chain upgrade handler
3. make one bootstrap commit
4. open a draft PR against `main`
5. stop

That draft PR is the coordination anchor for the rest of the upgrade work.

## Safety rules

- Always branch from the latest `main`, not from the current branch and not
  from an older upgrade branch.
- Prefer non-interactive git commands.
- If local work prevents switching to `main` or fast-forwarding it cleanly, do
  not bulldoze through it. Pause and ask the user how to proceed.
- Never include MLNode or devshard work in this bootstrap unless the caller
  explicitly broadens scope.
- Keep the initial scaffold intentionally small. Do not start implementing the
  actual upgrade features unless the caller asks.

## Bootstrap workflow

### 1. Prepare the branch

Start from the latest `main`:

```bash
git fetch origin
git switch main
git pull --ff-only origin main
git switch -c upgrade-vX.Y.Z
```

If the repo is dirty and switching branches would disturb local work, stop and
ask before proceeding.

### 2. Scaffold the upgrade handler

Create the minimal handler scaffold modeled after the current tracked-upgrade
pattern.
Touch only the upgrade-registration surface:

- `inference-chain/app/upgrades.go`
- `inference-chain/app/upgrades/vX_Y_Z/constants.go`
- `inference-chain/app/upgrades/vX_Y_Z/upgrades.go`
- `inference-chain/app/upgrades/vX_Y_Z/upgrades_test.go`

Do the following:

- Add the new import in `inference-chain/app/upgrades.go`
- Register the handler in `setupUpgradeHandlers()` using
  `app.setTrackedUpgradeHandler(...)`, not raw `SetUpgradeHandler(...)`
- Create `constants.go` with the exact on-chain upgrade name
- Create `upgrades.go` with a minimal handler that:
  - logs start and success
  - fixes missing `capability` version in `fromVM`
  - runs `mm.RunMigrations(...)`
  - does nothing else yet
- Create `upgrades_test.go` with a test that pins `UpgradeName` exactly

The initial scaffold should not:

- add real migration logic
- bump module `ConsensusVersion`
- register empty migrations in `registerMigrations()`
- create proposal/release/governance artifact docs
- modify join-stack image tags

Those belong to later upgrade work, not to bootstrap.

The registration helper matters: it keeps `LastUpgradeHeight` tracking attached
to every software upgrade handler automatically. Do not bypass it unless the
upgrade work is intentionally changing that behavior.

### 3. Keep the scaffold comments useful

The scaffold should explain the contract briefly:

- `UpgradeName` must exactly match the future on-chain proposal name
- future migration steps belong below the capability-version fix and above
  `RunMigrations`
- if later work bumps a module `ConsensusVersion`, that later work must also
  register the corresponding migration in `registerMigrations()`

Keep these comments short and operational.

### 4. Verify only the bootstrap surface

Run a narrow test for the new package. When using Go commands, always go
through `zsh -lc` so the shell loads the Go environment.

Example:

```bash
cd inference-chain
zsh -lc 'go test ./app/upgrades/vX_Y_Z'
```

If the new package needs broader verification because of compile-time
registration issues, extend to the smallest useful check. Do not turn bootstrap
into a full upgrade test pass.

### 5. Make the initial bootstrap commit

Use a focused commit message. Default:

```text
chore(upgrade): scaffold vX.Y.Z upgrade handler
```

Stage only the scaffold files and commit them. Do not mix unrelated work into
this commit.

### 6. Push and open the draft PR

Push the new branch and open a draft PR against `main`.

The PR is the canonical upgrade thread. It should exist immediately after the
bootstrap commit so future work has one shared place for review and scope.

Default PR title:

```text
Upgrade vX.Y.Z
```

Default PR body should be short and clearly marked as bootstrap. Include:

- that this is the draft upgrade train for the full automatic `inference-chain`
  + `decentralized-api` upgrade
- that the branch currently contains only the initial upgrade-handler scaffold
- that later commits on this branch will accumulate the actual upgrade changes
- that MLNode and devshard work are out of scope for this PR unless explicitly
  added later

Do not pad the PR with release, voting, or rollout instructions.

If GitHub connector tools are available, prefer them for PR creation. Otherwise
use a non-interactive `gh pr create --draft ...` flow.

## Stop condition

Stop as soon as all of the following exist:

- local branch `upgrade-vX.Y.Z`
- minimal handler scaffold in `inference-chain/app/upgrades/vX_Y_Z/`
- one bootstrap commit
- pushed branch
- draft PR against `main`

Do not continue into feature implementation unless the caller explicitly asks.

## Report back

When finished, report:

- branch name
- bootstrap commit hash
- test command run
- draft PR URL
- any blocker or deviation from the standard bootstrap flow
