import time
import requests
from requests.exceptions import RequestException
from typing import List
from multiprocessing import Process, Queue, Event

from pow.data import (
    ProofBatch,
    ValidatedBatch,
    InValidation,
)
from pow.compute.controller import (
    Controller,
    Phase,
)
from common.logger import create_logger

logger = create_logger(__name__)


class Sender(Process):
    def __init__(
        self,
        url: str,
        generation_queue: Queue,
        validation_queue: Queue,
        phase: Phase,
        r_target: float,
        fraud_threshold: float,
    ):
        super().__init__()
        self.url = url
        self.phase = phase
        self.generation_queue = generation_queue
        self.validation_queue = validation_queue
        self.in_validation_queue = Queue()
        self.r_target = r_target
        self.fraud_threshold = fraud_threshold

        self.in_validation: List[InValidation] = []
        self.generated_not_sent: List[ProofBatch] = []
        self.validated_not_sent: List[ValidatedBatch] = []
        self.stop_event = Event()
        
        # Counters for validation statistics
        self.total_validated_batches = 0
        self.total_fraud_detected = 0

    def _send_generated(self):
        if not self.generated_not_sent:
            return

        try:
            merged_batch = ProofBatch.merge(self.generated_not_sent)
        except AssertionError as e:
            logger.error(f"Failed to merge batches: {e}. Batches have different block_hash/block_height/public_key")
            # Clear the list to prevent infinite retry loop, this should not happen in normal operation as all batches come from the same epoch
            self.generated_not_sent = []
            return

        try:
            logger.info(f"Sending merged generated batch to {self.url} (merged {len(self.generated_not_sent)} batches, {len(merged_batch.nonces)} nonces)")
            response = requests.post(
                f"{self.url}/generated",
                json=merged_batch.__dict__,
            )
            response.raise_for_status()
            logger.info(f"Successfully sent merged generated batch ({len(merged_batch.nonces)} nonces)")
            # Clear the list on success
            self.generated_not_sent = []
        except RequestException as e:
            # Keep all batches for retry
            logger.error(f"Error sending merged generated batch to {self.url}: {e}")

    def _send_validated(self):
        if not self.validated_not_sent:
            return

        failed_batches = []

        for batch in self.validated_not_sent:
            try:
                logger.info(f"Sending validated batch to {self.url}")
                response = requests.post(
                    f"{self.url}/validated",
                    json=batch.__dict__,
                )
                response.raise_for_status()
                logger.info("Successfully sent validated batch")
            except RequestException as e:
                failed_batches.append(batch)
                logger.error(f"Error sending validated batch to {self.url}: {e}")

        self.validated_not_sent = failed_batches

    def _get_generated(self) -> ProofBatch:
        batches = [
            ProofBatch.merge(
                Controller.get_from_queue(self.generation_queue)
            )
        ]
        return ProofBatch.merge(batches)

    def _get_validated(self) -> List[ValidatedBatch]:
        batches = Controller.get_from_queue(self.validation_queue)
        in_validation = self._get_in_validation()
        for batch in batches:
            for in_val in in_validation:
                in_val.process(batch)

        in_validation_ready = [
            in_val.validated(self.r_target, self.fraud_threshold)
            for in_val in in_validation
            if in_val.is_ready()
        ]
        
        for validated_batch in in_validation_ready:
            self.total_validated_batches += 1
            if validated_batch.fraud_detected:
                self.total_fraud_detected += 1
            
            logger.info(f"{validated_batch}")
            logger.info(f"Stats: {self.total_validated_batches} validated, {self.total_fraud_detected} fraud")
        
        return in_validation_ready

    def _get_in_validation(self) -> List[InValidation]:
        batches = Controller.get_from_queue(self.in_validation_queue)
        batches = [
            InValidation(batch)
            for batch in batches
        ]
        self.in_validation.extend(batches)
        return self.in_validation

    def run(self):
        logger.info("Sender started")
        while not self.stop_event.is_set():
            try:
                if self.phase.value == Phase.GENERATE:
                    generated = self._get_generated()
                    if len(generated) > 0:
                        self.generated_not_sent.append(generated)
                    self._send_generated()

                elif self.phase.value == Phase.VALIDATE:
                    self.validated_not_sent.extend(self._get_validated())
                    self.in_validation = [
                        b for b in self.in_validation
                        if not b.is_ready()
                    ]
                    self._send_validated()
            except Exception as e:
                logger.error(f"Error in sender loop: {e}", exc_info=True)

            time.sleep(5)
        logger.info("Sender stopped")

    def stop(self):
        self.stop_event.set()
