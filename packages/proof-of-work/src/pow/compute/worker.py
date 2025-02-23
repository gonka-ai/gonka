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
from pow.utils import create_logger


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
        self.compute: Compute = None
        self.interrupt_flag = False
        self.exception = None

    def run(self):
        self.compute = Compute(
            params=self.params,
            block_hash=self.block_hash,
            block_height=self.block_height,
            public_key=self.public_key,
            r_target=self.r_target,
            devices=self.devices,
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

            batch = self.compute(
                nonces=nonces,
                public_key=self.public_key,
                target=self.compute.target,
                next_nonces=next_nonces,
            )
            # Not checking if prev call is done. Hope it's done during inference.
            batch.add_done_callback(self._process_result)

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
        grouped_batches = defaultdict(list)
        batch_sizes = defaultdict(int)
        start_time = time.time()

        while (time.time() - start_time) < max_wait_time:
            try:
                batch = q.get_nowait()
                grouped_batches[batch.public_key].append(batch)
                batch_sizes[batch.public_key] += len(batch.nonces)
                
                if any(size >= self.batch_size for size in batch_sizes.values()):
                    break
            except queue.Empty:
                break

        merged_batches = []
        for _, batches in grouped_batches.items():
            merged_batch = ProofBatch.merge(batches)
            merged_batches.extend(
                merged_batch.split(self.batch_size)
            )

        return merged_batches

    def _validate(self):
        logger.info(f"[{self.id}] Starting validate phase")
        while not self.is_stopped(Phase.VALIDATE):
            merged_batches = self._prepare_next_batch(self.to_validate_batch_queue)

            if not merged_batches:
                #TODO check later for a better way to do this
                time.sleep(0.01)
                continue

            for idx, batch in enumerate(merged_batches):
                logger.debug(f"[{self.id}] Validating batch {idx} / {len(merged_batches)}")
                try:
                    validated_batch = self.compute.validate(batch)
                    logger.debug(f"[{self.id}] Validated batch: {validated_batch}")
                    self.validated_batch_queue.put(validated_batch, timeout=10)
                except Exception as e:
                    logger.error(f"[{self.id}] Validation failed: {e}\n{batch}")

        logger.info(f"[{self.id}] Validation phase stopped")

    def is_stopped(self, current_phase):
        return self.phase.value != current_phase

    def cleanup(self):
        del self.compute
