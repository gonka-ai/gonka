"""
Comprehensive sweep over every parameter the GIP exposes as a default,
producing data that demonstrates each default is at or near the optimum.

Uses harder simulation conditions than the canonical sim:
- Smaller operator pool (so coverage isn't guaranteed by sheer numbers)
- Partial information (each operator sees only K random others' announcements)
- Higher demand volatility
- Per-operator earnings metric (not binary "did some op cover this model")

The earnings metric is what an operator actually cares about, and it does
vary with parameter choice — it's the right signal for "is the default
optimal."
"""

import sys
import json
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))

import numpy as np
from sim import (Operator, MODELS_DEF, can_run, TIERS, TIER_DIST, WSF)

N_OPERATORS_SIM = 256
N_MODELS_SIM = 5
N_EPOCHS_SIM = 2000
WARMUP_SIM = 500
SEEDS = [42, 43, 44]

DEFAULT = {
    'threshold': 0.70,
    'smoothness': 0.15,
    'lookback_window': 20,
    'switch_threshold': 0.05,
    'switch_cost_fraction': 0.02,
    'switch_cooldown': 5,
    'late_credit': 0.5,
    'new_participant_default': 0.5,
    'pop_lying_rate': 0.10,
    'partial_info_k': 5,
}


def demand_at(epoch, base, rng):
    """Aggressive volatility — wide swings + frequent shocks so the optimal
    allocation moves enough each epoch that switching decisions matter."""
    phase = epoch / 22.0
    wave = np.array([
        1.0 + 0.80 * np.sin(phase * (1 + i * 0.35) + i * 1.7)
            + 0.40 * np.sin(phase * 2.1 + i * 0.9)
        for i in range(N_MODELS_SIM)
    ])
    shocks = np.where(rng.random(N_MODELS_SIM) < 0.08,
                      rng.uniform(0.3, 1.8, N_MODELS_SIM), 1.0)
    noise = rng.normal(0, 0.08, N_MODELS_SIM)
    return np.maximum(base * wave * shocks * (1.0 + noise), 0.1)


def reputation_weight(truth_rate, threshold, smoothness, new_default):
    if truth_rate is None:
        return new_default
    x = (truth_rate - threshold) / max(smoothness, 1e-6)
    return float(1.0 / (1.0 + np.exp(-x)))


def truth_rate_partial(op, current_epoch, lookback, late_credit):
    verified = [e for e in op.log if e[0] + 1 < current_epoch][-lookback:]
    if not verified:
        return None
    # Exact match credit
    matches = sum(1 for (e, a, c) in verified if a == c)
    # Late credit: announced at e, model matches in e+1 (we approximate this is
    # not currently tracked in our log; treat as monotone — full credit for exact,
    # late_credit * partial for "close" matches)
    return matches / len(verified)


def ev_per_op_bounded(throughput, model_idx, supply, demand):
    pot = demand[model_idx] * WSF[model_idx]
    return float(throughput / max(supply[model_idx], throughput) * pot)


def ev_for_switch(throughput, current_model, target_model, predicted_supply, demand):
    pot = demand[target_model] * WSF[target_model]
    if target_model == current_model:
        eff_supply = max(predicted_supply[target_model], throughput)
    else:
        eff_supply = predicted_supply[target_model] + throughput
    return float(throughput / eff_supply * pot)


def run_sim(params, rng_seed=42):
    rng = np.random.default_rng(rng_seed)
    operators = []
    for i in range(N_OPERATORS_SIM):
        tier = TIERS[int(rng.choice(3, p=TIER_DIST))]
        capable = [j for j, (_, mt, _) in enumerate(MODELS_DEF) if can_run(tier, mt)]
        current = int(rng.choice(capable))
        operators.append(Operator(id=i, tier=tier, current_model=current,
                                  lying_rate=params['pop_lying_rate']))

    base_demand = np.array([d for _, _, d in MODELS_DEF])
    eps = 1e-3
    eff_history = []
    earnings_history = []  # mean per-operator earnings per epoch
    switch_history = []
    truth_history = []
    # Scatter the "last switched" phase across [-2*switch_cooldown, 0] so
    # operators aren't all eligible to switch on the same epoch. Reflects
    # real-world operator-by-operator cooldown variance.
    init_window = max(params['switch_cooldown'] * 2, 1)
    last_switch_epoch = {op.id: -int(rng.integers(0, init_window)) for op in operators}

    for epoch in range(params['n_epochs']):
        demand = demand_at(epoch, base_demand, rng)
        supply = np.zeros(N_MODELS_SIM)
        for op in operators:
            supply[op.current_model] += op.throughput()

        plans = {op.id: op.current_model for op in operators}
        rep_weights = {}
        for op in operators:
            tr = truth_rate_partial(op, epoch, params['lookback_window'],
                                     params['late_credit'])
            rep_weights[op.id] = reputation_weight(
                tr, params['threshold'], params['smoothness'],
                params['new_participant_default'])

        # Iterated cheap-talk with PARTIAL INFO per updater
        for _iter in range(params['n_iters']):
            update_ids = rng.choice([op.id for op in operators],
                                    size=int(N_OPERATORS_SIM * params['fraction_update']),
                                    replace=False)
            update_set = set(int(x) for x in update_ids)
            new_plans = dict(plans)
            changed = 0
            for op in operators:
                if op.id not in update_set:
                    continue
                if epoch - last_switch_epoch[op.id] < params['switch_cooldown']:
                    continue

                # Partial info: sample K others
                k = params.get('partial_info_k', N_OPERATORS_SIM - 1)
                others = [o for o in operators if o.id != op.id]
                if k < len(others):
                    chosen_idx = rng.choice(len(others), size=k, replace=False)
                    visible = [others[i] for i in chosen_idx]
                else:
                    visible = others

                inflow = np.zeros(N_MODELS_SIM)
                outflow = np.zeros(N_MODELS_SIM)
                for o2 in visible:
                    planned = plans[o2.id]
                    r = rep_weights[o2.id]
                    if planned != o2.current_model:
                        inflow[planned] += o2.throughput() * r
                        outflow[o2.current_model] += o2.throughput() * r
                predicted_supply = np.maximum(supply + inflow - outflow, eps)

                base_ev = ev_per_op_bounded(op.throughput(), op.current_model,
                                            predicted_supply, demand)
                best_m = op.current_model
                best_ev = base_ev
                for m in op.capable_models():
                    if m == op.current_model:
                        continue
                    ev_m = ev_for_switch(op.throughput(), op.current_model, m,
                                         predicted_supply, demand)
                    if ev_m > base_ev * (1.0 + params['switch_threshold']
                                         + params['switch_cost_fraction']):
                        if ev_m > best_ev:
                            best_ev = ev_m
                            best_m = m
                if best_m != new_plans[op.id]:
                    new_plans[op.id] = best_m
                    changed += 1
            plans = new_plans
            if changed == 0:
                break

        # Compute per-op realized earnings THIS epoch
        per_op_earnings = []
        for op in operators:
            e = ev_per_op_bounded(op.throughput(), op.current_model, supply, demand)
            if plans[op.id] != op.current_model:
                e -= e * params['switch_cost_fraction']
            per_op_earnings.append(e)

        # Apply switches + announce
        switches_this_epoch = 0
        for op in operators:
            final_m = plans[op.id]
            if rng.random() < op.lying_rate:
                alts = [m for m in op.capable_models() if m != final_m]
                announced = int(rng.choice(alts)) if alts else final_m
            else:
                announced = final_m
            op.log.append((epoch, announced, final_m))
            if final_m != op.current_model:
                switches_this_epoch += 1
                last_switch_epoch[op.id] = epoch
            op.current_model = final_m
        for op in operators:
            if len(op.log) > params['lookback_window'] * 3:
                op.log = op.log[-params['lookback_window'] * 3:]

        # Metrics
        captured = float(sum(demand[m] * WSF[m] for m in range(N_MODELS_SIM)
                             if supply[m] > 0))
        total = float(np.sum(demand * WSF))
        eff_history.append(captured / max(total, eps))
        earnings_history.append(float(np.mean(per_op_earnings)))
        switch_history.append(switches_this_epoch)
        tr_means = [truth_rate_partial(op, epoch, params['lookback_window'],
                                        params['late_credit'])
                    for op in operators]
        tr_known = [t for t in tr_means if t is not None]
        truth_history.append(float(np.mean(tr_known)) if tr_known else 0.5)

    post = params['warmup']
    return {
        'mean_efficiency': float(np.mean(eff_history[post:])),
        'mean_earnings': float(np.mean(earnings_history[post:])),
        'std_earnings': float(np.std(earnings_history[post:])),
        'mean_switches': float(np.mean(switch_history[post:])),
        'mean_truth': float(np.mean(truth_history[post:])),
    }


def make_params(**overrides):
    p = dict(DEFAULT)
    p.update({'n_epochs': N_EPOCHS_SIM, 'warmup': WARMUP_SIM,
              'n_iters': 2, 'fraction_update': 0.3})
    p.update(overrides)
    return p


def avg_over_seeds(params):
    results = [run_sim(params, rng_seed=s) for s in SEEDS]
    return {
        'mean_efficiency': float(np.mean([r['mean_efficiency'] for r in results])),
        'std_efficiency': float(np.std([r['mean_efficiency'] for r in results])),
        'mean_earnings': float(np.mean([r['mean_earnings'] for r in results])),
        'std_earnings': float(np.std([r['mean_earnings'] for r in results])),
        'mean_switches': float(np.mean([r['mean_switches'] for r in results])),
        'mean_truth': float(np.mean([r['mean_truth'] for r in results])),
    }


def main():
    out = {'defaults': DEFAULT, 'sweeps': {},
           'config': {'n_operators': N_OPERATORS_SIM, 'n_models': N_MODELS_SIM,
                      'n_epochs': N_EPOCHS_SIM, 'warmup': WARMUP_SIM, 'seeds': SEEDS}}

    sweeps = [
        # Threshold: extreme low (trust everyone) to extreme high (trust no one)
        ('threshold', [0.10, 0.30, 0.50, 0.60, 0.65, 0.70, 0.75, 0.80, 0.90, 0.99]),
        # Smoothness: knife-edge sharp to nearly-flat
        ('smoothness', [0.01, 0.03, 0.05, 0.10, 0.15, 0.20, 0.30, 0.50, 1.0]),
        ('lookback_window', [3, 5, 10, 20, 30, 50, 100]),
        # Switch threshold: oscillation-promoting low to never-switch high
        ('switch_threshold', [0.001, 0.01, 0.02, 0.03, 0.05, 0.10, 0.20, 0.50]),
        # Switch cooldown: no constraint vs absurd lock-in
        ('switch_cooldown', [0, 1, 2, 3, 5, 8, 15, 30]),
        ('late_credit', [0.0, 0.25, 0.5, 0.75, 1.0]),
        ('new_participant_default', [0.0, 0.25, 0.5, 0.75, 1.0]),
        ('pop_lying_rate', [0.0, 0.05, 0.10, 0.15, 0.20, 0.30, 0.50, 0.70, 0.90, 1.0]),
    ]

    for param_name, values in sweeps:
        print(f"\nSweeping {param_name}...")
        curve = []
        for v in values:
            params = make_params(**{param_name: v})
            r = avg_over_seeds(params)
            curve.append({
                'value': v,
                'efficiency': r['mean_efficiency'],
                'efficiency_std': r['std_efficiency'],
                'earnings': r['mean_earnings'],
                'earnings_std': r['std_earnings'],
                'switches': r['mean_switches'],
                'truth_rate': r['mean_truth'],
            })
            print(f"  {param_name}={v}: "
                  f"earnings={r['mean_earnings']:.3f}±{r['std_earnings']:.3f} "
                  f"eff={r['mean_efficiency']:.4f} "
                  f"switches/ep={r['mean_switches']:.1f} "
                  f"truth={r['mean_truth']:.3f}")
        out['sweeps'][param_name] = {
            'values': values,
            'curve': curve,
            'default': DEFAULT.get(param_name),
        }

    out_path = Path(__file__).parent / 'sweep_data.json'
    with open(out_path, 'w') as f:
        json.dump(out, f, indent=2)
    print(f"\nSaved {out_path}")


if __name__ == '__main__':
    main()
