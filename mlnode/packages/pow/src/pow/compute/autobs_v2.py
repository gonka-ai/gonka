from pow.compute.autobs import get_batch_size_from_memory
from pow.compute.autobs import GpuGroup, Params, PARAMS_V1, PARAMS_V2
from common.logger import create_logger
import math

logger = create_logger(__name__)

def get_batch_size_for_gpu_group(gpu_group: GpuGroup, params: Params, target_memory_usage: float = 0.9) -> int:
    if params == PARAMS_V1:
        return get_batch_size_from_memory(
            target_memory_usage=target_memory_usage,
            device_id=gpu_group.primary_device
        )
    
    if params == PARAMS_V2:
        return estimate_batch_size(gpu_group, params, target_memory_usage)
    
    return 100


def estimate_batch_size(gpu_group: GpuGroup, params: Params, target_memory_usage: float = 0.9) -> int:
    # --- 1. Define Constants and Assumptions ---
    BYTES_PER_ELEMENT = 2  # float16
    ACTIVATION_OVERHEAD_FACTOR = 8.0
    SAFETY_MARGIN = 0.90
    num_gpus = gpu_group.group_size

    if num_gpus == 0:
        return 1

    # --- 2. Calculate Static Memory Usage (Model Weights) ---
    # This calculation remains the same, as it's for the whole model.
    ffn_hidden_dim = params.multiple_of * math.ceil(
        (params.ffn_dim_multiplier * (2/3 * 4 * params.dim)) / params.multiple_of
    )
    attention_params = params.n_layers * (4 * params.dim**2)
    ffn_params = params.n_layers * (
        params.dim * ffn_hidden_dim
        + ffn_hidden_dim * params.dim
        + params.dim * ffn_hidden_dim
    )
    output_params = params.vocab_size * params.dim
    total_params = attention_params + ffn_params + output_params
    total_model_weights_mb = (total_params * BYTES_PER_ELEMENT) / (1024**2)

    # Assume accelerate balances the weights evenly across all GPUs.
    weights_per_gpu_mb = total_model_weights_mb / num_gpus

    # --- 3. Find the Bottleneck GPU ---
    # Get the free memory for each device individually.
    free_vram_per_device_mb = gpu_group.get_free_vram_mb_per_device()

    memory_for_activations_per_gpu = {}
    for device_id, free_mb in free_vram_per_device_mb.items():
        # For each GPU, calculate usable memory and subtract its share of the weights.
        usable_free_mb = free_mb * target_memory_usage * SAFETY_MARGIN
        memory_for_activations_per_gpu[device_id] = usable_free_mb - weights_per_gpu_mb

    # The true available memory is limited by the GPU with the LEAST space for activations.
    if not memory_for_activations_per_gpu:
        return 1
        
    bottleneck_memory_mb = min(memory_for_activations_per_gpu.values())
    
    if bottleneck_memory_mb <= 0:
        logger.warning(
            f"The most constrained GPU has no memory left after loading model weights. "
            f"Estimated weights per GPU: {weights_per_gpu_mb:.2f} MB. "
            f"Check `nvidia-smi` for other running processes."
        )
        return 1
    
    # --- 4. Calculate Dynamic Memory per Batch Item ---
    # This part is the same, as it represents the peak load that will hit the bottleneck GPU.
    kv_cache_bytes_per_item = 2 * params.n_layers * params.seq_len * params.dim * BYTES_PER_ELEMENT
    activations_bytes_per_item = ACTIVATION_OVERHEAD_FACTOR * params.seq_len * params.dim * BYTES_PER_ELEMENT
    attention_scores_bytes_per_item = params.n_heads * (params.seq_len**2) * BYTES_PER_ELEMENT
    
    memory_per_batch_item_mb = (
        kv_cache_bytes_per_item + 
        activations_bytes_per_item + 
        attention_scores_bytes_per_item
    ) / (1024**2)

    if memory_per_batch_item_mb < 1e-6:
        return 1

    # --- 5. Determine Final Batch Size based on the Bottleneck ---
    estimated_bs = math.floor(bottleneck_memory_mb / memory_per_batch_item_mb)
    
    return max(1, int(estimated_bs))