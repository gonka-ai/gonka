import time

import requests

from pow.models.utils import Params


def initiate(
    url, 
    chain_hash, 
    public_key, 
    batch_size, 
    r_target,
    params = Params()
):
    resp = requests.post(
        f"{url}/api/v0/initiate",
        json={
            "chain_hash": chain_hash,
            "public_key": public_key,
            "batch_size": batch_size,
            "r_target": r_target,
            "params": params.__dict__,
        },
    )
    if resp.status_code != 200:
        raise Exception(resp.text)
    return resp.json()


def stop(url):
    resp = requests.post(
        f"{url}/api/v0/stop"
    )
    if resp.status_code != 200:
        raise Exception(resp.text)
    return resp.json()


def get_status(url):
    resp = requests.get(
        f"{url}/api/v0/status"
    )
    if resp.status_code != 200:
        raise Exception(resp.text)
    return resp.json()


def wait_for_status(url, target_status):
    while True:
        status_resp = get_status(url)
        status = status_resp["status"]
        is_model_initialized = status_resp.get("is_model_initialized", True)
        if status == target_status and is_model_initialized:
            break
        time.sleep(1)


def start_generation(url):
    resp = requests.post(
        f"{url}/api/v0/start-generation"
    )
    if resp.status_code != 200:
            raise Exception(resp.text)
    return resp.json()


def get_generated(url):
    resp = requests.get(
        f"{url}/api/v0/generated"
    )
    if resp.status_code != 200:
        raise Exception(resp.text)
    return resp.json()


def start_validation(url):
    resp = requests.post(
        f"{url}/api/v0/start-validation"
    )
    if resp.status_code != 200:
        raise Exception(resp.text)
    return resp.json()


def validate(url, to_validate):
    resp = requests.post(
        f"{url}/api/v0/validate",
        json=to_validate
    )
    if resp.status_code != 200:
        raise Exception(resp.text)
    return resp.json()


def get_validated(url):
    resp = requests.get(
        f"{url}/api/v0/validated"
    )
    if resp.status_code != 200:
        raise Exception(resp.text)
    return resp.json()


def init_gen(
    url, 
    chain_hash, 
    public_key, 
    batch_size, 
    r_target,
    params = Params()
):
    initiate(url, chain_hash, public_key, batch_size, r_target, params)
    start_generation(url)
    wait_for_status(url, "GENERATING")
    return get_generated(url)


def init_val(
    url, 
    chain_hash, 
    public_key, 
    batch_size, 
    r_target,
    params = Params()
):
    initiate(url, chain_hash, public_key, batch_size, r_target, params)
    start_validation(url)
    wait_for_status(url, "VALIDATING")
    return get_validated(url)

