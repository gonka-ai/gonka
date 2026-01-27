# Security Findings - gonka-ai/gonka

**Date:** 2026-01-24
**Auditor:** AlexeySamosadov
**Scope:** decentralized-api, inference-chain

---

## Summary

| # | Severity | Vulnerability | File | Status |
|---|----------|--------------|------|--------|
| 1 | HIGH | Unbounded Request Body Read (DoS) | post_chat_handler.go:952 | Open |
| 2 | MEDIUM | Unbounded Response Read (DoS) | post_chat_handler.go:908, proxy.go:91 | Open |
| 3 | MEDIUM | Race Condition (TOCTOU) in AuthKey | post_chat_handler.go:113-147 | Open |
| 4 | LOW | Integer Truncation | post_chat_handler.go:167 | Open |
| 5 | HIGH | Predictable RNG for Executor Selection | epochgroup/random.go:70 | Open |
| 6 | MEDIUM | Unsafe Type Assertions (Panic) | completionapi/request.go:41,45 | Open |

---

## Vulnerability #1: Unbounded Request Body Read

**Severity:** HIGH
**Type:** Denial of Service (DoS)
**File:** `decentralized-api/internal/server/public/post_chat_handler.go`
**Lines:** 952-958

### Description

The `readRequestBody` function reads the entire HTTP request body into memory without any size limit. An attacker can send an arbitrarily large request body, causing memory exhaustion and crashing the node.

### Vulnerable Code

```go
func readRequestBody(r *http.Request) ([]byte, error) {
    var buf bytes.Buffer
    if _, err := io.Copy(&buf, r.Body); err != nil {  // NO SIZE LIMIT
        return nil, err
    }
    defer r.Body.Close()
    return buf.Bytes(), nil
}
```

### Attack Scenario

1. Attacker sends POST to `/v1/chat/completions` with multi-GB body
2. Server attempts to read entire body into memory
3. Memory exhaustion → OOM kill → node crash
4. Repeat to keep nodes offline (DoS)

### Recommended Fix

Use `http.MaxBytesReader` or `io.LimitReader` to enforce maximum body size:

```go
const MaxRequestBodySize = 10 * 1024 * 1024 // 10 MB

func readRequestBody(r *http.Request) ([]byte, error) {
    r.Body = http.MaxBytesReader(nil, r.Body, MaxRequestBodySize)
    var buf bytes.Buffer
    if _, err := io.Copy(&buf, r.Body); err != nil {
        return nil, err
    }
    defer r.Body.Close()
    return buf.Bytes(), nil
}
```

### Impact

- Node availability compromised
- Network-wide DoS if multiple nodes targeted
- No authentication required for attack

---

## Vulnerability #2: Unbounded Response Read

**Severity:** MEDIUM
**Type:** Denial of Service (DoS)
**Files:**
- `decentralized-api/internal/server/public/post_chat_handler.go:908`
- `decentralized-api/internal/server/public/proxy.go:91`

### Description

Multiple locations use `io.ReadAll(resp.Body)` to read HTTP responses without size limits. A malicious executor node could return extremely large responses, causing memory exhaustion on the Transfer Agent.

### Vulnerable Code

```go
// post_chat_handler.go:908
bodyBytes, err := io.ReadAll(resp.Body)  // NO SIZE LIMIT

// proxy.go:91
var bodyBytes, err = io.ReadAll(resp.Body)  // NO SIZE LIMIT
```

### Attack Scenario

1. Malicious actor registers as executor node
2. When selected for inference, returns multi-GB response
3. Transfer Agent reads entire response into memory
4. Memory exhaustion → TA node crash

### Recommended Fix

Use `io.LimitReader` to cap response size:

```go
const MaxResponseBodySize = 50 * 1024 * 1024 // 50 MB

limitedReader := io.LimitReader(resp.Body, MaxResponseBodySize)
bodyBytes, err := io.ReadAll(limitedReader)
```

### Impact

- Transfer Agent nodes can be crashed by malicious executors
- Requires attacker to be registered executor (lower risk than #1)

---

## Vulnerability #3: Race Condition (TOCTOU) in AuthKey Check

**Severity:** MEDIUM
**Type:** Race Condition / Time-of-Check to Time-of-Use
**File:** `decentralized-api/internal/server/public/post_chat_handler.go`
**Lines:** 113-147

### Description

The `checkAndRecordAuthKey` function has a race condition between checking if an AuthKey exists (with RLock) and recording it (with Lock). In the window between releasing RLock and acquiring Lock, another goroutine could record the same key.

### Vulnerable Code

```go
func checkAndRecordAuthKey(authKey string, currentBlockHeight int64, context AuthKeyContext) bool {
    authKeysMutex.RLock()
    existingContext, exists := usedAuthKeys[authKey]  // CHECK
    authKeysMutex.RUnlock()
    // <-- RACE WINDOW: another goroutine can add the same key here

    if exists {
        // ...
        authKeysMutex.Lock()
        usedAuthKeys[authKey] = existingContext | context  // USE
        // ...
    }

    // Key doesn't exist path also has same issue
    authKeysMutex.Lock()
    usedAuthKeys[authKey] = context
    // ...
}
```

### Attack Scenario

1. Attacker sends two identical requests with same AuthKey simultaneously
2. Both goroutines pass the RLock check (key doesn't exist)
3. Both goroutines acquire Lock and record the key
4. Result: AuthKey used twice, bypassing replay protection

### Recommended Fix

Use a single Lock for the entire check-and-record operation:

```go
func checkAndRecordAuthKey(authKey string, currentBlockHeight int64, context AuthKeyContext) bool {
    authKeysMutex.Lock()
    defer authKeysMutex.Unlock()

    existingContext, exists := usedAuthKeys[authKey]
    if exists {
        if existingContext&context != 0 {
            return true // Already used in this context
        }
        usedAuthKeys[authKey] = existingContext | context
        return false
    }

    usedAuthKeys[authKey] = context
    authKeysByBlock[currentBlockHeight] = append(authKeysByBlock[currentBlockHeight], authKey)

    if oldestBlockHeight == 0 {
        oldestBlockHeight = currentBlockHeight
    }

    cleanupExpiredAuthKeys(currentBlockHeight)
    return false
}
```

### Impact

- Potential replay attacks in narrow time window
- Could allow double-spending or duplicate inference requests

---

## Vulnerability #4: Integer Truncation in Expiration Calculation

**Severity:** LOW
**Type:** Integer Truncation
**File:** `decentralized-api/internal/server/public/post_chat_handler.go`
**Line:** 167

### Description

The expiration block calculation can truncate to zero for small `timestampExpiration` values due to integer division.

### Vulnerable Code

```go
expirationBlocks = (timestampExpiration * 2) / 4
// If timestampExpiration = 1: (1 * 2) / 4 = 0
```

### Impact

- If result is 0, auth keys may never expire or expire immediately
- Low severity as there's a minimum check on line 170

### Recommended Fix

Ensure minimum value or use ceiling division:

```go
expirationBlocks = (timestampExpiration*2 + 3) / 4  // Ceiling division
if expirationBlocks < 4 {
    expirationBlocks = 4
}
```

---

## Vulnerability #5: Predictable RNG for Executor Selection

**Severity:** HIGH
**Type:** Cryptographic Weakness / Predictable Randomness
**File:** `inference-chain/x/inference/epochgroup/random.go`
**Line:** 70

### Description

The `selectRandomParticipant` function uses Go's `math/rand` package without proper seeding for selecting which executor node handles an inference request. This makes the selection predictable.

### Vulnerable Code

```go
import "math/rand"  // NOT crypto/rand!

func selectRandomParticipant(participants []*group.GroupMember) string {
    cumulativeArray := computeCumulativeArray(participants)

    randomNumber := rand.Int63n(cumulativeArray[len(cumulativeArray)-1])  // PREDICTABLE!
    for i, cumulativeWeight := range cumulativeArray {
        if randomNumber < cumulativeWeight {
            return participants[i].Member.Address
        }
    }
    return participants[len(participants)-1].Member.Address
}
```

### Why This Is Vulnerable

1. **Global Random Source**: Uses `math/rand` global source which is not thread-safe and shares state
2. **No Seeding**: The global source is seeded with a default value (1) if not explicitly seeded
3. **Not Cryptographically Secure**: Even if seeded, `math/rand` uses a predictable PRNG algorithm
4. **Observable Sequence**: An attacker can observe selections and predict future outcomes

### Attack Scenario

1. Attacker observes several executor selections over time
2. Attacker reverse-engineers the PRNG state from observed outputs
3. Attacker predicts which executor will be selected for future requests
4. Attacker can:
   - Ensure their malicious executor is selected for specific requests
   - Front-run or manipulate inference results
   - Game the reward distribution system
   - Target specific users' inference requests

### Recommended Fix

Use deterministic seeding from blockchain state (like other functions in the codebase do):

```go
import (
    "crypto/sha256"
    "encoding/binary"
    "math/rand"
)

func selectRandomParticipant(participants []*group.GroupMember, blockHash []byte, epochIndex uint64) string {
    // Create deterministic seed from blockchain state
    seed := sha256.Sum256(append(blockHash, binary.BigEndian.AppendUint64(nil, epochIndex)...))
    seedInt := int64(binary.BigEndian.Uint64(seed[:8]))
    rng := rand.New(rand.NewSource(seedInt))

    cumulativeArray := computeCumulativeArray(participants)
    randomNumber := rng.Int63n(cumulativeArray[len(cumulativeArray)-1])

    for i, cumulativeWeight := range cumulativeArray {
        if randomNumber < cumulativeWeight {
            return participants[i].Member.Address
        }
    }
    return participants[len(participants)-1].Member.Address
}
```

Note: Other functions in the same codebase (`sampleEligibleParticipantsWithHistory` and `getMustBeValidatedInferences`) correctly use seeded RNG from blockchain state.

### Impact

- Executor selection can be predicted and manipulated
- Economic exploitation through gaming reward system
- Privacy concerns - attacker can predict which executor handles their requests
- Potential for coordinated attacks if multiple malicious actors exploit this

---

## Vulnerability #6: Unsafe Type Assertions (Panic)

**Severity:** MEDIUM
**Type:** Denial of Service (DoS) / Panic
**File:** `decentralized-api/completionapi/request.go`
**Lines:** 41, 45

### Description

The `ModifyRequestBody` function performs unsafe type assertions on user-controlled JSON input. If the JSON contains unexpected types, the assertion will panic and crash the server.

### Vulnerable Code

```go
func ModifyRequestBody(requestBytes []byte, defaultSeed int32) (*ModifiedRequest, error) {
    var requestMap map[string]interface{}
    if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
        return nil, err
    }

    // ...

    if doStream, ok := requestMap["stream"]; ok && doStream.(bool) {  // LINE 41: PANIC if stream is not bool!
        if _, ok := requestMap["stream_options"]; !ok {
            requestMap["stream_options"] = map[string]interface{}{"include_usage": true}
        } else {
            requestMap["stream_options"].(map[string]interface{})["include_usage"] = true  // LINE 45: PANIC!
        }
    }
    // ...
}
```

### Attack Scenario

1. Attacker sends request with malformed JSON:
   ```json
   {"stream": "true", "messages": [...]}
   ```
2. `requestMap["stream"]` exists and is not nil, so `ok` is true
3. `doStream.(bool)` panics because `"true"` is a string, not bool
4. Entire Transfer Agent process crashes

Alternative attack:
```json
{"stream": true, "stream_options": "invalid", "messages": [...]}
```
This will panic on line 45 when trying to cast string to map.

### Recommended Fix

Use safe type assertions with the comma-ok idiom:

```go
if doStream, ok := requestMap["stream"]; ok {
    if doStreamBool, ok := doStream.(bool); ok && doStreamBool {
        if streamOpts, ok := requestMap["stream_options"]; !ok {
            requestMap["stream_options"] = map[string]interface{}{"include_usage": true}
        } else if streamOptsMap, ok := streamOpts.(map[string]interface{}); ok {
            streamOptsMap["include_usage"] = true
        } else {
            // Handle invalid stream_options type - either ignore or return error
            requestMap["stream_options"] = map[string]interface{}{"include_usage": true}
        }
    }
}
```

### Impact

- Transfer Agent can be crashed by malformed requests
- No authentication required
- Easy to automate for sustained DoS attack

---

## References

- PR #534: SSRF prevention (merged)
- PR #540: Remove panic calls (merged)
- PR #544: Integer overflow protection (merged)
- PR #556: Free inference exploit fix (open)
- PR #625: uint32 truncation fix (open)

---

## Disclosure

These findings were identified during code review and have not been exploited. Fixes should be applied before public disclosure timeline.
