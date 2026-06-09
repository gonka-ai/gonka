"""
Sweep (threshold, smoothness, fixed_population_L) and Nash deviation analysis.

For each (threshold, smoothness) point in the sigmoid grid, run the simulation
with a fixed population-wide lying rate L. Find:
- Population-optimal L (maximizes overall market efficiency)
- Nash-equilibrium L (no single agent can do better by deviating)
"""

import sys
sys.path.insert(0, '.')

import numpy as np
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from sim import (run_simulation, N_OPERATORS, N_MODELS, N_EPOCHS,
                 WARMUP_EPOCHS, TIER_THROUGHPUT)
from sim import Operator, MODELS_DEF, can_run, TIERS, TIER_DIST, WSF
from sim import demand_at, reputation_weight, best_model_for, ev_per_op
from sim import ANNOUNCEMENT_WINDOW, SWITCH_MARGIN, SWITCH_COST


THRESHOLDS = [0.50, 0.60, 0.70, 0.80, 0.90]
SMOOTHNESSES = [0.05, 0.10, 0.20]
LYING_RATES = [0.0, 0.05, 0.10, 0.15, 0.20, 0.30, 0.50, 0.80]
SEEDS = [42, 43, 44]


def run_fixed_L(threshold, smoothness, pop_L, deviator_L=None, n_epochs=N_EPOCHS,
                rng_seed=42):
    """Run sim with all operators having fixed lying_rate=pop_L.
       Optionally, operator 0 uses deviator_L instead.
       Returns: dict with mean efficiency and operator 0's EV."""
    rng = np.random.default_rng(rng_seed)
    operators = []
    for i in range(N_OPERATORS):
        tier = TIERS[int(rng.choice(3, p=TIER_DIST))]
        capable = [j for j, (_, mt, _) in enumerate(MODELS_DEF) if can_run(tier, mt)]
        current = int(rng.choice(capable))
        lr = float(deviator_L) if (deviator_L is not None and i == 0) else float(pop_L)
        operators.append(Operator(id=i, tier=tier, current_model=current, lying_rate=lr))

    base_demand = np.array([d for _, _, d in MODELS_DEF])
    eps = 1e-3
    efficiency_history = []
    op0_ev_history = []

    for epoch in range(n_epochs):
        demand = demand_at(epoch, base_demand, rng)
        supply = np.zeros(N_MODELS)
        for op in operators:
            supply[op.current_model] += op.throughput()

        # Iterated cheap-talk (async, matches sim.py)
        plans = {op.id: op.current_model for op in operators}
        rep_weights = {op.id: reputation_weight(op.truth_rate(epoch), threshold, smoothness)
                       for op in operators}
        N_ITERS = 10
        FRACTION_UPDATE = 0.3
        for _iter in range(N_ITERS):
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

        # Per-op EV this epoch (using current supply, before switches)
        per_op_ev = {op.id: ev_per_op(op.throughput(), op.current_model, supply, demand)
                     for op in operators}

        # Apply switches + announcements
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

        # Metrics
        captured = float(sum(demand[m] * WSF[m] for m in range(N_MODELS) if supply[m] > 0))
        total = float(np.sum(demand * WSF))
        efficiency_history.append(captured / max(total, eps))
        op0_ev_history.append(per_op_ev[0])

    return {
        'mean_efficiency': float(np.mean(efficiency_history[WARMUP_EPOCHS:])),
        'op0_mean_ev': float(np.mean(op0_ev_history[WARMUP_EPOCHS:])),
        'operators': operators,
    }


def main():
    print(f"=== Sweep 1: Population-wide L scan per (threshold, smoothness) ===\n")
    # eff[threshold_i, smoothness_j, L_k] = population efficiency
    eff = np.zeros((len(THRESHOLDS), len(SMOOTHNESSES), len(LYING_RATES)))
    for ti, t in enumerate(THRESHOLDS):
        for si, s in enumerate(SMOOTHNESSES):
            for li, L in enumerate(LYING_RATES):
                vals = []
                for seed in SEEDS:
                    r = run_fixed_L(t, s, L, rng_seed=seed)
                    vals.append(r['mean_efficiency'])
                eff[ti, si, li] = float(np.mean(vals))
            best_li = int(np.argmax(eff[ti, si]))
            print(f"  threshold={t:.2f} smoothness={s:.2f} -> "
                  f"L* (pop-optimal)={LYING_RATES[best_li]:.2f} "
                  f"eff={eff[ti, si, best_li]:.4f} | "
                  f"sweep over L: {[f'{x:.3f}' for x in eff[ti, si]]}")

    # Plot heatmap of pop-optimal L per (threshold, smoothness)
    fig, ax = plt.subplots(1, 1, figsize=(10, 6))
    pop_optimal_L = np.array([[LYING_RATES[int(np.argmax(eff[ti, si]))]
                               for si in range(len(SMOOTHNESSES))]
                              for ti in range(len(THRESHOLDS))])
    im = ax.imshow(pop_optimal_L, aspect='auto', cmap='YlOrRd', origin='lower',
                   vmin=0, vmax=0.5)
    ax.set_xticks(range(len(SMOOTHNESSES)))
    ax.set_xticklabels([f'{s:.2f}' for s in SMOOTHNESSES])
    ax.set_yticks(range(len(THRESHOLDS)))
    ax.set_yticklabels([f'{t:.2f}' for t in THRESHOLDS])
    ax.set_xlabel('smoothness')
    ax.set_ylabel('threshold')
    ax.set_title('Population-optimal L (maximizes market efficiency)')
    for i in range(len(THRESHOLDS)):
        for j in range(len(SMOOTHNESSES)):
            best_eff = eff[i, j].max()
            ax.text(j, i, f'L*={pop_optimal_L[i,j]:.2f}\neff={best_eff:.3f}',
                    ha='center', va='center', color='black', fontsize=9)
    plt.colorbar(im, ax=ax, label='L*')
    plt.tight_layout()
    plt.savefig('pop_optimal_L.png', dpi=110, bbox_inches='tight')
    print(f"\nSaved pop_optimal_L.png")

    # Plot efficiency vs L curves at a few selected (threshold, smoothness) points
    fig, ax = plt.subplots(1, 1, figsize=(10, 6))
    # Show smoothness=0.10 only, all thresholds
    si = 1  # smoothness=0.10
    s = SMOOTHNESSES[si]
    for ti, t in enumerate(THRESHOLDS):
        ax.plot(LYING_RATES, eff[ti, si, :], marker='o',
                label=f'threshold={t:.2f}')
    ax.set_xlabel('Population lying rate L')
    ax.set_ylabel('Market efficiency')
    ax.set_title(f'Market efficiency vs population L (smoothness={s})')
    ax.legend()
    ax.grid(True, alpha=0.3)
    plt.tight_layout()
    plt.savefig('eff_vs_L.png', dpi=110, bbox_inches='tight')
    print(f"Saved eff_vs_L.png")

    # === Sweep 2: Nash analysis ===
    # For a fixed best (threshold, smoothness), is the pop-optimal L a Nash equilibrium?
    print(f"\n=== Sweep 2: Nash analysis ===")
    # Use best config (highest peak efficiency)
    best_idx = np.unravel_index(np.argmax(eff.max(axis=2)), eff.max(axis=2).shape)
    best_t = THRESHOLDS[best_idx[0]]
    best_s = SMOOTHNESSES[best_idx[1]]
    best_pop_L = LYING_RATES[int(np.argmax(eff[best_idx[0], best_idx[1]]))]
    print(f"\nBest config: threshold={best_t}, smoothness={best_s}, "
          f"pop-optimal L={best_pop_L}")

    # Single operator deviates while population stays at L*
    print(f"\nDeviation analysis (population L={best_pop_L}):")
    print(f"  deviator_L | op0_mean_ev | rel_vs_no_deviation")
    base_dev_ev = []
    for seed in SEEDS:
        r = run_fixed_L(best_t, best_s, best_pop_L, deviator_L=best_pop_L, rng_seed=seed)
        base_dev_ev.append(r['op0_mean_ev'])
    baseline_ev = float(np.mean(base_dev_ev))

    deviator_results = []
    for dL in LYING_RATES:
        vals = []
        for seed in SEEDS:
            r = run_fixed_L(best_t, best_s, best_pop_L, deviator_L=dL, rng_seed=seed)
            vals.append(r['op0_mean_ev'])
        dev_ev = float(np.mean(vals))
        rel = (dev_ev - baseline_ev) / max(abs(baseline_ev), 1e-6)
        deviator_results.append((dL, dev_ev, rel))
        marker = '  <-- BEST' if dev_ev == max(v[1] for v in deviator_results) else ''
        print(f"     {dL:.2f}     | {dev_ev:>10.3f} | {rel:+.4f}{marker}")

    # Plot deviator EV
    fig, ax = plt.subplots(1, 1, figsize=(10, 6))
    dL_arr = np.array([v[0] for v in deviator_results])
    dev_ev_arr = np.array([v[1] for v in deviator_results])
    ax.plot(dL_arr, dev_ev_arr, marker='o', color='steelblue', linewidth=2)
    ax.axvline(best_pop_L, color='red', linestyle='--',
               label=f'Population L = {best_pop_L}')
    nash_idx = int(np.argmax(dev_ev_arr))
    ax.axvline(dL_arr[nash_idx], color='green', linestyle=':',
               label=f'Best deviation L = {dL_arr[nash_idx]:.2f}')
    ax.set_xlabel('Deviator (operator 0) lying rate')
    ax.set_ylabel('Mean EV per epoch')
    ax.set_title(f'Single-operator Nash deviation\n'
                 f'(threshold={best_t}, smoothness={best_s}, '
                 f'population L={best_pop_L})')
    ax.legend()
    ax.grid(True, alpha=0.3)
    plt.tight_layout()
    plt.savefig('nash_dev.png', dpi=110, bbox_inches='tight')
    print(f"\nSaved nash_dev.png")
    print(f"  Nash analysis: population L={best_pop_L}, best deviation L={dL_arr[nash_idx]:.2f}")
    if dL_arr[nash_idx] == best_pop_L:
        print(f"  -> Population L is a Nash equilibrium (no profitable deviation)")
    else:
        print(f"  -> Population L is NOT Nash; operators would prefer L={dL_arr[nash_idx]:.2f}")


if __name__ == '__main__':
    main()
