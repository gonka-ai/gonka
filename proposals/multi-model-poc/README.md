# Proposal: Multi-Model PoC

> **Warning**: This proposal assumes the O(N^2) validation model (>50% weight threshold). Slot-based validation is out of scope.

## Goal / Problem

POC procedure is short term benchmark to compare how much compute each host has. It happens 1 time per epoch to define weight per each host which then used as consensus weight to produce blocks and for distributing tasks between nodes. Additionally there is Confirmation (random) POC which is used to confirm weight when network is underloaded by inference (to make sure hardware it still there).

POC phases:
- GENERATION (blocks equal to 1-5 min)
- VALIDATION (blocks equal to 2-10 min)
- INFERENCE PHASE (no POC but sometime might be interrupted to Confirmation POC)

Validation and inference theoretically can be done in parallel.

### Security Model

Required >50% of **total network consensus weight** to vote "valid". Without delegation, an attacker needs >50% of total network weight to corrupt any host's validation. Delegation adds an additional trust assumption (see "Delegation" and Appendix A).

The bitcoin-style part of reward distributed proportionally to this weight. On early phase it's main motivation as inference is much cheaper. 

---

Currently, there is single model used for PoC (Qwen3-235B-FP8). Therefore, chain also can't support another models for inference as it'd required to re-deploy model for POC (which is essentially impossible as it requires time and open network for attack when attacker deploy hardware only for POC phase).

=> option with re-deploy must not be used

## Proposal

### Terms

Let epoch $S$ be completed. The following defines weight computation for epoch $S+1$. Pre-eligibility ($PreE_{S+1}$) is determined $N$ blocks before epoch $S+1$ PoC starts. In this section, $*_S$ denotes values finalized in epoch $S$ and used as inputs for epoch $S+1$; for epoch $S+1$, group membership and delegation are evaluated at the pre-eligibility cutoff and treated as fixed for the epoch.

- $group_i$ — model group for model $i$ (members are hosts with MLNodes serving model $i$). Network supports $M$ models on-chain.

- $\text{poc\_weight}_S(group_i, p)$ — weight of host $p$ in $group_i$ at epoch $S$. Equals the number of nonces computed by $p$ in PoC procedure for this group and successfully validated. Local weight within the group.

- $\text{consensus\_koeff}_i$ — coefficient converting $\text{poc\_weight}$ in $group_i$ to consensus weight. Defined by governance per model.

- $\text{consensus\_weight}_S(p) = \sum_{i: group_i \in E_S} \text{consensus\_koeff}_i \times \text{poc\_weight}_S(group_i, p)$ — (see Appendix A for cap protection)

- $members(group_i) = \lbrace p : p \text{ has MLNode deployed for model } i \rbrace$ — hosts with MLNode deployed for the model

- $hosts_S(group_i) = \lbrace p : \text{consensus\_weight}_S(p) > 0 \text{ and } p \in members(group_i) \rbrace$

  Members with non-zero consensus weight. The weight may come from any eligible group, not necessarily $group_i$.

- $PreE_{S+1}$ — set of pre-eligible groups for epoch $S+1$. A group $group_i \in PreE_{S+1}$ if conditions 1-3 hold:
  1. Model $i$ is approved by governance with defined $\text{consensus\_koeff}_i$
  2. $\sum_{p \in members(group_i)} \text{consensus\_weight}_S(p) \geq W_{threshold} \times \sum_{p} \text{consensus\_weight}_S(p)$
  3. $|hosts_S(group_i)| \geq V_{min}$

- $E_{S+1}$ — set of consensus-eligible groups for epoch $S+1$. A group $group_i \in E_{S+1}$ if:
  - $group_i \in PreE_{S+1}$
  - At least $V_{min}$ hosts in the group pass PoC validation at epoch $S+1$ (see validation rule below)

- $W_{threshold}$ — minimum fraction of total network consensus weight required for group eligibility (governance parameter)

- $V_{min}$ — minimum number of hosts with non-zero consensus weight required in a group (governance parameter)

- Currently $group_{Qwen3-235B-FP8}$ is the only eligible group (single-model PoC). This proposal extends to multiple groups.

- The initial group ($group_{Qwen3-235B-FP8}$) is exempt from the weight cap (Appendix A) and provides base consensus weight for validating new groups.

- A host participating in multiple eligible groups requires separate hardware per group. PoC runs concurrently across all eligible groups within the same epoch.

- $delegation_S(group_i, p_{from}, p_{to})$ — consensus weight delegated from host $p_{from}$ to host $p_{to}$ for validation in $group_i$ at epoch $S$. Host $p_{from} \notin members(group_i)$; host $p_{to} \in members(group_i)$. Delegation is set before epoch start; changes during an epoch take effect from the next epoch.

- $\text{voting\_power}_S(group_i, p) = \text{consensus\_weight}_S(p) + \sum_{p_{from}} delegation_S(group_i, p_{from}, p)$ — total validation voting power of host $p$ in $group_i$

  Delegation constraints: $delegation_S(group_i, p_{from}, p_{to}) \ge 0$ and, for each $(group_i, p_{from})$, $\sum_{p_{to}} delegation_S(group_i, p_{from}, p_{to}) \le \text{consensus\_weight}_S(p_{from})$.

**Q1: Can a host split delegation across multiple hosts in the same group?**

**Q2: Any punishment if host does not delegate when not present in a group? (if there is explicit option to refuse delegation if no hosts in group are trusted)**

### Eligible Groups

Weight computed in PoC procedure for eligible model groups contributes to total consensus weight via governance-defined coefficient. Consensus weight determines:
- Block signing power
- Governance voting power
- PoC validation voting power
- **Bitcoin-style reward distribution** (proportional to consensus weight)

Within a group, inference requests are distributed according to $\text{poc\_weight}_S(group_i, p)$. Inference rewards follow the same distribution.

### PoC Validation

**Delegation**: Hosts not in a group can delegate their consensus weight to a host who is. The delegate votes on their behalf. Delegation is per-group and set before epoch start.

**Validation rule**: Host $p$'s PoC result in eligible $group_i$ is accepted if:

$$\frac{\sum_{v \text{ votes valid for } p} \text{voting\_power}_S(group_i, v)}{\sum_{q} \text{consensus\_weight}_S(q)} > \frac{1}{2}$$

- Numerator: sum of $\text{voting\_power}_S(group_i, v)$ from all validators $v$ who approved $p$
- Denominator: total network consensus weight (all hosts, all groups)

Hosts not in the group and not delegating effectively vote against approval. Delegation is therefore essential for any group whose direct members hold less than 50% of total network weight.

**Voting power details**:
- Number of MLNodes does not matter -- 1 MLNode or 100 MLNodes yields the same vote power
- Delegation changes take effect from next epoch

**Trust model**: Delegator trusts the delegate to vote correctly.

**TODO**: Mechanism to revoke delegation mid-epoch if delegate votes maliciously.

### Non-Eligible Groups

If a group is not eligible, PoC validation uses only the group's members as validators. A host $p$'s result is accepted if >50% of $\sum_{v \in members(group_i)} \text{consensus\_weight}_S(v)$ votes valid. Validators use their consensus weight from other eligible groups; validated hosts receive $\text{poc\_weight}$ in this group.

Non-eligible groups can still serve inference requests. Tasks are distributed proportionally to $\text{poc\_weight}_S(group_i, p)$.

Non-eligible groups cannot:
- Affect consensus weight
- Participate in governance decisions
- Receive bitcoin-style rewards

Hosts in non-eligible groups only receive payment for inference.

**Q3: Should non-eligible groups have higher dynamic pricing floor since hardware is not subsidized by bitcoin-style rewards?**

### New Group Onboarding

1. Governance proposal to add new model and create new group (defines $\text{consensus\_koeff}_i$)
2. Early adopters join the group with minimal hardware and serve inferences (group is non-eligible at this stage)
3. Once the group meets conditions 1-3 (sufficient hosts and consensus weight), group becomes pre-eligible ($group_i \in PreE_{S+1}$)
4. Pre-eligible group's PoC is validated by total network (>50% of $\text{voting\_power}$); if at least $V_{min}$ hosts pass, group becomes eligible ($group_i \in E_{S+1}$) and affects consensus

**Q4: Should participating in non-eligible group be replaceable with commitment (with deposit) to participate in PoC if group becomes eligible >= N blocks before epoch start?** N blocks gives time to deploy the model. If host fails to participate after commitment, deposit is burned. This helps collect sufficient weight without hosts losing consensus weight during non-eligible epochs.

## Implementation

[To be defined]

## Appendix A: Delegation-based Attack and Protection

**Attack:** Host accumulates >50% $\text{voting\_power}$ via delegation, validates fake participant claiming large weight, gains consensus control.

**Protection option:** Cap weight from each group by members' proven weight elsewhere.

$$\text{consensus weight from } group_i \leq f \times \sum_{p \in members(group_i)} \text{(}p\text{'s consensus weight from other eligible groups)}$$

If a group's raw PoC weight exceeds the cap, scale all members proportionally to fit.

For clarity: "other eligible groups" refers to consensus weight already earned from eligible groups excluding $group_i$ itself (i.e., using $\text{consensus\_weight}_S$ contributions from $E_S \setminus \lbrace group_i \rbrace$), to avoid circular dependence.

- Initial group exempt (no cap)
- $f$ is a governance parameter
- Delegation affects $\text{voting\_power}$ but not the cap (cap is PoC-weight-based)

This bounds the damage from fake participants: even if they pass validation, their weight contribution is limited by real members' stake in other groups. The cap is a secondary defense; validation (>50% of network weight) remains the primary one.

**Q5: What should $f$ be?**
