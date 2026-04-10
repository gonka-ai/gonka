from abc import (
    ABC,
    abstractmethod,
)
from concurrent.futures import Future, ThreadPoolExecutor
from typing import (
    List,
    Tuple,
    Optional,
)

import numpy as np
import torch

from pow.compute.utils import Stats
from pow.data import ProofBatch
from pow.compute.model_init import ModelWrapper
from pow.models.utils import Params
from pow.random import (
    get_inputs,
    get_permutations,
    get_target,
)


class BaseCompute(ABC):
    def __init__(
        self,
        public_key: str,
        devices,
    ):
        self.public_key = public_key
        self.devices = devices

    @abstractmethod
    def __call__(
        self, 
        nonces: List[int],
        public_key: str,
        target: np.ndarray,
        next_nonces: List[int] = None,
        use_cache: bool = False,
    ) -> Future[ProofBatch]:
        pass

    @abstractmethod
    def validate(
        self,
        proof_batch: ProofBatch,
    ) -> ProofBatch:
        pass


class Compute(BaseCompute):
    def __init__(
        self,
        params: Params,
        block_hash: str,
        block_height: int,
        public_key: str,
        r_target: float,
        devices: List[str],
        node_id: int,
    ):
        self.public_key = public_key
        self.block_hash = block_hash
        self.block_height = block_height
        self.r_target = r_target
        self.params = params
        self.stats = Stats()
        self.devices = devices
        
        self.model = ModelWrapper.build(
            hash_=self.block_hash,
            params=params,
            stats=self.stats.time_stats,
            devices=self.devices,
            max_seq_len=self.params.seq_len,
        )
        self.target = get_target(
            self.block_hash,
            self.params.vocab_size
        )
        self.node_id = node_id
        
        self.executor = ThreadPoolExecutor(max_workers=24)
        self.next_batch_future: Optional[Future] = None
        self.next_public_key: Optional[str] = None

        # if devices and "cuda" in devices[0]:
        #     available_devices = [f"cuda:{i}" for i in range(torch.cuda.device_count())]
        #     self.devices = [torch.device(d) for d in available_devices]
        # else:
        #     self.devices = [torch.device(d) for d in self.devices]
        
    def _prepare_batch(
        self,
        nonces: List[int],
        public_key: str,
        thread_batch_size: int = 256,
    ) -> Tuple[torch.Tensor, np.ndarray]:
        nonce_batches = [
            nonces[i : i + thread_batch_size]
            for i in range(0, len(nonces), thread_batch_size)
        ]

        def get_inputs_batch(nonce_batch):
            return get_inputs(
                self.block_hash,
                public_key,
                nonce_batch,
                dim=self.params.dim,
                seq_len=self.params.seq_len,
            )

        def get_permutations_batch(nonce_batch):
            return get_permutations(
                self.block_hash,
                public_key,
                nonce_batch,
                dim=self.params.vocab_size
            )

        with self.stats.time_stats.time_gen_inputs():
            inputs = self.executor.map(
                get_inputs_batch,
                nonce_batches
            )
            inputs = torch.cat(
                list(inputs),
                dim=0
            )

        with self.stats.time_stats.time_gen_perms():
            permutations = self.executor.map(
                get_permutations_batch,
                nonce_batches
            )
            permutations = np.concatenate(
                list(permutations),
                axis=0
            )
 
        return inputs, permutations

    def _process_batch(
        self,
        inputs: torch.Tensor,
        permutations: np.ndarray,
        target: np.ndarray,
        nonces: List[int],
        public_key: str = None,
    ) -> Future[ProofBatch]:
        if public_key is None:
            public_key = self.public_key

        self.stats.time_stats.next_iter()
        outputs = self.model(inputs, start_pos=0)
        with self.stats.time_stats.time_infer():
            with self.stats.time_stats.time_sync():
                for device in self.devices:
                    torch.cuda.synchronize(device=device)
            with self.stats.time_stats.time_numpy():
                outputs = outputs[:, -1, :].cpu().numpy()
        del inputs

        def get_batch(outputs):
            with self.stats.time_stats.time_perm():
                batch_indices = np.arange(outputs.shape[0])[:, None]
                outputs = outputs[batch_indices, permutations]

            with self.stats.time_stats.time_process():
                outputs = outputs / np.linalg.norm(outputs, axis=1, keepdims=True)
                distances = np.linalg.norm(
                    outputs - target,
                    axis=1
                )
                batch = ProofBatch(
                    public_key=public_key,
                    block_hash=self.block_hash,
                    block_height=self.block_height,
                    nonces=nonces,
                    dist=distances,
                    node_id=self.node_id,
                )

            return batch

        return self.executor.submit(
            get_batch,
            outputs
        )

    def __call__(
        self,
        nonces: List[int],
        public_key: str,
        target: np.ndarray,
        next_nonces: List[int] = None,
        use_cache: bool = False,
    ) -> Future[ProofBatch]:
        with self.stats.time_stats.time_total_gen():
            if (
                use_cache
                and self.next_batch_future is not None
                and self.next_public_key == public_key
            ):
                inputs, permutations = self.next_batch_future.result()
            else:
                inputs, permutations = self._prepare_batch(nonces, public_key)

            if next_nonces is not None:
                self.next_batch_future = self.executor.submit(
                    self._prepare_batch, next_nonces, public_key
                )
                self.next_public_key = public_key
            else:
                self.next_batch_future = None
                self.next_public_key = None

        return self._process_batch(inputs, permutations, target, nonces, public_key)

    def validate(
        self,
        proof_batch: ProofBatch,
    ) -> ProofBatch:
        nonces = proof_batch.nonces

        assert (
            proof_batch.block_hash == self.block_hash
        ), "Block hash must be the same as the one used to create the model"

        target_to_validate = get_target(proof_batch.block_hash, self.params.vocab_size)
        proof_batch = self(
            nonces=nonces,
            public_key=proof_batch.public_key,
            target=target_to_validate,
            next_nonces=None,
        ).result()
        return proof_batch
