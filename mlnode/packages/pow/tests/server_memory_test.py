import time
import numpy as np
import requests
from scipy import stats
from pow.service.client import PowClient
from pow.compute.autobs import (
    MODEL_PARAMS, 
    get_total_GPU_memory, 
    get_batch_size, 
    GPUMemoryMonitor
)
from pow.models.utils import Params
from common.wait import wait_for_server


def main():
    # Server configuration
    import os
    server_url = os.getenv("SERVER_URL", "http://localhost:8080")
    device_id = 0
    
    # Test parameters
    block_hash = "test_block_hash_12345"
    public_key = "test_public_key_67890"
    block_height = 12345
    r_target = 0.5
    fraud_threshold = 0.1
    url = "http://localhost:8080"  # URL for the PoW instance to send results
    
    # Model parameters
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
    
    print("Waiting for server to be available...")
    try:
        wait_for_server(server_url, timeout=60)
        print("Server is available!")
    except requests.exceptions.RequestException as e:
        print(f"Server is not available: {e}")
        print("Please make sure the server is running on localhost:8080")
        return
    
    # Initialize client
    client = PowClient(server_url)
    
    # Calculate batch sizes to test
    total_mem_mb = get_total_GPU_memory(device_id)
    target_memory_usage = 0.95
    max_batch_size = int(get_batch_size(total_mem_mb, target_memory_usage=target_memory_usage))
    
    # Test 10 different batch sizes
    batch_sizes = np.linspace(1, max_batch_size, 10, dtype=int)
    
    # Initialize GPU memory monitor
    gpu_monitor = GPUMemoryMonitor(device_id=device_id)
    
    print("Server PoW Memory Profiling Results:")
    print("=" * 80)
    print("Batch Size | Peak Memory (MB) | Memory Usage (%)")
    print("-" * 80)
    
    results = []
    
    for batch_size in batch_sizes:
        try:
            # Stop any existing PoW instance
            try:
                client.stop()
                time.sleep(5)  # Wait for cleanup
            except:
                pass  # Ignore if nothing was running
            
            gpu_monitor.start_monitoring()
            response = client.init_generate(
                node_id=0,
                node_count=1,
                url=url,
                block_hash=block_hash,
                block_height=block_height,
                public_key=public_key,
                batch_size=int(batch_size),
                r_target=r_target,
                fraud_threshold=fraud_threshold,
                params=params
            )
            
            # Wait for initialization to complete and memory to stabilize
            time.sleep(90)
            
            # Check status to ensure initialization completed
            status = client.status()
            
            # Stop memory monitoring and get peak usage
            peak_memory_mb = gpu_monitor.stop_monitoring()
            memory_usage_percent = (peak_memory_mb / total_mem_mb) * 100
            
            results.append({
                'batch_size': batch_size,
                'peak_memory_mb': peak_memory_mb,
                'memory_usage_percent': memory_usage_percent
            })
            
            print(f"{batch_size:10d} | {peak_memory_mb:15.1f} | {memory_usage_percent:13.1f}")
            
            # Stop the PoW instance before next iteration
            client.stop()
            time.sleep(5)  # Wait for cleanup
            
        except Exception as e:
            print(f"Error with batch size {batch_size}: {e}")
            # Try to stop and continue
            try:
                client.stop()
                time.sleep(2)
            except:
                pass
            continue
    
    print("-" * 80)
    print(f"Total GPU Memory: {total_mem_mb:.2f} MB")
    print(f"Max Batch Size (0.95 usage): {max_batch_size}")
    print("=" * 80)
    
    # Summary statistics
    if results:
        print("\nSummary:")
        print(f"Tested {len(results)} batch sizes")
        print(f"Memory usage range: {min(r['memory_usage_percent'] for r in results):.1f}% - {max(r['memory_usage_percent'] for r in results):.1f}%")
        print(f"Peak memory range: {min(r['peak_memory_mb'] for r in results):.1f} MB - {max(r['peak_memory_mb'] for r in results):.1f} MB")
    
    # Linear regression analysis
    if results:
        print("\nLinear Regression Analysis:")
        print("=" * 50)
        
        # Extract data for regression
        batch_sizes_array = np.array([r['batch_size'] for r in results])
        memory_array = np.array([r['peak_memory_mb'] for r in results])
        
        # Perform linear regression
        slope, intercept, r_value, p_value, std_err = stats.linregress(batch_sizes_array, memory_array)
        
        print(f"Memory Regression:")
        print(f"Equation: Memory = {slope:.4f} * batch_size + {intercept:.2f}")
        print(f"Slope (MB per batch): {slope:.4f}")
        print(f"Intercept (MB): {intercept:.4f}")
        print(f"R-squared: {r_value**2:.4f}")
        print(f"P-value: {p_value:.6f}")
        print(f"Standard error: {std_err:.4f}")
    
    print("\nServer PoW Memory Profiling Complete!")


if __name__ == "__main__":
    main() 