import numpy as np
import torch

torch.set_printoptions(precision=10)
seeds = [1, 42, 2024]
print(f"numpy version = {np.version.version}")
print(f"CUDA version = {torch.version.cuda}")
print('\n')
#Numpy check
print('Numpy RNG')
for seed in seeds:
    numpy_rng = np.random.default_rng(seed)
    numbers = numpy_rng.random(size=5)
    print(f"seed = {seed},\t {numbers}")
print('\n')

#Torch check
device_strs = ['cpu'] + [f'cuda:{d}' for d in np.arange(0, torch.cuda.device_count())]
for d in device_strs:
    print(f'Torch {d} RNG')
    for seed in seeds:
        device = torch.device(d)
        torch_rng = torch.Generator(device=device)
        torch_rng.manual_seed(seed)
        numbers = torch.randn(size=(5,), generator=torch_rng, device=torch_rng.device)
        print(f"seed = {seed},\t {numbers}")
    print('\n')
