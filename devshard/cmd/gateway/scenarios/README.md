# scenarios

The scenario tests run the real gateway — composed by [`app.Compose`](../app/composed.go), as `run` composes it — inside a `testing/synctest` bubble against a fake chain (`chain_test.go`) and real in-process hosts (`fleet_test.go`). An escrow's whole life (create, drain, timeout votes, settle, prune) runs on fake time, in seconds of wall time. Each test is a list of steps; after every step the harness checks every invariant below and stops at the first violation.

They exist because the escrow lifecycle fails in the seams between the planner, the scheduler, the hosts and the chain, where a unit test of one package cannot see. [`docs/escrows.md`](../docs/escrows.md) describes the escrow lifecycle as built.

## Running them

From `devshard/`:

```bash
go test ./cmd/gateway/scenarios/ -count=1 -timeout 30m                                   # every test
go test -race ./cmd/gateway/scenarios/ -count=1 -timeout 30m                             # the same under the race detector
go test ./cmd/gateway/scenarios/ -run '^TestAHedgeDoesNotRetireAnEscrowThatCanStillPay$' -count=1 -v   # one test, with the gateway's log tail on failure
go test ./cmd/gateway/scenarios/ -run '^TestFakeChain' -count=1                          # the fake chain's own tests
go test ./cmd/gateway/scenarios/ -run '^TestRandomSeedsKeepEveryInvariant$' -count=1 -v  # sixteen seeds under a sixty-second wall budget
go test ./cmd/gateway/scenarios/ -run '^TestRandomSeedsKeepEveryInvariant$' -short -count=1   # four seeds
GATEWAY_SCENARIO_SEEDS=64 go test ./cmd/gateway/scenarios/ -run '^TestRandomSeedsKeepEveryInvariant$' -count=1 -timeout 60m   # more seeds, by hand
go test ./cmd/gateway/scenarios/ -run '^$' -fuzz '^FuzzEscrowLifecycle$' -fuzztime=60s -parallel=1   # fuzzing, by hand only
```

`-count=1` makes every run execute. The validator timing in `TestAnInvalidatedInferenceReturnsItsCost` is tuned against the fleet's real-time behaviour: when the fleet's timing changes, run it with `-count=50` before trusting it. A seed failure prints the shrunk action list as a Go literal; paste it into a named test in `seeds_test.go` as the regression.

## How a test is built

A test starts from `defaultSpec()` (four participants, one model, one funded escrow, a guarantee of one full escrow, so a test that is not about the guarantee starts quiet) or `plannerSpec()` (the configuration's guarantee of two), changes what it needs, sets `steps` and calls `runSteps` (or `runStepsWith` to script the fake chain before boot). Steps send requests, advance fake time, align to the planner's tick, change the hosts' behaviour, move the chain, restart the gateway on the same store, reconfigure (`reconfigureModel` also moves the figures the invariants judge later creates by), act as the operator, fund the wallet, report an escrow missing, or check an expectation (`expectThat`, whose label names it in a failure). A request answered 503 also records the largest balance a routable escrow of its model held as the answer left (`balancesAtUnavailable`), so a test can tell a refusal no escrow could pay from one routing should have served.

A known bug is pinned, not skipped: the test lists the invariant key or expectation label the bug violates in `pins`, passes while the bug reproduces, and fails with "remove the pin" the day it stops. The fix removes the pin in the same change, which turns the test into its regression test. Never add a pin to make a red run green without naming the bug and the code line it lives on.

The fleet's serving sessions run the heartbeat's drain cadence: every 24 s a session with queued host transactions sends them, as production's heartbeat does on a quiet escrow; the heartbeat's own height stamps are not modelled, because the harness runs without height sync.

A fleet host hands the attempt's stream to its engine, as `transport/server.go` does over HTTP. A participant given a `contextLimit` refuses a prompt over it the way `devshardd` relays vLLM's 400 (`cmd/devshardd/inference/execute.go`): one data event carrying current vLLM's message with the host's limit and the request's token counts, then `[DONE]`, and its execution then fails after the signed receipt.

Every new test gets a row below; `TestEveryTestHasAReadmeRow` fails otherwise.

## Invariants

Checked after every step by `invariants_test.go`. A test that legitimately breaks one (an escrow the chain drops unsettled) sets `allowUnsettled`, which exempts `settlement match`'s prune check and `settle deadline`.

| Key | What it checks |
| --- | --- |
| `money identity` | every session's balance, fees, host costs and open reservations add up to the escrow's funded amount |
| `settlement match` | a settled escrow's costs and fees equal its session's and its refund is the amount less both; no escrow is pruned holding its whole amount |
| `settle deadline` | no escrow is still unsettled once the chain's effective epoch reaches its epoch + 2 |
| `unsettled budget` | a model never has more unsettled escrows on chain than its budget |
| `create reason` | every create is stored with a role and narrated with a reason that the fleet's state at that moment justifies |
| `active escrow routes` | every active row that is not parked for settlement is routable |
| `inactive escrow unroutable` | no inactive row is routable |
| `request ends` | no request stays open past the execution timeout and its buffer |
| `ledger match` | the nonce ledger's charged, reserved and challenged money equals the chain's settlement and the live sessions |
| `ledger classifies every nonce` | at rest, the nonce ledger has classified every nonce the chain assigned a live escrow, and never more; a gap is read again until none remains or the reservation return bound passes, so a fact still on its way is not read as a lost one |

## The tests

### Harness — `harness_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestHarnessBootsAndAnswersInsideABubble` | every other test assumes the composed gateway boots and serves inside a bubble | one request over a funded escrow is answered 200 and the shutdown leaves nothing running |
| `TestAQuietEscrowSequencesItsReceipts` | the harness must sequence a quiet escrow's receipts as production's heartbeat does, or timeouts are never voted | stalled attempts become Started with their executor's stamp, then TimedOut with one Missed per attempt on their executor |
| `TestHarnessRestartsTheGatewayOnTheSameStore` | restart tests rely on a second boot over the same store, chain and hosts | requests before and after a restart are answered 200 and the seeded row stays active |
| `TestHarnessRestartCutsTheVotesTheOldGatewayOwed` | a restart must sever the old process's sessions, as an exit does | the open session is severed and the refusal vote the old gateway owed dies with it |
| `TestHarnessOperatorStepsActOnTheRunningGateway` | operator steps must reach the running gateway, not a copy | wallet funding, the operator's create, deactivate and settle, and a contradicted missing report each take effect |

### Assertions — `assertions_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestBucketViolationFollowsThePlannersBucket` | the bucket check that many tests use must replay the planner's token bucket exactly | a replay refuses three creates at boot and a create fourteen seconds after a full-bucket burst, and accepts the rest |

### Invariants — `invariants_test.go`, `invariants_bridge_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestUnseenPinsListsOnlyThePinsThatNeverFired` | a pin that never fires must fail the test, or a fixed bug keeps its pin | only the pins never seen are reported |
| `TestMoneyIdentityViolation` | the `money identity` check is the base of every money assertion | a violation is reported exactly when balance, fees, costs and reservations do not add up to the amount |
| `TestRetentionMayPrune` | `ledger match` must not read the ledger's retention as a ledger that never opened an escrow | a prune is allowed only for an escrow created more than the retention before the current epoch, never with retention off |
| `TestCreatesAreJudgedByTheirNarratedReason` | `create reason` is the check that catches a planner funding the wrong thing | only creates whose narrated reason the fleet's state contradicts are flagged, a guard create included |
| `TestACreateWithNoRecordedReasonWaitsForTheRunToEnd` | a create's reason line may land after its row | a create without a reason waits while the run lasts and is flagged once it ends |
| `TestCreateReasonsAndPlansAreReadFromTheJournal` | the check reads reasons and plans from the log the gateway writes | each create's reason and role and the plan's figures are read back from a journal |
| `TestACreateWhoseRowWasDroppedTakesItsRoleFromItsCreateLine` | settlement can drop a create's row before it is judged | the stored row wins, the create line stands in for a dropped row, and a create with neither stays unresolved |

### Fake chain — `chain_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestFakeChainHoldsAFundedEscrow` | every test funds escrows through the fake chain | a funded escrow is found with its amount, epoch and the first four participants as slots |
| `TestFakeChainPrunesAnUnsettledEscrowAtEpochPlusTwo` | `settle deadline` and the deadline tests depend on the chain's prune rule | an unsettled escrow is pruned at effective epoch + 2 |
| `TestFakeChainRefusesASettleOutsideItsWindow` | a late settle must fail as on the real chain | a settle at effective epoch + 2 is refused and the escrow stays unsettled |
| `TestFakeChainRefundsAmountMinusCostsAndFees` | `settlement match` compares against the chain's refund | the creator is refunded the amount less costs and fees, and the settled escrow is pruned two switches later |
| `TestFakeChainFailsAScriptedLookupOnce` | tests script one failed lookup | the scripted lookup fails once and then answers |
| `TestFakeChainPaysTheFeesOnlyToTheSlotsASettleNames` | the fee split decides the refund the gateway must expect | each named slot takes a quarter of the fees and the refund keeps the unnamed slots' shares |
| `TestFakeChainRefusesASettleWhoseRemainderFeesFindNoSlot` | a settle the real chain refuses must be refused here too | a settle whose remainder coins exceed its named slots is refused |
| `TestFakeChainRefusesASettleNamingASlotOutsideTheGroup` | same | a settle naming a slot outside the group is refused as out of range |
| `TestFakeChainLookupsCanBeTakenDown` | outage tests take lookups down and back up | a lookup fails while down and answers once back up |
| `TestFakeChainKnobsChangeWhatTheChainAnswers` | tests change price, context, wallet and escrows mid-run | each knob changes exactly what the chain answers |
| `TestFakeChainHeightsAdvanceWithBubbleTime` | block-time tests need heights that follow fake time | the height advances one block per interval and a scripted move re-anchors it |

### Fleet — `fleet_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestFleetRefusesAnEscrowTheChainDoesNotHold` | a session over an unknown escrow must fail like a real host, not stop the test | serving an escrow the chain lacks returns an error |
| `TestFleetClientReturnsWhenTheCallerGivesUp` | a stalled host must not block the gateway's cancellation | a send returns the caller's cancellation within its deadline |
| `TestFleetHostOverItsContextRelaysVLLMsRefusal` | the over-context scenarios rely on the fleet answering as devshardd does | a host over its context streams vLLM's 400 as one data event and [DONE], and the gateway's parser reads its limit and the request's total |
| `TestFleetSeveredSessionLosesItsHostsAndMakesRoomForANewOne` | restarts sever sessions as an exit does | a severed session can no longer reach its hosts and a new one opens over it |
| `TestFleetDiffLogKeepsNonceOrderAndDropsRepeats` | the host side replays diffs in nonce order | the log answers each nonce once, in order |

### Observer — `observer_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestThePhaseObserverReadsTheFakeChain` | the real phase observer reads the fake chain through its public API | the snapshot carries the epoch, the heights and a weight for every participant |
| `TestThePhaseObserverDerivesTheFakeChainsEffectiveEpoch` | deadline tests depend on the effective epoch the observer derives | the effective epoch is the chain's own before, inside and after PoC, and unknown when hidden |
| `TestTheFakePublicAPIGoesDownAndNamesAConfirmationPoC` | outage and confirmation-PoC tests drive the public API | the API answers 503 while down and names an active confirmation PoC when one is on |

### Escrow lifecycle — `lifecycle_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestARefusedNonceIsRefundedByTheVote` | an offline host's nonce must come back by vote, not be lost | the offline slot is charged a miss and the reservation returns |
| `TestAnInvalidatedInferenceReturnsItsCost` | an inference voted invalid must refund its cost to the escrow | the executor's slot is invalidated and its cost goes back |
| `TestAReplacementWhoseAnswerWasLostIsRecovered` | a create whose broadcast answer is lost must not be lost or duplicated | exactly one escrow is created and its row is active |
| `TestAnEscrowThatServedTrafficSettles` | the basic money path: serve, retire, settle | the chain settles the escrow with costs above zero |
| `TestABridgeWindowCrossingPoCStartFundsOneTempSet` | the bridge must fund one temp set across PoC, whatever the chain does at the window's edge | one temp labelled with the next epoch covers the window and no second set is funded at the switch, or the bridge finishes cleanly when requests unblock |
| `TestDepletedEscrowsInOneTickFundAtMostTwoEscrows` | many depleted escrows at once must not buy a storm | the first tick funds at most two escrows, the minute exactly two, and the starved escrows keep serving |
| `TestAHedgeDoesNotRetireAnEscrowThatCanStillPay` | a hedge on an escrow near its floor must not report it exhausted | nothing is created, no balance mark reaches the planner and the escrow still serves |
| `TestAPinnedHedgeThatCannotPayNeverMarksItsEscrow` | a pinned hedge that cannot pay must be declined without marking its escrow | every hedge is declined as `ErrPinnedEscrowShort`, nothing is created and the escrow is never marked |
| `TestAHedgeTheEscrowCanPayIsSent` | the balance a race's hedge is priced against is already net of the race's first attempt, so counting the race's own hold again declined a hedge the escrow could pay | the request is answered, no hedge is declined as `ErrPinnedEscrowShort` and both attempts start on the escrow |
| `TestASlotAbsentAtOpenServesOnceItsHostIsListedAgain` | a session opened while a participant was off the chain holds a dead slot for its life, so its nonces burn until the escrow retires | once the participant is listed again the escrow is reopened while idle: every request is answered, no further nonce burns as absent from chain and the participant is sent to again |

### Bursts — `bursts_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestAFullContextBurstIsServedWithoutAReplacementStorm` | a full-context burst must be served while any escrow can pay, not trigger a create storm | forty full-context requests in four waves over four escrows sized for twelve attempts each, which the burst and its hedges run down through the range that pays one attempt but not two: every answer is 200 or 503, every 503 left while no routable escrow could pay one attempt, the seeded escrows keep serving and creates stay within the bucket |
| `TestInputHeavyRequestsReturnTheirSurplusAtFinish` | an over-reserved request must return its surplus | after quiet time no money is returning, late or stuck and no reservation is left |
| `TestSmallRequestsSpendTheStarvedResidueFirst` | residue on a starved escrow should be spent, not stranded | exactly the small requests land on the starved escrow and its balance falls by their cost |
| `TestAStandbyTakenWhileEveryRegularIsStarvedIsRepaired` | a standby taken into service must be replaced once creates work again | the standby serves the full-context request and exactly one standby lands later |
| `TestAThousandMoneyShortPicksBuyNoMoreThanTheBucket` | money-short picks at request rate must not become creates at request rate | creates are guard or capacity creates within the bucket, none once converged, and the wallet balances; the escrows on chain stay within the budget through `unsettled budget` |
| `TestAnEscrowBelowItsFloorServesWhatItCanPay` | an escrow below its floor still has money a small request can use | a small request is answered on it and its balance drops by the cost |
| `TestFiftyWakeupsInFifteenSecondsBuyAtMostTwo` | a burst of money-short wakeups must not buy one escrow per wakeup | at most two escrows are created in fifteen seconds |
| `TestRefusedAttemptsReplacedOnOneEscrowStayWithinItsHosts` | replacements on one escrow must be bounded by its hosts | with every host refusing, two concurrent requests whose nonces interleave each start exactly four attempts, one on each host of the escrow, and neither is answered 200 |
| `TestPicksRacingANearlyEmptyEscrowParkNothing` | a failed nonce start on a nearly empty escrow must not park it | three requests race an escrow that pays two attempts; in a run where a start failed for balance and reached the planner as an ignored mark (booted afresh up to forty times until one does), every request is answered 200 and the escrow keeps serving, never parked |
| `TestALargeOrUnmeasuredPromptIsNotRefusedAtIngest` | the gateway's byte estimate must not refuse what a host may serve | a large prompt and one on a model of unknown context are not answered 400 |
| `TestTokenDenseInputKeepsTheGuarantee` | token-dense prompts overspend the surplus estimate | the guarantee holds or is repaired, creates stay within the bucket and the planner's full count matches the fleet |
| `TestAnOverContextPromptIsAnsweredAfterTheFirstRefusal` | a prompt over a host running the model's whole length is every host's answer, not a reason to try them all | the client gets 400, one attempt holds the only reservation until the execution timeout returns it, and the pinned host defect charges that host a miss |
| `TestAShorterHostsContextRefusalIsRetriedOnALongerHost` | a host running shorter than the model must not refuse a prompt the model takes | the client gets 200 from a longer host, the refused attempt's reservation comes back by the execution timeout and the pinned host defect charges the shorter host a miss |
| `TestAPromptOverTheModelIsNotRetriedPastAShorterHost` | a prompt the model's length cannot take must not be retried because the first host was shorter | the client gets 400 after one attempt, whose reservation comes back by the execution timeout, and the pinned host defect charges the shorter host a miss |

### Data — `data_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestARestartBeforeTheNewColumnsAreResolvedResolvesThemLazily` | a row whose chain epoch and amount are unknown must keep serving across a restart | the row serves unresolved through a restart and resolves once lookups return |
| `TestABridgeWithoutRoomKeepsItsRegulars` | a bridge that cannot fit in the budget must not retire the fleet | nothing is created and the regular keeps serving, relabelled temp |

### Deadlines — `deadline_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestAnEscrowCreatedDuringPoCSettlesByItsChainEpoch` | an escrow's deadline comes from the epoch the chain stamped, not its row label | the escrow settles before the switch its chain epoch requires |
| `TestATempThatSurvivedItsBridgeIsRetiredAtItsDeadline` | a temp the finish could not retire still has a deadline | the temp settles inside its margin |
| `TestAnUnknownEffectiveEpochOutsidePoCStillSettlesBeforeTheSwitch` | a hidden effective epoch must not skip a deadline | the escrow settles before the switch |
| `TestAnUnknownEffectiveEpochReadsDeadlinesEarly` | inside PoC an unknown effective epoch must err early, not late | the escrow is parked and settled while the chain's effective epoch is still the old one |
| `TestTheDeadlineSettleRunsWithRotationOff` | the deadline rule must not depend on rotation | the escrow settles with rotation off |
| `TestTheDeadlineSettleRunsWithAnUnparsableModelsList` | the deadline rule must not depend on a valid models list | the escrow settles although the list never parsed |
| `TestWithSettlementOffAnEscrowAtItsMarginIsNarratedNotParked` | with settlement off the gateway must say so, not park | the escrow keeps serving unsettled and `settlement_disabled` is narrated once |
| `TestASettleThatNeverCommitsIsRebroadcastBeforeTheDeadline` | a settle that never commits must be retried in time | a second broadcast after the TTL settles the escrow |
| `TestASettleTheChainNeverTakesIsNarratedAndItsRowDropped` | a settle that never lands must end in a narrated loss, not a stuck row | `deadline_passed` is narrated, the escrow is pruned and its row dropped |

### Epochs and the wallet — `epochs_wallet_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestTheBridgeWindowOpensOverAStarvedFleet` | the bridge must cover the window even when the regulars are starved | one temp serves, the regulars leave after the full fill and a full-context request is answered |
| `TestAStandbyShortfallInsideTheWindowIsRepairedAfterIt` | the bridge window defers the standby, it does not forget it | no standby is created in the window; exactly one is created after the switch, with the reason standby, serving as a reserve labelled with the new epoch |
| `TestALateSetNewValidatorsLosesNoEscrow` | a long PoC must not push an escrow past its deadline | the bridge temp of chain epoch 7 under label 8, which no regular can retire once the chain refuses creates, settles inside the margin while the effective epoch is still 8, and the run crosses into epoch 9 |
| `TestConfirmationPoCBlocksEveryPlannerCreate` | a confirmation PoC blocks requests and creates alike | nothing is created during it and the guarantee and standby are funded once it ends |
| `TestMoneyShortDuringPoCCreatesNothing` | money-short marks during PoC must wait for the switch | in one case requests are blocked outside the bridge window, in the other the bridge window is open with requests unblocked; in each the planner creates nothing behind its one gate and funds the guarantee after the switch |
| `TestTheTickAfterPoCFundsRegularsBeforeTempsRetire` | the bridge temp must not retire before regulars replace it | two regulars are created first and the temp leaves only once the spread is met |
| `TestAShortWalletParksResidueInsteadOfLooping` | a wallet below one create must not loop on creates | no create is broadcast, the broken guarantee is narrated once and the starved escrow is parked and settled |
| `TestASettleThatNeverCommitsIsRebroadcastInRealBlockTime` | the TTL retry must work with real block times | the second broadcast settles the escrow before the height reaches the deadline |
| `TestAStaleSnapshotStillSettlesBeforeTheDeadline` | a frozen public API must not freeze the deadline | the escrow settles while the public API is down and the projection is narrated once |
| `TestLookupsDownForTenMinutesDeactivateNothing` | a lookup outage is not evidence that an escrow is gone | the row stays active and unresolved, then resolves |
| `TestAnEscrowPrunedWithSettlementOffIsMarkedGoneAndCounted` | a loss with settlement off must be recorded, not dropped | the row is inactive and gone from chain, `deadline_passed` is narrated and counted |
| `TestTwoModelsSharingAWalletAreRepairedInTurn` | a shared wallet must serve both models' guarantees | one create first, then each model has its own escrow |
| `TestATokenPriceRiseReevaluatesWithoutAStorm` | a price change must not make every escrow look short | a twentyfold price rise that puts the slot above both escrows' balances creates nothing: the planner still counts both escrows full at their own price, counts their money at the new price, which covers the re-priced slots, and creates stay within the bucket |
| `TestAChainThatRejectsEveryCreateIsNotLoopedOn` | a chain refusing creates must meet the breaker, not a loop | broadcasts stay within the breaker's back-off and the escrow keeps serving |
| `TestAContextRiseMisconfiguresWithoutACreateLoop` | a context no fresh escrow can cover is a configuration problem | the misconfiguration is narrated once, nothing is created and small requests are served |
| `TestAStarvedEscrowGoneFromChainLeavesTheCount` | an escrow the chain dropped must leave routing and the counts | a missing report the chain contradicts changes nothing; once confirmed the row is inactive and gone, a guarantee rise gets the creates max_unsettled leaves room for only without it, and inside its settle margin no deadline line, park or settle names it |
| `TestTwoModelsKeepIndependentGuarantees` | two models with different amounts must not share a guarantee | each model gets one escrow of its own amount and reports its guarantee met |

### Ledger — `ledger_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestAForcedSettleLeavesTheLedgerChargedAsTheChain` | a forced settle pays open records at finalize; the ledger must agree | the ledger's charged total equals the chain's and the comparison actually ran |

### Operator — `operator_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestEscrowsFallingBelowFullAreRepairedWithinTheBucket` | traffic that drains escrows below full must be repaired without a storm | in one case both full escrows fall below full while the fleet's money still covers its need, and exactly the guarantee's two guard creates repair it; in the other, in-flight demand outruns the fleet's money while two escrows stay full, and capacity creates alone repair it; creates stay within the bucket and a full-context request is served |
| `TestALoweredBudgetParksTheLeastFreeStarvedOnePerTick` | a lowered budget must shed the least useful escrows gradually | nothing is created and the two least funded starved escrows are parked and settled in order, a tick apart |
| `TestALoweredTargetRetiresSurplusOnePerTick` | a lowered target must retire surplus gradually | surplus leaves one per tick, nothing is created and the guarantee remains |
| `TestARaisedAnswerCapReevaluatesWithoutAStorm` | a larger answer cap must not make every escrow look short | where a fresh escrow pays the grown slot, the seeded escrows fall below it and exactly the guarantee's two guard creates land within the bucket; where none can, the misconfiguration is narrated once and nothing is created; in both, nothing more is created two minutes later |
| `TestAModelRemovedFromRotationServesThenSettlesAtItsDeadline` | a removed model's escrow still holds money and a deadline | removed while its guarantee is short and then given a funded wallet, it serves, nothing is created and it settles at its deadline |
| `TestAnOperatorDeactivatedEscrowIsCountedAndNarratedAtItsMargin` | an escrow the operator took out still counts and still has a deadline | it stays counted and `operator_deactivated` is narrated once at its margin |
| `TestAnOperatorRegisteredRowResolvesLazilyAndIsCounted` | a row registered without chain data must resolve | it resolves its chain epoch and amount and is counted |
| `TestOperatorActionsBesideThePlanner` | operator actions must not confuse the planner | the operator's create is narrated as such, one settle broadcast settles, and the planner creates nothing |
| `TestTwoHoursOfSmallTrafficKeepTheGuaranteeAndSpendTheResidue` | long steady traffic must neither lose the guarantee nor strand residue | two full escrows remain, the residue is spent and the budget never saturates |

### Planner — `planner_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestThePlannerNarratesAndExportsTheGuaranteeItRepairs` | an operator must see a repair in the log and the metrics | a guard create is narrated, exactly one lands, and the guarantee gauge reads zero |
| `TestABurstKeepsEveryPlannerCreateWithinTheBucket` | a repeated input-heavy burst is the planner's hardest steady load | every planner create stays within the bucket |

### Reservations — `reservations_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestAPendingRecordWhoseLadderARestartCutIsStuckNotWaitedOn` | a restart can cut a refusal ladder; the money must be accounted, not waited on | the stuck money is counted, the escrow serves, and the settle pays it out |
| `TestAStalledExecutorIsLateThenSwept` | a stalled executor's reservation must be swept after its deadline | it is reported late, then swept and refunded with nothing charged |
| `TestTwoHundredOverdueRecordsDrainWithinTheSweepBudget` | a large backlog must drain within the sweep budget | after a restart that cuts the races' own timeout votes, no Started record is left on any escrow within one tick per eight records past their deadline |
| `TestARefusalVoteShortOfQuorumIsAccountedStuck` | a vote that cannot reach quorum must not block settlement | the money is counted stuck and the escrow settles |
| `TestALongAnswerIsLateThenLeavesTheOpenSet` | a slow answer is late, not lost | it is reported late, the request ends and no Started record remains |
| `TestAChallengedRecordIsStuckMoney` | challenged money is neither free nor returning | the starved escrow keeps serving and its challenged money is counted stuck |
| `TestChallengedMoneyDrivesNoCreate` | stuck challenged money must not look like a shortage or demand | escrows whose money barely covers the guarantee hold Challenged records, nothing is created and the money is counted stuck |
| `TestAnEscrowSettlesInsideItsWindowWithValidationsUndone` | validations and executions in flight must not delay a deadline settle | the escrow, busy with two stalled requests and undone validations, settles inside its window while both requests are still open |
| `TestAStarvedEscrowIdleButForAChallengeIsRetired` | a starved escrow kept only by a challenge should still retire | it is retired as idle and settled while the full one serves |
| `TestAMajorityHolderVotingInvalidIsHandledWhicheverWayTheHostsResolveIt` | the hosts decide a majority holder's challenge; the gateway must handle both outcomes | a challenged record is counted stuck or its refund holds `money identity`, and the escrow settles with the wallet balanced |

### Restarts — `restarts_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestARestartOverStarvedEscrowsCreatesNoStorm` | a restart rebuilds the planner's state; it must not re-buy the fleet | nothing more is created and the rebuilt full count matches the fleet |
| `TestACreateWhoseAnswerALostRestartIsRecoveredOnce` | a restart between a create's broadcast and its row must not duplicate it | every created escrow has exactly one active row and the guarantee is not exceeded |
| `TestASettleWhoseAnswerARestartLostIsNotSettledTwice` | a restart between a settle's broadcast and its reconcile must not settle twice | one broadcast settles the escrow and its row is gone |
| `TestARestartRebuildsTheClassesOfStartedRecords` | late money must still be late after a restart, and still be swept | the late classes are rebuilt and no Started record remains |
| `TestARestartAcrossTheEpochSwitchKeepsTheDeadline` | a restart inside the margin must not lose the deadline | a temp of chain epoch 7 that nothing else can retire is settled by the restarted gateway while the effective epoch is still 8, and the run crosses into epoch 9 |
| `TestNoSurplusRetireBeforeTheDemandWindowFills` | after a restart demand is unknown; surplus must wait for the window | nothing retires before the window fills, and surplus retires after |
| `TestAColdStartWithoutAnEpochCreatesNothing` | without an epoch no deadline or create decision is sound | with no epoch index, and with epoch 8 named but no block height while the escrow of chain epoch 7 would sit inside its margin, nothing is parked, settled, narrated or created and the escrow keeps serving |
| `TestARestartKeepsTheLedgersTokenCounts` | the nonce ledger's token counts are history the tracker reads; a restart must neither lose nor repeat them | with finishes no sweep read when the restart begins, the ledger the shutdown saved counts them, and the restarted ledger holds the same input tokens, output tokens, counted nonces and estimated error after its first sweep and thirty seconds later |

### Seeds — `seeds_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestRandomSeedsKeepEveryInvariant` | the tests above script a fixed set of interleavings; random fleets and actions find the rest | every invariant holds after every step of sixteen seeds, within the wall budget |
| `FuzzEscrowLifecycle` | the fuzzer explores action lists the seeds never generate | every invariant holds for every decoded action list |
| `TestSeedsAreDeterministicAndInsideTheirRanges` | a seed must reproduce its actions to be a regression | the same seed gives the same shape and actions, inside their ranges |
| `TestTheLedgerPrunesSettledEscrowsPastItsRetention` | a shrunk seed: retention pruning must not read as a missing ledger | every invariant holds and the ledger did prune a charged settled escrow |
| `TestHarnessJudgesABootCreateWhenNoStepRuns` | regression: a boot create must be judged even with no step | the run ends clean with the boot create stored and narrated |
| `TestTheLedgerIsComparedOnceTheSessionsStandStill` | regression from a shrunk seed: the ledger compared while sessions still moved | every invariant holds and the ledger is compared at rest |
| `TestHarnessEndsAfterARestartThatLeftVotesOwed` | a shrunk seed: a restart's owed votes must not deadlock the bubble | the run ends clean and waits out the replaced engine's votes |

### This README — `readme_test.go`

| Test | Why it exists | What it checks |
| --- | --- | --- |
| `TestEveryTestHasAReadmeRow` | the next test must get a row here too | every test in the directory has exactly one row and every row names a test |
