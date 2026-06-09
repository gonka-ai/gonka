"""
Comprehensive Nash analysis: for each (threshold, smoothness) and population L,
measure the best individual deviation L. Find true Nash equilibria.

Also tests: does adding info noise / reducing iterations change whether bluffing
is individually rational?
"""

import sys
sys.path.insert(0, '.')

import numpy as np
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from sim import (N_OPERATORS, N_MODELS, N_EPOCHS, WARMUP_EPOCHS,
                 Operator, MODELS_DEF, can_run, TIERS, TIER_DIST, WSF,
                 demand_at, reputation_weight, best_model_for, ev_per_op,
                 ANNOUNCEMENT_WINDOW, SWITCH_COST)


def run(threshold, smoothness, pop_L, deviator_L, n_iters, fraction_update,
        partial_info=False, rng_seed=42):
    """Run sim with custom iteration / info-noise settings. Returns op0's mean EV."""
    rng = np.random.default_rng(rng_seed)
    operators = []
    for i in range(N_OPERATORS):
        tier = TIERS[int(rng.choice(3, p=TIER_DIST))]
        capable = [j for j, (_, mt, _) in enumerate(MODELS_DEF) if can_run(tier, mt)]
        current = int(rng.choice(capable))
        lr = float(deviator_L) if i == 0 else float(pop_L)
        operators.append(Operator(id=i, tier=tier, current_model=current, lying_rate=lr))

    base_demand = np.array([d for _, _, d in MODELS_DEF])
    eps = 1e-3
    op0_ev_history = []
    efficiency_history = []

    for epoch in range(N_EPOCHS):
        demand = demand_at(epoch, base_demand, rng)
        supply = np.zeros(N_MODELS)
        for op in operators:
            supply[op.current_model] += op.throughput()

        plans = {op.id: op.current_model for op in operators}
        rep_weights = {op.id: reputation_weight(op.truth_rate(epoch), threshold, smoothness)
                       for op in operators}
        for _iter in range(n_iters):
            inflow = np.zeros(N_MODELS)
            outflow = np.zeros(N_MODELS)
            for op in operators:
                planned = plans[op.id]
                r = rep_weights[op.id]
                if planned != op.current_model:
                    inflow[planned] += op.throughput() * r
                    outflow[op.current_model] += op.throughput() * r
            predicted_supply = np.maximum(supply + inflow - outflow, eps)
            update_ids = rng.choice([op.id for op in operators],
                                    size=int(N_OPERATORS * fraction_update),
                                    replace=False)
            update_set = set(int(x) for x in update_ids)
            changed = 0
            for op in operators:
                if op.id not in update_set:
                    continue
                if partial_info:
                    # Each updater sees only a random sample of K others
                    K_SAMPLE = 20
                    visible_ids = set(int(x) for x in
                                      rng.choice([o.id for o in operators if o.id != op.id],
                                                 size=K_SAMPLE, replace=False))
                    inflow_p = np.zeros(N_MODELS)
                    outflow_p = np.zeros(N_MODELS)
                    for o2 in operators:
                        if o2.id not in visible_ids:
                            continue
                        planned = plans[o2.id]
                        r = rep_weights[o2.id]
                        if planned != o2.current_model:
                            inflow_p[planned] += o2.throughput() * r
                            outflow_p[o2.current_model] += o2.throughput() * r
                    pred_supply_local = np.maximum(supply + inflow_p - outflow_p, eps)
                    new_m, _ = best_model_for(op, pred_supply_local, demand)
                else:
                    new_m, _ = best_model_for(op, predicted_supply, demand)
                if new_m != plans[op.id]:
                    plans[op.id] = new_m
                    changed += 1
            if changed == 0:
                break

        per_op_ev = {op.id: ev_per_op(op.throughput(), op.current_model, supply, demand)
                     for op in operators}
        for op in operators:
            final_m = plans[op.id]
            if rng.random() < op.lying_rate:
                alts = [m for m in op.capable_models() if m != final_m]
                announced = int(rng.choice(alts)) if alts else final_m
            else:
                announced = final_m
            op.log.append((epoch, announced, final_m))
            if final_m != op.current_model:
                per_op_ev[op.id] -= per_op_ev[op.id] * SWITCH_COST
            op.current_model = final_m
        for op in operators:
            if len(op.log) > ANNOUNCEMENT_WINDOW * 3:
                op.log = op.log[-ANNOUNCEMENT_WINDOW * 3:]

        op0_ev_history.append(per_op_ev[0])
        captured = float(sum(demand[m] * WSF[m] for m in range(N_MODELS) if supply[m] > 0))
        total = float(np.sum(demand * WSF))
        efficiency_history.append(captured / max(total, eps))

    return {
        'op0_ev': float(np.mean(op0_ev_history[WARMUP_EPOCHS:])),
        'efficiency': float(np.mean(efficiency_history[WARMUP_EPOCHS:])),
    }


def find_nash(threshold, smoothness, n_iters, fraction_update, partial_info, seeds, label):
    """For each pop_L in a grid, find the best deviator L. Returns (pop_L, best_dev_L, op0_ev, efficiency)."""
    POP_L_GRID = [0.0, 0.05, 0.10, 0.15, 0.20, 0.30, 0.50, 0.80]
    DEV_L_GRID = [0.0, 0.05, 0.10, 0.15, 0.20, 0.30, 0.50, 0.80]
    print(f"\n=== {label} ===")
    print(f"  threshold={threshold}, smoothness={smoothness}, n_iters={n_iters}, "
          f"fraction_update={fraction_update}, partial_info={partial_info}")
    print(f"  {'pop_L':>6} {'best_dev_L':>10} {'op0_ev (best)':>14} "
          f"{'pop_eff':>10} {'gain_vs_match':>14}")
    results = []
    for pop_L in POP_L_GRID:
        dev_evs = []
        for dev_L in DEV_L_GRID:
            evs = [run(threshold, smoothness, pop_L, dev_L, n_iters, fraction_update,
                       partial_info, rng_seed=s)['op0_ev'] for s in seeds]
            dev_evs.append((dev_L, float(np.mean(evs))))
        best = max(dev_evs, key=lambda x: x[1])
        match_ev = next(ev for dl, ev in dev_evs if dl == pop_L)
        pop_eff = float(np.mean([run(threshold, smoothness, pop_L, pop_L,
                                     n_iters, fraction_update, partial_info, rng_seed=s)['efficiency']
                                 for s in seeds]))
        gain = (best[1] - match_ev) / max(abs(match_ev), 1e-6)
        marker = '  <-- Nash' if best[0] == pop_L else ''
        results.append((pop_L, best[0], best[1], pop_eff, gain))
        print(f"  {pop_L:>6.2f} {best[0]:>10.2f} {best[1]:>14.3f} "
              f"{pop_eff:>10.3f} {gain:>+14.4f}{marker}")

    # Find Nash equilibrium: smallest pop_L where best_dev_L == pop_L
    nash_pop_L = None
    for pop_L, best_dev_L, *_ in results:
        if abs(best_dev_L - pop_L) < 0.001:
            nash_pop_L = pop_L
            break
    if nash_pop_L is not None:
        print(f"  -> Nash equilibrium: L* = {nash_pop_L}")
    else:
        print(f"  -> No clean Nash in grid (best deviations push L)")
    return results


def main():
    seeds = [42, 43, 44]

    # Compare three scenarios at the same (threshold, smoothness):
    # 1. Default: 10 iterations, 30% update, full info — cleanest equilibrium
    # 2. Reduced iterations: 2 iterations
    # 3. Partial info: each agent sees only 20 others
    config = (0.7, 0.10)  # threshold, smoothness — a "moderate" config

    print(f"\n{'='*80}")
    print(f"Testing config: threshold={config[0]}, smoothness={config[1]}")
    print(f"{'='*80}")

    r1 = find_nash(config[0], config[1], n_iters=10, fraction_update=0.3,
                   partial_info=False, seeds=seeds, label='Default (clean equilibrium)')
    r2 = find_nash(config[0], config[1], n_iters=2, fraction_update=0.3,
                   partial_info=False, seeds=seeds, label='Reduced iterations (less convergence)')
    r3 = find_nash(config[0], config[1], n_iters=10, fraction_update=0.3,
                   partial_info=True, seeds=seeds, label='Partial info (each sees 20/100)')
    r4 = find_nash(config[0], config[1], n_iters=2, fraction_update=0.3,
                   partial_info=True, seeds=seeds, label='Reduced iters + partial info')

    # Plot: for each scenario, show pop_eff and op0_ev(deviator at best_L) vs pop_L
    fig, axes = plt.subplots(1, 2, figsize=(14, 6))

    for results, label in [(r1, 'default'), (r2, '2 iters'),
                            (r3, 'partial info'), (r4, '2 iters + partial')]:
        pop_Ls = [r[0] for r in results]
        effs = [r[3] for r in results]
        gains = [r[4] for r in results]
        axes[0].plot(pop_Ls, effs, marker='o', label=label)
        axes[1].plot(pop_Ls, gains, marker='o', label=label)

    axes[0].set_xlabel('Population lying rate L')
    axes[0].set_ylabel('Market efficiency')
    axes[0].set_title(f'Efficiency vs population L\n(threshold={config[0]}, smoothness={config[1]})')
    axes[0].legend()
    axes[0].grid(True, alpha=0.3)

    axes[1].set_xlabel('Population lying rate L')
    axes[1].set_ylabel('Best deviation gain (fraction)')
    axes[1].axhline(0, color='red', linestyle='--', alpha=0.5)
    axes[1].set_title(f'Individual incentive to deviate vs population L\n(>0 = profit from being different)')
    axes[1].legend()
    axes[1].grid(True, alpha=0.3)

    plt.tight_layout()
    plt.savefig('nash_full.png', dpi=110, bbox_inches='tight')
    print(f"\nSaved nash_full.png")


if __name__ == '__main__':
    main()
