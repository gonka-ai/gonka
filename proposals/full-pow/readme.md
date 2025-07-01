# Proof of Compute Proposal

Chain <> Participant's API Node <> Participant's ML Nodes

![participants](participants.png)


## Phase 1 - Sprint

epochParams.IsStartOfPoCStage
epochParams.IsEndOfPoCStage

### Initiation

- Sprint generation – at the start of each epoch the chain derives a 256‑bit Sprint_Seed_1 from the latest block hash via a deterministic VRF. This seed is identical for every device participating in the Sprint.

- Model initialization – every ML node initializes Transformer based on Sprint_Seed_1.

- Node Seed creation - after the model is initialized, every participant generates its unique Node_Seed that
is based on its public key.

- Sprint_Seed_2 broadcast – once enough time passes to load a model for all nodes has passed, the chain emits Sprint_Seed_2, which marks the beginning of the 5‑minute compute window. *TBD: estimate time*

### Compute

- Each ML‑node iterates over nonce values. For every nonce, it derives an Input Seed:
InputSeed = H(Node_Seed, Sprint_Seed_2, nonce).

- The Input Seed is mapped to a 4‑token sequence, which is fed through the Transformer.

- The last output vector of the sequence is extracted; its L2‑norm is calculated and stored as the artefact (node_id, nonce, norm).

- Nodes batch 1 000 artefacts at a time and stream them to the API node

- The API node appends each batch into a Merkle tree corresponding to the node (leaf = hash(nonce, norm, node_id))

### Wrap-up

- When the Sprint timer expires, API finalises the Merkle tree and submits participant id, node id, corresponding Merkle tree root and leaf count. *Possibly also leaf count per node.*

![Sprint](sprint.png)

## Phase 2 - Validation

### Proof Generation

- Determine nonces to validate: deterministically sample N = 200 nonce ids per participant (sampled uniformly across all leaves from that participant’s nonces).

- API node extracts each (nonce, norm) and its Merkle path and posts proof on chain.

### Proof Validation

- Participants’ ML‑nodes split the work of re‑running the 200 nonces for every peer.

- Each check: recompute ||output||_2 and compare with submitted proof.

- Results posted on chain as a float p. *For example, probability of the fact that the participant submitted honest number of nonces).*

- Finalisation: if a participant’s proof‑set gets > 1/2 weighted yes votes, their voting power = leafCount for the epoch. *Voting weights for this vote are equal to voting weights from the previous epoch.*

- Compensation: nodes parked by the scheduler (running inference during the Sprint) receive voting weight compensation equal to their nodes leafCount from previous epoch. *Can complicate, average over time but probably don't need to.*

![Validation](validation.png)

current start & end: 
- `epochParams.IsStartOfPoCValidationStage`
- `epochParams.IsEndOfPoCValidationStage`

