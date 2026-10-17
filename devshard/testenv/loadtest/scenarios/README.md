# Load scenarios split from prod-like-15m

These scenarios split the production-like run (prod-like-15m) into focused runs that each answer one question. prod-like-15m fires its faults back to back, so the report could judge almost no recovery, the faults compounded, and harness stalls voided large parts of the run. Each scenario here runs one group of faults far enough apart that the report judges every fault and its recovery on its own. prod-like-15m.yaml keeps its faults and timing, so old reports stay comparable.

All scenarios use seed 1515 and the prod-like topology. That is five participants: P0 (versiond-0 and versiond-1, an HA pair) and P1 (versiond-2) are never faulted and are the honest controls; P2 is versiond-3, P3 is versiond-4 (sqlite storage) and P4 is versiond-5. Each container has its own mock ML node (mock-openai-N), so an ML fault lands on one participant. Traffic is 8 requests a second for the first minute and 16 after it, over model-a (70 %, settles) and model-b (30 %, settlement off), half streamed and half with logprobs, unless a scenario says otherwise. Fault definitions are copied from prod-like-15m; only their times changed, and `participant-absent-on-chain` is 45 s instead of 30 s. P4's peer chat budget (`host-over-peer-chat-budget`) in prod-like-steady is lower than prod-like-15m's (see there).

Known-bug markers: `session-nonce-within-max-nonce` (`nonce-overrun`) everywhere, `host-under-poc-challenge` (#1823) in faults-host, `host-over-peer-chat-budget` (#1988) in prod-like-steady and `timeout-vote-fanout` (#1988) in vote-fanout. `deactivated-escrow-runtime-retired` has no marker here: #1988 retires the runtime of a model that does not settle (cmd/devshardctl/gateway.go:5102–5104), and `no-runtime-for-deactivated-escrow` passed in every v6 run of batch A and batch B, with rotations in each. prod-like-15m keeps its markers unchanged.

Every fault is placed so the report can judge it. The recovery check (`recovery-after-fault`) needs 60 s after the fault, or 90 s after a gateway restart, plus one 30 s bucket before the next fault starts, so faults are at least 2m30s apart (3m30s after a gateway restart). The error check (`error-rate-and-latency`) gives a request to the faults active while it ran, else to the fault whose 3m10s grace tail its start falls in, and a request in two tails is judged by no fault, so each fault ends at least 3m25s after the previous one. A gateway restart stays active past the moment its process is back, until the first request that starts after that and succeeds (at most its 90 s recovery budget), so requests failing because the restarted gateway cannot serve yet count for the restart, not for another fault. Consecutive faults hit different participants, two faults on one participant are at least 4 minutes apart, and each run lasts the full grace tail after its last fault. scenario_split_test.go checks all of this from the planned timelines.

Execution timeout. The protocol timeout also cancels a live model run (#2003), and a prod-like generation of the 2048-token class takes 45–63 s, so a 30 s execution timeout cuts honest hosts at their deadline ("context deadline exceeded" after streaming) and charges them misses from the first minute on. Every scenario here, and prod-like-15m, therefore sets `execution_timeout: 90s`. vote-fanout needs the timeout to fire inside its hang, so its hang lasts 1m45s (see vote-fanout); prod-like-15m, which carries that case, does the same. Only missrate-catch-up keeps 30 s, because it requires an applied timeout from a 30 s pause. Scenarios without a `protocol` block use the mock chain's 1200 s default. The scenarios other than vote-fanout pin `grace: 3m10s`, the 30 s value the fault spacing above is built on. That still covers every fault here: each one is either refused by the host, so the 5 s refusal timeout applies, or lifted when its window ends, so no nonce waits out the 90 s deadline after a fault. A hang does hold its nonces to the 90 s deadline, so vote-fanout pins no grace and gets the default 5 s + 90 s + the gateway's 2m5s sweep grace + its 30 s sweep tick = 4m10s. `max_pending_age` follows the timeout: that sum + 30 s = 4m40s. scenario_split_test.go checks which scenarios keep 30 s, which pin the grace, and why.

Run a scenario from devshard/testenv, one at a time, with nothing heavy running beside it: `make build-devshardd && go run ./cmd/loadtest -scenario loadtest/scenarios/<name>.yaml -output loadtest/results/<name>-<date>`. Add `-gateway gateway` for the new gateway where that driver exists. Each run writes report.md; `go run ./cmd/loadtest -report <run dir>` renders it again from the run's files, and `go run ./cmd/loadtest -recheck <run dir>` judges every check again from the run's files, `run-inputs.json` included, without a stack (see the main README, "Run report"). A run lasts its workload plus about 6m30s at most: about 45 s to start, up to 2 minutes for requests still in flight, up to 3 minutes of drain and about 45 s of checks. The first build takes longer.

The report has a load-step table for every scenario whose traffic has several stages. Each row is one stage and shows:
- offered over planned: the requests the generator released against the stage's rps times its length;
- the error share and the share of requests the generator never sent, max_in_flight drops and generator_behind slots together;
- p50 and p95 latency of completed requests;
- the gateway's CPU in cores, from process_cpu_seconds_total in metrics.jsonl;
- the gateway's attempts per sent request;
- each host's Chat terminals per second, from devshard_request_terminal_total in metrics.jsonl, refusals included.

It marks "degradation starts here" at the first stage whose p95 within one max_tokens class is above twice that class's p95 in the first stage, or whose error or never-sent share is above the scenario's bound. A class is compared only when both stages have at least 20 completed requests of it; when no class has, the stage's overall p95 is compared. Comparing within a class keeps a shift in the token mix from reading as load: with weights 40/35/20/5 the overall p95 flips between the 512-token class (about 16 s) and the 2048-token class (about 60 s) from stage to stage, while each class's p95 stays flat.

The generator schedules each arrival from the previous scheduled arrival, not from when the last one was sent, so a late wake-up catches up. A slot more than 1 s behind, or still unsent at the end of the workload, is not sent: it is recorded in requests.jsonl as dropped_by_generator with error generator_behind and counts as never sent, so a catch-up never releases more than one second's worth of requests at once.

Right under the verdict line the report gives the run's stalled share, harness_health.invalid_fraction from invariants.json. Only checks whose every judged window overlaps a stall turn INVALID, so a run can read INVALID: 0 while half of it is void. host-stats.jsonl holds only all-RPC counts every 30 s, so the Chat rate comes from the host metrics instead.

## prod-like-steady

Purpose: the baseline error rate and latency the other scenarios are compared with. It is prod-like-15m without its timed faults: the same 15 minutes and the same traffic stages, including the 2-minute peak stage (16 requests a second, as in prod-like-15m), and no fault.

It keeps one whole-run condition: Chat refused because P4's peer budget is spent (`host-over-peer-chat-budget`). P4 boots with a budget of 6 Chats a minute, and a background marker over the whole run gives P4's refusals a case. This condition belongs only here, for three reasons:
- the budget and its marker must go together, or P4's refusals count as misses on an honest host;
- in the load ladder the budget would cap P4, so the ladder would measure P4 instead of the gateway;
- in the fault scenarios it would overlap P4's own faults.

Why 6 and not 300: in the v6 run (batch A, 20261014T085518Z) P4 served 2.1 Chats a second at 8 requests a second and 3.8–4.1 at 16, under the 5 a second that 300 allows, so P4 refused no Chat, `faults-fired-and-ml-nodes-bound` failed and `misses-only-in-fault-windows` and `quarantines-match-faults` passed with nothing to judge. A higher share of traffic for P4 is not a scenario knob. A lower budget only causes refusals below 10 a minute: from 10 a minute up, the gateway paces its own Chats to the budget P4 advertises (transport/rpc_chat.go takes from it, transport/rpc_budget.go), so it would wait rather than be refused. Below 10 the gateway treats the budget as not advertised. #1988's skip of a host whose chat budget is spent also applies only at or above the network default (6000 a minute). So the gateway sends to P4, P4 refuses (`rate_limited`, no receipt), and #1988 pauses P4 for 1 s (`sender_rate_quarantine`). At 6 a minute P4 admits one Chat every 10 s against the 2–4 a second it is offered, so it is over budget for the whole run; scenario_split_test.go checks this from the stages. Every peer's own RPCs to P4 share that 6-weight budget, so validators fetch P4's payloads (weight 6) about once a minute each. Because P4 now serves about one Chat in 40, there are about that many fewer payloads to fetch, so the pressure on validators is about what it was at 300 (when hosts already paced their GetPayloads to P4).

What each `host-over-peer-chat-budget` check should now show:
- `faults-fired-and-ml-nodes-bound.host-over-peer-chat-budget.fired`: PASS, because P4's `rate_limited` no-receipt terminal count rises. It has no case, so no marker applies.
- `quarantines-match-faults.host-over-peer-chat-budget.reason`: P4's quarantines are `sender_rate`. XPASS confirms #1988's 1 s sender-rate quarantine; XFAIL names the reason the gateway used instead.
- `misses-only-in-fault-windows.host-over-peer-chat-budget.waived_misses`: no miss is charged to P4 for a Chat it refused before a receipt. XPASS confirms it; XFAIL counts the misses. A refused validation fetch is not P4's miss, but if a peer fails P4 for one, it shows up here too.
`host-over-peer-chat-budget` keeps its #1988 marker until such a run shows XPASS. prod-like-15m keeps 300 (unchanged), so its `host-over-peer-chat-budget` fires no Chat refusal either.

Good: errors stay at or below 2 % for the run and at or below 0.5 % in the quiet window, latency stays flat across the run, `faults-fired-and-ml-nodes-bound.host-over-peer-chat-budget.fired` passes, and the budget's `misses-only-in-fault-windows` and `quarantines-match-faults` checks show XFAIL, or XPASS if the #1988 fixes cover it.

Run: `make build-devshardd && go run ./cmd/loadtest -scenario loadtest/scenarios/prod-like-steady.yaml -output loadtest/results/prod-like-steady`

## faults-ml-node

Purpose: does the gateway ride out a misbehaving model server? Three faults on the ML node behind one host, 15 minutes:
- `receipt-then-empty-stream` on P2's node, 2m30s–3m30s: the host sends its receipt, then the ML node's stream ends with no content;
- `ml-server-returns-429` on P3's node, 6m00s–7m00s: the ML node answers 429, a genuine overload of the host;
- `host-hangs-after-first-token` on P2's node, 9m30s–11m00s: the ML node sends one token and then holds the stream; the held streams are released when the fault ends.

`host-hangs-after-first-token` no longer overlaps the gateway kill (`gateway-killed-mid-race`) as it did in prod-like-15m. There, every hung request also touched the kill and was judged by no fault.

Good:
- racing hides each fault, so each fault's error share stays at or below 2 % and the run at or below 2 %;
- the error rate is back under 0.5 % within 60 s after each fault;
- P2 is quarantined for empty_stream after `receipt-then-empty-stream` and for stalled_winner, transport or empty_stream after `host-hangs-after-first-token` (the gateway sees a receipt and no content), and P3 for http after `ml-server-returns-429`;
- no honest host is quarantined.

Run: `make build-devshardd && go run ./cmd/loadtest -scenario loadtest/scenarios/faults-ml-node.yaml -output loadtest/results/faults-ml-node`

## faults-host

Purpose: does the gateway ride out a misbehaving host? Four faults on the hosts themselves, 18 minutes:
- `host-paused-then-catches-up`: P4's container is frozen for 2m30s–3m30s, then catches up;
- `host-withholds-payload`: P2 drops the payloads it should serve to validators, 6m15s–7m00s;
- `host-under-poc-challenge`: P3 is under a PoC challenge, so its ML-node acquires answer ResourceExhausted, 9m30s–10m30s;
- `host-public-port-down`: P2's public devshardd port answers 503 while its admin port still reports ready, 13m10s–13m55s.

`host-under-poc-challenge` is a known bug (#1823) and reports XFAIL.

Good:
- each fault's error share stays at or below 2 % and the run at or below 2 %;
- errors recover within 60 s after each fault;
- P4 catches up after the pause, with no state-root disagreement among honest hosts and no pending work older than 4m40s, burned nonces apart;
- P2 is quarantined only for http or transport during `host-public-port-down`;
- no honest host is quarantined.

Run: `make build-devshardd && go run ./cmd/loadtest -scenario loadtest/scenarios/faults-host.yaml -output loadtest/results/faults-host`

## faults-gateway-restart

Purpose: gateway restarts and changes in chain membership. 15m30s in total:
- `gateway-killed-mid-race`: the gateway is killed with SIGKILL and started again at 2m30s, on normal racing traffic.
- `participant-unavailable-then-absent-on-chain`: two separate steps on P4, `participant-unavailable-on-chain` 6m00s–6m30s (the chain reports P4 unavailable) and `participant-absent-on-chain` 10m35s–11m20s (the chain no longer lists P4). They are 4m05s apart, so each step is judged and attributed on its own.
- `gateway-restart-while-participant-absent`: the gateway restarts at 10m45s, inside the absent step, because a restart while P4 is absent is the point of the case.

Consequences of this nesting:
- the restart's recovery check is NOT_JUDGED by construction. Its error check judges only requests that start after the absent step ends and before the gateway serves again, under the 100 % restart bound;
- the outage between the restart and P4's return (the gateway has no runtime while P4 is absent) falls in the `participant-absent-on-chain+gateway-restart-while-participant-absent` overlap, which neither bound judges, and not in the absent step's 2 % bound;
- the absent step's recovery check, whose 60 s budget ends with the restart's 90 s budget, judges how the gateway comes back;
- the restart's verdict is the check that no escrow is marked missing while the chain still has it (`no-escrow-falsely-marked-missing`).

`gateway-restart-while-participant-absent` is a normal check since #1988 (192318583): a participant NotFound at startup now skips the runtime and keeps the escrow active, and mock-chain answers NotFound for a missing participant.

Good:
- the gateway is back to under 0.5 % errors within 90 s of each restart, and the run stays at or below 12 % errors (prod-like-15m's bound, which pays for restarts);
- the unavailable step quarantines nobody;
- no escrow is falsely marked missing.

Run: `make build-devshardd && go run ./cmd/loadtest -scenario loadtest/scenarios/faults-gateway-restart.yaml -output loadtest/results/faults-gateway-restart`

## epoch-change

Purpose: the epoch change is a heavy event, so it is judged on its own. Epoch change with PoC-blocking phases (`epoch-change-with-poc`), 6m00s–7m00s of a 13-minute run: PoCGenerate for 40 s, then PoCValidate for 20 s, then mock-chain moves to the next epoch and the phase returns to Inference.

The mock chain's phase stub never reports an epoch switch on its own, so this is the only epoch boundary in the run. The advance at 7m comes after the chain passed its first PoC start height (500, about 5 minutes in), so it fast-forwards a full 400-block epoch, as prod-like-15m's advance did. There are 4 minutes of quiet traffic before the fault and 6 minutes after it.

Good:
- the gateway answers only 503 or 429 while PoC blocks admission and admits nothing during the blocking phases;
- errors are back under 0.5 % within 60 s after the advance;
- the quiet traffic after the grace tail shows no errors. Errors that last for minutes after the epoch, as in an earlier prod-like run, show up there as quiet-window errors.

Run: `make build-devshardd && go run ./cmd/loadtest -scenario loadtest/scenarios/epoch-change.yaml -output loadtest/results/epoch-change`

## vote-fanout

Purpose: a heavy event, judged on its own; the gateway's machine-wide stall began right after it in an earlier prod-like-15m run. Peer RPC budget at 32 rps with a timeout-vote fan-out (`timeout-vote-fanout`). Traffic rises to 32 requests a second for 4m00s–8m00s. P3's ML node hangs from 4m30s to 6m15s, so its nonces time out and every host votes on them. A hung nonce times out 90–95 s after it starts (the 90 s execution timeout, plus up to the 5 s refusal timeout), so the first timeouts fire 10–15 s before the hang lifts, as the 30 s timeout's did inside the earlier 45 s hang, and the last fire by 7m50s, still inside the 32 rps stage.

The hosts keep the real default peer budget: 6000 weight a minute with a Chat weighing 1 since #1988, which is about 100 Chats a second per sender, so 32 requests a second no longer reaches it with Chats alone; the fan-out is now the timeout votes, which #1988 caps at 64 concurrent. The run continues 5m45s after the hang, to 12m, covering the 4m10s in which the sweep votes on the hung nonces.

Before mock-openai read the body of a hung request, it never saw P3's host give up on one: Go's HTTP/1.1 server notices a caller that hung up only after the body is read. So the 600 calls the hang held, its whole 100-worker, 500-queue capacity, stayed held after the hang lifted, every later call got 503 "capacity exhausted", and P3 finished almost nothing for the rest of the run. Runs made before that fix show P3 quarantined after the hang and ml-stats `requests_received` above the sum of successes, failures and timeouts on mock-openai-4.

The generator allows 1024 requests in flight, because about 260 are in flight at 32 requests a second with a healthy gateway. If the load ladder shows that this machine cannot hold 32 requests a second, lower `peak_rps`.

`timeout-vote-fanout` keeps its #1988 marker until a run on 192318583 confirms the fixes (Chat weight 1, the 64-vote cap, the 1 s sender-rate quarantine) and reports XFAIL or XPASS.

Good:
- no host is quarantined for a sender-rate reason, and Chats are not starved;
- the error share during the hang stays at or below 2 % and recovers within 60 s;
- no harness stall follows the hang.

Run: `make build-devshardd && go run ./cmd/loadtest -scenario loadtest/scenarios/vote-fanout.yaml -output loadtest/results/vote-fanout`

## load-ladder

Purpose: find the step where degradation starts. No faults, 10 minutes: 2 minutes each at 8, 16, 24 and 32 requests a second, then 2 minutes back at 16. The last step shows whether the gateway comes back after the peak, and lets the trend check compare two windows at the same rate. The peak is 32 because the earlier ladder (up to 64 rps) degraded from 32 on: 86 % errors at 48-64 and 34 % on the step back at 16, so the steps above 32 measured only the wreck.

Three settings make the ladder measure the gateway rather than the harness or a host:
- The generator allows 1024 requests in flight, about 4x the 260 in flight at 32 requests a second with a healthy gateway. With prod-like's 256, it would start dropping requests at about 31 requests a second.
- The max_tokens weights are 40/35/22/3 instead of 40/35/20/5. With 20/5, the overall p95 sits on the edge between the 512-token class (about 16 s) and the 2048-token class (about 60 s). The mark now compares p95 within a class, so this only keeps the table's overall p95 column readable.
- P4 has no tight budget.

The hosts keep the real default peer budget, which is production: 6000 weight a minute with a Chat weighing 1 since #1988, about 100 Chats a second per sender. The earlier ladder ran with a Chat weighing 10 (about 10 a second) and its hosts sat near 8 a second at 32 requests a second. A host Chat rate that flattens near the ceiling means the ladder hit a host's limit. A high gateway CPU means the gateway's limit. Harness stalls (invalid_fraction in invariants.json) or out-of-memory kills mean the machine's limit. Escrows rotate faster as the rate rises: model-a about every 70 s at 32 requests a second.

Good: every step offers close to its planned requests and stays at or below 2 % errors and drops, with each max_tokens class's p95 within twice the first step's; if a step crosses a bound, the table marks it, and its gateway CPU and host Chat rates say whose limit it is.

Run: `make build-devshardd && go run ./cmd/loadtest -scenario loadtest/scenarios/load-ladder.yaml -output loadtest/results/load-ladder`
