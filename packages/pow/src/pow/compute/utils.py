import time
import itertools
from contextlib import contextmanager
from textwrap import dedent
from dataclasses import dataclass
from pow.data import ProofBatch
from common.logger import create_logger


logger = create_logger(__name__)


class Phase:
    IDLE = 0
    GENERATE = 1
    VALIDATE = 2
    STOP = 3


class TimeStats:
    def __init__(self):
        self.start = time.time()
        self.total_gen_inputs_time = 0
        self.total_gen_perms_time = 0
        self.total_infer_time = 0
        self.total_perm_time = 0
        self.total_to_cuda_time = 0
        self.total_gen_time = 0
        self.total_process_time = 0
        self.model_load_time = 0
        self.total_numpy_time = 0
        self.total_sync_time = 0
        self.n_iter = 0
    @contextmanager
    def time_gen_inputs(self):
        start_time = time.time()
        try:
            yield
        finally:
            self.total_gen_inputs_time += time.time() - start_time

    @contextmanager
    def time_gen_perms(self):
        start_time = time.time()
        try:
            yield
        finally:
            self.total_gen_perms_time += time.time() - start_time

    @contextmanager
    def time_total_gen(self):
        start_time = time.time()
        try:
            yield
        finally:
            self.total_gen_time += time.time() - start_time

    @contextmanager
    def time_to_cuda(self):
        start_time = time.time()
        try:
            yield
        finally:
            self.total_to_cuda_time += time.time() - start_time

    @contextmanager
    def time_infer(self):
        start_time = time.time()
        try:
            yield
        finally:
            self.total_infer_time += time.time() - start_time

    @contextmanager
    def time_perm(self):
        start_time = time.time()
        try:
            yield
        finally:
            self.total_perm_time += time.time() - start_time

    @contextmanager
    def time_process(self):
        start_time = time.time()
        try:
            yield
        finally:
            self.total_process_time += time.time() - start_time

    @contextmanager
    def time_model_load(self):
        start_time = time.time()
        try:
            yield
        finally:
            self.model_load_time += time.time() - start_time

    @contextmanager
    def time_numpy(self):
        start_time = time.time()
        try:
            yield
        finally:
            self.total_numpy_time += time.time() - start_time
    
    @contextmanager
    def time_sync(self):
        start_time = time.time()
        try:
            yield
        finally:
            self.total_sync_time += time.time() - start_time

    def next_iter(self):
        self.n_iter += 1

    def __str__(self) -> str:
        return dedent(
            f"""\
        TimeStats(
            [SYNC]total_gen={self.total_gen_time:.2f},
            [SYNC]to_cuda={self.total_to_cuda_time:.2f},
            [SYNC]infer={(self.total_infer_time):.2f} | {self.total_infer_time / self.n_iter:.2f} per iter,
                sync={self.total_sync_time:.2f},
                to_numpy={self.total_numpy_time:.2f},
            [ASYNC]gen_inputs={self.total_gen_inputs_time:.2f},
            [ASYNC]gen_perms={self.total_gen_perms_time:.2f},
            [ASYNC]perm={self.total_perm_time:.2f},
            [ASYNC]process={self.total_process_time:.2f},
            model_load={self.model_load_time:.2f}
        )"""
        )


class Stats:
    def __init__(
        self,
        time_stats: TimeStats = TimeStats()
    ):
        self.time_stats = time_stats
        self.total_checked_nonces = 0
        self.total_valid_nonces = 0
        self.total_time = 0

    def reset(
        self
    ):
        self.total_checked_nonces = 0
        self.total_valid_nonces = 0
        self.start_time = time.time()
        self.total_time = 0

    def update_time(self):
        if hasattr(self, 'start_time'):
            self.total_time = time.time() - self.start_time

    def count_batch(
        self,
        batch: ProofBatch,
        valid: ProofBatch
    ):
        self.total_checked_nonces += len(batch.nonces)
        self.total_valid_nonces += len(valid.nonces)
        self.update_time()

    def report(
        self,
        detailed: bool = False,
        worker_id: int = None
    ) -> str:
        time_rate = 0
        if self.total_time > 0:
            time_rate = self.total_valid_nonces / self.total_time
        success_rate = 0
        if self.total_valid_nonces > 0:
            success_rate = self.total_checked_nonces / self.total_valid_nonces
        
        raw_rate = 0
        if self.total_time > 0:
            raw_rate = self.total_checked_nonces / self.total_time * 60
        
        worker_prefix = f"[{worker_id}] " if worker_id is not None else ""
        report = f"{worker_prefix}Generated: {self.total_valid_nonces} / {self.total_checked_nonces} (1 in {success_rate:.0f}) Time: {self.total_time/ 60:.2f}min ({time_rate * 60:.2f} valid/min, {raw_rate:.2f} raw/min)"
        
        if detailed:
            report += "\n" + str(self.time_stats)
        return report


@dataclass
class NonceIterator:
    node_id: int
    n_nodes: int
    group_id: int
    n_groups: int
    _current_x: int = 0

    def __iter__(self):
        return self

    def __next__(self):
        offset = self.node_id + self.group_id * self.n_nodes
        step = self.n_groups * self.n_nodes
        value = offset + self._current_x * step
        self._current_x += 1
        return value