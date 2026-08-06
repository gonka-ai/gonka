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

- [coeff_i_min, coeff_i_max] - coefficient range

- D_i - relative model inference complexity (measured based on PoC results as correlated with real inference difficulty by PoC definition). Unlike the current coefficient, D_i carries no economic assumptions and is used solely to normalize nonces to comparable units. It is defined by throughput parity on a reference server (e.g., 8xH100) relative to a base model. If a model exceeds reference server capacity, it is measured on the next server by capacity (e.g., 8xH200):

  Example: If base model M_1 has throughput of 4736 nonces/min and model M_2 has 3072 nonces/min on the reference server:
  D_1 = 1.0
  D_2 = 4736 / 3072 = 1.54

  Relative difficulties vary across hardware configurations, but high precision is not required.

- T_i - target compute share of the model, where T_i in [0, 1] and sum(T_i) = 1. Targets are set as shares rather than absolute nonces to avoid updating parameters as total chain capacity fluctuates.

The actual compute share (share_i) for model M_i is:

share_i = (D_i * sum_j(W[i,j])) / sum_k(D_k * sum_j(W[k,j]))

where W[i,j] is the pocWeight of host j in model i.


Hosts H_j has list of MLNodes [node_j] with:
- e_i_j - 


### Experiments

