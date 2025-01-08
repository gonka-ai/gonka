import requests
from dataclasses import dataclass, asdict


HISTORY_NODE_HOST = "localhost"
HISTORY_NODE_API_PORT = "8080"
HISTORY_NODE_CHAIN_PORT = "26657"

TRUSTED_VERIFIER_NODE_HOST = "localhost"
TRUSTED_VERIFIER_NODE_API_PORT = "8080"


@dataclass
class Validator:
    #address: str
    pub_key: str
    voting_power: int


def get_url(host: str, port: str, path: str) -> str:
    return f"http://{host}:{port}/{path}"


def get_genesis_validators() -> list[Validator]:
    genesis = get_genesis()
    return extract_validators_from_genesis(genesis)


def extract_validators_from_genesis(genesis):
    validators = []
    for tx in genesis["app_state"]["genutil"]["gen_txs"]:
        for msg in tx["body"]["messages"]:
            if msg["@type"] != "/cosmos.staking.v1beta1.MsgCreateValidator":
                continue

            v = Validator(
                pub_key=msg["pubkey"]["key"],
                voting_power=int(msg["value"]["amount"]),
            )
            validators.append(v)

    return validators


def extract_validators_from_active_participants(active_participants) -> list[Validator]:
    validators = []
    for val in active_participants["active_participants"]["participants"]:
        v = Validator(
            pub_key=val["validatorKey"],
            voting_power=int(val["weight"]),
        )
        validators.append(v)

    return validators


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
    url = get_url(TRUSTED_VERIFIER_NODE_HOST, TRUSTED_VERIFIER_NODE_API_PORT, "v1/verify-proof")
    payload = {
        "value": active_participants["active_participants_bytes"],
        "app_hash": active_participants["block"][2]["header"]["app_hash"],
        "proof_ops": active_participants["proof_ops"],
        "epoch": active_participants["active_participants"]["epochGroupId"],
    }
    response = requests.post(url, json=payload)
    response.raise_for_status()


def verify_block(prev_validators: list[Validator], block):
    url = get_url(TRUSTED_VERIFIER_NODE_HOST, TRUSTED_VERIFIER_NODE_API_PORT, "v1/verify-block")
    payload = {
        "block": block,
        "validators": [asdict(v) for v in prev_validators],
    }
    response = requests.post(url, json=payload)
    response.raise_for_status()


def main():
    current_active_participants = get_active_participants(epoch="current")
    # TODO: Rename epochGroupId > epoch_group_id/epoch
    current_epoch = current_active_participants["active_participants"]["epochGroupId"]

    print(f"Current epoch: {current_epoch}")

    prev_validators = None
    for i in range(1, current_epoch + 1):
        if i == 1:
            prev_validators = get_genesis_validators()

        active_participants = get_active_participants(epoch=str(i))

        verify_proof(active_participants)
        verify_block(prev_validators, active_participants["block"][2])

        prev_validators = extract_validators_from_active_participants(active_participants)
        print(f"Verified epoch {i}. prev_validators: {prev_validators}")


def debug_main():
    prev_validators = get_genesis_validators()
    print(prev_validators)


if __name__ == '__main__':
    main()
