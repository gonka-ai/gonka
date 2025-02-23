import os
os.environ["CUBLAS_WORKSPACE_CONFIG"] = ":4096:8"

import numpy as np
import hashlib
import time
import torch
from tqdm.notebook import tqdm
import sys
sys.path.append('../src')
from pow.compute.pipeline import Pipeline
from pow.compute.compute import AttentionModel, Compute

torch.backends.cudnn.deterministic = True
torch.backends.cudnn.benchmark = False

difficulty = 0
public_key = '1'
hidden_size = 1000
race_duration = 20


def get_score(hidden_size, race_duration, difficulty, device):
    pf = Compute('1', device, hid=hidden_size)
    pipeline = Pipeline(public_key, pf, difficulty)
    pipeline.race(race_duration)
    return len(pipeline.proof)

results = {'gpu':[], 'cpu':[]}
hs = [10, 50, 100, 200, 300, 500, 700, 1000, 1500, 2000]
for h in hs:
    cpu_power = get_score(h, race_duration, difficulty, torch.device('cpu'))
    gpu_power = get_score(h, race_duration, difficulty, torch.device('cuda'))
    print(f'Hidden size = {h}, CPU hashes = {cpu_power}, GPU_hashes = {gpu_power}')
    results['gpu'].append(gpu_power)
    results['cpu'].append(cpu_power)

print(hs)
print(results)