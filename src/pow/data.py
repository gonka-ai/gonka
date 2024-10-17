from dataclasses import dataclass, field
from textwrap import dedent
from typing import List, Dict

import numpy as np


@dataclass
class ProofBatch:
    public_key: str
    chain_hash: str
    nonces: List[int]
    dist: List[float]

    def sub_batch(
        self,
        r_target: float
    ) -> 'ProofBatch':
        """
        Returns a sub batch of the current batch
        where all distances are less than r_target
        """
        sub_nonces = []
        sub_dist = []
        for nonce, dist in zip(self.nonces, self.dist):
            if dist < r_target:
                sub_nonces.append(nonce)
                sub_dist.append(float(dist))
        return ProofBatch(
            public_key=self.public_key,
            chain_hash=self.chain_hash,
            nonces=sub_nonces,
            dist=sub_dist,
        )

    def __len__(
        self
    ) -> int:
        return len(self.nonces)

    def split(
        self,
        batch_size: int
    ) -> List['ProofBatch']:
        """
        Splits the current batch into sub batches of size batch_size
        """
        sub_batches = []
        for i in range(0, len(self.nonces), batch_size):
            sub_batch = ProofBatch(
                public_key=self.public_key,
                chain_hash=self.chain_hash,
                nonces=self.nonces[i:i+batch_size],
                dist=self.dist[i:i+batch_size]
            )
            sub_batches.append(sub_batch)

        assert len(self.nonces) == sum(
            [len(sub_batch) for sub_batch in sub_batches]
        ), "All nonces must be accounted for"

        return sub_batches

    def sort_by_nonce(
        self
    ) -> 'ProofBatch':
        idxs = np.argsort(self.nonces)
        return ProofBatch(
            public_key=self.public_key,
            chain_hash=self.chain_hash,
            nonces=np.array(self.nonces)[idxs].tolist(),
            dist=np.array(self.dist)[idxs].tolist()
        )

    @staticmethod
    def merge(
        proof_batches: List['ProofBatch']
    ) -> 'ProofBatch':
        if len(proof_batches) == 0:
            return ProofBatch.empty()

        chain_hashes = [proof_batch.chain_hash for proof_batch in proof_batches]
        assert len(set(chain_hashes)) == 1, \
            "All chain hashes must be the same"
        public_keys = [proof_batch.public_key for proof_batch in proof_batches]
        assert len(set(public_keys)) == 1, \
            "All public keys must be the same"
        all_nonces = []
        all_dist = []
        for proof_batch in proof_batches:
            all_nonces.extend(proof_batch.nonces)
            all_dist.extend(proof_batch.dist)

        return ProofBatch(
            public_key=proof_batches[0].public_key,
            chain_hash=proof_batches[0].chain_hash,
            nonces=all_nonces,
            dist=all_dist,
        )

    @staticmethod
    def empty() -> 'ProofBatch':
        return ProofBatch(
            public_key="",
            chain_hash="",
            nonces=[],
            dist=[]
        )

    def __str__(
        self
    ) -> str:
        return dedent(f"""\
        ProofBatch(
            public_key={self.public_key}, 
            chain_hash={self.chain_hash}, 
            nonces={self.nonces[:5]}, 
            dist={self.dist[:5]}, 
            length={len(self.nonces)}
        )""")


@dataclass
class InValidation:
    batch: ProofBatch
    nonce2valid_dist: Dict[int, float] = field(default_factory=dict)

    def process(
        self,
        batch: ProofBatch
    ):
        if batch.chain_hash != self.batch.chain_hash or batch.public_key != self.batch.public_key:
            return

        for n, dist in zip(batch.nonces, batch.dist):
            self.nonce2valid_dist[n] = dist

    def is_ready(
        self
    ) -> bool:
        return len(set(self.nonce2valid_dist.keys())) == len(set(self.batch.nonces))

    def validated(
        self,
        r_target: float
    ) -> 'ValidatedBatch':
        return ValidatedBatch(
            public_key=self.batch.public_key,
            chain_hash=self.batch.chain_hash,
            nonces=self.batch.nonces,
            received_dist=self.batch.dist,
            dist=[self.nonce2valid_dist[n] for n in self.batch.nonces],
            r_target=r_target
        )


@dataclass
class ValidatedBatch(ProofBatch):
    received_dist: List[float]
    r_target: float

    n_invalid: int = field(default=-1)

    def __post_init__(self):
        if self.n_invalid >= 0:
            return

        self.n_invalid = 0
        for received_dist, computed_dist in zip(self.received_dist, self.dist):
            assert received_dist < self.r_target, \
                "Received distance is greater than r_target"
            if computed_dist > self.r_target:
                self.n_invalid += 1

    @staticmethod
    def empty() -> 'ValidatedBatch':
        return ValidatedBatch(
            public_key="",
            chain_hash="",
            nonces=[],
            dist=[],
            received_dist=[],
            r_target=0.0
        )

    def __str__(self) -> str:
        return dedent(f"""\
        ValidatedBatch(
            public_key={self.public_key}, 
            chain_hash={self.chain_hash}, 
            nonces={self.nonces[:5]}..., 
            dist={self.dist[:5]}..., 
            received_dist={self.received_dist[:5]}..., 
            r_target={self.r_target},
            length={len(self.nonces)}
        )""")