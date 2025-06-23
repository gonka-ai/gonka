# Where epoch info is written:

1. EndBlocker
2. InitGenesis

Each write also creates a corresponding epoch group.

# Where epoch info is read:

1. PoC message handlers. There we need the latest/upcoming epoch, for which we are doing PoC at the moment!
   a. `msg_server_submit_poc_batch.go`
   b. `msg_server_submit_poc_validation.go`
2. ...
