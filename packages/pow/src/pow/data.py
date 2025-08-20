from dataclasses import dataclass, field
from textwrap import dedent
from typing import List, Dict

import numpy as np
from scipy.stats import binomtest

PROBABILITY_MISMATCH = 5e-4 #depends on target distance and hardware, 0.05 ~ 1 in 1000 appropriates

@dataclass
class ProofBatch:
    public_key: str
    block_hash: str
    block_height: int
    nonces: List[int]
    dist: List[float]
    node_id: int

    def __post_init__(self):
        """Initialize keys or perform custom initialization"""
        # Add your key initialization logic here
        # Example:
        # if not hasattr(self, '_initialized'):
        #     self._initialize_keys()
        #     self._initialized = True
        pass

    def _initialize_keys(self):
        """Custom key initialization method"""
        # Add your key initialization logic here
        pass

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
            block_hash=self.block_hash,
            block_height=self.block_height,
            nonces=sub_nonces,
            dist=sub_dist,
            node_id=self.node_id,
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
                block_hash=self.block_hash,
                block_height=self.block_height,
                nonces=self.nonces[i:i+batch_size],
                dist=self.dist[i:i+batch_size],
                node_id=self.node_id,
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
            block_hash=self.block_hash,
            block_height=self.block_height,
            nonces=np.array(self.nonces)[idxs].tolist(),
            dist=np.array(self.dist)[idxs].tolist(),
            node_id=self.node_id,
        )

    @staticmethod
    def merge(
        proof_batches: List['ProofBatch']
    ) -> 'ProofBatch':
        if len(proof_batches) == 0:
            return ProofBatch.empty()

        block_hashes = [proof_batch.block_hash for proof_batch in proof_batches]
        assert len(set(block_hashes)) == 1, \
            "All block hashes must be the same %s" % block_hashes
        block_heights = [proof_batch.block_height for proof_batch in proof_batches]
        assert len(set(block_heights)) == 1, \
            "All block heights must be the same %s" % block_heights
        public_keys = [proof_batch.public_key for proof_batch in proof_batches]
        assert len(set(public_keys)) == 1, \
            "All public keys must be the same %s" % public_keys
        all_nonces = []
        all_dist = []
        for proof_batch in proof_batches:
            all_nonces.extend(proof_batch.nonces)
            all_dist.extend(proof_batch.dist)

        return ProofBatch(
            public_key=proof_batches[0].public_key,
            block_hash=proof_batches[0].block_hash,
            block_height=proof_batches[0].block_height,
            nonces=all_nonces,
            dist=all_dist,
            node_id=proof_batches[0].node_id,
        )

    @staticmethod
    def empty() -> 'ProofBatch':
        return ProofBatch(
            public_key="",
            block_hash="",
            block_height=-1,
            nonces=[],
            dist=[],
            node_id=-1,
        )

    def __str__(
        self
    ) -> str:
        return dedent(f"""\
        ProofBatch(
            public_key={self.public_key}, 
            block_hash={self.block_hash}, 
            block_height={self.block_height},
            nonces={self.nonces[:5]}, 
            dist={self.dist[:5]}, 
            length={len(self.nonces)},
            node_id={self.node_id}
        )""")


@dataclass
class InValidation:
    batch: ProofBatch
    nonce2valid_dist: Dict[int, float] = field(default_factory=dict)

    def process(
        self,
        batch: ProofBatch
    ):
        if batch.block_hash != self.batch.block_hash or \
            batch.public_key != self.batch.public_key or \
                batch.block_height != self.batch.block_height:
            return

        for n, dist in zip(batch.nonces, batch.dist):
            self.nonce2valid_dist[n] = dist

    def is_ready(
        self
    ) -> bool:
        return all(n in self.nonce2valid_dist for n in self.batch.nonces)

    def validated(
        self,
        r_target: float,
        fraud_threshold: float
    ) -> 'ValidatedBatch':
        return ValidatedBatch(
            public_key=self.batch.public_key,
            block_hash=self.batch.block_hash,
            block_height=self.batch.block_height,
            nonces=self.batch.nonces,
            received_dist=self.batch.dist,
            dist=[self.nonce2valid_dist[n] for n in self.batch.nonces],
            r_target=r_target,
            fraud_threshold=fraud_threshold,
            node_id=self.batch.node_id,
        )


@dataclass
class ValidatedBatch(ProofBatch):
    received_dist: List[float]
    r_target: float
    fraud_threshold: float
    
    n_invalid: int = field(default=-1)
    probability_honest: float = field(default=-1.0)
    fraud_detected: bool = field(default=False)

    def __post_init__(self):
        if self.n_invalid >= 0:
            return

        self.n_invalid = 0
        self.probability_honest = 1.0
        for received_dist, computed_dist in zip(self.received_dist, self.dist):
            assert received_dist < self.r_target, \
                "Received distance is greater than r_target"
            if computed_dist > self.r_target:
                self.n_invalid += 1

        if len(self) > 0:
            self.probability_honest = float(
                binomtest(
                    k=self.n_invalid,
                    n=len(self),
                    p=PROBABILITY_MISMATCH,
                    alternative='greater'
                ).pvalue
            )  # computes P(that p_invalid is < p_honest mismatch)
            self.fraud_detected = bool(self.probability_honest < self.fraud_threshold)

    @staticmethod
    def empty() -> 'ValidatedBatch':
        return ValidatedBatch(
            public_key="",
            block_hash="",
            block_height=-1,
            nonces=[],
            dist=[],
            received_dist=[],
            r_target=0.0,
            fraud_threshold=0.0,
            fraud_detected=False,
            node_id=-1,
        )

    def __str__(self) -> str:
        return dedent(f"""\
        ValidatedBatch(
            public_key={self.public_key}, 
            block_hash={self.block_hash}, 
            block_height={self.block_height},
            nonces={self.nonces[:5]}..., 
            dist={self.dist[:5]}..., 
            received_dist={self.received_dist[:5]}..., 
            r_target={self.r_target},
            fraud_threshold={self.fraud_threshold},
            length={len(self.nonces)},
            fraud_detected={self.fraud_detected},
            node_id={self.node_id}
        )""")
