import numpy as np
from torch.utils.data import IterableDataset
from torch.distributed.checkpoint.stateful import Stateful


def get_all_rotations(n: int) -> np.ndarray:
    """Returns all possible rotations of a sequence of integers from 0 to n-1.
    
    Args:
        n: Length of sequence to generate rotations for
        
    Returns:
        np.ndarray: Array of shape (n, n) containing all rotations
        
    Example:
        >>> get_all_rotations(4)
        array([[1, 2, 3, 4],
               [2, 3, 4, 1], 
               [3, 4, 1, 2],
               [4, 1, 2, 3]])
    """
    lst = [i for i in range(n)]
    rotations = []
    
    for i in range(n):
        # Create new rotation starting from index i
        rotation = lst[i:] + lst[:i]
        rotations.append(rotation)
        
    return np.array(rotations)

def get_indexings(n: int, world_size: int) -> list[np.ndarray]:
    """Generate distributed data indexings for parallel processing.
    
    Args:
        n: Total number of data samples
        world_size: Number of parallel workers
        
    Returns:
        list[np.ndarray]: List of length world_size containing indexing arrays,
            where each array represents the data indices for one worker
    """
    parts = []
    n_even = n - n % world_size
    for i in np.arange(world_size):
        parts.append(np.arange(n_even)[i::world_size])
    parts = np.stack(parts)
    rotations = get_all_rotations(world_size)
    indexings = [np.concatenate(parts[rotations[i]]) for i in range(world_size)]
    return indexings


class SplitIterableDataset(IterableDataset, Stateful):
    def __init__(self, dataset, world_size: int, rank: int):
        self.dataset = dataset
        self.world_size = world_size
        self.rank = rank

    def __iter__(self):
        # Yield items where index % world_size == rank
        for idx, item in enumerate(self.dataset):
            if idx % self.world_size == self.rank:
                yield item

    def state_dict(self):
        # Delegate state_dict to the underlying dataset
        return self.dataset.state_dict()

    def load_state_dict(self, state_dict):
        # Delegate load_state_dict to the underlying dataset
        self.dataset.load_state_dict(state_dict)