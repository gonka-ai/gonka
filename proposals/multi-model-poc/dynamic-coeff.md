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
- Governance defines a bounded range [coeff_min, coeff_max] per model.
- The protocol dynamically adjusts coefficients within these bounds to align actual hardware distribution with the target shares.

-----

### Questions 

1. How to define target allocation?
2. How to define range?
3. What happens if exceed target values
4. Attack when re-deploy


### Formalization

Governance defines three parameters for each model M_i:

- [coeff_i_min, coeff_i_max] - tight coefficient range. The maximum value coeff_i_max provides a ~5% incentive to switch to model M_i, while the minimum value coeff_i_min provides a ~5% incentive to switch back to the base model.

- D_i - relative model inference complexity (measured based on PoC results as correlated with real inference difficulty by PoC definition). Unlike the current coefficient, D_i carries no economic assumptions and is used solely to normalize nonces to comparable units. It is defined by throughput parity on a reference server (e.g., 8xH100) relative to a base model. If a model exceeds reference server capacity, it is measured on the next server by capacity (e.g., 8xH200):

  Example: If base model M_1 has throughput of 4736 nonces/min and model M_2 has 3072 nonces/min on the reference server:
  D_1 = 1.0
  D_2 = 4736 / 3072 = 1.54

  Relative difficulties vary across hardware configurations, but high precision is not required.

- T_i - target compute share of the model, where T_i in [0, 1] and sum(T_i) = 1. Targets are set as shares rather than absolute nonces to avoid updating parameters as total chain capacity fluctuates.

The actual compute share (share_i) for model M_i is:

share_i = (D_i * sum_j(W[i,j])) / sum_k(D_k * sum_j(W[k,j]))

where W[i,j] is the pocWeight of host j in model i.


Each host H_j has a list of MLNodes [node_k]. For each node:

- e[i,j,k] - relative performance of node k on model M_i, measured against the same reference configuration as D_i. This is internal host knowledge used for rational model selection.

  Example: If model M_i has throughput of 3072 nonces/min on the reference server and 10072 nonces/min on node k:
  e[i,j,k] = 10072 / 3072 = 3.28


Assuming each host is a rational agent. 


#### Protocol

Weight computation after PoC for epoch N is structurally unchanged:
```
coeff[i,N] = f(share[i,N-1], coeff[i,N-1], params_i)
W_j = sum_i(W[i,j] * coeff[i,N])
```
where params_i is the full governance parameter set from Formalization: [coeff_i_min, coeff_i_max], D_i, T_i.





### Experiments

