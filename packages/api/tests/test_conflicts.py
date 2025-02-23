from time import sleep
import toml
from datetime import datetime
import hashlib
import requests
from pow.models.utils import Params

from pow.service.client import PowClient
from inference.client import InferenceClient
from zeroband.service.client import TrainClient


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


#variables
# batch_reciever_url = "http://vn2-2.s.filfox.io:19002"
# server_url = "http://xj7-5.s.filfox.io:19234"
SERVER_IP = "xj7-5.s.filfox.io"
SERVER_PORT = 19234
SERVER_URL = f"http://{SERVER_IP}:{SERVER_PORT}"
BATCH_RECIEVER_URL = "http://vn2-2.s.filfox.io:19002"


response = requests.post(f"{SERVER_URL}/api/v1/mlnode/stop")


date_str = datetime.now().strftime('%Y-%m-%d_%H-%M-%S')
block_hash = hashlib.sha256(date_str.encode()).hexdigest()
public_key = f"pub_key_1_{date_str}"


# train setup
print("train setup")    
train_client = TrainClient(SERVER_URL)
train_config_dict = toml.load("/app/mlnode/packages/train/configs/1B_3090_1x1.toml")
env_dicts = get_env_dictionaries([SERVER_IP], SERVER_IP)

#inference setup  
print("inference setup")
inference_client = InferenceClient(SERVER_URL)
model_name = "unsloth/llama-3-8b-Instruct"


#pow setup
print("pow setup")
BATCH_SIZE = 5000
pow_client = PowClient(SERVER_URL)
R_ESTIMATE = 4
params = Params(
    dim=512,
    n_layers=64,
    n_heads=128,
    n_kv_heads=128,
    vocab_size=8192,
    ffn_dim_multiplier=16.0,
    multiple_of=1024,
    norm_eps=1e-05,
    rope_theta=500000.0,
    use_scaled_rope=True,
    seq_len=4
)
fraud_threshold = 0.01



#start train
print("start train")
train_client.start(train_config_dict, env_dicts[0])
try:
    inference_client.inference_setup(model_name, "bfloat16")
except requests.exceptions.HTTPError as e:
    print(e)

try:
    pow_client.init_generate(
        url=BATCH_RECIEVER_URL,
        block_hash=block_hash,
        block_height=1,
        public_key=public_key,
        batch_size=BATCH_SIZE,
    r_target=R_ESTIMATE,
    fraud_threshold=fraud_threshold,
        params=params,
    )
except requests.exceptions.HTTPError as e:
    print(e)

train_client.stop()

#inference setup
print("inference start")
inference_client.inference_setup(model_name, "bfloat16")    

try:
    pow_client.init_generate(
        url=BATCH_RECIEVER_URL,
        block_hash=block_hash,
    block_height=1,
    public_key=public_key,
    batch_size=BATCH_SIZE,
    r_target=R_ESTIMATE,
        fraud_threshold=fraud_threshold,
        params=params,
    )
except requests.exceptions.HTTPError as e:
    print(e)

try:
    train_client.start(train_config_dict, env_dicts[0])
except requests.exceptions.HTTPError as e:
    print(e)
    
inference_client.inference_down()

#pow setup
print("pow start")
pow_client.init_generate(
    url=BATCH_RECIEVER_URL,
    block_hash=block_hash,
    block_height=1,
    public_key=public_key,
    batch_size=BATCH_SIZE,
    r_target=R_ESTIMATE,
    fraud_threshold=fraud_threshold,
    params=params,
)

try:
    train_client.start(train_config_dict, env_dicts[0])
except requests.exceptions.HTTPError as e:
    print(e)
    
    
try:
    inference_client.inference_setup(model_name, "bfloat16") 
except requests.exceptions.HTTPError as e:
    print(e)
    
pow_client.stop()
