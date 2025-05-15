import requests
from time import sleep


def wait_for_server(url, timeout=120, check_interval=3):
    for _ in range(timeout // check_interval):
        try:
            response = requests.get(url, timeout=check_interval)
            return response
        except requests.exceptions.RequestException:
            pass
        sleep(check_interval)
    raise requests.exceptions.RequestException(f"Server at {url} did not start in time")