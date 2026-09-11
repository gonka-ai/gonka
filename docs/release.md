# Release branches

Both release types follow the same initial lifecycle:

1. Branch from `main` and keep a PR open against `main`.
2. Tag the original release on that branch.
3. Merge only after governance has approved the change and it is live on mainnet.
4. Create a maintenance branch from the merge commit on `main`. Later tags for this version live there. Each fix is also opened as a PR to `main`.

`upgrade-vX.Y.Z` and `devshard-vX.Y.Z-vN` can be open at the same time.

## Mainnet: `upgrade-vX.Y.Z`

Covers `inferenced`, `decentralized-api`, and `edge-api`.
Original tag: `release/vX.Y.Z`. Later tags: `release/vX.Y.Z-postN` on `vX.Y.Z`.

```
          upgrade-v0.2.16                      upgrade-v0.2.17
          from main                            from main
          develop + governance                 next develop
          *---- tag: release/v0.2.16 ----*     *-- (no tags yet)
          |                              |     |
main -----*------------------------------*-----*---->
                                       merge
                                         |
                                         +-- v0.2.16
                                             from this merge
                                             maintenance
                                             tags: release/v0.2.16-post1, ...
```

Proposal and test checklist: [prepare-upgrade-proposal.md](./prepare-upgrade-proposal.md).
Cosmovisor and handlers: [upgrades.md](./upgrades.md).

## Devshard: `devshard-vX.Y.Z-vN`

Not a chain software upgrade. It ships through `approved_versions` and can move on its own schedule. Multiple protocol versions can be live, so older maintenance branches stay active.

The chain version in the develop-branch name is the one the work was built against.
Original tag: `release/devshard/vN.0.0`. Later tags: `release/devshard/vN.0.x` on `devshard/vN`.

```
          devshard-v0.2.15-v5                          devshard-v0.2.16-v6
          from main                                    from main
          develop + governance                         next develop
          *---- tag: release/devshard/v5.0.0 ----*     *-- (no tags yet)
          |                                      |     |
main -----*--------------------------------------*-----*---->
                                               merge
                                                 |
                                                 +-- devshard/v5
                                                     from this merge
                                                     maintenance
                                                     tags: release/devshard/v5.0.1, ...
```

Write a hotfix for a live version on the latest released maintenance branch (`devshard/v5`), not on the next develop branch. Release it first (`release/devshard/v5.0.1`), then merge that PR to `main`. Cherry-pick onto every still-active older line (`devshard/v4`, ...). In-progress next-version work picks the fix up by merging or rebasing `main`.

```
hotfix on devshard/v5
  released as release/devshard/v5.0.1
    |
    +--> then that PR merges to main
    +--> cherry-pick to each live older line (devshard/v4)
           tag: release/devshard/v4.0.x
```
