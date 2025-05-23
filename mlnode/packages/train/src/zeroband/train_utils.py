import torch

from zeroband.config import Config
from zeroband.utils import WorldInfo


def get_denominator(micro_batches):
    n_items = 0
    for mb in micro_batches:
        n_items += torch.sum(mb['labels'] != -100)
    return n_items


def set_random_seed(seed: int):
    """
    Set the random seed for all relevant libraries to ensure reproducibility.
    """
    import os
    import random
    import numpy as np
    import torch

    torch.manual_seed(seed)
    torch.cuda.manual_seed(seed)
    torch.cuda.manual_seed_all(seed)  # If using multi-GPU.
    np.random.seed(seed)
    random.seed(seed)

    # Ensure that all operations are deterministic on GPU.
    # torch.backends.cudnn.deterministic = True
    # torch.backends.cudnn.benchmark = False

    # For newer versions of PyTorch, you might need to set the hash seed as well.
    os.environ["PYTHONHASHSEED"] = str(seed)
    
    
def derive_params(config: Config, world_info: WorldInfo):
    total_world_size = world_info.global_world_size * world_info.local_world_size
    batch_size = config.optim.batch_size // world_info.local_world_size
    assert (batch_size % config.train.micro_bs == 0
            ), "batch size must be a multiple of micro batch size"
    gradient_accumulation_steps = batch_size // config.train.micro_bs
    
    if config.ckpt is not None and config.ckpt.interval is not None and config.diloco is not None:
        assert (
                config.ckpt.interval % config.diloco.inner_steps == 0
            ), "ckpt interval must be a multiple of diloco inner steps"
    return total_world_size, batch_size, gradient_accumulation_steps