import pytest
import requests
from time import sleep
import os

from common.wait import wait_for_server


def test_mlnode():
    server_url = os.getenv("SERVER_URL")
    
    wait_for_server(os.getenv("SERVER_URL"))
    response = requests.get(f"{server_url}/api/v1/state")
    assert response.status_code == 200
    print(response.json())

    sleep(10)

    response = requests.post(f"{server_url}/api/v1/stop")
    assert response.status_code == 200
    print(response.json())
