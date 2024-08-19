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
                docker_command = [
                    "docker", "run", "--rm", "-it",
                    f"-v{MOUNT_PATH}/requester:/root/{STATE_DIR_NAME}",
                    "--network", "inference-ignite_net-public",
                    IMAGE_NAME, APP_NAME, "tx", "bank", "send",
                    funded_address, extracted_address, "1icoin",
                    "--keyring-backend", "test",
                    f"--keyring-dir=/root/{STATE_DIR_NAME}",
                    f"--chain-id={CHAIN_ID}", "--yes",
                    f"--node=tcp://{funded_name}-node:26657"
                ]

                result = subprocess.run(docker_command, check=False, capture_output=True)
                if result.returncode == 0:
                    print(f"Account {name} funded. Address: {extracted_address}")
                    time.sleep(5)
                    add_participant(name, port)
                else:
                    print("Error funding account.")
                return extracted_address
            else:
                print("Funded account information is missing.")
                return None
        else:
            print("No valid account ID found in the error response.")
            return None
    else:
        # If the response is successful and contains an ID, return it
        data = response.json()
        account_id = data.get("id")
        print(f"Account {name} already added. Address: {account_id}")
        return account_id


def add_participant(name, port):
    url = f"http://localhost:{port}/v1/participants"
    payload = {
        "url": f"http://{name}-api:8080",
        "models": ["unsloth/llama-3-8b-Instruct"]
    }

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
