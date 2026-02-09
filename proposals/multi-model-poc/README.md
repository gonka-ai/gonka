# Proposal: Multi-Model PoC

## Goal / Problem

POC procedure is short term benchmark to compare how much compute each host has. It happens 1 time per epoch to define weight per each host which then used as consensus weight to produce blocks and for distributing tasks between nodes. Additionally there is Confirmation (random) POC which is used to confirm weight when network is underloaded by inference (to make sure hardware it still there).

POC phases:
- GENERATION (blocks equal to 1-5 min)
- VALIDATION (blocks equal to 2-10 min)
- INFERENCE PHASE (no POC but sometime might be interrupted to Confirmation POC)

Validation and inference theoretically can be done in parallel.

### Security Model

Required >50% of **all host weight** to vote "valid". An attacker needs >50% of total network weight to corrupt any host's validation.

The bitcoin-style part of reward distributed proportionally to this weight. On early phase it's main motivation as inference is much cheaper. 

---

Currently, there is single model used for PoC (Qwen3-235B-FP8). Therefore, chain also can't support another models for inference as it'd required to re-deploy model for POC (which is essentially impossible as it requires time and open network for attack when attacker deploy hardware only for POC phase).

=> option with re-deploy must not be used

## Proposal

### Terms

Let epoch $S$ be completed. The following defines weight computation for epoch $S+1$. Pre-eligibility ($PreE_{S+1}$) is determined $N$ blocks before epoch $S+1$ PoC starts.

- $group_i$ — set of MLNodes serving model $i$. Network supports $N$ models on-chain.

- $poc\_weight_S(group_i, p)$ — weight of host $p$ in $group_i$ at epoch $S$. Equals the number of nonces computed by $p$ in PoC procedure for this group and successfully validated. Local weight within the group.

- $consensus\_koeff_i$ — coefficient converting $poc\_weight$ in $group_i$ to consensus weight. Defined by governance per model.

- $consensus\_weight_S(p) = \sum_{i: group_i \in E_S} consensus\_koeff_i \times poc\_weight_S(group_i, p)$

- $members(group_i) = \{p : p$ has MLNode deployed for model $i\}$ — hosts with MLNode deployed for the model

- $hosts_S(group_i) = \{p : consensus\_weight_S(p) > 0$ and $p \in members(group_i)\}$ — hosts with non-zero consensus weight at epoch $S$ who have MLNode deployed for model $i$

- $PreE_{S+1}$ — set of pre-eligible groups for epoch $S+1$. A group $group_i \in PreE_{S+1}$ if conditions 1-3 hold:
  1. Model $i$ is approved by governance with defined $consensus\_koeff_i$
  2. $\sum_{p \in members(group_i)} consensus\_weight_S(p) \geq W_{threshold} \times \sum_{p} consensus\_weight_S(p)$
  3. $|hosts_S(group_i)| \geq V_{min}$

- $E_{S+1}$ — set of consensus-eligible groups for epoch $S+1$. A group $group_i \in E_{S+1}$ if:
  - $group_i \in PreE_{S+1}$, AND
  4. At least $V_{min}$ hosts in the group pass PoC validation at epoch $S+1$ (each with >50% of total network $voting\_power$ approving their result)

- $W_{threshold}$ — minimum fraction of total network consensus weight required for group eligibility (governance parameter)

- $V_{min}$ — minimum number of hosts with non-zero consensus weight required in a group (governance parameter)

- Currently $group_{Qwen3-235B-FP8}$ is the only eligible group (single-model PoC). This proposal extends to multiple groups.

- $delegation_S(group_i, p_{from}, p_{to})$ — consensus weight delegated from host $p_{from}$ to host $p_{to}$ for validation in $group_i$ at epoch $S$. Host $p_{from} \notin members(group_i)$; host $p_{to} \in members(group_i)$. Delegation is set before epoch start; changes during epoch take effect from epoch $S+1$.

- $voting\_power_S(group_i, p) = consensus\_weight_S(p) + \sum_{p_{from}} delegation_S(group_i, p_{from}, p)$ — total validation voting power of host $p$ in $group_i$

**Q1: Can a host split delegation across multiple hosts in the same group?**

**Q2: Any punishment if host does not delegate when not present in a group? (if there is explicit option to refuse delegation if no hosts in group are trusted)**

### Eligible Groups

Weight computed in PoC procedure for eligible model groups contributes to total consensus weight via governance-defined coefficient. Consensus weight determines:
- Block signing power
- Governance voting power
- PoC validation voting power
- **Bitcoin-style reward distribution** (proportional to consensus weight)

Within a group, inference requests are distributed according to $poc\_weight_S(group_i, p)$. Inference rewards follow the same distribution.

### PoC Validation

Within an eligible group, validation requires >50% of total network $voting\_power$ to approve a host's result.

When host $p$ validates in $group_i$, their $voting\_power_S(group_i, p)$ counts toward the validation vote — regardless of how many MLNodes $p$ has in $group_i$. A host with 1 MLNode in the group votes with the same full power as if they had 100.

**Delegation**: Hosts not present in a group can delegate their consensus power to a host who is. The delegate's vote then carries their own weight plus all delegated weight. This enables reaching 50% threshold without requiring every host to deploy every model.

Delegation is per-group. Changes take effect from the next epoch.

**Trust model**: Currently, delegator trusts the delegate to vote correctly.

**TODO**: Introduce mechanism to revoke/invalidate delegation mid-epoch if delegate votes maliciously.

### Non-Eligible Groups

If a group is not eligible, PoC validation happens based on >50% of $consensus\_weight$ from hosts within the group (normalized to group members only, not total network weight).

Non-eligible groups can still serve inference requests. Tasks are distributed proportionally to local $poc\_weight_S(group_i, p)$.

Non-eligible groups cannot:
- Affect consensus weight
- Participate in governance decisions
- Receive bitcoin-style rewards

Hosts in non-eligible groups only receive payment for inference.

**Q3: Should non-eligible groups have higher dynamic pricing floor since hardware is not subsidized by bitcoin-style rewards?**

### New Group Onboarding

1. Governance proposal to add new model and create new group (defines $consensus\_koeff_i$)
2. Early adopters join the group with minimal hardware and serve inferences (group is non-eligible at this stage)
3. Once the group meets conditions 1-3 (sufficient hosts and consensus weight), group becomes pre-eligible ($group_i \in PreE_{S+1}$)
4. Pre-eligible group's PoC is validated by total network (>50% of $voting\_power$); if at least $V_{min}$ hosts pass, group becomes eligible ($group_i \in E_{S+1}$) and affects consensus

**Q4: Should participating in non-eligible group be replaceable with commitment (with deposit) to participate in PoC if group becomes eligible >= N blocks before epoch start?** N blocks gives time to deploy the model. If host fails to participate after commitment, deposit is burned. This helps collect sufficient weight without hosts losing consensus weight during non-eligible epochs.

## Implementation

[To be defined]
