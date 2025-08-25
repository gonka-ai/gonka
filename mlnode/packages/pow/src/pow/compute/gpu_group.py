from typing import List, Dict
import torch
from pow.models.utils import PARAMS_V1, PARAMS_V2, Params


def get_min_group_vram(params: Params) -> float:
    if params == PARAMS_V1:
        return 10.0
    elif params == PARAMS_V2:
        return 38.0
    else:
        return 38.0

class GpuGroup:
    def __init__(self, devices: List[int]):
        if not devices:
            raise ValueError("GPU group must have at least one device")
        
        self.devices = devices
        self.primary_device = devices[0]  # First device is primary
        self.group_size = len(devices)
    
    def __repr__(self):
        return f"GpuGroup(devices={self.devices}, primary={self.primary_device})"
    
    def get_device_strings(self) -> List[str]:
        return [f"cuda:{device_id}" for device_id in self.devices]
    
    def get_primary_device_string(self) -> str:
        return f"cuda:{self.primary_device}"
    
    def get_total_vram_gb(self) -> float:
        if not torch.cuda.is_available():
            return 0.0
            
        total_vram = 0.0
        for device_id in self.devices:
            if device_id < torch.cuda.device_count():
                props = torch.cuda.get_device_properties(device_id)
                total_vram += props.total_memory / (1024**3)  # Convert to GB
        return total_vram

    def get_free_vram_mb_per_device(self) -> Dict[int, int]:
        if not torch.cuda.is_available():
            return {device_id: 0 for device_id in self.devices}
        
        free_vram_map = {}
        for device_id in self.devices:
            if device_id < torch.cuda.device_count():
                free_mem_bytes, _ = torch.cuda.mem_get_info(device_id)
                free_vram_map[device_id] = int(free_mem_bytes / (1024**2))
            else:
                free_vram_map[device_id] = 0
        return free_vram_map

    def get_free_vram_gb(self) -> float:
        free_vram_per_device_mb = self.get_free_vram_mb_per_device()
        
        total_free_vram_mb = sum(free_vram_per_device_mb.values())
        
        return total_free_vram_mb / 1024

def create_gpu_groups(min_vram_gb: float = None, params: Params = None) -> List[GpuGroup]:

    if not torch.cuda.is_available():
        return [GpuGroup([0])]  # CPU fallback

    if min_vram_gb is None:
        min_vram_gb = get_min_group_vram(params)

    device_count = torch.cuda.device_count()
    if device_count == 0:
        return [GpuGroup([0])]  # CPU fallback

    # Get VRAM for each device, sorted by device_id for determinism
    device_vram = []
    for device_id in range(device_count):
        props = torch.cuda.get_device_properties(device_id)
        vram_gb = props.total_memory / (1024**3)
        device_vram.append((device_id, vram_gb))

    groups = []
    available_devices = list(device_vram)
    preferred_sizes = [1, 2, 4, 8]

    while available_devices:
        group_formed = False
        for group_size in preferred_sizes:
            if len(available_devices) >= group_size:
                potential_group_tuples = available_devices[:group_size]
                total_vram = sum(vram for _, vram in potential_group_tuples)

                if total_vram >= min_vram_gb:
                    device_ids = [device_id for device_id, _ in potential_group_tuples]
                    groups.append(GpuGroup(device_ids))
                    available_devices = available_devices[group_size:]
                    group_formed = True
                    break  # Found a valid group, move to next block of available devices
        
        if not group_formed:
            # Could not form a valid group starting with the current device.
            # Discard it and try to form a group from the remaining devices.
            available_devices.pop(0)

    return groups
