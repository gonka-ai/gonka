import copy
import gc
import io
import multiprocessing
import os
import pathlib
import pickle
import shutil
import threading
import time
from dataclasses import dataclass
from typing import Any, Literal
import uuid

import fsspec
from fsspec.generic import rsync as rsync_fsspec
from pydantic import model_validator
from pydantic_config import BaseConfig
import torch
from torch import nn
from torch.distributed.checkpoint.stateful import Stateful
from torch.distributed._tensor.api import DTensor
from torch.optim import Optimizer
from torch.optim.lr_scheduler import LambdaLR
from torchdata.stateful_dataloader import StatefulDataLoader
import torch.distributed as dist

from zeroband.utils.logging import get_logger
from zeroband.utils.world_info import get_world_info
from zeroband.utils.state_dict_send_recv import _get_sendable_state_dict, recv_state_dict, send_state_dict, send_tensor_and_state_dict

## code inspired by torchtitan https://github.com/pytorch/torchtitan/blob/main/torchtitan/checkpoint.py


LOGGER = get_logger()


@dataclass
class TrainingProgress(Stateful):
    total_tokens: int
    outer_step: int
    step: int
    total_items: int

    def state_dict(self) -> dict[str, Any]:
        return {"total_tokens": self.total_tokens, "outer_step": self.outer_step, "step": self.step, "total_items": self.total_items}

    def load_state_dict(self, state_dict: dict[str, Any]) -> None:
        self.total_tokens = state_dict["total_tokens"]
        self.outer_step = state_dict["outer_step"]
        self.step = state_dict["step"]
        self.total_items = state_dict["total_item"]


class ModelWrapper(Stateful):
    def __init__(self, model: nn.Module) -> None:
        self.model = model

    def state_dict(self) -> dict[str, Any]:
        return self.model.state_dict()

    def load_state_dict(self, state_dict: dict[str, Any]) -> None:
        self.model.load_state_dict(state_dict)


class OptimizerWrapper(Stateful):
    def __init__(
        self,
        model: nn.Module,
        optim: torch.optim.Optimizer,
    ) -> None:
        self.model = model
        self.optim = optim

    def state_dict(self) -> dict[str, Any]:
        return self.optim.state_dict()

    def load_state_dict(self, state_dict: dict[str, Any]) -> None:
        self.optim.load_state_dict(state_dict)


class OuterOptimizerWrapper(Stateful):
    def __init__(self, optimizer: Optimizer) -> None:
        self.optimizer = optimizer

    def state_dict(self) -> dict[str, Any]:
        # Directly return the optimizer's state dict
        return self.optimizer.state_dict()

    def load_state_dict(self, state_dict: dict[str, Any]) -> None:
        # Load the optimizer's state dict directly
        self.optimizer.step()
        self.optimizer.load_state_dict(state_dict)



class CkptConfig(BaseConfig):
    path: str | None = None
    interval: int | None = None
    topk: int | None = None
    resume: str | None = None
    live_recovery_rank_src: int | None = None
    data_version: Literal["v1", "v2"] = "v2"
    data_path: str | None = None

    token_count: int | None = None

    @model_validator(mode="after")
    def validate_path_and_interval(self):
        if (self.path is None) != (self.interval is None):
            raise ValueError("path and interval must be both set or both None")

        return self
        return self


def non_error_barrier():
    logger = get_logger()
    try:
        dist.barrier()
    except Exception as e:
        logger.info(f"Error in data checkpointing barrier: {e}, continuing training")


class CkptManager:
    """Its name CkptManager because I (sami) always misstyped checkpoint.

    Checkpoints are saved in a folder with the following structure:
    ckpt_path/
        step_0/
            _0_0.pt
            _1_0.pt
            ...
        step_1/
            ...
    """

    states: dict[str, Stateful]

    def __init__(
        self,
        config: CkptConfig,
        model: nn.Module,
        optimizer: Optimizer,
        scheduler: LambdaLR,
        dataloader: StatefulDataLoader,
        training_progress: TrainingProgress,
        data_rank: int,
        diloco_offloaded_param_list: list[nn.Parameter] | None,
        diloco_offloaded_optimizer: Optimizer | None,
    ):
        self.config = config

        self.model = model
        self.optimizer = optimizer
        self.scheduler = scheduler
        self.dataloader = dataloader
        self.training_progress = training_progress
        self.data_rank = data_rank

        assert (diloco_offloaded_param_list is None) == (
            diloco_offloaded_optimizer is None
        ), "diloco_offloaded_model and diloco_offloaded_optimizer must be both None or both have values"

        self.diloco_offloaded_optimizer = diloco_offloaded_optimizer  # he we don't use Wrapper because it failed
        # which might make the ckpt less generic in term of loading from different number of device. FSDP ckpt seems to be a mess tho
        self.diloco_offloaded_param_list = diloco_offloaded_param_list

        self._init_state()

        self._logger = get_logger()
        self.world_info = get_world_info()

        self.non_blocking_process: list[multiprocessing.Process] = []
        self.blocking_process: list[multiprocessing.Process] = []
        self._live_reco_thread: threading.Thread | None = None

        if self.world_info.local_rank == 0:
            if self.config.path is not None:
                self.check_path_access(self.config.path)


        self._inner_optimizer_non_tensor_state_dict = None
        self._inner_optimizer_tensors = None

    def check_path_access(
        self,
        ckpt_path: str,
    ):
        rank = uuid.uuid4()
        dummy_file_path = os.path.join(ckpt_path, f".dummy_file_{rank}.txt")

        try:
            # Create the directory if it doesn't exist
            fs, _ = fsspec.core.url_to_fs(ckpt_path)
            fs.makedirs(ckpt_path, exist_ok=True)

            with fsspec.open(dummy_file_path, "w") as f:
                f.write("This is a dummy file for testing access.")
        except Exception as e:
            self._logger.error(f"Error checking path access {ckpt_path}: {e}, aborting training")
            raise e

    def _init_state(self):
        # states can only be stateful object, hence we need to wrap Model and Optimizer
        if type(self.model) == torch.nn.parallel.distributed.DistributedDataParallel:
            model = self.model.module
        else:
            model = self.model
        self.states: dict[str, Stateful] = {
            "model": model,
            "optimizer": OptimizerWrapper(model, self.optimizer),
            "scheduler": self.scheduler,
            # "dataloader": self.dataloader, # ignoring dataloader for now as each rank has its own dataloader
            "training_progress": self.training_progress,
        }

        # if self.diloco_offloaded_optimizer is not None:
        #     # even if the diloco_offloaded target the cpu list model, we still use the gpu model to load and save state.
        #     # main reason is that we actually don't a cpu model but just a list of cpu parameters.
        #     self.states["diloco_optimizer"] = self.diloco_offloaded_optimizer

    def save(self, minimum: bool = False) -> None:
        """
        Each rank will save the right shard of the model and optimizer.

        Saving is done inplace.

        Save in the subfolder `step_<step>`.

        """

        step_ckpt_path = os.path.join(self.config.path, f"step_{self.training_progress.step}")
        if minimum:
            step_ckpt_path = step_ckpt_path + f"_minimum"
        # if we are not in self recovery mode we save to disk
        if self.world_info.local_rank == 0 and self.world_info.global_rank == 0:
            time_start = time.perf_counter()
            self._save(step_ckpt_path)
            self._logger.info(f"Saved checkpoint to {step_ckpt_path} in {time.perf_counter() - time_start} seconds")
        non_error_barrier()
        return step_ckpt_path

    def _save(self, ckpt_path: str):
            self.wait_for_blocking_job()
            if type(self.model) == torch.nn.parallel.distributed.DistributedDataParallel:
                model_state_dict = self.model.module.state_dict()
            else:
                model_state_dict = self.model.state_dict()
            checkpoint = {
                "model_state_dict": model_state_dict,
                "optimizer_state_dict": self.optimizer.state_dict(),
                "scheduler_state_dict": self.scheduler.state_dict(),
                "training_progress": self.training_progress.state_dict(),
            }

            path = pathlib.Path(ckpt_path)
            path.mkdir(parents=True, exist_ok=True)
            with open(path / "checkpoint.pth", "wb") as f:
                torch.save(checkpoint, f)

            LOGGER.info('gc.collect()') 
            gc.collect()

    @staticmethod
    def save_data_v2(data_path: str, dataloader, local_rank: int):
        os.makedirs(data_path, exist_ok=True)
        with open(os.path.join(data_path, f"_{local_rank}.pt"), "wb") as f:
            state = {"data_loader": dataloader.state_dict()}
            torch.save(state, f)


    def wait_for_blocking_job(self):
        for process in self.blocking_process:
            process.join()

        self.blocking_process = []

        if self.world_info.local_rank == 0:
            if self.config.topk is not None:
                delete_topk(self.config.path, self.config.topk)

    def _del__(self):
        self.wait_for_blocking_job()

        for process in self.non_blocking_process:
            process.join()

    def _load_data(self, resume_ckpt_path: str):
        ## we have two formats to to save the dataloader:
        ## 1. v1: save the dataloader in the same file as the outer optimizer
        ## 2. v2: save the dataloader in a data folder inside the ckpt path
        self._logger.debug(f"loading data from {resume_ckpt_path}")
        world_info = get_world_info()

        if self.config.data_version == "v2":
            data_path = os.path.join(resume_ckpt_path, "data")

            if os.path.exists(os.path.join(data_path, f"_{world_info.local_rank}.pt")):
                with open(os.path.join(data_path, f"_{world_info.local_rank}.pt"), "rb") as f:
                    state = torch.load(f)
                    self.dataloader.load_state_dict(state["data_loader"])
                return
            else:
                self._logger.debug(f"Data version is v2 but data folder {data_path} does not exist. trying v1 loading")

        with open(os.path.join(resume_ckpt_path, f"__{world_info.local_rank}_0.pt"), "rb") as f:
            rank_state_dict = torch.load(f)

        try:
            self.dataloader.load_state_dict(rank_state_dict["data_loader"])
        except KeyError as e:
            self._logger.warning(
                "Data_loader state_dict is not found. You probably are loading a v2 ckpt with v1 dataloader. Aborting"
            )
            raise e

    def load(
        self,
        resume_ckpt_path: str,
        diloco_rank: int | None = None,
        data_path: str | None = None,
    ) -> None:
        time_start = time.perf_counter()

        world_info = get_world_info()
        if self.diloco_offloaded_param_list is not None:
            rank = diloco_rank if diloco_rank is not None else world_info.diloco_rank
            resume_ckpt_path = os.path.join(resume_ckpt_path, f"diloco_{rank}")
            if data_path is not None:
                data_path = os.path.join(data_path, f"diloco_{rank}")

        path = pathlib.Path(resume_ckpt_path)
        path.mkdir(parents=True, exist_ok=True)
        with open(path / "checkpoint.pth", "rb") as f:
            checkpoint = torch.load(f)

        self.model.module.load_state_dict(checkpoint["model_state_dict"])
        self.optimizer.load_state_dict(checkpoint["optimizer_state_dict"])
        self.scheduler.load_state_dict(checkpoint["scheduler_state_dict"])
        self.training_progress.load_state_dict(checkpoint["training_progress"])

        if self.config.token_count is not None:
            self.training_progress.total_tokens = self.config.token_count

        # Additional logic if needed

        self._logger.info(f"Loaded checkpoint from {resume_ckpt_path} in {time.perf_counter() - time_start} seconds")

    def recv_ckpt_from_peer(self, global_pg: dist.ProcessGroup):
        assert self.diloco_offloaded_param_list is not None, "recv_ckpt_from_peers is only supported with diloco"

        time_start = time.perf_counter()
        self._logger.debug(f"Start receiving ckpt from rank {self.config.live_recovery_rank_src}")

        jobs = []
        buffers = []
        for i, param in enumerate(self.diloco_offloaded_param_list):
            data = param.data  # Standard tensor

            buffer = torch.empty_like(data)
            buffers.append(buffer)
            jobs.append(global_pg.recv([buffer], self.config.live_recovery_rank_src, i))

        for job in jobs:
            job.wait()

        for buffer, param in zip(buffers, self.diloco_offloaded_param_list):
            data = param.data
            data.copy_(buffer)

        self._logger.debug("Live recovery progress: offloaded model received 1/5")

        # Receive optimizer state dict
        outer_opt_state_dict = recv_state_dict(
            global_pg, self.config.live_recovery_rank_src, self.diloco_offloaded_optimizer.state_dict()
        )
        self.diloco_offloaded_optimizer.load_state_dict(outer_opt_state_dict)

        self._logger.debug("Live recovery progress: outer optimizer state dict received 2/5")

        # Receive training progress
        training_process_state_dict = recv_state_dict(
            global_pg, self.config.live_recovery_rank_src, self.training_progress.state_dict()
        )
        self.training_progress.load_state_dict(training_process_state_dict)
        self._logger.debug("Live recovery progress: training progress state dict received 3/5")

        # Initialize gradients
        for group in self.optimizer.param_groups:
            for p in group["params"]:
                if p.grad is not None:
                    p.grad = torch.randn_like(p)

        # Step optimizer
        self.optimizer.step()
        self.optimizer.zero_grad()

        # Receive inner optimizer state dict
        inner_opt_state_dict = recv_state_dict(
            global_pg, self.config.live_recovery_rank_src, self.optimizer.state_dict()
        )
        self.optimizer.load_state_dict(inner_opt_state_dict)

        self._logger.debug("Live recovery progress: inner optimizer state dict received 4/5")

        # Receive scheduler state dict
        scheduler_state_dict = recv_state_dict(
            global_pg, self.config.live_recovery_rank_src, self.scheduler.state_dict()
        )
        self.scheduler.load_state_dict(scheduler_state_dict)

        self._logger.debug("Live recovery progress: scheduler state dict received 5/5")

        self._logger.debug(
            f"Received ckpt from rank {self.config.live_recovery_rank_src} in {time.perf_counter() - time_start} seconds"
        )

    def send_ckpt_to_peer(self, global_pg: dist.ProcessGroup, dest_rank: int):
        def async_send():
            assert self.diloco_offloaded_param_list is not None, "send_ckpt_to_peers is only supported with diloco"
            time_start = time.perf_counter()
            self._logger.debug(f"Start sending ckpt to rank {dest_rank}")

            try:
                # Send parameters
                jobs = []
                for i, param in enumerate(self.diloco_offloaded_param_list):
                    data = param.data  # Standard tensor
                    jobs.append(global_pg.send([data], dest_rank, i))

                for job in jobs:
                    job.wait()

                # Send optimizer state dict
                send_state_dict(global_pg, self.diloco_offloaded_optimizer.state_dict(), dest_rank)

                # Send training progress
                send_state_dict(global_pg, self.training_progress.state_dict(), dest_rank)

                send_tensor_and_state_dict(
                    global_pg, dest_rank, self._inner_optimizer_non_tensor_state_dict, self._inner_optimizer_tensors
                )

                send_state_dict(global_pg, self.scheduler.state_dict(), dest_rank)
            except RuntimeError as e:
                self._logger.error(f"Error sending ckpt to rank {dest_rank}: {e}")
            else:
                self._logger.debug(f"Sent ckpt to rank {dest_rank} in {time.perf_counter() - time_start} seconds")

        thread = threading.Thread(target=async_send)
        thread.start()
        self._logger.debug("Live recovery thread started")

        self._live_reco_thread = thread

    def cache_inner_optimizer(self):
        """
        Cache the inner optimizer to cpu and cast DTensor to local tensor to be ready to send.
        """
        if self._live_reco_thread is not None:
            self._logger.debug("Waiting for live recovery thread to finish")
            self._live_reco_thread.join()
            self._live_reco_thread = None
            self._logger.debug("Live recovery thread finished")

        _inner_optimizer_non_tensor_state_dict, _inner_optimizer_tensors = _get_sendable_state_dict(
            self.optimizer.state_dict()
        )
        self._inner_optimizer_tensors = [tensor.cpu() for tensor in _inner_optimizer_tensors]
        self._inner_optimizer_non_tensor_state_dict = copy.deepcopy(_inner_optimizer_non_tensor_state_dict)


def delete_topk(ckpt_path: str, topk: int):
    checkpoints_to_delete = get_checkpoints_to_delete(ckpt_path, topk)
    for ckpt_path in checkpoints_to_delete:
        shutil.rmtree(ckpt_path, ignore_errors=True)
    if len(checkpoints_to_delete) > 0:
        get_logger().info(f"Deleted {checkpoints_to_delete} checkpoints")


def get_checkpoints_to_delete(ckpt_path: str, topk: int) -> list[str]:
    checkpoints = [d for d in os.listdir(ckpt_path) if d.startswith("step_") and 'minimum' not in d]
    sorted_checkpoints = sorted(checkpoints, key=lambda x: int(x.split("_")[1]), reverse=True)
    return [os.path.join(ckpt_path, d) for d in sorted_checkpoints[topk:]]