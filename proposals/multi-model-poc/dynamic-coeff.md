# Simple Dynamic Coefficients for Multi-Model PoC

## Goal / Problem

Flat coefficients fail to properly incentivize a multi-model network. Because relative nonce throughput on different hardware varies by model, flat coefficients cause entire hardware classes to migrate to a single model. Hardware distribution becomes a direct function of static coefficients rather than demand.

Assume we now have models:
- M1, coeff1
- M2, coeff2
- M3, coeff3

Then, before group cap and collateral/power adjustments, for each host:

```
W = W1*coeff1 + W2*coeff2 + W3*coeff3
```

where `Wi` is the host's pocWeight in `Mi`.


## Proposal

Fully automated demand-driven coefficients are complex, vulnerable to manipulation, and cause high redeployment churn. 

This proposal introduces a transitional approach:

- Governance sets target compute allocation shares per model (share of total compute capacity, which directly defines model throughput when network size is stable) based on demand signals (revenue, data from brokers, expectations from open source data, etc).
- Governance defines a bounded range [coeff_i_min, coeff_i_max] per model.
- The protocol dynamically adjusts base coefficients coeff_i within these bounds to align actual hardware distribution with target shares.
- To prevent sudden hardware jumps, any compute share exceeding the target is diluted at coeff_i_min within the epoch.

-----

### Questions 

1. How to define target allocation?
2. How to define range?
3. What happens if exceed target values
4. Attack when re-deploy
5. Attack when host lies about intent

TODO:
- ADJUST TO REWARD ~ relative weight
- Improve the adaptive step for faster convergence


### Formalization

Governance defines three parameters for each model `M_i`:

- `[coeff_i_min, coeff_i_max]` - tight coefficient range. The maximum value `coeff_i_max` provides a ~5% incentive to switch to model `M_i`, while the minimum value `coeff_i_min` provides a ~5% incentive to switch back to the base model.

- `D_i` - relative model inference complexity (measured based on PoC results as correlated with real inference difficulty by PoC definition). Unlike the current coefficient, `D_i` carries no economic assumptions and is used solely to normalize nonces to comparable units. It is defined by throughput parity on a reference server (e.g., 8xH100) relative to a base model. If a model exceeds reference server capacity, both the base and target models are measured on the next server by capacity (e.g., 8xH200) to compute the ratio:

  Example: If base model `M_1` has throughput of 4736 nonces/min and model `M_2` has 3072 nonces/min on the reference server:
  D_1 = 1.0
  D_2 = 4736 / 3072 = 1.54

  Relative difficulties vary across hardware configurations, but high precision is not required.

- `T_i` - target compute share of the model, where `T_i` is in `[0, 1]` and `sum(T_i) = 1`. Using shares avoids parameter updates as total network capacity fluctuates.

The actual compute share (`share_i`) for model `M_i` is:

```
share_i = (D_i * sum_j(W[i,j])) / sum_k(D_k * sum_j(W[k,j]))
```

where `W[i,j]` is the `pocWeight` of host `j` in model `i`.


Each host `H_j` has a list of MLNodes `[node_k]`. Each host knows `throughput[i,k]`, the throughput (nonces/min) of model `M_i` on node `k`. This is internal host knowledge used for rational model selection.

Assuming each host is a rational agent. 


#### Protocol

At epoch `N` formation, the protocol computes the participant weights:

```
coeff[i,N] = f(share[i,N-1], coeff[i,N-1], s[i,N-1], prev_sign, params_i)

R_i = coeff[i,N] * min(share[i,N], T_i) + coeff_i_min * max(0, share[i,N] - T_i)
effective_coeff[i,N] = R_i / share[i,N]

W_j = sum_i(W[i,j] * effective_coeff[i,N])
```

where `params_i` is the governance parameter set `[coeff_i_min, coeff_i_max]`, `D_i`, and `T_i`. `Z` (target zone half-width), `s_min`, and `s_max` are global protocol parameters. `s[i,N-1]` and `prev_sign` are per-model adjustment state carried between epochs.

Share up to `T_i` earns `coeff[i,N]`, while any excess earns `coeff_i_min`. The blended rate `effective_coeff[i,N]` dilutes marginal returns above the target, incentivizing hosts to self-limit allocation. When `share[i,N] = 0`, `effective_coeff[i,N] = coeff[i,N]`, so a new model advertises its full coefficient to attract its first hosts. The adjustment function `f` uses `share_i` as-is. Dilution does not feed back into the base coefficient adjustment.

Dilution starts at `T_i` while the adjustment deadband of `f` extends to `T_i + Z`. This gap allows stable allocations in `(T_i, T_i + Z]` where dilution holds hosts at parity and `f` remains constant.


#### Design of f

> NEEDS IMPROVEMENT, CLARITY, GRAPHS. CURRENT IS TOO SLOW TO DIVERGE

The protocol adjusts `coeff_i` until `share_i` enters the target zone `[T_i - Z, T_i + Z]`:

```
coeff[i,N] = f(share[i,N-1], coeff[i,N-1], s[i,N-1], prev_sign, params_i)
```

where:
```
err_i = T_i - share[i,N-1]

if |err_i| <= Z:
    coeff[i,N] = coeff[i,N-1]
else:
    coeff[i,N] = clamp(coeff[i,N-1] * (1 + sign(err_i) * s[i,N]), coeff_i_min, coeff_i_max)
```

The adaptive step size `s[i,N]` is updated per model:
```
if |err_i| <= Z:
    s[i,N] = s_max / 2; prev_sign = 0
else if sign(err_i) == prev_sign or prev_sign == 0:
    s[i,N] = min(2 * s[i,N-1], s_max)
else:
    s[i,N] = min(max(s[i,N-1] / 2, s_min), s_max)
```

Defaults: `Z = 0.05`, `s_min = 0.005`, and `s_max = 0.05` (with `s_max = 0.25` when `share[i,N-1] < 0.01`).
If `T_i = 0`, `coeff_i = coeff_i_min`.
New models start at `coeff_i_min` with `s[i,0] = 0.025` and `prev_sign = 0`. At rollout, existing models retain their current coefficients.

> NEED IMG HERE


#### Host response

Note: group cap and collateral adjustments are excluded from the model as independent mechanisms.

Epochs last ~24 hours and allocations cannot be changed during the epoch
=> hosts act on expected effective coefficients at the final anticipated distribution.

A rational host chooses assignments to maximize total weight:

```
W_j = sum_i(W[i,j] * effective_coeff_i)
```

For fixed coefficients, this objective is separable per node
=> each node `k` is assigned to:

```
argmax_i throughput[i,k] * effective_coeff_i
```

Node assignments shift shares and coefficients, including for the host's own nodes
=> hosts recompute shares and coefficients during optimization.

Switching node `k` from model `a` to `b` occurs when:

```
effective_coeff_b / effective_coeff_a > throughput[a,k] / throughput[b,k]
```

Redeploying nodes requires effort and carries risk. A rational host switches models only when the expected gain exceeds a cost threshold `epsilon` (e.g., 1-2%).

Above the target, dilution lowers `effective_coeff_b` to drive the network toward a stable allocation. The parity point `p` for a hardware class is the effective coefficient where expected weight is equal on both models:

```
p = effective_coeff_a * throughput[a,k] / throughput[b,k]
```

- `p > coeff[b,N]` => the hardware class does not switch to `b` (parity is unreachable this epoch).
- `coeff_b_min < p <= coeff[b,N]` => the class switches until dilution pins `effective_coeff_b` at `p` or all such hardware is exhausted.
- `p <= coeff_b_min` => the class always runs `b` (dilution cannot drop the coefficient below the floor).

To estimate expected coefficients, hosts use public pre-epoch data (current assignments and declared intents) to simulate best-response dynamics. Trusting declared intents is an assumption of the host model.

```
allocation = current assignments + declared intents
repeat until no node moves:
    for each node k:
        share_i = compute shares from allocation
        effective_coeff[i] = effective coefficients from share_i
        best_model = argmax_i throughput[i,k] * effective_coeff[i]
        current_model = allocation[k]
        best_val = throughput[best_model,k] * effective_coeff[best_model]
        current_val = throughput[current_model,k] * effective_coeff[current_model]
        if best_val > current_val * (1 + epsilon):
            reassign k to best_model
```

Each move shifts coefficients and can trigger other moves. With `epsilon` above the single-node impact, the dynamics settle within an `epsilon` margin of parity (observed in simulation, not a formal guarantee). A fixed cap on passes (e.g., 1000) guarantees termination.


#### Bounds and stability

Bounding `coeff_i` limits maximum manipulation payoffs to the ratio `coeff_i_max / coeff_i_min`.

Stable states:
- Within the target zone => `coeff_i` remains constant.
- Pinned at `coeff_i_min` with `share_i > T_i + Z` => oversupply. Governance must raise `T_i` or accept the excess.
- Pinned at `coeff_i_max` with `share_i < T_i - Z` => infeasible target. Governance must lower `T_i` or widen the bounds.
- Base model (coefficient fixed at 1) => its share is not directly controlled and is determined residually by other models' shares.