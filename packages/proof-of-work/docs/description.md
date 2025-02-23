

# Race Procedure

1. **Receive Race Start Signal**
   - Synchronize and get the race deadline and difficulty level.
   - `deadline = GetDeadline()`
   - `difficulty = GetDifficulty()`

2. **Obtain Distributed Seed**
   - Based on the blockchain or other distributed source:
   - `distributedSeed = GetDistributedSeed(chain)`

3. **Initialize Model**
   - Initialize the LLM model with the distributed seed and difficulty:
   - `llmModel = LLM(distributedSeed, difficulty)`

4. **Generate Hashes for X Seconds**
   - Generate hashes until the deadline:
   - `hashes = GenerateHashes(llmModel, X, difficulty, deadline)`

5. **Send Generated Hashes to Network**  
    // How to guarantee it's send before deadline? May be send by batch?


## FUN GenerateHashes(llmModel, X, difficult, deadline)
```
hashes = []
publicKey = GetNodePublicKey()  

while True: 
    salt = GetNextSalt()
    hash = GenerateHash(publicKey, salt, llmModel)

    if CurretTime() > deadline: # How to guarantee it's send before deadline? may be send them one by one?
        return hashes

    if GetLeadingZerosAmount(hash) >= difficult:
        hashes.add((hash, seed))
```

## GenerateHash(publicKey, salt, llmModel)
```
tokens = GetModelInput(publicKey, salt) # it's tokensEmbedding  directly

output = llmModel.forward(tokens) # 1 step or more?
hash = SHA256(output)
return hash
```

## ValidateHash(nodePublicKey, hash, salt, difficult)
Any node can validate generated hashes with this function

```
llmModel = LLM(seed, difficul)
hash = GenerateHash(nodePublicKey, salt, llmModel)
if GetLeadingZerosAmount(hash) >= difficult:
    return True

return False
```

## LLM
LLM-like model. Currently transformer with replaced layer to achive reproducibility

## GetModelInput(publicKey, salt)
Generate Matrix of token embeddings
Output shape is `N_tokens x dim`

