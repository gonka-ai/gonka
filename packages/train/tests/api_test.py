from zeroband.service.client import TrainClient
from time import sleep
import toml

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

master_server = "0.0.0.0"
servers = ["0.0.0.0"]

client_ports = map_ports(servers, 8080)
clients = [TrainClient(f"http://{server}:{port}") for server, port in zip(servers, client_ports)]

train_config_dict = toml.load("/app/mlnode/packages/train/configs/1B_3090_1x1.toml")
env_dicts = get_env_dictionaries(servers, master_server)
for i, env_dict in enumerate(env_dicts):
    clients[i].start(train_config_dict, env_dict)
sleep(120)
for client in clients:
    client.stop()