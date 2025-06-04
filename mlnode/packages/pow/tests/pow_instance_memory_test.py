import torch
import numpy as np
from pow.compute.autobs import (
    MODEL_PARAMS, 
    get_total_GPU_memory, 
    get_batch_size, 
    empirical_memory_estimate,
    WEIGHTS_GPU_MEMORY_CONSUMPTION,
    GPUMemoryMonitor,
    compute_memory_profile
)
from pow.compute.compute import Compute
from pow.models.utils import Params

def main():
    device_id = 0
    device_name = f'cuda:{device_id}'
    
    block_hash = "test_block_hash_12345"
    public_key = "test_public_key_67890"
    block_height = 12345
    r_target = 0.5
    
    params = Params(
        dim=MODEL_PARAMS.dim,
        n_layers=MODEL_PARAMS.n_layers,
        n_heads=MODEL_PARAMS.n_heads,
        n_kv_heads=MODEL_PARAMS.n_kv_heads,
        vocab_size=MODEL_PARAMS.vocab_size,
        ffn_dim_multiplier=MODEL_PARAMS.ffn_dim_multiplier,
        multiple_of=MODEL_PARAMS.multiple_of,
        norm_eps=MODEL_PARAMS.norm_eps,
        rope_theta=MODEL_PARAMS.rope_theta,
        use_scaled_rope=MODEL_PARAMS.use_scaled_rope,
        seq_len=MODEL_PARAMS.seq_len,
    )
    
    print("Initializing Compute instance...")
    compute_instance = Compute(
        params=params,
        block_hash=block_hash,
        block_height=block_height,
        public_key=public_key,
        r_target=r_target,
        devices=[device_name],
    )
    print("Compute instance initialized!")
    
    total_mem_mb = get_total_GPU_memory(device_id)
    model_size_mb = WEIGHTS_GPU_MEMORY_CONSUMPTION
    target_memory_usage = 0.95
    max_batch_size = int(get_batch_size(total_mem_mb, target_memory_usage=target_memory_usage))
    
    batch_sizes = np.linspace(1, max_batch_size, 10, dtype=int)
    gpu_monitor = GPUMemoryMonitor(device_id=device_id)
    
    print("Compute Instance Memory Profiling Results:")
    print("------------------------------------------------------------------------------------------------------------")
    print("Batch | Weights | Activations | Torch Res | Usage (%) | SMI Peak | Predicted | Diff (P-S)")
    print("------------------------------------------------------------------------------------------------------------")
    
    for batch_size in batch_sizes:
        torch.cuda.empty_cache()
        torch.cuda.reset_peak_memory_stats(device_id)
        torch.cuda.synchronize()
        
        compute_instance.model.eval()
        for param in compute_instance.model.parameters():
            param.grad = None
        
        gpu_monitor.start_monitoring()
        w_mb, a_mb = compute_memory_profile(compute_instance, batch_size, public_key)
        nvidia_smi_peak_mb = gpu_monitor.stop_monitoring()
        
        predicted_mb = empirical_memory_estimate(batch_size)
        torch_reserved_mb = w_mb + a_mb
        memory_usage_percent = (torch_reserved_mb / total_mem_mb) * 100
        difference_mb = predicted_mb - nvidia_smi_peak_mb
        
        print(f"{batch_size:5d} | {w_mb:7.1f} | {a_mb:11.1f} | {torch_reserved_mb:9.1f} | {memory_usage_percent:9.1f} | {nvidia_smi_peak_mb:8.1f} | {predicted_mb:9.1f} | {difference_mb:10.1f}")
        
        torch.cuda.empty_cache()
        torch.cuda.synchronize()
    
    print("---------------------------------------------------------------------------------------------------------------")
    print(f"Total GPU Memory: {total_mem_mb:.2f} MB")
    print(f"Model Size (Weights): {model_size_mb:.2f} MB")
    print(f"Max Batch Size (0.95 usage): {max_batch_size}")
    print("Compute Instance Memory Profiling Done!")

if __name__ == "__main__":
    main() 