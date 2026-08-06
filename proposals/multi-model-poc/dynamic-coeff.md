# Simply Dynamic Coefficients for Multi-Model PoC

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

1. How to define target allocation? Assuming chain wants to 


### Formalization


### Experiments

