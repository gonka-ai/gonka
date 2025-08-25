import time
import os
from typing import Any, List

import torch

from accelerate import dispatch_model, infer_auto_device_map
from accelerate.utils import get_balanced_memory

from pow.compute.autobs import get_total_GPU_memory
from pow.compute.utils import TimeStats
from pow.models.llama31 import ModelArgs, Transformer
from pow.models.utils import Params, count_params, set_default_dtype
from pow.random_pool_optimized import initialize_model_with_pool
from common.logger import create_logger


logger = create_logger(__name__)


class ModelWrapper(torch.nn.Module):
    def __init__(
        self,
        module: torch.nn.Module,
        devices: List[str],
        output_device: int = None,
        stats: TimeStats = None,
    ):
        super().__init__()
        self.output_device = output_device
        self.stats = stats
        self.module = module

    def forward(self, inputs: torch.Tensor, **kwargs: Any) -> torch.Tensor:
        with torch.no_grad():
            with self.stats.time_infer():
                device = self.module.layers[0].attention.wq.weight.device
                inputs = inputs.to(device)
                return self.module(inputs, **kwargs)

    @staticmethod
    def build(
        hash_: str,
        stats: TimeStats,
        params: Params = Params(),
        seed: int = 42,
        max_seq_len: int = 1024,
        max_batch_size: int = 1,
        devices: List[str] = None,
        dtype: torch.dtype = torch.float16,
    ) -> "ModelWrapper":
        with stats.time_model_load():
            devices = [torch.device(device) for device in devices]
            primary_device = devices[0]

            torch.manual_seed(seed)
            start_time = time.time()

            model_args: ModelArgs = ModelArgs(
                max_seq_len=max_seq_len,
                max_batch_size=max_batch_size,
                flash=False,
                **(params.__dict__),
            )

            logger.info("Creating model...")
            with torch.device("meta"):
                model = Transformer(model_args)
            model.to_empty(device="cpu")
            logger.info(f"Loaded in {time.time() - start_time:.2f} seconds")

            model.eval()
            model.requires_grad_(False)
            
            # Convert model to specified dtype before moving to GPUs
            if dtype == torch.float16:
                model = model.half()
                logger.info("Model converted to float16")
            elif dtype == torch.bfloat16:
                model = model.bfloat16()
                logger.info("Model converted to bfloat16")
            elif dtype == torch.float32:
                model = model.float()
                logger.info("Model converted to float32")

            initialize_model_with_pool(model, str(hash_), dtype=dtype, pool_fraction=0.05)
            # Recompute freqs_cis after model is on CPU and properly initialized
            model.recompute_freqs_cis()

            init_time = time.time() - start_time
            logger.info(f"Model initialized in {init_time:.2f}s | {count_params(model)} params")

            try:
                max_memory = {}
                for device in devices:
                    device_id = device.index
                    max_memory[device_id] = f"{get_total_GPU_memory(device_id)}MB"
                max_memory = get_balanced_memory(model, max_memory=max_memory)
                device_map = infer_auto_device_map(
                    model,
                    max_memory=max_memory,
                    no_split_module_classes=["TransformerBlock"],
                    dtype=dtype
                )
                logger.info(f"Inferred device map: {device_map}")
                model = dispatch_model(model, device_map=device_map)
                logger.info("Multi-GPU distribution successful")
            except Exception as e:
                logger.error(f"Multi-GPU distribution failed: {e}")
                logger.error("Falling back to single GPU")
                raise e
            
            model.eval()
            model.requires_grad_(False)

            set_default_dtype(device=primary_device, dtype=dtype)
            
            logger.info("Wrapping model in ModelWrapper")
            model_wrapper = ModelWrapper(model, devices=devices, stats=stats)
            logger.info(f"ModelWrapper created in {stats.model_load_time:.2f}s")

            return model_wrapper
