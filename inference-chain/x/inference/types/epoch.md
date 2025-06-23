# Where epoch info is written:

1. `EndBlock` in `module.go`
    a. Create a new upcoming epoch when it's `IsStartOfPocStage`
    b. Set the effective epoch pointer to the upcoming epoch when it's `IsSetNewValidatorsStage`
2. `InitGenesis`

Each write also creates a corresponding epoch group.

# Where epoch info is read:

## Chain-node

1. PoC message handlers. There we need the **latest/upcoming** epoch, for which we are doing PoC at the moment!
   a. `msg_server_submit_poc_batch.go`
   b. `msg_server_submit_poc_validation.go`
2. ...

## API-node

-- TODO: WHAT EPOCH DO WE NEED TO MAKE ShouldBeOperational work correctly? current vs upcoming?

1. Phase tracking in `phase_tracker.go`. We use it to determine if a node should be operational. 
