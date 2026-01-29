# rules & context 
- same dir
- .cursorrules/rules.md

# Motivation 

new poc takes too much memory on chain and motivate to record too much transaction 
=> we want to change system to record only commit, instead of full PoC Batches

Proposal:
- during PoC phase, `api` maintain storage of PoCArtifactV2 in merkle tree like:
[artifact0, artifact1, ... artifactn]
- splitting between node_id/node_num is also somehow maintained locally
- after each /generated, `api` updated merkle tree and has previous root hash (versioning must be supported or we must have incremental merkle tree or we must recreate it on the go)

- chain maintain new transaction:
```
message PoCV2StoreCommit {
  string participant_address = 1;
  int64 poc_stage_start_block_height = 2;
  uint32 n_nonces
  bytes root_hash
}
```

every N sec (5s), participant record transaction on chain with last merkle tree root 
chain record only if n_nonces > last_recorded.n_nonces or just pass (not reject but doesn't update record). reason - to avoid overriding with old data if transactions out of order
chain not allows to update storage too many times. e.g. limitation once in 5 sec. for example it might be counter on chain and if (current_block.time - poc_block_start.time) / N or smth like that (i can imagine that it might be just time of last record. but let's thing critical)
motivation - we want to not allow to send this tx too many times to not overwhelm chain. hard limit 

after the end of generation phase, each participant report weight distribution accros nodes which participate in PoC (reuse current way to detect such node). tx smth like:
```
message MLNodeWeight {
  string node_id
  uint32 weight 
}

message MLNodeWeightDistribution {
  option (cosmos.msg.v1.signer) = "creator";
  string creator = 1;
  repeated PoCValidationV2 validations = 2;
  repeated MLNodeWeight weight
}
```
(that must be send but not that urgent)

chain strictly check that total weight matches last_commit.n_nonces (`api` must check which value is on chain after generation phase as it might have sent newer version)
chain set weight of mlnodes based on that 

also, during the poc validation, each participant sample VALIDATION_BATCH (get constant from current code) of idx from each participant and directly request from another participant artifacts on this positions (e.g. (0, 2, 49, 1999, n_nonces-1))
in response it gets data from this position + merke proof for each nonce (for position)
    1. define request response format (similat to offchain payloads)
    2. define how data is stored. should we use postgress / same local storage as for inference or it'll by much more efficient just use byes
    3. define how to limit access to this endpoint (signed request? only from previous validators signed by private key? also limit amount of request from the same IP to this endpoint (what is standard way to not implement manyally))
        TIP: requester must send SignerAddr to not check signatures by all possuble signer keys (warm + cold)
    4. requesting from each participant must be in random order and with randomization in timing. to avoid sincornization when they hit each other
    5. seems like requester should have limited amount of paralel workers, less then validators. to not overload.

then requester validate every merle proof and then validate in the same way as it validate sampled nonces now 
    Important: if response had at least one not unique nonce => mark participant as invalid
    If data was incorrect / proof not valid - try to receive once again if not - mark as invalid (does it make sense to request again? or we would know?) 

When decision about all participant is done (or K blocks before end of validation), participant must set decision as single (or multiple each under 500 votes) as MsgSubmitPocValidationsV2
```



------
Offtop: we definitely want to save time for poc generation start block and poc generation end block to be able to normalize weight. it's protection agains higher weight if blocks are long 

Offtop 2: int64 -> int32
```
message PoCArtifactV2 {
  int64 nonce = 1; <-- here
  bytes vector = 2; // Opaque bytes (encoding defined at application level)
}
```


# Part 2: Implementation & Testing (each is separate plan and separate .md)

## Phase 1: Implement Storage and generating merkle tree in required way and proofs 
- cover by tests
- check incremental 
- store on disk and in memory 
- integrate it's generation to `api`

## Phase 2: implement api to get proofs by merke tree, add it's check to testermint tests (maybe as part of `bandwidth limiter with rate limiting` as it's open at my screen)
- cover by test 
- checking signatures (with effective caching of granter / grantee!!! it's importnat. but make requirement for signature disablable )
- function to request with all required headers

I'll run testerming and it must check both that merkle tree produced and correct proof generated for requestrs

## Phase 3 add new proto / transaction types
- add commit 
- add writing commit each N sec (without disableing batches and changing logic)
- add verification of that to testermint
- add mlnode distribution 
- make sure it's recorded 
- make sure this data is used to set mlnode distribution weight instead of curent per node recording 

## Phase 4: Validation & Switching to new approach 
- ...testerming

## Phase 5: cleanup
