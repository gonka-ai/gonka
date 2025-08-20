# Cosmos SDK Collections: Quickstart (Code-Heavy)

Minimal, example-first guide to move from direct KVStore to Collections. Uses real snippets from x/collateral and x/inference, but the patterns apply to any module.

Why Collections (goals and reasoning):
- Deterministic, safer state access: typed keys/values, ordered iteration (consensus-safe), easier correctness.
- Built-in indexing: secondary indexes without manual key gymnastics.
- Simpler iteration and pagination: first-class Walk/Iterate and query helpers.
- Clear separation of concerns: prefixes live in types/keys.go; wiring in keeper; logic in keepers/handlers.

Process at a glance:
1) Define all prefixes in types/keys.go (never inline).
2) Wire a SchemaBuilder in the keeper and create Items/Maps/KeySets/IndexedMaps.
3) Use Set/Get/Has for single-key ops; prefer Iterate/Walk for deterministic ranges and GetAll.
4) Expose queries with query.CollectionPaginate for consistent pagination.

References:
- Collections docs: https://docs.cosmos.network/main/build/packages/collections

Quick pattern: GetAll with Iterate (recommended)
To collect all values from a Map, prefer Iterate (or Walk) over manual key scans. Example from x/inference:

```go
// GetAllParticipant returns all participant
func (k Keeper) GetAllParticipant(ctx context.Context) (list []types.Participant) {
  iter, err := k.Participants.Iterate(ctx, nil)
  if err != nil {
    return nil
  }
  participants, err := iter.Values()
  if err != nil {
    return nil
  }
  return participants
}
```

---

## 0) Wiring Keeper with Collections
Create Keys and prefixes (define ALL prefixes in types/keys.go; never inline prefixes in keepers, handlers, or tests):
DELETE the old keys for each collection converted.

```go
// x/collateral/types/keys.go
var (
  ParamsKey  = collections.NewPrefix(0)
  CurrentEpochKey = collections.NewPrefix(1)
  CollateralKey   = collections.NewPrefix(2)
  UnbondingCollPrefix               = collections.NewPrefix(4)
  UnbondingByParticipantIndexPrefix = collections.NewPrefix(5)
  JailedKey = collections.NewPrefix(6)
)
```

When selecting keys, all Participant/address keys should be `sdk.AccAddressKey`, not strings. Add conversion as needed. Prefer using MustAccAddressFromBech32 rather than separate if (err) panic(err)
```go
// x/collateral/keeper/keeper.go
sb := collections.NewSchemaBuilder(storeService)

// Secondary index for IndexedMap (participant -> (epoch, participant))
unbondingIdx := UnbondingIndexes{
  ByParticipant: indexes.NewReversePair[types.UnbondingCollateral](
    sb,
    types.UnbondingByParticipantIndexPrefix,
    "unbonding_by_participant",
    collections.PairKeyCodec(collections.Uint64Key, sdk.AccAddressKey),
  ),
}

k := Keeper{
  params:        collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
  CollateralMap: collections.NewMap(sb, types.CollateralKey, "collateral", sdk.AccAddressKey, codec.CollValue[sdk.Coin](cdc)),
  CurrentEpoch:  collections.NewItem(sb, types.CurrentEpochKey, "current_epoch", collections.Uint64Value),
  Jailed:        collections.NewKeySet(sb, types.JailedKey, "jailed", sdk.AccAddressKey),
  UnbondingIM:  *collections.NewIndexedMap(
    sb,
    types.UnbondingCollPrefix,
    "unbonding_collateral",
    collections.PairKeyCodec(collections.Uint64Key, sdk.AccAddressKey),
    codec.CollValue[types.UnbondingCollateral](cdc),
    unbondingIdx,
  ),
}

schema, err := sb.Build()
if err != nil { panic(err) }
k.Schema = schema
```

---

## 1) Item[T]: single values (Params, Counters)

```go
// get/set current epoch
func (k Keeper) GetCurrentEpoch(ctx sdk.Context) uint64 {
  v, err := k.CurrentEpoch.Get(ctx)
  if err != nil { panic(err) }
  return v
}

func (k Keeper) SetCurrentEpoch(ctx sdk.Context, epoch uint64) {
  if err := k.CurrentEpoch.Set(ctx, epoch); err != nil { panic(err) }
}
```

Value codecs:
- primitives: collections.Uint64Value
- proto/SDK types: codec.CollValue[T](cdc)

---

## 2) Map[K,V]: simple k→v (example: account → collateral)

```go
// set/get/remove collateral
func (k Keeper) SetCollateral(ctx context.Context, addr sdk.AccAddress, amount sdk.Coin) {
  if err := k.CollateralMap.Set(ctx, addr, amount); err != nil { panic(err) }
}

func (k Keeper) GetCollateral(ctx context.Context, addr sdk.AccAddress) (sdk.Coin, bool) {
  coin, err := k.CollateralMap.Get(ctx, addr)
  return coin, err == nil
}

func (k Keeper) RemoveCollateral(ctx context.Context, addr sdk.AccAddress) {
  _ = k.CollateralMap.Remove(ctx, addr)
}

// iterate deterministically
func (k Keeper) IterateCollaterals(ctx context.Context, fn func(sdk.AccAddress, sdk.Coin) bool) {
  err := k.CollateralMap.Walk(ctx, nil, func(a sdk.AccAddress, c sdk.Coin) (bool, error) {
    return fn(a, c), nil
  })
  if err != nil { panic(err) }
}
```

Key codec examples:
- sdk.AccAddressKey, collections.Uint64Key, etc.

---

## 3) KeySet[K]: membership flags (e.g., jailed participants)

```go
func (k Keeper) SetJailed(ctx sdk.Context, addr sdk.AccAddress) {
  if err := k.Jailed.Set(ctx, addr); err != nil { panic(err) }
}

func (k Keeper) RemoveJailed(ctx sdk.Context, addr sdk.AccAddress) {
  if err := k.Jailed.Remove(ctx, addr); err != nil { panic(err) }
}

func (k Keeper) IsJailed(ctx sdk.Context, addr sdk.AccAddress) bool {
  found, err := k.Jailed.Has(ctx, addr)
  if err != nil { panic(err) }
  return found
}

func (k Keeper) GetAllJailed(ctx sdk.Context) []sdk.AccAddress {
  it, err := k.Jailed.Iterate(ctx, nil)
  if err != nil { panic(err) }
  addrs, err := it.Keys()
  if err != nil { panic(err) }
  return addrs
}
```

---

## 4) IndexedMap with secondary index (example: (epoch, addr) → UnbondingCollateral; index by participant)

Proto value:
```proto
message UnbondingCollateral {
  string participant = 1;
  cosmos.base.v1beta1.Coin amount = 2 [(gogoproto.nullable) = false];
  uint64 completion_epoch = 3;
}
```

Upsert / aggregate:
```go
// Add or aggregate unbonding for (epoch, participant)
func (k Keeper) AddUnbondingCollateral(ctx sdk.Context, addr sdk.AccAddress, epoch uint64, amount sdk.Coin) {
  pk := collections.Join(epoch, addr)
  if existing, err := k.UnbondingIM.Get(ctx, pk); err == nil {
    amount = amount.Add(existing.Amount)
  }
  entry := types.UnbondingCollateral{Participant: addr.String(), CompletionEpoch: epoch, Amount: amount}
  k.setUnbondingCollateralEntry(ctx, entry)
}

func (k Keeper) setUnbondingCollateralEntry(ctx sdk.Context, u types.UnbondingCollateral) {
  pAddr, err := sdk.AccAddressFromBech32(u.Participant)
  if err != nil { panic(err) }
  pk := collections.Join(u.CompletionEpoch, pAddr)
  if err := k.UnbondingIM.Set(ctx, pk, u); err != nil { panic(err) }
}
```

Exact get/remove:
```go
func (k Keeper) GetUnbondingCollateral(ctx sdk.Context, addr sdk.AccAddress, epoch uint64) (types.UnbondingCollateral, bool) {
  v, err := k.UnbondingIM.Get(ctx, collections.Join(epoch, addr))
  if err != nil { return types.UnbondingCollateral{}, false }
  return v, true
}

func (k Keeper) RemoveUnbondingCollateral(ctx sdk.Context, addr sdk.AccAddress, epoch uint64) {
  _ = k.UnbondingIM.Remove(ctx, collections.Join(epoch, addr))
}
```

Iterate by fixed first key (by epoch):
```go
func (k Keeper) GetUnbondingByEpoch(ctx sdk.Context, epoch uint64) []types.UnbondingCollateral {
  it, err := k.UnbondingIM.Iterate(ctx, collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epoch))
  if err != nil { panic(err) }
  defer it.Close()
  var out []types.UnbondingCollateral
  for ; it.Valid(); it.Next() {
    v, err := it.Value(); if err != nil { panic(err) }
    out = append(out, v)
  }
  return out
}
```

Query by secondary index (by participant):
```go
func (k Keeper) GetUnbondingByParticipant(ctx sdk.Context, addr sdk.AccAddress) []types.UnbondingCollateral {
  idx, err := k.UnbondingIM.Indexes.ByParticipant.MatchExact(ctx, addr)
  if err != nil { panic(err) }
  defer idx.Close()
  var out []types.UnbondingCollateral
  for ; idx.Valid(); idx.Next() {
    pk, err := idx.PrimaryKey(); if err != nil { panic(err) }
    v, err := k.UnbondingIM.Get(ctx, pk); if err != nil { panic(err) }
    out = append(out, v)
  }
  return out
}
```

Batch delete by epoch:
```go
func (k Keeper) RemoveUnbondingByEpoch(ctx sdk.Context, epoch uint64) {
  it, err := k.UnbondingIM.Iterate(ctx, collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epoch))
  if err != nil { panic(err) }
  defer it.Close()
  for ; it.Valid(); it.Next() {
    pk, err := it.Key(); if err != nil { panic(err) }
    if err := k.UnbondingIM.Remove(ctx, pk); err != nil { panic(err) }
  }
}
```

Export all (genesis):
```go
func (k Keeper) GetAllUnbondings(ctx sdk.Context) []types.UnbondingCollateral {
  it, err := k.UnbondingIM.Iterate(ctx, nil)
  if err != nil { panic(err) }
  defer it.Close()
  var out []types.UnbondingCollateral
  for ; it.Valid(); it.Next() {
    v, err := it.Value(); if err != nil { panic(err) }
    out = append(out, v)
  }
  return out
}
```

---

## 5) Pagination Patterns (gRPC Queries)

Use query.CollectionPaginate to paginate over a collections.Map or IndexedMap.

Example: paginate Participants (Map[sdk.AccAddress, types.Participant]):
```go
// x/inference/keeper/query_participant.go
func (k Keeper) ParticipantAll(ctx context.Context, req *types.QueryAllParticipantRequest) (*types.QueryAllParticipantResponse, error) {
  if req == nil { return nil, status.Error(codes.InvalidArgument, "invalid request") }
  sdkCtx := sdk.UnwrapSDKContext(ctx)

  participants, pageRes, err := query.CollectionPaginate(
    ctx,
    k.Participants,         // collections.Map[sdk.AccAddress, types.Participant]
    req.Pagination,         // *query.PageRequest (key/offset/limit/reverse)
    func(_ sdk.AccAddress, v types.Participant) (types.Participant, error) { return v, nil },
  )

  return &types.QueryAllParticipantResponse{
    Participant: participants,
    Pagination:  pageRes,
    BlockHeight: sdkCtx.BlockHeight(),
  }, err
}
```

Notes:
- The mapper func converts (K,V) to the element you want to collect; often return V.
- Works over Maps and IndexedMaps (via .Iterate or index Match + manual paging if needed).
- For IndexedMap by epoch, you can paginate a prefixed range by streaming results through your own pager, or materialize small slices if safe.

---

## 6) Determinism tips

- Never iterate Go maps; use collection iterators (deterministic by key order).
- Avoid randomness in state transitions.
- Don’t use maps in proto state types.

---

## 7) Cheat Sheet: Choose the type

- Item[T]: one value per module (Params, counters)
- Map[K,V]: single index lookups (addr → coin)
- KeySet[K]: membership flags (jailed addresses)
- IndexedMap[Pair/Triple, V, Idx]: composite keys and secondary lookups (epoch+addr, index by addr)

---

## 8) Common helpers

- Join composite keys: `collections.Join(first, second)`
- Range by first key: `collections.NewPrefixedPairRange[First, Second](firstVal)`
- Secondary index by other pair component: `indexes.NewReversePair`
