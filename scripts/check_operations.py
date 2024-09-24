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

torch.set_printoptions(precision=10)
torch.use_deterministic_algorithms(True)
torch.backends.cudnn.benchmark = False

difficulty = 0
public_key = '1'
hidden_size = 5

a = np.arange(hidden_size)[:, None] @ np.arange(hidden_size)[None, :] + np.eye(hidden_size)
a = torch.FloatTensor(a) / hidden_size


for d in ['cpu', 'cuda:0']:
    print(d)
    device = torch.device(d)
    pf = Compute('1', device, hid=hidden_size)
    out = pf.forward(a.to(device))
    h = pf.get_hash(out)
    print(out)
    print(h)
