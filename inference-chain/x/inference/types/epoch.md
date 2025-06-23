# Where epoch info is written:

### Epoch data

1. `EndBlock` in `module.go`
    a. Create a new upcoming epoch when it's `IsStartOfPocStage`
    b. Set the effective epoch pointer to the upcoming epoch when it's `IsSetNewValidatorsStage`
2. `InitGenesis`
    a. Sets the epoch group 0

Each write also creates a corresponding epoch group.

### Epoch group data

# Where epoch info is read:

## Chain-node

### Epoch data

1. PoC message handlers. There we need the **latest/upcoming** epoch, for which we are doing PoC at the moment!
   a. `msg_server_submit_poc_batch.go`
   b. `msg_server_submit_poc_validation.go`
2. `module.go`, `EndBlock`: `onSetNewValidatorsStage` settling accounts: we need **current** for settling accounts
3. `module.go`, `EndBlock`: `onSetNewValidatorsStage` computing new weights: we need both the **latest/upcoming** epoch and the **current** epoch, for computing new PoC weights
4. `module.go`, `EndBlock`: `onSetNewValidatorsStage` move upcoming to effective by updating the effective epoch pointer: we need the **upcoming** epoch for this

### Epoch group data

## API-node

-- TODO: WHAT EPOCH DO WE NEED TO MAKE ShouldBeOperational work correctly? current vs upcoming?

1. Phase tracking in `phase_tracker.go`. We use it to determine if a node should be operational. 
