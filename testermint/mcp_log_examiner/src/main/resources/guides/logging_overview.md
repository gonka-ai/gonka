# Logging Overview
An overview of the structure of the logs

## Fields

### Service
The logs include logs from ALL parts of the test. Each is denoted in the `service` field in the logs:
- Each chain node's docker container logs - `node`
- Each AI node's docker container logs - `dapi`
- Testermint logs themselves - `test` logs directly from the testermint framework

### Pair
Each log will have information about the pair it came from as well in the `pair` field:
- genesis - The genesis node and API
- joinx - 1 for ever how many extra nodes have joined.

### Level
Log levels are fairly standard:
- ERROR
- WARN
- DEBUG
- TRACE

### Subsystem
Subsystem is especially useful. It represents the specific subsystem inside of the chain, and possible values include:

- Payments - relating to actual movement of coins
- EpochGroup - relating to setting up and changing EpochGroups, used to track power and members in an Epoch
- PoC - relating to storing, updating or calculating Proof of Compute and voting power
- Tokenomics - relating to calculating long term mining rewards 
- Pricing - related to votes for setting prices of inferences
- Validation - related to validating inferences
- Settle - related to calculating the amounts owed at the end of an epoch
- System - lower level actions
- Claims - related to claiming rewards and validating claims at the end of epochs
- Inferences - related to serving and routing inferences
- Participants - related to updating participants and participant lists
- Messages - related to sending and receiving messages in the block chain
- Nodes - related to updating ML nodes for an API
- Config - related to updating or retrieving config values
- EventProcessing - related to processing events coming from the chain to the API
- Upgrades - related to upgrading components of the systen
- Server - related to the API server (requests, responses, etc)
- Training - related to AI training
- Stages - related to moving between stages of an Epoch
- Balances - related to storing coin balances before the Settle period

Filtering on these values to find what specifically is going wrong can be very helpful for debugging

### Operation
This can give hints as to what testermint was trying to accomplish or what part of testermint was running

### SPECIAL: TestSection:
There are logs where the message starts with `TestSection:` followed by a description of what is going on in the test.

These are CRUCIAL for quick understanding of the context of other logs, and where exactly a test failed or had an issue. Always try to include these in queries.
