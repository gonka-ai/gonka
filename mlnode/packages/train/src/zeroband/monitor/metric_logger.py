import os
import pickle
from typing import Any, Protocol

import toml
import wandb
from filelock import FileLock

from zeroband.config import Config
from zeroband.utils.world_info import WorldInfo

class MetricLogger(Protocol):
    def __init__(self, project, config): ...

    def log(self, metrics: dict[str, Any]): ...

    def finish(self): ...


def prepare_config_for_wandb(config: Config, world_info: WorldInfo) -> dict[str, Any]:
    config = config.model_dump()
    config["world_info"] = world_info.json()
    return config


class WandbMetricLogger:
    def __init__(
        self,
        config: Config,
        world_info: WorldInfo,
        resume: bool
    ):
        self.world_info = world_info
        self.rank_zero = world_info.rank == 0
        if not self.rank_zero:
            return
        self.wandb_config = prepare_config_for_wandb(config, world_info)
        name = self.wandb_config.get("name", None)
        if name is not None:
            name = f"{name}::{world_info.global_rank + 1}/{world_info.global_world_size}"
        wandb.init(
            project=self.wandb_config.get("project", None),
            name=name,
            notes=self.wandb_config.get("notes", None),
            group=self.wandb_config.get("group", None),
            tags=self.wandb_config.get("tags", None),
            config=self.wandb_config,
            resume="auto" if resume else None
        )

        original_config_path = "config-original.toml"
        lock_path = original_config_path + ".lock"
        with FileLock(lock_path):
            with open(original_config_path, "w") as f:
                toml.dump(config.model_dump(), f)
                wandb.save(original_config_path)
            os.remove(original_config_path)

    def log(self, metrics: dict[str, Any]):
        if self.rank_zero:
            wandb.log(metrics)

    def finish(self):
        if self.rank_zero:
            wandb.finish()


class DummyMetricLogger:
    def __init__(
        self,
        config: Config,
        world_info: WorldInfo,
        *args,
        **kwargs
    ):
        self.world_info = world_info
        self.rank_zero = world_info.rank == 0
        if not self.rank_zero:
            return

        self.project = config.project
        self.config = prepare_config_for_wandb(config, world_info)  
        open(self.config["project"], "a").close()  # Create an empty file at the project path

        self.data = []

    def log(self, metrics: dict[str, Any]):
        if self.rank_zero:
            self.data.append(metrics)

    def finish(self):
        if self.rank_zero:
            with open(self.config["project"], "wb") as f:
                pickle.dump(self.data, f)
