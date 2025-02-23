import requests
from time import sleep

server_url = "http://xj7-5.s.filfox.io:19234"

response = requests.post(f"{server_url}/api/v1/mlnode/state")
assert response.status_code == 200
response_data = response.json()
print(response_data)

sleep(10)
response = requests.post(f"{server_url}/api/v1/mlnode/stop")
assert response.status_code == 200
response_data = response.json()
print(response_data)