import requests

from pow.models.utils import Params
from pow.compute.compute import ProofBatch

from pow.service.manager import PowInitRequestUrl

class PowClient:
    def __init__(self, base_url):
        self.base_url = base_url

    def _request(self, method, endpoint, json=None):
        url = f"{self.base_url}/api/v1{endpoint}"
        response = getattr(requests, method)(url, json=json)
        try:
            response.raise_for_status()
        except requests.HTTPError as e:
            print(f"HTTP Error: {e}")
            print(f"Response content: {response.text}")
            raise
        return response.json()

    def init(
        self,
        url,
        block_hash,
        block_height,
        public_key,
        batch_size,
        r_target,
        fraud_threshold,
        params=Params()
    ):
        return self._request("post", "/pow/init", json={
            "url": url,
            "block_hash": block_hash,
            "block_height": block_height,
            "r_target": r_target,
            "fraud_threshold": fraud_threshold,
            "params": params.__dict__,
        })

    def init_generate(
        self,
        node_id,
        node_count,
        url,
        block_hash,
        block_height,
        public_key,
        batch_size,
        r_target,
        fraud_threshold,
        params=None
    ):
        if params is None:
            params = Params()
        return self._request("post", "/pow/init/generate", json={
            "node_id": node_id,
            "node_count": node_count,
            "url": url,
            "block_hash": block_hash,
            "block_height": block_height,
            "public_key": public_key,
            "batch_size": batch_size,
            "r_target": r_target,
            "fraud_threshold": fraud_threshold,
            "params": params.__dict__,
        })

    def init_validate(
        self,
        url,
        block_hash,
        block_height,
        public_key,
        batch_size,
        r_target,
        fraud_threshold,
        params=Params()
    ):
        return self._request("post", "/pow/init/validate", json={
            "url": url,
            "block_hash": block_hash,
            "block_height": block_height,
            "r_target": r_target,
            "fraud_threshold": fraud_threshold,
            "params": params.__dict__,
        })

    def validate(self, proof_batch: ProofBatch):
        return self._request("post", "/pow/validate", json=proof_batch.__dict__)

    def start_generation(self):
        return self._request("post", "/pow/phase/generate")

    def start_validation(self):
        return self._request("post", "/pow/phase/validate")

    def status(self):
        return self._request("get", "/pow/status")

    def stop(self):
        return self._request("post", "/pow/stop")
