# `funding` — one model's creates and retires

Pure: `Plan` reads one `ModelState` and returns one `Decision`; `Bucket` and `DemandWindow` are values the escrow manager keeps between ticks and hands in. Nothing here reads a clock, a store or the chain. The manager executes the decision; see [`escrow/README.md`](../escrow/README.md), "The funding planner".

## Inputs

Per escrow (`EscrowState`): its money and flags from [`liquidity`](../liquidity/README.md), its role (standby or temp), its bridge label, whether it is at its nonce cap, whether it is `Unread` (an active row with no live session yet, entered by the manager as a fresh escrow holding its stored amount with nothing in flight), its own full-context price `FullCost`, and how long it has been idle. Per model (`ModelState`): `counted` (every store row of the model plus its commitment rows), the model's counts and `max_unsettled` and `full_context_slots` (`K`), the amount and the chain's create fee, the model-level prices (`FullCost` and `Slot` at the chain's current price), the demand peak and whether the demand window is full, the surplus streak (consecutive earlier scheduled ticks on which `Decision.SurplusHeld` was true) and whether this tick is a scheduled one or a wakeup (`ScheduledTick`), `Parking` (the model's rows already parked or settling and not yet settled), `PendingRegularCreates` and `PendingStandbyCreates` (the model's open create commitments by role, transactions that may still land), the money-short mark, whether the wallet refused the model's last create (`WalletShort`, cleared by the next create that lands), the phase (requests blocked, epoch known, bridge windows, the current label), the create gates (served by the network, breaker), and the bucket's tokens.

An escrow is **routable** when it is not standby and not at its nonce cap. An unread escrow counts in every sum and count below, routable or not by the same rule, so a fresh escrow whose session is not open yet is never funded twice; it is never a retire candidate.

## The algorithm

```
fullCount    = #{escrows not nonce-capped with Full}
guardShort   = priced && !misconfigured ? max(0, K − fullCount) : 0
L            = Σ_routable atModelPrice(max(0, free − FullCost_e) + returning)
need         = 1.5 × D_peak + (misconfigured ? 0 : K × Slot)
capShort     = priced && (moneyShort || need > L) ? ceil(max(1, need − L) / (Amount − CreateFee − FullCost)) : 0
spreadShort  = max(0, TargetCount − #{routable non-temp of the current label})
standbyShort = max(0, ReserveCount − #{standby of the current label or later})
room         = MaxUnsettled − counted − (near the bridge window and not in it ? TempCount : 0)
regulars     = max(0, max(guard, cap, spread) − pending regular creates)
standbys     = max(0, standbyShort − pending standby creates)
creates      = allowed ? min(regulars + standbys, room, bucket) : 0, regulars first
broken       = regular creates + pending regular creates < guardShort, or the wallet is short while guardShort > 0
budget       = regulars + standbys > room          (Decision.BudgetReached)
deficit      = max(guard, cap, spread) − room
```

`allowed` is requests not blocked, an epoch known, the model served by the network, the regular create breaker not cooling down, and the tick outside the bridge window: inside it `prepareBridge` owns every create and retire, since a regular created there is retired by its next pass. A regular create's reason is `guard` while its index is under `guardShort`, then `capacity` while under `capShort`, then `spread`; every reason therefore matches the state it was decided in (the scenarios' `create reason` invariant). Every sum and product saturates instead of wrapping, and `capShort` is bounded by `MaxUnsettled + 1`.

A model is **misconfigured** when `Amount − CreateFee < Slot`: a fresh escrow could never be full, so the guarantee is off. Full means a balance of one slot here as in `liquidity.Classify` and routing, with no margin on top. `Decision.AmountNeeded` is that floor, `CreateFee + Slot`. Its `need` drops the `K × Slot` term too, so capacity creates follow demand alone and never fund escrows that could not be full. An **unpriced** model (no chain price yet, or an overflow) has no guard, no capacity and no planned retire but the nonce cap.

`Decision.Need` and `Decision.Liquid` carry `need` and `L`, so the manager can narrate what a capacity create was sized from.

## Money at one price

An escrow's session keeps the token price it was opened with for its whole life, so its money buys requests at that price, while `need` prices the guarantee's slots at the chain's current price. `L` therefore counts each escrow's money at the current price: `atModelPrice(amount) = amount × FullCost / FullCost_e`, where `FullCost_e` is the escrow's own full-context price and `FullCost` the model's at the chain's current price (`plan.go`, `atModelPrice`; the product saturates). After a price rise, money on an escrow opened at the old price buys more than its face value and no capacity create is bought for a fleet that can still pay; after a fall it buys less. An escrow with no price yet, or a model unpriced, counts at face value. An unread escrow and every new create are priced at the current price, so their ratio is one. Each escrow's `full` is read at its own price (`liquidity.Classify` against its own `Slot`), the price it actually serves at, so the guard and `L` agree on what an escrow can pay. The demand peak is reserved money as the chain charged it, at each escrow's own price, and is not converted.

## Retires

The nonce cap retires every capped escrow at once. Inside the bridge window that is the only retire. Then at most one planned retire per tick, in this order:

| Rule | Condition |
| --- | --- |
| budget pressure | `deficit > Parking`, or the wallet refused the last create while a regular create is still wanted and nothing is parking: the least-free starved routable escrow, busy or not |
| idle starved | a starved routable escrow idle for ten minutes (`idleRetireAfter`): nothing open at a tick and no race took it since the tick before (`escrow/planner_state.go`, `idleFor`, reading the registry's `Holds`). A race that starts and settles between two ticks would otherwise never be seen, and a steadily used escrow would be retired as idle |
| surplus | the demand window is full, no regular create is wanted (`max(guard, cap, spread) = 0`), `#routable > max(TargetCount, K + ceil(max(0, need − Σ_routable full atModelPrice(free)) / (Amount − FullCost)))`, and it has held on three scheduled ticks in a row (`surplusSamples`, counted as the streak plus one on a scheduled tick and the streak alone on a wakeup): the least-free starved escrow while `fullCount > K`, else the least-free full one while `fullCount ≥ K + 2` |

No planned retire chooses an unread escrow: it has served nothing yet, and the next read gives its real money. Least-free means lowest free money, then lowest id, so the same input always gives the same decision. A planned retire therefore never takes `fullCount` below `K`.

Budget pressure reads the regular want only, so a missing standby never parks a serving escrow; the budget flag still counts standbys, since their creates are cut too. Each parking row frees one unit of budget once it settles, so pressure parks one more escrow only while the deficit exceeds the rows already parking; otherwise a deficit of one would drain every starved escrow while settlements wait. A short wallet presses the same way, one escrow at a time, so the residue of a starved escrow returns to the wallet through its settlement.

Surplus never fires in a decision that wants a regular create of any reason, so it never parks an escrow while creating its replacement (a spread create beside the retire of the only current-label escrow), and it sums only routable full escrows, the set `L` is drawn from, so a standby's money never makes a short model look surplus. A wakeup tick never advances the streak: only scheduled ticks are samples.

## Rate

`Bucket` holds two tokens and earns one per tick interval of wall time, and banks nothing while full, so back-to-back wakeup ticks cannot exceed about one create per fifteen seconds sustained. A full bucket re-anchors at every refill, earned token or not, so a wakeup one second after a tick that left it full earns its next token fifteen seconds after that wakeup.

## Demand

`DemandWindow` is the model's total reserved money at its last forty scheduled ticks — ten minutes. The manager records a sample only on a tick its ticker started, never on a wakeup, so extra ticks cannot skew the peak. `Full` gates the surplus rule, so a restart or a mode change never retires on an empty window.

## Constants

`h = 1.5` (half again the peak demand as headroom), `idleRetireAfter` (10 min), `surplusSamples` (3), the bucket's two tokens, and the forty-sample window. They become configuration only if production metrics show a need.
