from sympy import mobius
import torch
from pow.models.llama31 import Transformer, ModelArgs
from contextlib import contextmanager
from typing import Tuple, Optional
import subprocess
import threading
import time
import numpy as np
from pow.models.utils import Params, PARAMS_V1, PARAMS_V2
from pow.compute.gpu_group import GpuGroup

BIAS = 6500
COEFF = 30.5

MODEL_PARAMS = ModelArgs(
    dim=1024,
    n_layers=32,
    n_heads=32,
    n_kv_heads=32,
    vocab_size=8196,
    ffn_dim_multiplier=10.0,
    multiple_of=8*256,
    norm_eps=1e-5,
    rope_theta=10000.0,
    use_scaled_rope=False,
    seq_len=128
)

class GPUMemoryMonitor:
    def __init__(self, device_id=0, poll_interval=0.01):
        self.device_id = device_id
        self.poll_interval = poll_interval
        self.peak_memory_mb = 0
        self.monitoring = False
        self.monitor_thread = None
    
    def _monitor_memory(self):
        while self.monitoring:
            try:
                result = subprocess.run([
                    'nvidia-smi', 
                    '--query-gpu=memory.used', 
                    '--format=csv,noheader,nounits',
                    f'--id={self.device_id}'
                ], capture_output=True, text=True, timeout=1)
                
                if result.returncode == 0:
                    memory_mb = float(result.stdout.strip())
                    self.peak_memory_mb = max(self.peak_memory_mb, memory_mb)
                
                time.sleep(self.poll_interval)
            except (subprocess.TimeoutExpired, ValueError, FileNotFoundError):
                time.sleep(self.poll_interval)
    
    def start_monitoring(self):
        self.peak_memory_mb = 0
        self.monitoring = True
        self.monitor_thread = threading.Thread(target=self._monitor_memory, daemon=True)
        self.monitor_thread.start()
    
    def stop_monitoring(self):
        self.monitoring = False
        if self.monitor_thread:
            self.monitor_thread.join(timeout=1)
        return self.peak_memory_mb

def get_total_GPU_memory(device_id):
    if torch.cuda.is_available():
        props = torch.cuda.get_device_properties(device_id)
        total_mem = props.total_memory
        total_mem_mb = total_mem/1024**2
        return total_mem_mb
    else:
        print("No CUDA GPUs found.")
        return 0

def empirical_memory_estimate(bs):
    return BIAS + COEFF * bs


def get_batch_size(total_memory, target_memory_usage):
    target_memory = total_memory * target_memory_usage
    if target_memory < (BIAS + COEFF * 1):
        raise ValueError(f"Insufficient memory: need at least {BIAS + COEFF * 1:.1f} MB, but target is {target_memory:.1f} MB")
    target_batch_size = np.floor((target_memory - BIAS) / COEFF)
    return int(target_batch_size)


def get_batch_size_from_memory(target_memory_usage, device_id):
    total_memory = get_total_GPU_memory(device_id)
    target_batch_size = get_batch_size(total_memory, target_memory_usage)
    return int(target_batch_size)


def compute_memory_profile_per_device(compute_instance, batch_size: int, public_key: str = "test_key"):
    devices = compute_instance.devices
    
    nonces = list(range(batch_size))
    target = compute_instance.target
    
    for device in devices:
        torch.cuda.reset_peak_memory_stats(device)
    torch.cuda.synchronize()
    
    with torch.no_grad():
        future_result = compute_instance(
            nonces=nonces,
            public_key=public_key,
            target=target,
            next_nonces=None,
            use_cache=False
        )
        
        proof_batch = future_result.result()
        torch.cuda.synchronize()
    

    peak_memory_bytes = {device: torch.cuda.max_memory_reserved(device) for device in devices}
    peak_memory_mb = {device: peak_memory_bytes[device] / (1024 * 1024) for device in devices}

    model_weight_by_device = {device: 0 for device in devices}
    for param in compute_instance.model.module.parameters():
        weight = param.numel() * param.element_size()
        device = str(param.device)
        model_weight_by_device[device] += weight
    
    weights_memory_mb = {device: model_weight_by_device[device] / (1024 * 1024) for device in devices}
    activations_memory_mb = {device: peak_memory_mb[device] - weights_memory_mb[device] for device in devices}
    
    return weights_memory_mb, activations_memory_mb

def get_model_weights_memory_mb_per_device(compute_instance):
    devices = compute_instance.devices
    print(f"Devices: {devices}")
    model_weight_by_device = {device: 0 for device in devices}
    for param in compute_instance.model.module.parameters():
        weight = param.numel() * param.element_size()
        device = str(param.device)
        model_weight_by_device[device] += weight
    return {device: model_weight_by_device[device] / (1024 * 1024) for device in devices}

def get_model_weights_memory_mb(compute_instance):
    return sum(get_model_weights_memory_mb_per_device(compute_instance).values())

def compute_memory_profile(compute_instance, batch_size: int, public_key: str = "test_key"):
    devices = compute_instance.devices
    weights_memory_mb, activations_memory_mb = compute_memory_profile_per_device(compute_instance, batch_size, public_key)
    return max(weights_memory_mb.values()), max(activations_memory_mb.values())

def _tensor_bytes(t: torch.Tensor) -> int:
    return t.numel() * t.element_size()

@contextmanager
def _restore_mode(model: torch.nn.Module):
    training = model.training
    try:
        yield
    finally:
        model.train(training)

