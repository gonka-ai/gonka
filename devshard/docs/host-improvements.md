# Host Software Improvements

Issues discovered during mainnet escrow settlement (2026-04-29).

## 1. Gossip recovery: chunk large diff fetches

**Problem**: `tryRecovery` fetches all missing diffs in a single `GET /diffs?from=X&to=Y` request. For large gaps (30k+ nonces), this produces a 100-150MB JSON response that times out under the 30s `QueryTimeout`. Recovery silently fails and retries every 60s with the same result.

**Where**: `gossip/gossip.go:tryRecovery()` → `fetcher.GetDiffs(ctx, lastAppliedNonce+1, highestSeen)`

**Fix**: Chunk the recovery fetch into batches (e.g. 200 diffs per request), same as proxy-side `sendCatchUp`. Apply each batch, update `lastAfterReqNonce`, then fetch the next batch. Stop early if the host reaches `highestSeen`.

## 2. Gossip recovery: suppressed when host receives any user request

**Problem**: Recovery only triggers when `time.Since(lastAfterReq) > recoveryDelay` (60s). On a busy proxy, the host continuously receives inference requests for other escrows on the same machine, keeping `lastAfterReq` fresh. A host that's 30k nonces behind on escrow A but actively serving escrow B will never trigger recovery for A.

**Where**: `gossip/gossip.go:tryRecovery()` line 323

**Fix**: Track `lastAfterReq` per escrow, not globally. Or decouple the "am I receiving user traffic" check from the recovery trigger — recovery should run if the host is behind regardless of other activity.

## 3. Gossip recovery: single peer as diff source

**Problem**: `DiffFetcher` is wired to a single peer's `HTTPClient`. If that peer doesn't have the full diff range (pruned, restarted, or offline), recovery fails with no fallback.

**Where**: `gossip/gossip.go:tryRecovery()` line 333 — only one `fetcher`

**Fix**: Try multiple peers. Either rotate the fetcher peer on failure, or fan out to K peers and use the first successful response.

## 4. Host should return its current nonce in error responses

**Problem**: When the proxy sends a catch-up diff with a nonce gap (e.g. sends nonce 27345 but the host is at 49350), the host's `ApplyDiff` fails with `ErrInvalidNonce` and returns an HTTP 500 with no nonce information. The proxy doesn't learn where the host actually is.

**Where**: `host/host.go:HandleRequest()` line 274-276 — returns error on diff apply failure

**Fix**: Include the host's current `sm.LatestNonce()` in error responses so the proxy can skip forward immediately instead of needing a successful first chunk to discover the host's position.

## 5. Host `GET /signatures` should return the host's latest nonce

**Problem**: The `GET /signatures?nonce=N` endpoint only returns signatures for the requested nonce. There's no lightweight way to ask a host "what's your latest nonce?" without sending a diff.

**Where**: `transport/server.go` — signatures endpoint

**Fix**: Add the host's current nonce to the signatures response (e.g. `{"signatures": {...}, "host_nonce": 49350}`). This lets the proxy know the host's position before starting catch-up, potentially skipping the entire diff send if the host just needs to sign.

## 6. Host diff storage limits

**Problem**: Hosts store all diffs since session start. For long-running sessions (50k+ nonces), the SQLite state DB grows to 70MB+. Recovery peers need to serve this entire history.

**Where**: Host state DB, diff storage

**Fix**: Consider diff pruning with a configurable retention window. Hosts only need recent diffs for gossip recovery. Settlement only needs the final state + signatures, not the full diff history.
