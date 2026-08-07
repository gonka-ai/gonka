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
- Governance defines a bounded range `[coeff_min, coeff_max]` per model.
- The protocol dynamically adjusts coefficients within these bounds to align actual hardware distribution with the target shares.

-----

### Questions 

1. How to define target allocation?
2. How to define range?
3. What happens if exceed target values
4. Attack when re-deploy
5. Attack when host lies about intent


### Formalization

Governance defines three parameters for each model `M_i`:

- `[coeff_i_min, coeff_i_max]` - tight coefficient range. The maximum value `coeff_i_max` provides a ~5% incentive to switch to model `M_i`, while the minimum value `coeff_i_min` provides a ~5% incentive to switch back to the base model.

- `D_i` - relative model inference complexity (measured based on PoC results as correlated with real inference difficulty by PoC definition). Unlike the current coefficient, `D_i` carries no economic assumptions and is used solely to normalize nonces to comparable units. It is defined by throughput parity on a reference server (e.g., 8xH100) relative to a base model. If a model exceeds reference server capacity, it is measured on the next server by capacity (e.g., 8xH200):

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
coeff[i,N] = f(share[i,N-1], coeff[i,N-1], params_i)

R_i = coeff[i,N] * min(share[i,N], T_i + Z) + coeff_i_min * max(0, share[i,N] - (T_i + Z))
effective_coeff[i,N] = R_i / share[i,N]

W_j = sum_i(W[i,j] * effective_coeff[i,N])
```

where `params_i` is the governance parameter set `[coeff_i_min, coeff_i_max]`, `D_i`, and `T_i`, and `Z` is the target zone half-width.

`coeff[i,N]` is the stable window coefficient from epoch `N-1` data.

Share up to `T_i + Z` earns `coeff[i,N]`, while any excess earns `coeff_i_min`. The blended rate `effective_coeff[i,N]` dilutes marginal returns above the target, incentivizing hosts to self-limit allocation. The adjustment function `f` uses the unscaled compute share.


#### Design of f

The protocol adjusts `coeff_i` until `share_i` enters the target zone `[T_i - Z, T_i + Z]`:

```
err_i = T_i - share[i,N-1]

if |err_i| <= Z:
    coeff[i,N] = coeff[i,N-1]
else if err_i > 0:
    coeff[i,N] = min(coeff[i,N-1] * (1 + s), coeff_i_max)
else:
    coeff[i,N] = max(coeff[i,N-1] * (1 - s), coeff_i_min)
```

Defaults: `Z = 0.02`, `s = 0.01`. If `T_i = 0`, `coeff_i = coeff_i_min`.
New models start at `coeff_i_min`. At rollout, existing models keep their current coefficients.

#### Host response

A rational host assigns node `k` to the model maximizing consensus weight:

```
argmax_i throughput[i,k] * effective_coeff_i
```

Switching node `k` from model `a` to `b` occurs when:

```
effective_coeff_b / effective_coeff_a > throughput[a,k] / throughput[b,k]
```

Above the target zone, dilution lowers `effective_coeff_b` and drives the network toward a stable allocation (no node gains by switching). For any hardware class, the parity point `p` is the value of `effective_coeff_b` that yields equal weight on both models:

```
p = effective_coeff_a * throughput[a,k] / throughput[b,k]
```

- If `p > coeff[b,N]`, the hardware class never switches to `b`.
- If `coeff_b_min < p <= coeff[b,N]`, the class switches until dilution pins `effective_coeff_b` at `p` or all such hardware is exhausted.
- If `p <= coeff_b_min`, the class always runs `b` because dilution cannot drop the coefficient below the minimum floor.

The relation between `coeff_b_min` and class parity determines whether a hardware class is permanently pinned to model `b`.

The stable allocation has no closed form. Because PoC weights, intents, and throughputs are public, hosts can find it by simulating best-response dynamics:

```
allocation = current assignments + declared intents
repeat until no node moves:
    for each node k:
        share_i = compute shares from allocation
        eff_i   = effective_coeff from share_i
        reassign k to argmax_i throughput[i,k] * eff_i
```

Nodes move one at a time against recomputed coefficients. Each move strictly increases the moving node's payoff, guaranteeing convergence. Hosts run this simulation to predict effective coefficients and optimize physical hardware allocation before the epoch starts.

#### Bounds and stability

With `coeff_i` bounded by `[coeff_i_min, coeff_i_max]`, the maximum manipulation payoff is capped by `coeff_i_max / coeff_i_min`.

Stable states:
- Within the target zone, `coeff_i` remains constant.
- Pinned at `coeff_i_min` with `share_i > T_i + Z` represents oversupply. Governance can raise `T_i` or accept the excess.
- Pinned at `coeff_i_max` with `share_i < T_i - Z` represents an unfeasible target. Governance must lower `T_i` or widen the bounds.