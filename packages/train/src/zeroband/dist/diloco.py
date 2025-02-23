import re
import time
from functools import lru_cache

import torch
import torch.distributed as dist
from pydantic_config import BaseConfig
from torch import nn

from zeroband.dist.allreduce import all_reduce
from zeroband.dist.device_mesh import ElasticDeviceMesh
from zeroband.utils.logging import get_logger
from zeroband.utils.world_info import get_world_info


class DilocoConfig(BaseConfig):
    outer_lr: float = 0.7
    inner_steps: int

    retry_all_reduce: int = 3


@lru_cache(maxsize=None)
def _find_first_number(s: str) -> int:
    match = re.search(r"\d+", s)
    if match:
        return int(match.group())
    else:
        return -1


class Diloco:
    """
    This class implements the diloco algorithm from  https://arxiv.org/abs/2311.08105 and https://arxiv.org/abs/2407.07852.

    It handles the outer loop as well as the inter node communication.

    There is no VRAM overhead with this implementation as the model is outer optimizer is offloaded to cpu.
    All reduce communication are also done on cpu using GLOO.

    Example usage:

    # Example usage in a training loop:

    diloco = Diloco(config.diloco, model, elastic_device_mesh)

    for outer_step in range(num_outer_steps):
        for inner_step in range(config.diloco.inner_steps):
            # Regular inner training loop
                optimizer.zero_grad()
                loss = model(batch)
                loss.backward()
                optimizer.step()

        diloco.step(model)
    """

    def __init__(
        self,
        config: DilocoConfig,
        model: nn.Module,
        elastic_device_mesh: ElasticDeviceMesh,
    ):
        self.config = config
        self.elastic_device_mesh = elastic_device_mesh

        self._logger = get_logger()
        self.world_info = get_world_info()

        self._init_offloaded_optimizer(model=model)

    @torch.no_grad()
    def _init_offloaded_optimizer(self, model: nn.Module):
        self.param_list_cpu = self.get_offloaded_param(model)
        self.outer_optimizer = torch.optim.SGD(
            self.param_list_cpu, lr=self.config.outer_lr, momentum=0.9, nesterov=True
        )
        self._logger.debug("offload model to cpu")

    @torch.no_grad()
    def sync_pseudo_gradient(
        self, model: nn.Module, fake: bool = False, flag: str = "outer", num_effective_peers: int | None = None
    ):
        """
        Sync the pseudo gradient from the local process group to the global process group
        """
        _start_time = time.perf_counter()

        world_size_pre_init = self.elastic_device_mesh.global_pg.size()
        self.elastic_device_mesh.maybe_reinit_global_pg(admit_joiners=False)
        world_size_post_init = self.elastic_device_mesh.global_pg.size()

        if world_size_pre_init == world_size_post_init and num_effective_peers is not None:
            world_size = num_effective_peers
        else:
            world_size = world_size_post_init

        self._logger.debug("Sync pseudo gradient %s with world size %d", " fake" if fake else "", world_size)

        global_pg = self.elastic_device_mesh.global_pg
        for i in range(self.config.retry_all_reduce):
            for param_offloaded, (name, param) in zip(self.param_list_cpu, model.named_parameters()):
                if fake:
                    param_offloaded.grad.zero_()
                else:
                    param_offloaded.grad.copy_(param_offloaded.data)
                    param_offloaded.grad.sub_(param.data.to(param_offloaded.data.device))
            try:
                self.offloaded_grad_flat_tensor.div_(world_size)
                _collective_start_time = time.perf_counter()
                self._logger.debug("Waiting on barrier")
                self.elastic_device_mesh.monitored_barrier(flag)

                self._logger.debug("Beginning all reduce")
                for j, tensor_group in enumerate(self._offloaded_grad_grouped_tensor):
                    t0 = time.perf_counter()
                    all_reduce(tensor_group, dist.ReduceOp.SUM, global_pg)
                    self._logger.debug(
                        f"{j}/{len(self._offloaded_grad_grouped_tensor)} all reduce bucket done in {time.perf_counter() - t0:.6f} seconds, numel: {tensor_group.numel()}"
                    )

                self._logger.debug(
                    f"All reduce takes {time.perf_counter() - _collective_start_time:.6f} seconds numels: {self.offloaded_grad_flat_tensor.numel()}"
                )
                break
            except Exception as e:
                self._logger.error(f"Error syncing pseudo gradient: {e}, retry {i+1}/{self.config.retry_all_reduce}")
                global_pg = self.elastic_device_mesh.get_global_pg(maybe_reinit=True)
        else:
            self._logger.error(
                "Failed to sync pseudo gradient after %d retries. Resorting to calculating pseudo-gradient without reduce",
                self.config.retry_all_reduce,
            )
            for param_offloaded, param in zip(self.param_list_cpu, model.parameters()):
                if fake:
                    param_offloaded.grad.zero_()
                else:
                    param_offloaded.grad.copy_(param_offloaded.data)
                    param_offloaded.grad.sub_(param.data.to(param_offloaded.data.device))

        self._logger.info(f"Sync pseudo-gradient in {time.perf_counter() - _start_time:.6f} seconds")

    @torch.no_grad()
    def sync_inner_model(self, model: nn.Module):
        """
        Sync the inner model from the CPU outer model to GPU
        """
        self._logger.debug("Sync inner model")
        for param_offloaded, param in zip(self.param_list_cpu, model.parameters()):
            param.data.copy_(param_offloaded.data)

    @torch.no_grad()
    def get_offloaded_param(self, model: nn.Module) -> list[nn.Parameter]:
        """
        Offload the model parameters to CPU
        """
        param_items = [(name, param) for name, param in model.named_parameters() if param.requires_grad]
        numels = sum(param.numel() for _, param in param_items)

        self.offloaded_data_flat_tensor = torch.empty((numels,), device="cpu", dtype=torch.float32)
        self.offloaded_grad_flat_tensor = torch.zeros((numels,), device="cpu", dtype=torch.float32)
        current_offset = 0
        offloaded_params = []
        param_group_cutoff = []

        prev_id = None
        for name, param in param_items:
            if _find_first_number(name) != prev_id:
                param_group_cutoff.append(current_offset)
                prev_id = _find_first_number(name)

            target = param.data.detach()
            data_tensor = self.offloaded_data_flat_tensor.as_strided(target.size(), target.stride(), current_offset)
            grad_tensor = self.offloaded_grad_flat_tensor.as_strided(target.size(), target.stride(), current_offset)
            current_offset += data_tensor.numel()
            data_tensor.copy_(target)

            offloaded_param = nn.Parameter(data_tensor)
            offloaded_param.grad = grad_tensor
            offloaded_param.requires_grad = True
            offloaded_params.append(offloaded_param)

        param_group_cutoff.append(current_offset)

        # Group gradient tensors for all_reduce
        self._offloaded_grad_grouped_tensor = [
            self.offloaded_grad_flat_tensor.as_strided((j - i,), (1,), i)
            for i, j in zip(param_group_cutoff, param_group_cutoff[1:])
        ]
        return offloaded_params

    @torch.no_grad()
    def step(self, model: nn.Module, fake: bool = False, num_effective_peers: int | None = None, flag: str = "outer"):
        """
        Step the optimizer
        """
        time_start = time.perf_counter()
        self.sync_pseudo_gradient(model, fake=fake, flag=flag, num_effective_peers=num_effective_peers)
        self._logger.info(f"All reduce pseudo gradient in: {time.perf_counter() - time_start} seconds")

        if self.outer_optimizer is not None:
            self.outer_optimizer.step()

        self.sync_inner_model(model)