import queue
import time
import multiprocessing
from collections import defaultdict

from concurrent.futures import Future
from multiprocessing import Event, Queue, Value
from typing import List, Iterator

from pow.data import ProofBatch
from pow.compute.compute import Compute
from pow.compute.utils import Phase
from pow.models.utils import Params
from common.logger import create_logger


logger = create_logger(__name__)


class Worker(multiprocessing.Process):
    def __init__(
        self,
        idx: int,
        phase: Value,
        generated_batch_queue: Queue,
        to_validate_batch_queue: Queue,
        validated_batch_queue: Queue,
        model_init_event: Event,
        params: Params,
        block_hash: str,
        block_height: int,
        public_key: str,
        batch_size: int,
        r_target: float,
        devices: List[str],
        generator: Iterator[int],
        node_id: int,
    ):
        super().__init__()
        self.phase = phase
        self.generated_batch_queue = generated_batch_queue
        self.to_validate_batch_queue = to_validate_batch_queue
        self.validated_batch_queue = validated_batch_queue
        self.model_init_event = model_init_event
        self.params = params
        self.block_hash = block_hash
        self.block_height = block_height
        self.public_key = public_key
        self.batch_size = batch_size
        self.r_target = r_target
        self.devices = devices
        self.generator = generator
        self.id = idx
        self.node_id = node_id
        self.compute: Compute = None
        self.interrupt_flag = False
        self.exception = None
        self.last_report_time = 0  # Track last time debug was printed

    def run(self):
        self.compute = Compute(
            params=self.params,
            block_hash=self.block_hash,
            block_height=self.block_height,
            public_key=self.public_key,
            r_target=self.r_target,
            devices=self.devices,
            node_id=self.node_id,
        )
        self.model_init_event.set()
        logger.info(f"[{self.id}] Worker initiated and models are created")

        while True:
            current_phase = self.phase.value

            if current_phase == Phase.STOP:
                logger.info(f"[{self.id}] Stopping worker")
                break
            elif current_phase == Phase.GENERATE:
                self._generate()
            elif current_phase == Phase.VALIDATE:
                self._validate()
            else:
                time.sleep(0.01)

        logger.info(f"[{self.id}] Worker stopped.")
        self.cleanup()

    def _generate(self):
        logger.info(f"[{self.id}] Starting generate phase")
        self.compute.stats.reset()
        next_nonces = [next(self.generator) for _ in range(self.batch_size)]
        batch: Future = None

        while not self.is_stopped(Phase.GENERATE):
            if self.exception is not None:
                raise self.exception

            if self.interrupt_flag:
                break

            nonces = next_nonces
            next_nonces = [next(self.generator) for _ in range(self.batch_size)]
            logger.debug(f"[{self.id}] Generated batch: {nonces}")

            batch = self.compute(
                nonces=nonces,
                public_key=self.public_key,
                target=self.compute.target,
                next_nonces=next_nonces,
            )
            batch.add_done_callback(self._process_result)
            
            current_time = time.time()
            if current_time - self.last_report_time >= 15.0:
                if self.compute.stats.total_valid_nonces > 0:
                    stats_report = self.compute.stats.report(detailed=False, worker_id=self.id)
                    logger.info(stats_report)
                self.last_report_time = current_time

    def _process_result(self, future: Future):
        try:
            with self.compute.stats.time_stats.time_process():
                proof_batch = future.result()
                filtered_batch = proof_batch.sub_batch(self.r_target)
                self.compute.stats.count_batch(proof_batch, filtered_batch)

                if filtered_batch.nonces:
                    try:
                        self.generated_batch_queue.put(filtered_batch, timeout=10)
                    except (BrokenPipeError, EOFError, IOError, TimeoutError):
                        logger.error(f"[{self.id}] Failed to put generated batch")
                        self.interrupt_flag = True
                        return

                logger.debug(f"[{self.id}] Generated batch: {filtered_batch}")
                logger.debug(f"[{self.id}] {self.compute.stats.report(detailed=True)}")
        except Exception as e:
            if self.is_stopped(Phase.GENERATE):
                return

            logger.error(f"Exception during batch processing: {e}")
            self.interrupt_flag = True
            self.exception = e

    def _prepare_next_batch(
        self,
        q: Queue,
        max_wait_time: float = 1.
    ):
        start_time = time.time()

        batches_to_process = []
        while (time.time() - start_time) < max_wait_time:
            try:
                batch = q.get_nowait()
                batches_to_process.append(batch)
            except queue.Empty:
                break

        batches = []
        for batch in batches_to_process:
            batches.extend(batch.split(self.batch_size))

        return batches

    def _validate(self):
        logger.info(f"[{self.id}] Starting validate phase")
        while not self.is_stopped(Phase.VALIDATE):
            merged_batches = self._prepare_next_batch(self.to_validate_batch_queue)

            if not merged_batches:
                #TODO check later for a better way to do this
                time.sleep(0.01)
                continue

            for idx, batch in enumerate(merged_batches):
                logger.info(f"[{self.id}] Validating batch {idx} / {len(merged_batches)}")
                try:
                    validated_batch = self.compute.validate(batch)
                    logger.info(f"[{self.id}] Validated batch: {validated_batch}")
                    self.validated_batch_queue.put(validated_batch, timeout=10)
                except Exception as e:
                    logger.error(f"[{self.id}] Validation failed: {e}\n{batch}")

        logger.info(f"[{self.id}] Validation phase stopped")

    def is_stopped(self, current_phase):
        return self.phase.value != current_phase

    def cleanup(self):
        del self.compute
