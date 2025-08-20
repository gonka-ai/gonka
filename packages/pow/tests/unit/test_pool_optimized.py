"""
Minimal test for pool-optimized weight generation.
"""

import time
import torch
from pow.random import get_rng, initialize_model_weights_from_rng
from pow.random_pool_optimized import (
    initialize_model_with_pool
)
from pow.random import get_rng
from pow.models.utils import Params, count_params
from pow.models.llama31 import ModelArgs, Transformer


def test_pool_optimized_performance():
    """Test pool-optimized weight generation performance."""
    # Create test model
    params = Params(
        dim=1024,
        n_layers=4,
        n_heads=32,
        n_kv_heads=32,
        vocab_size=8196,
        ffn_dim_multiplier=4.0,
        multiple_of=1024,
        seq_len=128
    )
    
    model_args = ModelArgs(
        max_seq_len=128,
        max_batch_size=1,
        flash=False,
        **(params.__dict__)
    )
    
    torch.set_default_device("cpu")
    model = Transformer(model_args)
    model.eval()
    model.requires_grad_(False)
    model = model.half()
    
    param_count = count_params(model, print_summary=False)
    print(f"Test model parameters: {param_count:,} ({param_count/1e6:.1f}M)")
    
    # Test original method
    print("\n=== Original Method ===")
    model_orig = Transformer(model_args)
    model_orig.eval().requires_grad_(False).half()
    
    rng_orig = get_rng("test_original", 4)
    start_time = time.time()
    initialize_model_weights_from_rng(model_orig, rng_orig, dtype=torch.float16)
    orig_time = time.time() - start_time
    
    print(f"Original time: {orig_time:.2f}s ({param_count/orig_time:,.0f} params/sec)")
    
    # Test pool-optimized method
    print("\n=== Pool-Optimized Method ===")
    model_pool = Transformer(model_args)
    model_pool.eval().requires_grad_(False).half()
    
    start_time = time.time()
    initialize_model_with_pool(
        model_pool, "test_pool", dtype=torch.float16
    )
    pool_time = time.time() - start_time
    
    print(f"Pool-optimized time: {pool_time:.2f}s ({param_count/pool_time:,.0f} params/sec)")
    
    # Calculate improvement and extrapolation
    improvement = orig_time / pool_time
    print(f"\n=== Results ===")
    print(f"Improvement: {improvement:.1f}x faster")
    
    # Extrapolate to 26.88B parameters
    full_model_params = 26.88e9
    orig_full_time = (full_model_params / param_count) * orig_time
    pool_full_time = (full_model_params / param_count) * pool_time
    
    print(f"Original method for 26.88B model: {orig_full_time:.1f}s ({orig_full_time/60:.1f} min)")
    print(f"Pool-optimized for 26.88B model: {pool_full_time:.1f}s ({pool_full_time/60:.1f} min)")
    
    if pool_full_time < 60.0:
        print("🎉 Pool-optimized method achieves <1 minute target!")
    else:
        print(f"⚠️ Pool-optimized method: {pool_full_time:.1f}s (still above 1 minute)")
    
    # Verify reproducibility
    print("\n=== Reproducibility Check ===")
    model_test1 = Transformer(model_args)
    model_test1.eval().requires_grad_(False).half()
    model_test2 = Transformer(model_args)
    model_test2.eval().requires_grad_(False).half()
    
    # Initialize with same seed
    initialize_model_with_pool(model_test1, "test_repro", dtype=torch.float16)
    initialize_model_with_pool(model_test2, "test_repro", dtype=torch.float16)
    
    # Check parameters are identical
    identical = True
    for (name1, param1), (name2, param2) in zip(model_test1.named_parameters(), model_test2.named_parameters()):
        if not torch.allclose(param1, param2, atol=1e-8):
            print(f"❌ Parameter {name1} differs")
            identical = False
            break
    
    if identical:
        print("✅ Pool-optimized method is reproducible")
    
    # Test weight distribution quality
    print("\n=== Weight Distribution Quality ===")
    all_weights = []
    for param in model_pool.parameters():
        all_weights.append(param.detach().flatten())
    all_weights = torch.cat(all_weights)
    
    mean = all_weights.mean().item()
    std = all_weights.std().item()
    print(f"Weight statistics: mean={mean:.6f}, std={std:.6f}")
    print(f"Expected: mean≈0.0, std≈0.02")
    
    # Check if distribution is reasonable
    distribution_ok = abs(mean) < 0.001 and 0.015 < std < 0.025
    if distribution_ok:
        print("✅ Weight distribution is reasonable")
    else:
        print("⚠️ Weight distribution may be suboptimal")
    
    # Test different seed strings produce different results
    print("\n=== Seed Variation Check ===")
    model_seed1 = Transformer(model_args)
    model_seed1.eval().requires_grad_(False).half()
    model_seed2 = Transformer(model_args)
    model_seed2.eval().requires_grad_(False).half()
    
    initialize_model_with_pool(model_seed1, "seed_test_1", dtype=torch.float16)
    initialize_model_with_pool(model_seed2, "seed_test_2", dtype=torch.float16)
    
    # Check parameters are different
    different = False
    for (name1, param1), (name2, param2) in zip(model_seed1.named_parameters(), model_seed2.named_parameters()):
        if not torch.allclose(param1, param2, atol=1e-6):
            different = True
            break
    
    if different:
        print("✅ Different seeds produce different weights")
    else:
        print("❌ Different seeds produce identical weights")
    
    return {
        'original_time': orig_time,
        'pool_time': pool_time,
        'improvement': improvement,
        'pool_full_time': pool_full_time,
        'achieves_target': pool_full_time < 60.0,
        'reproducible': identical,
        'distribution_ok': distribution_ok,
        'seed_variation': different
    }


if __name__ == "__main__":
    results = test_pool_optimized_performance()
    print(f"\nFinal result: {'SUCCESS' if results['achieves_target'] else 'PARTIAL'}")
