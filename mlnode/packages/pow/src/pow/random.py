import hashlib
from typing import List

import numpy as np
import torch
from numpy.random import SeedSequence, default_rng
from tqdm.auto import tqdm


def get_extended_entropy(
    seed_string: str,
    num_hashes: int
) -> np.ndarray:
    entropy_list = []
    for i in range(num_hashes):
        modified_string = f"{seed_string}_{i}"
        hash_digest = hashlib.sha256(modified_string.encode('utf-8')).digest()
        entropy = np.frombuffer(hash_digest, dtype=np.uint32)
        entropy_list.append(entropy)
    extended_entropy = np.concatenate(entropy_list)
    return extended_entropy


def get_rng(
    seed_string: str,
    num_hashes: int = 4
):
    entropy = get_extended_entropy(seed_string, num_hashes)
    seed_seq = SeedSequence(entropy)
    return default_rng(seed_seq)


def get_random_emb(
    seed_string: str,
    batch_size: int,
    seq_len: int,
    dim: int,
    num_hashes: int = 4,
) -> np.ndarray:
    rng = get_rng(seed_string, num_hashes)
    embeddings = rng.standard_normal((batch_size, seq_len, dim))
    return embeddings


def get_uniform_vector_on_sphere(
    rng: np.random.Generator,
    dim: int,
    batch_size: int = 1,
) -> np.ndarray:
    """
    Generate a batch of vectors uniformly distributed on the surface of a sphere in R^d.

    Parameters:
    - rng: numpy random generator
    - dim: dimension of the space
    - batch_size: number of vectors to generate

    Returns:
    - A numpy array of shape (batch_size, dim) with vectors on the sphere.
    """
    y = rng.standard_normal((batch_size, dim))  # Random vector in R^d
    y /= np.linalg.norm(y, axis=1, keepdims=True)  # Normalize to unit length
    return y


def meets_required_zeros(
    bytes: bytes,
    min_leading_zeros: int
) -> bool:
    total_bits = len(bytes) * 8
    target = (1 << (total_bits - min_leading_zeros)) - 1
    hash_int = int.from_bytes(bytes, byteorder='big')
    
    return hash_int <= target


def initialize_model_weights_from_rng(
    model: torch.nn.Module,
    rng: np.random.Generator,
    dtype: torch.dtype = torch.float16,
) -> None:
    for _, param in tqdm(model.named_parameters()):
        param_shape = param.shape
        random_values = rng.normal(
            loc=0.0,
            scale=0.02,
            size=param_shape
        )
        random_tensor = torch.tensor(random_values, dtype=dtype)
        random_tensor = random_tensor.to(param.device)
        
        with torch.no_grad():
            param.copy_(random_tensor)


def get_input(
    hash_str: str,
    public_key: str,
    nonce: str,
    batch_size: int = 256,
    seq_len: int = 16,
    dim: int = 4096,
    dtype: torch.dtype = torch.float16,
    device: str = "cpu",
):
    """
    Generate a random embedding for the model.
    WARNING: this function generate all batch from 1 nonce.
    """
    emb = get_random_emb(
        seed_string=f"{hash_str}_{public_key}_nonce{nonce}",
        batch_size=batch_size,
        seq_len=seq_len,
        dim=dim
    )
    emb = torch.tensor(emb, dtype=dtype)
    emb = emb.to(device)
    return emb


def get_inputs(
    hash_str: str,
    public_key: str,
    nonces: List[str],
    seq_len: int = 16,
    dim: int = 4096,
    dtype: torch.dtype = torch.float16,
):
    """
    Generate a random embedding for the model.
    This function generates 1 vector for each nonce.
    """
    embeddings = []
    for nonce in nonces:
        embeddings.append(
            get_random_emb(
                seed_string=f"{hash_str}_{public_key}_nonce{nonce}",
                batch_size=1,
                seq_len=seq_len,
                dim=dim
            )
        )

    embeddings = torch.tensor(
        np.concatenate(embeddings),
        dtype=dtype
    )

    return embeddings


def get_permutations(
    hash_str: str,
    public_key: str,
    nonces: List[str],
    dim: int = 4096,
):
    permutations = []
    for nonce in nonces:
        rng = get_rng(f"{hash_str}_{public_key}_nonce_{nonce}_permutations", 1)
        permutations.append(
            rng.permutation(dim)
        )
    return np.array(permutations)


def get_target(     
    hash_str: str,
    vocab_size: int
):
    rng = get_rng(f"{hash_str}_target", 1)
    target = get_uniform_vector_on_sphere(
        rng,
        dim=vocab_size,
        batch_size=1
    )[0]
    return target
