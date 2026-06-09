"""
Multi-model scheduling simulation.

Tests how the (threshold, smoothness) parameters of the announcement-honesty
sigmoid affect market efficiency, herding amplitude, and the equilibrium
distribution of personal lying rates that operators converge to.

Model:
- N operators, 3 hardware tiers, M models with tier requirements.
- Demand per model fluctuates (sine waves + noise).
- Each epoch, operators decide what to run next epoch based on:
  - current supply per model
  - reputation-weighted predicted inflow/outflow from this round's announcements
- Each operator has a personal lying rate L (prob of bluffing per announcement).
- L adapts via noisy hill-climb based on realized EV.
- Reputation = sigmoid((truth_rate - threshold) / smoothness).
"""

from __future__ import annotations
import numpy as np
from dataclasses import dataclass, field
from typing import Optional

# ----- Static config -----
N_OPERATORS = 100
N_EPOCHS = 600
WARMUP_EPOCHS = 200  # discard for metrics — let dynamics settle
ANNOUNCEMENT_WINDOW = 20
SWITCH_MARGIN = 0.05  # 5% EV margin required
SWITCH_COST = 0.02    # 2% one-epoch earnings loss (~30 min downtime)
ADAPTATION_INTERVAL = 25
ADAPTATION_NOISE = 0.04
LR_DEFAULT_NEW = 0.5  # neutral reputation for new operators

# Hardware tier throughput multipliers
TIERS = ['small', 'mid', 'flagship']
TIER_THROUGHPUT = {'small': 1.0, 'mid': 4.0, 'flagship': 10.0}
TIER_DIST = [0.50, 0.35, 0.15]
TIER_RANK = {t: i for i, t in enumerate(TIERS)}

# Models: (name, min_tier, base_demand)
MODELS_DEF = [
    ('M_s1', 'small',    50.0),
    ('M_s2', 'small',    30.0),
    ('M_m1', 'mid',     150.0),
    ('M_m2', 'mid',     100.0),
    ('M_l',  'flagship', 400.0),
]
N_MODELS = len(MODELS_DEF)
# Layer 2 weight_scale_factor — proportional to demand baseline so larger models
# pay more per PoC unit (the multi-model PoC GIP intent)
WSF = np.array([d for _, _, d in MODELS_DEF])
WSF = WSF / WSF.max() * 2.0


def can_run(tier: str, min_tier: str) -> bool:
    return TIER_RANK[tier] >= TIER_RANK[min_tier]


@dataclass
class Operator:
    id: int
    tier: str
    current_model: int
    lying_rate: float
    # log[i] = (epoch_announced, announced_target, actual_at_target_epoch)
    log: list = field(default_factory=list)
    # realized EV per epoch for adaptation
    ev_history: list = field(default_factory=list)
    lr_history: list = field(default_factory=list)
    last_lr_change: float = 0.0  # last delta applied to L (for hill climb direction)

    def throughput(self) -> float:
        return TIER_THROUGHPUT[self.tier]

    def capable_models(self) -> list:
        return [i for i, (_, mt, _) in enumerate(MODELS_DEF) if can_run(self.tier, mt)]

    def truth_rate(self, current_epoch: int) -> Optional[float]:
        # Verified announcements: ones where target epoch has passed
        verified = [(e, a, c) for (e, a, c) in self.log if e + 1 < current_epoch]
        if not verified:
            return None
        recent = verified[-ANNOUNCEMENT_WINDOW:]
        return sum(1 for (e, a, c) in recent if a == c) / len(recent)


def reputation_weight(truth_rate: Optional[float], threshold: float, smoothness: float) -> float:
    if truth_rate is None:
        return LR_DEFAULT_NEW
    x = (truth_rate - threshold) / max(smoothness, 1e-6)
    return float(1.0 / (1.0 + np.exp(-x)))


def make_operators(rng: np.random.Generator, init_lr_dist: str = 'uniform') -> list[Operator]:
    ops = []
    for i in range(N_OPERATORS):
        tier = TIERS[int(rng.choice(3, p=TIER_DIST))]
        capable = [j for j, (_, mt, _) in enumerate(MODELS_DEF) if can_run(tier, mt)]
        current = int(rng.choice(capable))
        if init_lr_dist == 'uniform':
            lr = float(rng.uniform(0.0, 0.4))
        elif init_lr_dist == 'all_honest':
            lr = 0.0
        elif init_lr_dist == 'all_liars':
            lr = 0.8
        elif init_lr_dist == 'mixed':
            lr = float(rng.beta(1.5, 4.0))  # bias toward low
        else:
            lr = 0.15
        ops.append(Operator(id=i, tier=tier, current_model=current, lying_rate=lr))
    return ops


def demand_at(epoch: int, base: np.ndarray, rng: np.random.Generator) -> np.ndarray:
    """Demand fluctuates with overlapping sine waves + occasional small shocks.
    Calibrated so optimal allocation drifts on a timescale of 10-30 epochs,
    long enough for agents to adapt and switch profitably."""
    phase = epoch / 60.0
    wave = np.array([1.0 + 0.35 * np.sin(phase * (1 + i * 0.2) + i * 1.7)
                     + 0.15 * np.sin(phase * 1.8 + i * 0.9)
                     for i in range(N_MODELS)])
    # Rare shocks (3% chance per model per epoch, smaller magnitude)
    shocks = np.where(rng.random(N_MODELS) < 0.03,
                      rng.uniform(0.7, 1.3, N_MODELS),
                      1.0)
    noise = rng.normal(0, 0.04, N_MODELS)
    return np.maximum(base * wave * shocks * (1.0 + noise), 0.1)


def ev_per_op(throughput: float, model_idx: int, supply: np.ndarray, demand: np.ndarray) -> float:
    """Realized earnings per epoch for an operator currently on the given model.
       Used for current-epoch realized payoff, not predicted EV of a candidate switch.
       Caller MUST ensure supply[model_idx] >= throughput (the op is on this model)."""
    pot = demand[model_idx] * WSF[model_idx]
    return float(throughput / max(supply[model_idx], throughput) * pot)


def ev_if_op_switches_to(op_throughput: float, current_model: int, target_model: int,
                         predicted_supply: np.ndarray, demand: np.ndarray) -> float:
    """Predicted EV if this op commits to target_model next epoch.
       If they're not currently on target_model, their throughput must be ADDED to
       predicted_supply[target_model] since they will contribute by switching there.
       Bounded by pot — they can never earn more than the full model pot (alone case)."""
    pot = demand[target_model] * WSF[target_model]
    if target_model == current_model:
        # Stay: op's throughput already in supply
        eff_supply = max(predicted_supply[target_model], op_throughput)
    else:
        # Switch: op's throughput added to predicted_supply
        eff_supply = predicted_supply[target_model] + op_throughput
    return float(op_throughput / eff_supply * pot)


def best_model_for(op: Operator, predicted_supply: np.ndarray, demand: np.ndarray) -> tuple[int, float]:
    """Pick best EV model accounting for switch cost + margin."""
    base_ev = ev_if_op_switches_to(op.throughput(), op.current_model, op.current_model,
                                    predicted_supply, demand)
    best_m = op.current_model
    best_ev = base_ev
    for m in op.capable_models():
        if m == op.current_model:
            continue
        ev_m = ev_if_op_switches_to(op.throughput(), op.current_model, m,
                                    predicted_supply, demand)
        if ev_m > base_ev * (1.0 + SWITCH_MARGIN + SWITCH_COST):
            if ev_m > best_ev:
                best_ev = ev_m
                best_m = m
    return best_m, best_ev


def run_simulation(threshold: float, smoothness: float, n_epochs: int = N_EPOCHS,
                    init_lr_dist: str = 'uniform', rng_seed: int = 42,
                    adapt: bool = True) -> dict:
    rng = np.random.default_rng(rng_seed)
    operators = make_operators(rng, init_lr_dist=init_lr_dist)
    base_demand = np.array([d for _, _, d in MODELS_DEF])

    # Per-epoch tracking
    history = {
        'epoch': [],
        'avg_lr': [],
        'lr_std': [],
        'efficiency': [],
        'supply_per_model': [],
        'truth_rates': [],
        'switches': [],
    }

    eps = 1e-3

    for epoch in range(n_epochs):
        demand = demand_at(epoch, base_demand, rng)

        # Current supply per model
        supply = np.zeros(N_MODELS)
        for op in operators:
            supply[op.current_model] += op.throughput()

        # --- Iterated cheap-talk: synchronous updates oscillate (everyone flees to "best"
        # model simultaneously, then all flee away). Use ASYNCHRONOUS updates: each
        # iteration, only a random subset of agents updates. Converges to a stable
        # allocation where no agent has a profitable single-deviation switch.
        plans = {op.id: op.current_model for op in operators}
        rep_weights = {op.id: reputation_weight(op.truth_rate(epoch), threshold, smoothness)
                       for op in operators}
        N_ITERS = 10
        FRACTION_UPDATE = 0.3  # 30% of agents update per iteration
        for _iter in range(N_ITERS):
            # Recompute predicted supply
            inflow = np.zeros(N_MODELS)
            outflow = np.zeros(N_MODELS)
            for op in operators:
                planned = plans[op.id]
                r = rep_weights[op.id]
                if planned != op.current_model:
                    inflow[planned] += op.throughput() * r
                    outflow[op.current_model] += op.throughput() * r
            predicted_supply = np.maximum(supply + inflow - outflow, eps)
            # Random subset updates plan
            update_ids = rng.choice([op.id for op in operators],
                                    size=int(N_OPERATORS * FRACTION_UPDATE),
                                    replace=False)
            update_set = set(int(x) for x in update_ids)
            changed = 0
            for op in operators:
                if op.id not in update_set:
                    continue
                new_m, _ = best_model_for(op, predicted_supply, demand)
                if new_m != plans[op.id]:
                    plans[op.id] = new_m
                    changed += 1
            if changed == 0:
                break

        # --- Now each agent commits = announces their converged plan (with possible bluff) ---
        final_actions = plans
        switches_this_epoch = sum(1 for op in operators if plans[op.id] != op.current_model)
        announcements = []
        for op in operators:
            final_m = plans[op.id]
            if rng.random() < op.lying_rate:
                alts = [m for m in op.capable_models() if m != final_m]
                announced = int(rng.choice(alts)) if alts else final_m
            else:
                announced = final_m
            announcements.append((op, announced, final_m))

        # Compute THIS epoch's realized earnings (using THIS epoch's actual current_model
        # and supply that were running, BEFORE applying switches)
        per_op_ev_this_epoch = {}
        for op in operators:
            per_op_ev_this_epoch[op.id] = ev_per_op(op.throughput(), op.current_model, supply, demand)

        # Apply switches + log announcement vs actual
        for op, announced, _ in announcements:
            final = final_actions[op.id]
            op.log.append((epoch, announced, final))
            this_epoch_ev = per_op_ev_this_epoch[op.id]
            if final != op.current_model:
                # Switch cost will hit next epoch as downtime — reflect in adaptation signal
                this_epoch_ev -= this_epoch_ev * SWITCH_COST
            op.ev_history.append(this_epoch_ev)
            op.lr_history.append(op.lying_rate)
            op.current_model = final

        # Trim per-op logs
        for op in operators:
            if len(op.log) > ANNOUNCEMENT_WINDOW * 3:
                op.log = op.log[-ANNOUNCEMENT_WINDOW * 3:]
            if len(op.ev_history) > ADAPTATION_INTERVAL * 4:
                op.ev_history = op.ev_history[-ADAPTATION_INTERVAL * 4:]
                op.lr_history = op.lr_history[-ADAPTATION_INTERVAL * 4:]

        # --- Adaptation: every ADAPTATION_INTERVAL epochs, hill-climb on lying_rate ---
        if adapt and epoch > 0 and epoch % ADAPTATION_INTERVAL == 0 and epoch > ADAPTATION_INTERVAL * 2:
            for op in operators:
                # Compare recent EV (last interval) to previous interval
                if len(op.ev_history) >= ADAPTATION_INTERVAL * 2:
                    recent = float(np.mean(op.ev_history[-ADAPTATION_INTERVAL:]))
                    prev = float(np.mean(op.ev_history[-ADAPTATION_INTERVAL*2:-ADAPTATION_INTERVAL]))
                    # If recent > prev, the previous lr change was good direction; continue
                    # If recent < prev, reverse
                    direction = 1.0 if recent > prev else -1.0
                    step = direction * op.last_lr_change if op.last_lr_change != 0 else float(rng.normal(0, ADAPTATION_NOISE))
                    # Add noise to avoid getting stuck
                    step += float(rng.normal(0, ADAPTATION_NOISE * 0.5))
                    new_lr = float(np.clip(op.lying_rate + step, 0.0, 1.0))
                    op.last_lr_change = new_lr - op.lying_rate
                    op.lying_rate = new_lr
                else:
                    step = float(rng.normal(0, ADAPTATION_NOISE))
                    new_lr = float(np.clip(op.lying_rate + step, 0.0, 1.0))
                    op.last_lr_change = new_lr - op.lying_rate
                    op.lying_rate = new_lr

        # Record metrics
        # Efficiency = fraction of total value pot the network actually captures.
        # Per epoch, model m's pot = demand_m * WSF_m. That pot is collected only if
        # at least one operator runs m. If all operators stampede onto a single model,
        # the others' pots are wasted. Perfect efficiency = 1.0 (every populated model
        # captured), achieved when supply > 0 on every model.
        captured_pot = float(sum(demand[m] * WSF[m] for m in range(N_MODELS) if supply[m] > 0))
        total_pot = float(np.sum(demand * WSF))
        efficiency = captured_pot / max(total_pot, eps)

        # Truth rates aggregate
        truth_rates_now = [op.truth_rate(epoch) for op in operators]
        truth_rates_known = [t for t in truth_rates_now if t is not None]

        history['epoch'].append(epoch)
        history['avg_lr'].append(float(np.mean([op.lying_rate for op in operators])))
        history['lr_std'].append(float(np.std([op.lying_rate for op in operators])))
        history['efficiency'].append(efficiency)
        history['supply_per_model'].append(supply.copy())
        history['truth_rates'].append(float(np.mean(truth_rates_known)) if truth_rates_known else 0.5)
        history['switches'].append(switches_this_epoch)

    # Convert to arrays
    for k, v in history.items():
        history[k] = np.array(v)

    # Summary statistics over post-warmup period
    post = WARMUP_EPOCHS
    summary = {
        'mean_efficiency': float(np.mean(history['efficiency'][post:])),
        'mean_avg_lr': float(np.mean(history['avg_lr'][post:])),
        'std_avg_lr': float(np.mean(history['lr_std'][post:])),
        'mean_truth_rate': float(np.mean(history['truth_rates'][post:])),
        'mean_switches': float(np.mean(history['switches'][post:])),
        'supply_variance': float(np.mean([np.var(supply / max(supply.sum(), 1e-6))
                                          for supply in history['supply_per_model'][post:]])),
        'efficiency_temporal_std': float(np.std(history['efficiency'][post:])),
        'final_lr_distribution': [op.lying_rate for op in operators],
    }
    return {'history': history, 'summary': summary, 'operators': operators}


if __name__ == '__main__':
    # Quick smoke run
    result = run_simulation(threshold=0.7, smoothness=0.1)
    s = result['summary']
    print(f"threshold=0.7 smoothness=0.1:")
    for k, v in s.items():
        if isinstance(v, list):
            print(f"  {k}: mean={np.mean(v):.3f} std={np.std(v):.3f}")
        else:
            print(f"  {k}: {v:.4f}")
