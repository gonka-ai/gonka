from typing import Literal
from pydantic import model_validator

from zeroband.data.loader import DataConfig
from zeroband.monitor.checkpoint import CkptConfig
from zeroband.dist.diloco import DilocoConfig
from pydantic_config import BaseConfig


class OptimConfig(BaseConfig):
    lr: float = 4e-4
    weight_decay: float = 0.1
    adam_betas1: float = 0.9
    adam_betas2: float = 0.95

    sched_type: Literal["cosine", "linear", "wsd-sqrt"] = "cosine"
    warmup_steps: int = 1000
    stable_steps: int = 80_000
    total_steps: int = 88_000
    batch_size: int = 512



class TrainConfig(BaseConfig):
    micro_bs: int
    reshard_after_forward: bool = True  # old shard grad op True mean full shard
    reduce_fp32: bool = False  # should be True if SXM. Keep to false as default for backward compatibility
    eval_interval: int = 200
    attn_fn: Literal["flash", "sdpa"] = "flash"


class Config(BaseConfig):
    # main config

    project: str = "zeroband"
    run_id: str | None = None
    metric_logger_type: Literal["wandb", "dummy"] = "wandb"
    wandb_resume: bool = False

    name: str | None = None
    description: str | None = None
    tags: list[str] | None = []
    group: str | None = None

    # sub config
    diloco: DilocoConfig | None = None
    data: DataConfig = DataConfig()
    optim: OptimConfig = OptimConfig()
    train: TrainConfig

    ckpt: CkptConfig = CkptConfig()

    @model_validator(mode="after")
    def ckpt_diloco_step(self):
        if self.ckpt is not None and self.ckpt.interval is not None and self.diloco is not None:
            assert (
                self.ckpt.interval % self.diloco.inner_steps == 0
            ), "ckpt interval must be a multiple of diloco inner steps as we only save at the end of an outer step"
        return self
