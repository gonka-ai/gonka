import os
import urllib.parse
import pytest
from zeroband.service.client import TrainClient
from time import sleep
import toml
from common.wait import wait_for_server

def map_ports(addresses, start_port):
    port_map = {}
    ports = []
    for addr in addresses:
        if addr not in port_map:
            port_map[addr] = start_port
        else:
            port_map[addr] += 1
        ports.append(port_map[addr])
    return ports

def get_env_dictionaries(servers, master_server):
    env_dicts = []
    base_ports = map_ports(servers, 10001)
    for i, base_port in enumerate(base_ports):
        env_dict = {
            "GLOBAL_ADDR": master_server,
            "GLOBAL_PORT": "5565",
            "GLOBAL_RANK": str(i),
            "GLOBAL_UNIQUE_ID": str(i),
            "GLOBAL_WORLD_SIZE": str(len(servers)),
            "BASE_PORT": str(base_port)
        }
        env_dicts.append(env_dict)
    return env_dicts

def test_train_pipeline():
    MASTER_SERVER = os.getenv("SERVER_URL")
    master_server_host = urllib.parse.urlparse(MASTER_SERVER).hostname
    master_server_port = urllib.parse.urlparse(MASTER_SERVER).port
    scheme = urllib.parse.urlparse(MASTER_SERVER).scheme
    servers = [master_server_host]
    wait_for_server(MASTER_SERVER)

    client_ports = map_ports(servers, master_server_port)
    clients = [TrainClient(f"{scheme}://{server}:{port}") for server, port in zip(servers, client_ports)]

    train_config_dict = toml.load("/app/packages/train/resources/configs/1B_3090_1x1.toml")
    env_dicts = get_env_dictionaries(servers, master_server_host)

    for i, env_dict in enumerate(env_dicts):
        clients[i].start(train_config_dict, env_dict)

    sleep(120)

    for client in clients:
        client.stop()
