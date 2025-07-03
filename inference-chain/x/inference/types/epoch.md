# Where epoch info is written:

# Terminology

1. Current/Effective epoch: the epoch that has validators who are currently the chain validators.
2. Upcoming epoch: the epoch that is being prepared for the next PoC stage, which will become the current/active epoch after the next `EndBlock`.
3. Previous epoch: the epoch before the current/effective epoch.
4. Latest epoch: the latest epoch that has been created, which can be either the current or upcoming epoch.

# Creation

1. `EndBlock` in `module.go`
    a. Create a new upcoming epoch when it's `IsStartOfPocStage`. Create new `EpochGroupData` and `Group` via a call to `CreateEpochGroup` and .
    b. Set the effective epoch pointer to the upcoming epoch when it's `IsSetNewValidatorsStage`
2. `InitGenesis`
    a. Sets the epoch group 0

Each write also creates a corresponding epoch group.

### Epoch group data

1. Root epoch group data is created only in `EndBlock` `IsStartOfPocStage`

# Where epoch info is read:

## Chain-node

### EpochData

1. PoC message handlers. There we need the **latest/upcoming** epoch, for which we are doing PoC at the moment!
   a. `msg_server_submit_poc_batch.go`
   b. `msg_server_submit_poc_validation.go`
2. `module.go`, `EndBlock`: `onSetNewValidatorsStage` settling accounts: we need **current** for settling accounts
3. `module.go`, `EndBlock`: `onSetNewValidatorsStage` computing new weights: we need both the **latest/upcoming** epoch and the **current** epoch, for computing new PoC weights
4. `module.go`, `EndBlock`: `onSetNewValidatorsStage` move upcoming to effective by updating the effective epoch pointer: we need the **upcoming** epoch for this
5. Pricing msg handler: `SubmitUnitOfComputePriceProposal`. We need the **current/effective** epoch, because new price will be computed at epoch transition

### Epoch group data

## API-node

1. Phase tracking in `phase_tracker.go`. We use it to determine if a node should be operational. `latest` epoch is used. 
