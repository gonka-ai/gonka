import requests

from pow.models.utils import Params
from pow.compute.compute import ProofBatch


class ClientV1:
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

    def init(self, url, chain_hash, public_key, batch_size, r_target, params=Params()):
        return self._request("post", "/init", json={
            "url": url,
            "chain_hash": chain_hash,
            "public_key": public_key,
            "batch_size": batch_size,
            "r_target": r_target,
            "params": params.__dict__,
        })

    def init_generate(self, url, chain_hash, public_key, batch_size, r_target, params=None):
        if params is None:
            params = Params()
        return self._request("post", "/init-generate", json={
            "url": url,
            "chain_hash": chain_hash,
            "public_key": public_key,
            "batch_size": batch_size,
            "r_target": r_target,
            "params": params.__dict__,
        })

    def init_validate(self, url, chain_hash, public_key, batch_size, r_target, params=Params()):
        return self._request("post", "/init-validate", json={
            "url": url,
            "chain_hash": chain_hash,
            "public_key": public_key,
            "batch_size": batch_size,
            "r_target": r_target,
            "params": params.__dict__,
        })

    def validate(self, proof_batch: ProofBatch):
        return self._request("post", "/validate", json=proof_batch.__dict__)

    def start_generation(self):
        return self._request("post", "/start-generation")

    def start_validation(self):
        return self._request("post", "/start-validation")

    def status(self):
        return self._request("get", "/status")

    def stop(self):
        return self._request("post", "/stop")
