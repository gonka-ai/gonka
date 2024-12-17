import requests
from dataclasses import dataclass


HISTORY_NODE_HOST = "localhost"
HISTORY_NODE_API_PORT = "8080"
HISTORY_NODE_CHAIN_PORT = "26657"

TRUSTED_VERIFIER_NODE_HOST = "localhost"
TRUSTED_VERIFIER_NODE_API_PORT = "8080"


@dataclass
class Validator:
    address: str
    public_key: str

def get_url(host: str, port: str, path: str) -> str:
    return f"http://{host}:{port}/{path}"


def get_genesis_active_participants():
    genesis = get_genesis()
    # TODO: get validators from genesis
    return genesis["active_participants"]


def get_genesis():
    # TODO: implement genesis endpoint for the API node (proxy to chain node)
    url = get_url(HISTORY_NODE_HOST, HISTORY_NODE_CHAIN_PORT, "genesis")
    response = requests.get(url)
    response.raise_for_status()

    return response.json()["result"]["genesis"]


def get_active_participants(epoch: str) -> dict[str, any]:
    url = get_url(HISTORY_NODE_HOST, HISTORY_NODE_API_PORT, f"v1/epochs/{epoch}/participants")
    response = requests.get(url)
    response.raise_for_status()

    return response.json()


def verify_proof(active_participants):
    pass


def main():
    current_active_participants = get_active_participants(epoch="current")
    # TODO: Rename epochGroupId > epoch_group_id/epoch
    current_epoch = current_active_participants["active_participants"]["epochGroupId"]

    print(f"Current epoch: {current_epoch}")

    return

    prev_validators = None
    for i in range(1, current_epoch + 1):
        if i == 1:
            prev_validators = get_genesis_active_participants()

        active_participants = get_active_participants(epoch=str(i))

        verify_proof(active_participants)
        verify_signature(prev_validators, block)

        prev_validators = active_participants


if __name__ == '__main__':
    main()
