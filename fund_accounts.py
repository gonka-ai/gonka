#!/usr/bin/env python3

import os

import requests
import subprocess
import json
import re
import time

# Constants for docker command (you'll need to define these)
APP_NAME = "inferenced"
IMAGE_NAME = "inferenced"
CHAIN_ID = "prod-sim"
COIN_DENOM = "icoin"
STATE_DIR_NAME = ".inference"
# Define MOUNT_PATH in Python
MOUNT_PATH = os.path.join(os.getcwd(), "prod-sim")
fundingAmount = "20000000icoin"


def get_or_create_account(port, name, funded_address=None, funded_name=None):
    try:
        response = add_participant(name, port)
        response.raise_for_status()
    except requests.exceptions.HTTPError as err:
        error_message = err.response.text

        # Look for the account ID in the error message
        match = re.search(r'account (\w+)', error_message)
        if match:
            extracted_address = match.group(1)
            print(f"Account {name} not found. Address: {extracted_address}")

            if funded_address and funded_name:
                # Run the docker command to fund the new account
                fund_account(extracted_address, funded_address, funded_name, name, port)
                # set_account_to_validator(extracted_address, funded_address, funded_name, name, port)
                return extracted_address
            else:
                print("Funded account information is missing.")
                return None
        else:
            print("No valid account ID found in the error response.")
            print(error_message)
            return None
    else:
        # If the response is successful and contains an ID, return it
        data = response.json()
        account_id = data.get("id")
        print(f"Account {name} already added. Address: {account_id}")
        return account_id


def fund_account(extracted_address, funded_address, funded_name, name, port):
    docker_command = [
        "docker", "run", "--rm", "-it",
        f"-v{MOUNT_PATH}/requester:/root/{STATE_DIR_NAME}",
        "--network", "inference-ignite_net-public",
        IMAGE_NAME, APP_NAME, "tx", "bank", "send",
        funded_address, extracted_address, fundingAmount,
        "--keyring-backend", "test",
        f"--keyring-dir=/root/{STATE_DIR_NAME}",
        f"--chain-id={CHAIN_ID}", "--yes",
        f"--node=tcp://{funded_name}-node:26657"
    ]
    print(" ".join(docker_command))
    result = subprocess.run(docker_command, check=False, capture_output=True)
    if result.returncode == 0:
        print(f"Account {name} funded. Address: {extracted_address}")
        time.sleep(5)
        add_participant(name, port)
    else:
        print("Error funding account.")
        print("stdout:", result.stdout)

def set_account_to_validator(extracted_address, funded_address, funded_name, name, port):
    # Command to fetch the public key of the validator node
    pubkey = get_pubkey(name)

    # Create the JSON content for the validator
    validator_data = {
        "pubkey": {
            "@type": "/cosmos.crypto.ed25519.PubKey",
            "key": pubkey
        },
        "amount": "100000icoin",
        "moniker": f"{name}-validator",
        "identity": "",
        "website": f"https://{name}validator.example.com",
        "security": "security@example.com",
        "details": f"{name} validator's details",
        "commission-rate": "0.1",
        "commission-max-rate": "0.2",
        "commission-max-change-rate": "0.01",
        "min-self-delegation": "1"
    }
    with open(f"{MOUNT_PATH}/{name}/{name}-validator.json", "w") as f:
        json.dump(validator_data, f)

    # setup as validator:
    docker_command = [
        "docker", "run", "--rm", "-it",
        f"-v{MOUNT_PATH}/{name}:/root/{STATE_DIR_NAME}",
        "--network", "inference-ignite_net-public",
        IMAGE_NAME, APP_NAME, "tx", "staking", "create-validator",
        f"/root/{STATE_DIR_NAME}/{name}-validator.json",
        "--chain-id", CHAIN_ID,
        "--from", name,
        "--keyring-backend", "test",
        f"--keyring-dir=/root/{STATE_DIR_NAME}",
        "--yes",
        f"--node=tcp://{funded_name}-node:26657"
    ]
    # print out command line:
    print(" ".join(docker_command))
    result = subprocess.run(docker_command, check=False, capture_output=False)
    if result.returncode == 0:
        print(f"Account {name} setup as validator. Address: {extracted_address}")
        time.sleep(5)
        add_participant(name, port)
    else:
        print("Error setting up as validator.")
        print(result.stdout)
        print(result.stderr)


def get_pubkey(name):
    pubkey_dict = get_pubkey_dict(name)
    pubkey = pubkey_dict["key"]
    return pubkey


def get_pubkey_dict(name):
    docker_get_pubkey_command = [
        "docker", "run", "--rm",
        "-v", f"{MOUNT_PATH}/{name}:/root/{STATE_DIR_NAME}",
        "--network", "inference-ignite_net-public",
        IMAGE_NAME, APP_NAME, "tendermint", "show-validator"
    ]
    # Execute the command to get the public key
    pubkey_result = subprocess.run(docker_get_pubkey_command, capture_output=True, text=True)
    pubkey_json = pubkey_result.stdout.strip()
    # Parse the JSON output to get the 'key' field
    return json.loads(pubkey_json)


def add_participant(name, port):
    pubkey = get_pubkey(name)
    url = f"http://localhost:{port}/v1/participants"
    payload = {
        "url": f"http://{name}-api:8080",
        "models": ["unsloth/llama-3-8b-Instruct"],
        "validator_key": pubkey
    }

    print(payload)

    while True:
        response = requests.post(url, json=payload)
        if "please wait for first block: invalid height" in response.text:
            print("Blockchain not ready, waiting...", end="\r")
            time.sleep(5)  # Wait for 5 seconds before retrying
        else:
            return response


# Example usage of the function
requester_account = get_or_create_account(8080, "requester")
get_or_create_account(8081, "executor", requester_account, "requester")
get_or_create_account(8082, "validator", requester_account, "requester")
