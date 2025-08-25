import hashlib
import torch
import numpy as np
from tqdm.auto import tqdm
from pow.random import get_rng

def initialize_model_with_pool(
    model: torch.nn.Module,
    hash_: str,
    dtype: torch.dtype = torch.float16,
    pool_fraction: float = 0.01,
) -> None:
    """Fast deterministic model initialization for PoC scenarios (target: <30s for 18B models).
    
    Optimized reimplementation of `initialize_model_weights_from_rng` that generates a small
    pool of random values and uses deterministic patterns to fill all model weights.
    Avoids generating billions of random numbers by reusing a small pool with tiling.

    Args:
        model: The PyTorch model to initialize (CPU-only).
        hash_: Hash string used for deterministic initialization.
        dtype: The target data type for the model's parameters.
        pool_fraction: The fraction of total parameters to generate for the random pool.
    """
    rng = get_rng(hash_, 4)
    
    total_params = 0
    param_info = []
    for name, param in model.named_parameters():
        param_info.append((name, param, param.numel()))
        total_params += param.numel()

    # Create a small pool of random values on the CPU.
    pool_size = max(50000, int(total_params * pool_fraction))
    pool_values = rng.normal(0.0, 0.02, size=pool_size).astype(np.float32)
    pool_tensor = torch.from_numpy(pool_values).to(dtype=dtype)

    with torch.no_grad():
        for name, param, param_size in tqdm(param_info, desc="Fast Model Initialization"):
            # Combine hash_ and parameter name for deterministic, unique starting point
            combined_hash_input = f"{hash_}_{name}"
            name_hash = int(hashlib.sha256(combined_hash_input.encode('utf-8')).hexdigest()[:8], 16)

            if param_size <= pool_size:
                start_idx = name_hash % (pool_size - param_size + 1)
                values = pool_tensor[start_idx : start_idx + param_size]
            else:
                full_tiles = param_size // pool_size
                remainder = param_size % pool_size

                tiled = pool_tensor.repeat(full_tiles)
                if remainder > 0:
                    partial = pool_tensor[:remainder]
                    values = torch.cat([tiled, partial])
                else:
                    values = tiled

                shift = name_hash % param_size
                if shift > 0:
                    values = torch.cat([values[shift:], values[:shift]])

            param.copy_(values.view(param.shape))

