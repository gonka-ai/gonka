import time
from itertools import chain
from typing import Any, List

import torch
import torch.multiprocessing as mp
from torch.nn.parallel import DataParallel

from pow.compute.utils import TimeStats
from pow.models.llama31 import ModelArgs, Transformer
from pow.models.utils import Params, count_params, set_default_dtype
from pow.random import get_rng, initialize_model_weights_from_rng
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
        self.num_gpus = len(devices)
        self.device_ids = [torch.device(device) for device in devices]
        if self.num_gpus > 1:
            self.module = DataParallel(module, device_ids=self.device_ids)
        else:
            self.module = module

    def forward(self, inputs: torch.Tensor, **kwargs: Any) -> torch.Tensor:
        assert inputs.device.type == "cpu", "Inputs must be on CPU"

        if not torch.cuda.is_available():
            logger.warning("CUDA is not available, using CPU instead")
            return self.module(inputs, **kwargs)

        if self.num_gpus > 1:
            return self.forward_parallel(inputs, **kwargs)
        else:
            return self.forward_single(inputs, **kwargs)

    def forward_single(self, inputs: torch.Tensor, **kwargs: Any) -> Any:
        assert self.num_gpus == 1, "This method is intended for single GPU models"

        if torch.cuda.is_available():
            with self.stats.time_to_cuda():
                inputs = inputs.to(self.device_ids[0])

        with self.stats.time_infer():
            return self.module(inputs, **kwargs)

    def forward_parallel(self, *inputs: Any, **kwargs: Any) -> Any:
        assert self.num_gpus > 1, "This method is intended for parallel models"

        with self.stats.time_to_cuda():
            if not self.device_ids:
                return self.module.module(*inputs, **kwargs)

            for t in chain(
                self.module.module.parameters(), self.module.module.buffers()
            ):
                if t.device != self.module.src_device_obj:
                    raise RuntimeError(
                        "module must have its parameters and buffers "
                        f"on device {self.module.src_device_obj} (device_ids[0]) but found one of "
                        f"them on device: {t.device}"
                    )

            inputs, module_kwargs = self.module.scatter(
                inputs, kwargs, self.module.device_ids
            )
            if not inputs and not module_kwargs:
                inputs = ((),)
                module_kwargs = ({},)

            if len(self.module.device_ids) == 1:
                return self.module.module(*inputs[0], **module_kwargs[0])
            replicas = self.module.replicate(
                self.module.module, self.module.device_ids[: len(inputs)]
            )

        with self.stats.time_infer():
            outputs = self.module.parallel_apply(replicas, inputs, module_kwargs)
            return self.module.gather(outputs, self.module.output_device)

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
            if len([d.type for d in devices]) > 1:
                raise ValueError(f"Only one device type is supported: {devices}")
            device = devices[0]

            torch.manual_seed(seed)
            start_time = time.time()

            model_args: ModelArgs = ModelArgs(
                max_seq_len=max_seq_len,
                max_batch_size=max_batch_size,
                flash=False,
                **(params.__dict__),
            )

            logger.info("Creating model...")
            model = Transformer(model_args)
            logger.info(f"Loaded in {time.time() - start_time:.2f} seconds")

            model.eval()
            model.requires_grad_(False)

            rng = get_rng(str(hash_), 4)
            initialize_model_weights_from_rng(model, rng)
            init_time = time.time() - start_time

            logger.info(f"Model initialized in {init_time:.2f}s | {count_params(model)} params")

            set_default_dtype(device=device, dtype=dtype)
            model = model.to(device=device, dtype=dtype)

            logger.info("Wrapping model in ModelWrapper")
            model = ModelWrapper(model, devices=devices, stats=stats)
            logger.info(f"ModelWrapper created in {stats.model_load_time:.2f}s")

        return model
