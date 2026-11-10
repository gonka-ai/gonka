"""Simulate 20 epochs from the epoch-403 inventory and static coefficients."""

import csv
import json
import random
from collections import Counter
from pathlib import Path

from simulation import f, get_effective_coeff

HERE = Path(__file__).resolve().parent
SNAPSHOT = HERE.parent / 'data' / 'snapshot-2026-09-23'
MODELS = ('MiniMax', 'GLM', 'DeepSeek')
IDS = {
    'MiniMaxAI/MiniMax-M2.7': 'MiniMax',
    'zai-org/GLM-5.3-Flash': 'GLM',
    'deepseek-ai/DeepSeek-V4-Flash-0731': 'DeepSeek',
}
GPUS = ('H100', 'H200', 'B200', 'B300')
PARAMS = {
    'MiniMax': dict(coeff_i_min=0.3024, coeff_i_max=0.3024, D_i=1, T_i=0.3334),
    'GLM': dict(coeff_i_min=0.508468965517, coeff_i_max=0.847197025352,
                D_i=2.668169014084, T_i=0.3333),
    'DeepSeek': dict(coeff_i_min=0.183272727272, coeff_i_max=0.48951,
                     D_i=1.541666666666, T_i=0.3333),
}
INITIAL = {'MiniMax': 0.3024, 'GLM': 0.62, 'DeepSeek': 0.246}
EPSILON = 0.01
MAX_PASSES = 1000
SEED = 0


def read_csv(name):
    with (SNAPSHOT / name).open(newline='') as source:
        return list(csv.DictReader(source))


def inputs():
    benchmarks = {}
    for row in read_csv('benchmarks_8gpu.csv'):
        label = row['model']
        model = 'MiniMax' if 'MiniMax' in label else 'GLM' if 'GLM' in label else 'DeepSeek'
        benchmarks.setdefault((model, row['gpu_type']), []).append(
            (int(row['tensor_parallel_size']), int(row['nonces_per_min'])))
    assignments = {(r['participant'], r['node_id']): IDS[r['model_id']]
                   for r in read_csv('chain_poc_nodes.csv')}
    nodes, excluded = [], Counter()
    for row in read_csv('chain_node_hardware.csv'):
        key = row['participant'], row['local_id']
        if key not in assignments:
            continue
        gpu = next((g for g in GPUS if g in row['gpu_type']), None)
        count = int(row['gpu_count'])
        if gpu is None:
            excluded[row['gpu_type']] += count
            continue
        # Each measured TP group is one replica. Spare GPUs remain assigned
        # to the node but idle when they cannot fit another replica.
        rates = {m: max((count // tp) * rate for tp, rate in benchmarks[m, gpu])
                 for m in MODELS}
        assert rates[assignments[key]] > 0
        nodes.append(dict(host=key[0], node=key[1], gpu=gpu, count=count,
                          initial_model=assignments[key], rates=rates))
    nodes.sort(key=lambda n: (n['host'], n['node']))
    assert len({(n['host'], n['node']) for n in nodes}) == len(nodes)
    assert sum(n['count'] for n in nodes) == 609
    assert sum(excluded.values()) == 64
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
            reward_before = sum(own[m] * effective[m] for m in MODELS) / network_weight
            # Switching cost equals 1% of this node's current epoch reward.
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
                reward_after = own_weight / sum(trial[m] * trial_eff[m] for m in MODELS)
                gain = reward_after - reward_before
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
    assert sum(sum(v.values()) for v in hardware.values()) == 609
    assert abs(sum(share.values()) - 1) < 1e-12
    assert all(PARAMS[m]['coeff_i_min'] <= effective[m] <= coeff[m] <= PARAMS[m]['coeff_i_max']
               for m in MODELS)
    return dict(epoch=epoch, base_coefficients=coeff.copy(), effective_coefficients=effective,
                normalized_shares=share, gpu_allocation=hardware,
                gnk_per_8gpu_relative_to_8h100=relative, allocation_passes=passes,
                switches=switches, controller_state={m: s.copy() for m, s in state.items()})


def run():
    nodes, excluded = inputs()
    rng = random.Random(SEED)
    allocation = [n['initial_model'] for n in nodes]
    coeff = INITIAL.copy()
    state = {m: dict(s=0.025, prev_sign=0) for m in MODELS}
    epochs = [record(0, nodes, allocation, coeff, state, 0, 0)]
    for epoch in range(1, 21):
        coeff = f(shares(totals(nodes, allocation)), coeff, PARAMS, 0.05, state)
        passes, switches = settle(nodes, allocation, coeff, rng)
        epochs.append(record(epoch, nodes, allocation, coeff, state, passes, switches))
    result = dict(seed=SEED, epsilon=EPSILON, max_passes=MAX_PASSES,
                  params=PARAMS, excluded_gpus=excluded, epochs=epochs)
    path = HERE / 'epoch403-results.json'
    path.write_text(json.dumps(result, indent=2) + '\n')
    print(path)
    for r in epochs:
        print(r['epoch'], 'coeff', {m: round(v, 6) for m, v in r['effective_coefficients'].items()},
              'shares', {m: round(v, 4) for m, v in r['normalized_shares'].items()},
              'passes', r['allocation_passes'], 'switches', r['switches'])


if __name__ == '__main__':
    run()
