# PoC: NoW

**IMPORTANT**: Before working on this document, ALWAYS read `general-rules.md` first for complete rules, testing framework, and documentation guidelines.

Proof-of-Compute workflow starting from ParallelController:

- **GPU Detection & Instance Creation**: `ParallelController._get_all_torch_devices()` detects available CUDA devices via `torch.cuda.device_count()` and creates one `Controller` instance per GPU in `ParallelController.__init__()`: `for idx, device in enumerate(devices or [])`
- **Single Model Per GPU**: Each GPU runs a single model instance. Each Controller gets exactly one device in `ParallelController.__init__()`: `devices=[device]`, so `ModelWrapper` always receives a single-device list, making the DataParallel path unreachable
- **Process Spawning**: Each `Controller` spawns a separate `Worker` process using multiprocessing context ("spawn") with isolated memory space in `ParallelController.__init__()`: `ctx = mp.get_context("spawn")`
- **Reproducible Weight Initialization**: Model weights are initialized from `block_hash` string via `get_rng()` in `random.py`: `entropy = get_extended_entropy(seed_string, num_hashes)` → `ModelWrapper.build()`: `rng = get_rng(str(hash_), 4); initialize_model_weights_from_rng(model, rng)`. **⚠️ WARNING**: This deterministic weight generation from string MUST remain reproducible across different machines and GPU configurations
- **Model Loading Time**: Weight initialization and model setup takes significant time (~several seconds per GPU) as logged in `ModelWrapper.build()`: `logger.info(f"Model initialized in {init_time:.2f}s | {count_params(model)} params")`
- **Unique Nonce Generation**: Each worker gets a `NonceIterator` that generates collision-free nonces in `NonceIterator.__next__()`: `value = offset + self._current_x * step` where `offset = self.node_id + self.device_id * self.n_nodes`
- **Sequence Processing**: Workers continuously generate nonce batches in `Worker._generate()`: `next_nonces = [next(self.generator) for _ in range(self.batch_size)]`, process them through `Compute.__call__()` to create model inputs, run inference, and calculate distances to target in `Compute._process_batch()`

## General info

**Batch Size Management**: Automatic batch size calculation using `get_batch_size_from_memory()` with 90% target memory usage in `Controller.__init__()`: `batch_size = get_batch_size_from_memory(target_memory_usage=0.9, device_id=idx)` to optimize GPU memory utilization per device

**Asynchronous Processing**: Uses `ThreadPoolExecutor(max_workers=24)` in `Compute.__init__()` with `Future` objects for non-blocking batch processing. Model inference and distance calculations run asynchronously while next batch preparation happens in parallel

**Queue Architecture**: Three-queue coordination system - `generated_batch_queue` for completed proofs, `to_validate_batch_queue` for incoming validation requests, `validated_batch_queue` for validation results. All queues created with `ctx.Queue(maxsize=0)` for unlimited buffering

**Phase Management**: Workers operate in synchronized phases via shared `ctx.Value('i', Phase.IDLE)` - `Phase.IDLE` (waiting), `Phase.GENERATE` (proof generation), `Phase.VALIDATE` (proof validation), `Phase.STOP` (shutdown). Phase transitions coordinate all workers simultaneously

**Distance Calculation**: L2 norm distance between normalized model outputs and target vector in `Compute._process_batch()`: `distances = np.linalg.norm(outputs - target, axis=1)` where outputs are normalized via `outputs / np.linalg.norm(outputs, axis=1, keepdims=True)`

**Filtering Logic**: Valid proofs selected using `ProofBatch.sub_batch(self.r_target)` which filters nonces with distances below threshold. Only filtered batches with `filtered_batch.nonces` are queued for submission

**Validation Process**: Validation phase merges incoming batches by `public_key` using `ProofBatch.merge(batches)`, splits them into `batch_size` chunks via `merged_batch.split(self.batch_size)`, then validates each chunk through `compute.validate(batch)` before queuing results

### Model Parameters for On-Chain PoC

The following model parameters are used for on-chain proof-of-concept deployment:

```python
params = {
    "dim": 1024,
    "n_layers": 32,
    "n_heads": 32,
    "n_kv_heads": 32,
    "vocab_size": 8196,
    "ffn_dim_multiplier": 10.0,
    "multiple_of": 2048,  # 8*256
    "norm_eps": 1e-5,
    "rope_theta": 10000.0,
    "use_scaled_rope": False,
    "seq_len": 128,
}
```

## Production Deployment

**Docker-Only Deployment**: All production deployments MUST use Docker containers. No direct host installations are supported for production environments. This ensures consistent runtime environments, dependency isolation, and reproducible deployments across different infrastructure configurations.

## Nonce Generation Performance Testing

**Benchmark Script**: Run `tests/test_nonce_rate.sh` to measure nonce generation rate. Script starts server without Docker, initializes generation with high `R_TARGET=5.0`, and outputs nonces/min after 120 seconds plus model initialization time.

**GPU Configuration**: Use `CUDA_VISIBLE_DEVICES` to test specific GPUs:
```bash
CUDA_VISIBLE_DEVICES=0 tests/test_nonce_rate.sh      # 1 GPU
CUDA_VISIBLE_DEVICES=0,1 tests/test_nonce_rate.sh    # 2 GPUs  
CUDA_VISIBLE_DEVICES=0,1,2,3 tests/test_nonce_rate.sh # 4 GPUs
```

**Performance Results**: Run the benchmark commands above to get actual nonce generation rates for your hardware configuration. Model initialization takes ~15-60 seconds before generation begins.

## Tasks

[DONE]: Nonce Generation Performance Testing Implementation
    Complete Python script (`tests/test_nonce_rate.py`) for measuring nonce generation rates across GPU configurations. Features automated server startup, process cleanup, detailed logging analysis, and per-worker rate aggregation.
    
    Key improvements:
    - Enhanced logging with detailed worker analysis and GPU detection
    - Robust process cleanup (uvicorn, python, GPU processes)
    - Individual worker rate tracking with total aggregation
    - Debug log preservation for troubleshooting
    
    Usage:
    ```bash
    CUDA_VISIBLE_DEVICES=0 python tests/test_nonce_rate.py      # 1 GPU
    CUDA_VISIBLE_DEVICES=0,1 python tests/test_nonce_rate.py    # 2 GPUs
    CUDA_VISIBLE_DEVICES=0,1,2,3 python tests/test_nonce_rate.py # 4 GPUs
    ```


# PoC: V2 GPU parallelization for model

## High-Level Goal

**Objective**: Enable PoC to work efficiently on >40 GPU servers by implementing GPU parallelization to utilize memory bandwidth and prevent participants with many small/cheap GPUs from achieving unfair advantages.

**Current Architecture**: Single `Compute` instance per GPU (1:1 mapping) with DataParallel path unreachable due to `devices=[device]` in `ParallelController.__init__()`

**Target Architecture**: GPU groups whose total VRAM meets `get_min_group_vram(params)` (returns a constant for now), running one model instance per group with parallelization across group GPUs

**⚠️ CRITICAL CONSTRAINT**: Models will be scaled to 30-40GB and **will NOT fit on single GPUs**. This eliminates DataParallel as an option - we need true parallelization where the model is 
**distributed across GPUs**, not replicated.


**Current Architecture (1 Controller per GPU):**
```
ParallelController
├── Controller 1 (devices=[0]) → Worker Process 1 → Model Instance (GPU 0)
├── Controller 2 (devices=[1]) → Worker Process 2 → Model Instance (GPU 1)  
├── Controller 3 (devices=[2]) → Worker Process 3 → Model Instance (GPU 2)
└── Controller 4 (devices=[3]) → Worker Process 4 → Model Instance (GPU 3)
```

**Final Target Architecture (1 Controller per GPU Group):**
```
ParallelController
├── Controller 1 (devices=[0,1]) → Worker Process 1 → Single Model (Sharded GPU 0+1)
└── Controller 2 (devices=[2,3]) → Worker Process 2 → Single Model (Sharded GPU 2+3)
                                                      ↑
                                            Zero communication between groups                                 
```
## Phase 1: GPU Group Architecture




[DONE]: Basic GPU Group Structure
    Create minimal `GpuGroup` class and update architecture to use groups instead of individual devices. Model still runs on first GPU in group only. **MINIMAL CHANGES ONLY** - preserve exact same behavior, just change internal representation.
    
    **Completed Implementation**:
    1. ✅ Created `GpuGroup` class with `devices: List[int]` and `primary_device: int` (first device)
    2. ✅ Updated `ParallelController._get_all_torch_devices()` to return list of `GpuGroup` objects
    3. ✅ Updated `Controller.__init__()` to accept `GpuGroup` instead of `devices: List[str]`
    4. ✅ Use `group.primary_device` for batch size calculation instead of `device_id`  
    5. ✅ **NO parallelization yet** - model runs only on `primary_device` to maintain current behavior
    6. ✅ Added comprehensive test suite in `test_gpu_groups.py` with 16 test cases
    
    **Key Achievement**: Architecture now uses `GpuGroup` abstraction while maintaining identical behavior. Ready for VRAM-based grouping logic.

[DONE]: VRAM-Based GPU Grouping
    Add VRAM-based grouping logic while maintaining single-GPU model execution. **ISOLATED CHANGES ONLY** - only modify grouping logic, keep all other behavior identical.
    
    **Completed Implementation**:
    - ✅ **VRAM Function**: `get_min_group_vram()` returns 23.0GB (TODO: implement estimation from params)
    - ✅ **Device Discovery**: Collect `(device_id, memory_gb)` via `torch.cuda.get_device_properties()`
    - ✅ **Greedy Grouping Logic**: `create_gpu_groups()` now uses a greedy algorithm. It iterates through available GPUs and forms the first possible group of size 1, 2, 4, or 8 that meets the `min_vram_gb` requirement.
    - ✅ **Deterministic Grouping**: Sorts devices by `device_id` to ensure group assignments are reproducible.
    - ✅ **Comprehensive Tests**: Replaced the previous test suite with a simplified, more robust set of 12 test cases covering all scenarios, including mixed VRAM configurations and edge cases.
    
    **Result**: The grouping logic is now simpler, more reliable, and correctly handles cases where devices cannot form a valid group. For example, a 4xRTX3090 system (23.6GB each) correctly forms 4 single-GPU groups.

[DONE]: Nonce Distribution for Groups
    Update nonce generation to work with GPU groups instead of individual GPUs. **MINIMAL CHANGES ONLY** - only change nonce offset calculation, preserve all collision-free guarantees.
    
    **Completed Implementation**:
    - ✅ **Parameter Renaming**: Updated `NonceIterator` to use `group_id` and `n_groups` instead of `device_id` and `n_devices`
    - ✅ **Formula Update**: Changed offset calculation from `node_id + device_id * n_nodes` to `node_id + group_id * n_nodes`
    - ✅ **ParallelController Integration**: Updated nonce iterator instantiation to use `group_id=idx` and `n_groups=len(gpu_groups)`
    - ✅ **Collision-Free Guarantees**: All existing collision avoidance properties preserved - each group gets unique nonce subsequence
    - ✅ **Comprehensive Testing**: Added 3 new test functions verifying group-based distribution, backward compatibility, and multi-node collision avoidance
    - ✅ **Test Verification**: All 30 unit tests and 4 GPU tests pass, confirming no regressions
    
    **Key Achievement**: Nonce generation now operates at the group level while maintaining identical collision-free behavior. Ready for GPU parallelization implementation.

[DONE]: Enhanced Custom Transformer for Multi-GPU Support
    Enhanced custom `Transformer` with optimized initialization and multi-GPU compatibility.
    
    **Implementation**:
    - **Pool-Based Weights**: `initialize_model_with_pool()` generates small random pool (1% of params) and tiles deterministically - 10x faster initialization
    - **Meta Device Init**: `with torch.device("meta")` for memory-efficient model creation
    - **Device Fixes**: `self.freqs_cis.to(h.device, h.dtype)` for proper multi-GPU dtype consistency
    
    **Result**: Custom Transformer with 10x faster initialization and multi-GPU readiness.

## Phase 2: GPU Parallelization

[DONE]: Accelerate Multi-GPU Distribution
    Automatic GPU parallelization using Accelerate library for models exceeding single GPU memory.
    
    **Implementation**:
    - **Auto Distribution**: `infer_auto_device_map()` + `dispatch_model()` automatically distributes layers across GPU group
    - **Forward Simplification**: Removed DataParallel - simple device detection via `layers[0].attention.wq.weight.device`
    - **Multi-GPU Sync**: `torch.cuda.synchronize()` across all group devices
    
    **Result**: Seamless single/multi-GPU operation within groups for 30-40GB models.

[DONE]: Implement parallelization between multiple GPUs
    Multi-GPU parallelization complete - combines pool optimization with Accelerate distribution.
    
    **Result**: GPU groups support single-GPU (small models) and multi-GPU (30-40GB models) with automatic balancing.

[TODO]: Model Parameter Research & Selection
    Research larger model parameters that fit the 40GB VRAM constraint with ≥500 nonces/hour per GPU group. **ISOLATED CHANGES ONLY** - only add new `MODEL_PARAMS_BIG` constants, preserve all model architecture code. Current `MODEL_PARAMS` in `autobs.py`: `dim=1024, n_layers=32, n_heads=32, vocab_size=8196` (~6.5GB weights). Explore larger `dim` (2048, 4096) and `n_layers` (48, 64). Add `MODEL_PARAMS_BIG` and validate fit using `get_batch_size_from_memory()`. **Initialization Time Constraint**: Larger models MUST initialize in <60s to maintain practical deployment times.

[TODO]: Multi-Server Testing
    I'll manually deploy on multiple servers with different GPU configurations. **ISOLATED CHANGES ONLY** - only run existing tests on different hardware, no code modifications. Run `tests/test_nonce_rate.py` on both primary (4×3090) and secondary servers. Compare nonce rates, memory utilization, grouping stability, and determinism. Confirm `ParallelController._get_all_torch_devices()` detects and groups GPUs consistently across hardware.

[TODO]: Cross-GPU Validation Testing
    Create comprehensive test for PoC validation between different GPU configurations. **ISOLATED CHANGES ONLY** - add new test file without modifying existing validation logic.
    
    - **Test Scenarios**: Validate proofs generated on 4 GPUs can be validated on 1 GPU, and vice versa
    - **Parameter Testing**: Test with both current model parameters and newly found larger parameters  
    - **Target Distance**: Use `r_target = 1.3971164020989417` for validation threshold
    - **Validation Logic**: Generate proofs on source configuration, serialize/deserialize proof data, validate on target configuration
    - **Determinism Check**: Ensure identical `block_hash` produces same validation results regardless of GPU configuration
    - **Implementation**: Add test in `tests/unit-gpu/test_cross_validation.py` that spawns processes with different `CUDA_VISIBLE_DEVICES` settings

## Implementation Strategy & Key Concerns

**⚠️ CRITICAL REQUIREMENTS**:

1. **Deterministic Weight Initialization**: Must preserve existing `initialize_model_weights_from_rng(model, get_rng(block_hash))` logic to ensure reproducible model weights across different hardware configurations.

2. **API Compatibility**: Preserve existing `llama31.py` model API and `model(inputs, start_pos=0)` interface.

3. **Process Management**: Each GPU group spawns one Worker process with proper device management.

**Implementation Strategy**:

- **Determinism**: Pass/fail validation decisions for identical inputs MUST remain unchanged across different GPU configurations
- **Memory Management**: Current `BIAS = 6500, COEFF = 30.5` in `autobs.py` works for single-GPU operation
- **Testing Framework**: All changes must pass existing tests in `tests/unit-gpu/`
- **Docker Compatibility**: All GPU groups run inside single container
- **Backward Compatibility**: Single-GPU operation remains the default and must stay functional