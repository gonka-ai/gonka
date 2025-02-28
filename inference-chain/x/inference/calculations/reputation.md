## Reputation Calculation
Calculates the reputation of a participant, normalized between 0 and 100. The reputation is used to determine how often to validate the participants work.
### Variables
- EpochCount: The number of epochs the participant has actively contributed to
- EpochMissPercentages: A list of the percentage of missed requests for each epoch
- ValidationParams: Global parameters:
   - EpochsToMax: The number of epochs it takes to reach the maximum reputation
   - MissPercentageCutoff: Below this percentage of missed requests, the participant will not be penalized
   - MissRequestsPenalty: A multiple for the miss percentage to enhance (or decrease) the penalty beyond the Epoch value itself

---

## Reputation Calculation

Let:
- $E =$ `EpochCount`
- $\text{ETM} =$ `EpochsToMax`
- $\text{MPC} =$ `MissPercentageCutoff`
- $\text{MRP} =$ `MissRequestsPenalty`
- $\{m_1, m_2, \dots, m_k\}$ be the list of epoch miss‐percentages.

### 1. AddMissCost

We define $\mathrm{AddMissCost}$ to figure out how much "penalty" to subtract from $E$.

1. Start at `penalty = 0`.
2. For each $m_i$ that is **strictly greater** than $\text{MPC}$, update:

$$
\text{penalty} =
\bigl(\text{penalty} + \frac{m_i}{\text{ETM}}\bigr)
\times
\text{MRP}.
$$

3. After all qualifying misses, multiply once by $\text{ETM}$:

$$
\mathrm{AddMissCost}(m_1,\dots,m_k)=
\text{penalty}
\times
\text{ETM}.
$$

### 2. Actual Epoch Count

The participant's **actual** epoch count is:

$$
\text{ActualEpochCount} = E-\mathrm{AddMissCost}(m_1,\dots,m_k).
$$

### 3. Reputation (clamped to 0–100)

Finally, the **reputation** is calculated as:

$$
\text{Reputation}=
\begin{cases}
100,
& \text{if }
\text{ActualEpochCount} > \text{ETM},\\[6pt]
0,
& \text{if }
\text{ActualEpochCount} \leq 0,\\[6pt]
\displaystyle
\left\lfloor
\frac{\text{ActualEpochCount}}{\text{ETM}}
\times
100
\right\rfloor,
& \text{otherwise}.
\end{cases}
$$

