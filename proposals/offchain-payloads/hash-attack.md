# Response Hash Attack Fix

## Summary

Fixed security vulnerability where `response_hash` only covered message content, not logprobs. Attackers could serve fake logprobs with valid content, bypassing validation.

## Vulnerability

```go
// BEFORE: Only hashed message content
func (r *JsonCompletionResponse) GetHash() (string, error) {
    var builder strings.Builder
    for _, choice := range r.Resp.Choices {
        builder.WriteString(choice.Message.Content)
    }
    return computeHash(builder.String())
}
```

**Attack vector:**
1. Executor commits `response_hash = sha256(text_content_only)` on-chain
2. When validator requests payload, serves modified response with same text but fake logprobs
3. Hash verification passes (content unchanged), validation runs with fake logprobs
4. Attacker passes validation with crafted logprobs

## Fix

Hash full payload bytes instead of just content:

```go
// AFTER: Hashes full JSON including logprobs
func (r *JsonCompletionResponse) GetHash() (string, error) {
    if len(r.Bytes) == 0 {
        return "", errors.New("CompletionResponse: can't compute hash, empty bytes")
    }
    return utils.GenerateSHA256Hash(string(r.Bytes)), nil
}
```

Same fix applied to `StreamedCompletionResponse.GetHash()` and `utils.GetResponseHash()`.

## Files Changed

- `decentralized-api/completionapi/completionresponse.go` - Core hash functions
- `decentralized-api/internal/utils/utils.go` - Duplicate hash logic
- `decentralized-api/payloadstorage/file_storage_test.go` - Added logprobs coverage test
- `testermint/src/test/kotlin/InferenceTests.kt` - Updated Kotlin hash function
- `testermint/src/test/kotlin/InvalidationTests.kt` - Added manipulation detection test

## Migration

No breaking change for old inferences. Pre-upgrade inferences (with `PromptPayload` on-chain) fall back to `retrievePayloadsFromChain()` which bypasses hash verification.

