import requests

class TrainClient:
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

    def start(
        self,
        train_config_dict: dict, 
        train_env_dict: dict = None
        ):
        train_dict = {
            "train_config": train_config_dict,
            "train_env": train_env_dict
        }
        return self._request("post", "/train/start", json=train_dict)

    def stop(self):
        return self._request("post", "/train/stop")

    def status(self):
        return self._request("get", "/train/status")