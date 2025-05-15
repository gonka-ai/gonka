import requests


class InferenceClient:
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
    
    def inference_setup(self, model, dtype, additional_args=[]):
        return self._request("post", "/inference/up", json={
            "model": model,
            "dtype": dtype,
            "additional_args": additional_args,
        })

    def inference_down(self):
        return self._request("post", "/inference/down")
