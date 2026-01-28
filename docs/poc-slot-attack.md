# POC_SLOT Attack

## Overview

An attacker can exploit the current PoC / POC_SLOT allocation logic to obtain a large number of `POC_SLOT = true` positions while running significantly less hardware than honest nodes and without reliably serving confirmation PoCs.

## Attack Scenario

### 1. Mass registration before PoC

- The attacker creates many participant addresses (e.g. on the order of 1000) **before** an epoch’s PoC phase.
- They temporarily rent enough hardware to pass the PoC for these addresses and enter the epoch group.

### 2. Stop hardware after entry

- Once included in the epoch, the attacker stops (or drastically reduces) hardware.
- These addresses **do not receive rewards** for that epoch (downtime / confirmation punishment).

### 3. Next epoch PoC

- Before the next epoch, the attacker again rents hardware only long enough to pass the **new** PoC for the **same** (and possibly additional) addresses.
- Some of these addresses are then **elected for `POC_SLOT = true`** in the upcoming epoch.

### 4. Exploiting POC_SLOT

- Nodes with `POC_SLOT = true` serve inference during the PoC phase and are **not** subject to confirmation PoC checks on that weight.
- The attacker serves inference using **much less hardware** than honest participants who must also run confirmation PoC, because:
  - They concentrate capacity on inference-serving slots.
  - They avoid the cost of reliably participating in confirmation PoCs.

### 5. Compounding over epochs

- Each epoch the attacker can add more addresses and repeat the pattern.
- Over time they accumulate a growing share of PoC-free `POC_SLOT = true` slots at low hardware cost.

## Root Cause

When selecting nodes for `POC_SLOT = true`, the system **does not check** whether a participant was **reward-eligible** in the previous epoch.

As a result, participants who:

- Failed confirmation PoC or downtime checks, and  
- Received **no reward** for the previous epoch  

can still be treated as eligible in the next epoch’s PoC slot allocation and be chosen for `POC_SLOT = true`.

## Required Invariant

**Only participants who were reward-eligible (i.e. actually received rewards) in the previous epoch should be eligible for `POC_SLOT = true` in the next epoch.**

- **Honest nodes** (that passed confirmation PoC, did not fail downtime checks, and earned rewards) should be the only pool from which `POC_SLOT = true` nodes are drawn.
- **Dishonest or unreliable nodes** (zero reward due to missed inferences/validations or confirmation failure) should **not** be eligible for `POC_SLOT = true` in the subsequent epoch.

Enforcing this would prevent attackers from cheaply farming `POC_SLOT = true` positions with throwaway, non-rewarded participants, and would tie PoC slot eligibility to **actual reward-earning behavior**.
