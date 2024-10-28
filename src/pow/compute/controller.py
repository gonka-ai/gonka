import torch.multiprocessing as mp
import queue
import time
from multiprocessing import Event, Queue, Value
from typing import List, Iterator
from itertools import count
import torch

from pow.compute.compute import ProofBatch
from pow.compute.utils import Phase
from pow.compute.worker import Worker
from pow.models.utils import Params
from pow.utils import create_logger


logger = create_logger(__name__)


class Controller:
    def __init__(
        self,
        idx: int,
        params: Params,
        chain_hash: str,
        public_key: str,
        batch_size: int,
        r_target: float,
        devices: List[str],
        generator: Iterator[int],
        phase: Value,
        generated_batch_queue: Queue,
        validated_batch_queue: Queue,
        to_validate_batch_queue: Queue,
    ):
        ctx = mp.get_context("spawn")

        self.id = idx
        self.generated_batch_queue = generated_batch_queue
        self.to_validate_batch_queue = to_validate_batch_queue
        self.validated_batch_queue = validated_batch_queue
        self.phase = phase
        self.model_init_event = ctx.Event()
        self.devices = devices

        self.process = ctx.Process(
            target=self._worker_process,
            args=(
                self.id,
                self.phase,
                self.generated_batch_queue,
                self.to_validate_batch_queue,
                self.validated_batch_queue,
                self.model_init_event,
                params,
                chain_hash,
                public_key,
                batch_size,
                r_target,
                devices,
                generator,
            ),
            daemon=False,
        )

    def _worker_process(
        self,
        idx: int,
        phase: Value,
        generated_batch_queue: Queue,
        to_validate_batch_queue: Queue,
        validated_batch_queue: Queue,
        model_init_event: Event,
        params: Params,
        chain_hash: str,
        public_key: str,
        batch_size: int,
        r_target: float,
        devices: List[str],
        generator: Iterator[int],
    ):
        worker = Worker(
            idx,
            phase,
            generated_batch_queue,
            to_validate_batch_queue,
            validated_batch_queue,
            model_init_event,
            params,
            chain_hash,
            public_key,
            batch_size,
            r_target,
            devices,
            generator,
        )
        worker.run()

    def start(self):
        if not self.process.is_alive():
            self.process.start()
            time.sleep(1)

    def stop(self):
        self.process.join(timeout=10)
        if self.process.is_alive():
            logger.error("Worker process did not stop in time")
            self.process.terminate()
            self.process.join(timeout=30)

        if self.process.is_alive():
            logger.critical("Worker process did not stop in time")
            self.process.kill()

    def get_generated(self) -> List[ProofBatch]:
        return self.get_from_queue(self.generated_batch_queue)

    def get_validated(self) -> List[ProofBatch]:
        return self.get_from_queue(self.validated_batch_queue)

    @staticmethod
    def get_from_queue(q: Queue) -> List[ProofBatch]:
        batches = []
        while True:
            try:
                batch = q.get_nowait()
                batches.append(batch)
            except queue.Empty:
                break

        return batches

    def is_model_initialized(self) -> bool:
        return self.model_init_event.is_set()


class ParallelController:
    def __init__(
        self,
        params: Params,
        chain_hash: str,
        public_key: str,
        batch_size: int,
        r_target: float,
        devices: List[str] = None,
    ):
        ctx = mp.get_context("spawn")

        if devices is None:
            devices = self._get_all_torch_devices()

        self.phase = ctx.Value('i', Phase.IDLE)
        
        self.generated_batch_queue = ctx.Queue(maxsize=0)
        self.validated_batch_queue = ctx.Queue(maxsize=0)
        self.to_validate_batch_queue = ctx.Queue(maxsize=0)

        self.r_target = r_target
        self.params = params
        self.chain_hash = chain_hash
        self.public_key = public_key
        self.batch_size = batch_size

        self.controllers = [
            Controller(
                idx=idx,
                params=params,
                chain_hash=chain_hash,
                public_key=public_key,
                batch_size=batch_size,
                r_target=r_target,
                devices=device,
                generator=count(idx, len(devices)),
                phase=self.phase,
                generated_batch_queue=self.generated_batch_queue,
                validated_batch_queue=self.validated_batch_queue,
                to_validate_batch_queue=self.to_validate_batch_queue,
            )
            for idx, device in enumerate(devices)
        ]

    def set_phase(self, new_phase: int):
        self.phase.value = new_phase
        logger.info(f"Phase changed to: {new_phase}")

    def get_phase(self) -> int:
        return self.phase.value

    def is_running(self) -> bool:
        return any(controller.process.is_alive() for controller in self.controllers)

    def start_generate(self):
        self.set_phase(Phase.GENERATE)

    def stop_generate(self):
        self.set_phase(Phase.IDLE)

    def start_validate(self):
        self.set_phase(Phase.VALIDATE)

    def stop_validate(self):
        self.set_phase(Phase.IDLE)

    def start(self):
        for controller in self.controllers:
            controller.start()

    def stop(self):
        self.set_phase(Phase.STOP)
        for controller in self.controllers:
            controller.stop()

    def get_generated(self) -> List[ProofBatch]:
        all_generated = []
        for controller in self.controllers:
            all_generated.extend(controller.get_generated())
        return all_generated

    def get_validated(self) -> List[ProofBatch]:
        all_validated = []
        for controller in self.controllers:
            all_validated.extend(controller.get_validated())
        return all_validated

    def to_validate(self, batch: ProofBatch):
        self.to_validate_batch_queue.put(batch)

    def is_model_initialized(self) -> bool:
        return all(controller.is_model_initialized() for controller in self.controllers)

    def terminate(self):
        for controller in self.controllers:
            controller.process.terminate()

    @staticmethod
    def _get_all_torch_devices():
        if not torch.cuda.is_available():
            return [["cpu"]]

        all_devices = []
        for device_id in range(torch.cuda.device_count()):
            all_devices.append([f"cuda:{device_id}"])
        return all_devices
