"""Simulate from the epoch-424 inventory and existing dynamic coefficients."""

import argparse
import csv
import json
import random
from collections import Counter
from fractions import Fraction
from pathlib import Path

from simulation import f, get_effective_coeff

HERE = Path(__file__).resolve().parent
SNAPSHOT = HERE.parent / 'data' / 'epoch424'
MODELS = ('MiniMax', 'GLM', 'DeepSeek')
IDS = {
    'MiniMaxAI/MiniMax-M2.7': 'MiniMax',
    'zai-org/GLM-5.3-Flash': 'GLM',
    'deepseek-ai/DeepSeek-V4-Flash-0731': 'DeepSeek',
}
GPUS = ('H100', 'H200', 'B200', 'B300')
PARAMS = {m: dict(T_i=t) for m, t in zip(MODELS, (0.3334, 0.3333, 0.3333))}
INITIAL = {}
INITIAL_STATE = {}
EPSILON = 0.01
MAX_PASSES = 1000
SEED = 0


def read_csv(name):
    with (SNAPSHOT / name).open(newline='') as source:
        return list(csv.DictReader(source))


def inputs():
    with (HERE.parent / 'data' / 'mlnode-4.0.0-prefill.csv').open(newline='') as source:
        measurements = list(csv.DictReader(source))
    reference = {}
    for row in measurements:
        if row["gpu_type"] == "H100":
            m = "MiniMax" if "MiniMax" in row["model"] else "GLM" if "GLM" in row["model"] else "DeepSeek"
            reference[m] = max(reference.get(m, 0), int(row["nonces_per_min_8gpu"]))
    with (HERE.parent / "data" / "mlnode-4.0.0-max-8gpu.csv").open(newline="") as source:
        measurements = list(csv.DictReader(source))
    current_bounds = {}
    for row in read_csv('models.csv'):
        m = IDS[row['model_id']]
        current_bounds[m] = (Fraction(row['coeff_min']), Fraction(row['coeff_max']))
        INITIAL[m] = float(row['base_coefficient'])
        INITIAL_STATE[m] = dict(s=float(row['adaptive_step']), prev_sign=int(row['prev_sign']))
    benchmarks = {}
    for row in measurements:
        label = row['model']
        model = 'MiniMax' if 'MiniMax' in label else 'GLM' if 'GLM' in label else 'DeepSeek'
        benchmarks.setdefault((model, row['gpu_type']), []).append(int(row['nonces_per_min_8gpu']))
    # Evaluate exact rational formulas, then truncate once to 12 decimal places.
    q = {m: {g: Fraction(max(benchmarks[m, g])) for g in GPUS} for m in MODELS}
    base = Fraction(str(INITIAL['MiniMax']))
    for m in MODELS:
        parity = {g: q['MiniMax'][g] / q[m][g] for g in GPUS}
        # Retain the proposal difficulties from the original measurements.
        values = dict(D_i=Fraction(reference['MiniMax'], reference[m]),
                      coeff_i_min=base if m == 'MiniMax' else base * min(parity.values()) / Fraction('1.05'),
                      coeff_i_max=base if m == 'MiniMax' else base * max(parity.values()) * Fraction('1.05'))
        # Preserve the full existing range so activation does not force a clamp.
        values['coeff_i_min'] = min(values['coeff_i_min'], current_bounds[m][0])
        values['coeff_i_max'] = max(values['coeff_i_max'], current_bounds[m][1])
        for key, value in values.items():
            PARAMS[m][key] = (value.numerator * 10**12 // value.denominator) / 10**12
    nodes, excluded = [], Counter()
    for row in read_csv('nodes.csv'):
        gpu = next((g for g in GPUS if g in row['gpu_type']), None)
        count = int(row['gpu_count'])
        if gpu is None:
            excluded[row['gpu_type']] += count
            continue
        rates = {m: max(benchmarks[m, gpu]) * count / 8 for m in MODELS}
        nodes.append(dict(host=row['participant'], node=row['node_id'], gpu=gpu, count=count,
                          initial_model=IDS[row['model_id']], rates=rates))
    nodes.sort(key=lambda n: (n['host'], n['node']))
    assert len({(n['host'], n['node']) for n in nodes}) == len(nodes)
    assert sum(n['count'] for n in nodes) == 700
    assert sum(excluded.values()) == 156
    return nodes, dict(excluded)


def totals(nodes, allocation):
    result = dict.fromkeys(MODELS, 0)
    for node, model in zip(nodes, allocation):
        result[model] += node['rates'][model]
    return result


def shares(raw):
    normalized = {m: raw[m] * PARAMS[m]['D_i'] for m in MODELS}
    total = sum(normalized.values())
    return {m: normalized[m] / total for m in MODELS}


def settle(nodes, allocation, coeff, rng):
    raw = totals(nodes, allocation)
    host_raw = {n['host']: dict.fromkeys(MODELS, 0) for n in nodes}
    for n, m in zip(nodes, allocation):
        host_raw[n['host']][m] += n['rates'][m]
    switches = 0
    for pass_number in range(1, MAX_PASSES + 1):
        moved = False
        order = list(range(len(nodes)))
        rng.shuffle(order)
        for index in order:
            node = nodes[index]
            current = allocation[index]
            own = host_raw[node['host']]
            effective = get_effective_coeff(shares(raw), coeff, PARAMS)
            network_weight = sum(raw[m] * effective[m] for m in MODELS)
            share_before = sum(own[m] * effective[m] for m in MODELS) / network_weight
            # Switching cost equals 1% of this node's current network-weight share.
            cost = EPSILON * node['rates'][current] * effective[current] / network_weight
            best, best_gain = current, cost
            for candidate in MODELS:
                if candidate == current or node['rates'][candidate] == 0:
                    continue
                trial = raw.copy()
                trial[current] -= node['rates'][current]
                trial[candidate] += node['rates'][candidate]
                trial_eff = get_effective_coeff(shares(trial), coeff, PARAMS)
                own_weight = sum(own[m] * trial_eff[m] for m in MODELS)
                own_weight -= node['rates'][current] * trial_eff[current]
                own_weight += node['rates'][candidate] * trial_eff[candidate]
                share_after = own_weight / sum(trial[m] * trial_eff[m] for m in MODELS)
                gain = share_after - share_before
                if gain > best_gain + 1e-15:
                    best, best_gain = candidate, gain
            if best != current:
                raw[current] -= node['rates'][current]
                raw[best] += node['rates'][best]
                own[current] -= node['rates'][current]
                own[best] += node['rates'][best]
                allocation[index] = best
                switches += 1
                moved = True
        if not moved:
            return pass_number, switches
    raise RuntimeError(f'Allocation did not settle in {MAX_PASSES} passes')


def record(epoch, nodes, allocation, coeff, state, passes, switches):
    raw = totals(nodes, allocation)
    share = shares(raw)
    effective = coeff.copy() if epoch == 0 else get_effective_coeff(share, coeff, PARAMS)
    hardware = {m: dict.fromkeys(GPUS, 0) for m in MODELS}
    gpu_weight = dict.fromkeys(GPUS, 0)
    gpu_count = dict.fromkeys(GPUS, 0)
    for node, model in zip(nodes, allocation):
        hardware[model][node['gpu']] += node['count']
        gpu_weight[node['gpu']] += node['rates'][model] * effective[model]
        gpu_count[node['gpu']] += node['count']
    reference = 8 * gpu_weight['H100'] / gpu_count['H100']
    relative = {g: (8 * gpu_weight[g] / gpu_count[g]) / reference for g in GPUS}
    assert sum(sum(v.values()) for v in hardware.values()) == 700
    assert abs(sum(share.values()) - 1) < 1e-12
    assert epoch == 0 or all(PARAMS[m]['coeff_i_min'] - 1e-12 <= effective[m] <= coeff[m] + 1e-12
               and coeff[m] <= PARAMS[m]['coeff_i_max'] + 1e-12
               for m in MODELS)
    return dict(epoch=epoch, base_coefficients=coeff.copy(), effective_coefficients=effective,
                normalized_shares=share, gpu_allocation=hardware,
                poc_weight_per_8gpu_relative_to_8h100=relative, allocation_passes=passes,
                switches=switches, controller_state={m: s.copy() for m, s in state.items()})


def run():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target-bps', nargs=3, type=int, default=[3334, 3333, 3333],
                        metavar=('MINIMAX', 'GLM', 'DEEPSEEK'))
    parser.add_argument('--output', type=Path, default=HERE / 'epoch424-results.json')
    parser.add_argument('--epochs', type=int, default=50, help='number of epochs (default: 50)')
    args = parser.parse_args()
    if args.epochs < 1:
        parser.error('epochs must be positive')
    if any(t < 0 for t in args.target_bps) or sum(args.target_bps) != 10000:
        parser.error('target basis points must be nonnegative and total 10000')
    for model, target in zip(MODELS, args.target_bps):
        PARAMS[model]['T_i'] = target / 10000

    nodes, excluded = inputs()
    rng = random.Random(SEED)
    allocation = [n['initial_model'] for n in nodes]
    coeff = INITIAL.copy()
    state = {m: INITIAL_STATE[m].copy() for m in MODELS}
    epochs = [record(0, nodes, allocation, coeff, state, 0, 0)]
    for epoch in range(1, args.epochs + 1):
        coeff = {m: min(max(coeff[m], PARAMS[m]['coeff_i_min']), PARAMS[m]['coeff_i_max'])
                 for m in MODELS}
        coeff = f(shares(totals(nodes, allocation)), coeff, PARAMS, 0.05, state)
        passes, switches = settle(nodes, allocation, coeff, rng)
        epochs.append(record(epoch, nodes, allocation, coeff, state, passes, switches))
    result = dict(seed=SEED, epsilon=EPSILON, max_passes=MAX_PASSES,
                  params=PARAMS, excluded_gpus=excluded, epochs=epochs)
    result['measurement'] = 'mlnode-4.0.0-max-8gpu: MAX 8xGPUs nonces/min'
    result['snapshot_epoch'] = 424
    path = args.output
    path.write_text(json.dumps(result, indent=2) + '\n')
    print(path)
    for r in epochs:
        print(r['epoch'], 'coeff', {m: round(v, 6) for m, v in r['effective_coefficients'].items()},
              'shares', {m: round(v, 4) for m, v in r['normalized_shares'].items()},
              'passes', r['allocation_passes'], 'switches', r['switches'])


if __name__ == '__main__':
    run()
