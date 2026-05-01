# Commands

All commands assume the `upgrade-v0.2.12` branch. Use a worktree to avoid switching branches:

```bash
git fetch origin upgrade-v0.2.12
git worktree add /tmp/gonka-pr1112 origin/upgrade-v0.2.12
cd /tmp/gonka-pr1112/inference-chain
```

## Run all self-vote unit tests

```bash
go test ./x/bls/keeper/... -v -run "TestDetermineValidDealers" -count=1
```

## Run state-machine transition tests

```bash
go test ./x/bls/keeper/... -v -run "TestCompleteDKG|TestProcessDKGPhaseTransitionForEpoch_Verifying|TestActiveEpoch" -count=1
```

## Run dispute resolution tests (excluded verifier feature)

```bash
go test ./x/bls/keeper/... -v -run "TestApplyDealerComplaintOutcomes|TestAdjudicateComplaints|TestDetermineValidDealersWithConsensus_ExcludedVerifiers" -count=1
```

## Full BLS keeper regression suite

```bash
go test ./x/bls/keeper/... -count=1
```

## Clean up worktree

```bash
git worktree remove /tmp/gonka-pr1112
```