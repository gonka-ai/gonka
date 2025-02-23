import time
import os


from pow.models.utils import Params
from pow.app.client_v0 import (
    get_generated,
    get_validated,
    validate,
    init_gen,
    init_val,
    validate,
    stop
)

import datetime
import hashlib
from textwrap import dedent
from random import sample


date_str = datetime.datetime.now().strftime('%Y-%m-%d_%H-%M-%S')

# 1 good nonce for 500 checked
R_TARGET = os.getenv("R_TARGET", None)
if R_TARGET is None:
    print("R_TARGET is not set, using default")
    R_TARGET = 1.3931334028176634
R_TARGET = float(R_TARGET)

URL_1 = os.getenv("URL_1", None)
if URL_1 is None:
    print("URL_1 is not set, using default")
    URL_1 = "http://34.16.114.65:8080"
URL_2 = os.getenv("URL_2", None)
if URL_2 is None:
    print("URL_2 is not set, using default")
    URL_2 = "http://34.45.171.196:8080"

BLOCK_HASH = os.getenv("BLOCK_HASH", None)
if BLOCK_HASH is None:
    print("BLOCK_HASH is not set, using default")
    BLOCK_HASH = hashlib.sha256(date_str.encode()).hexdigest()

BATCH_SIZE_1 = os.getenv("BATCH_SIZE_1", None)
if BATCH_SIZE_1 is None:
    print("BATCH_SIZE_1 is not set, using default")
    BATCH_SIZE_1 = 10000
BATCH_SIZE_2 = os.getenv("BATCH_SIZE_2", None)
if BATCH_SIZE_2 is None:
    print("BATCH_SIZE_2 is not set, using default")
    BATCH_SIZE_2 = 10000


PUBLIC_KEY_1 = f"pub_key_1_{date_str}"
PUBLIC_KEY_2 = f"pub_key_2_{date_str}"

def init():
    params = Params(
        dim=1024,
        n_layers=32,
        n_heads=32,
        n_kv_heads=32,
        vocab_size=8192,
        ffn_dim_multiplier=16,
        seq_len=4
    )
    init_gen(URL_1, BLOCK_HASH, PUBLIC_KEY_1, BATCH_SIZE_1, R_TARGET, params)
    init_val(URL_2, BLOCK_HASH, PUBLIC_KEY_2, BATCH_SIZE_2, R_TARGET, params)

def stop_all():
    try:
        stop(URL_1)
    except Exception as e:
        print(e)
    try:
        stop(URL_2)
    except Exception as e:
        print(e)


def check_generated_and_send_to_validate():
    url_from = URL_1
    url_to = URL_2

    last_max_nonce = 0
    sent_to_validate = set()
    received_from_validate = set()

    nonce_to_dist_gen = {}
    nonce_to_dist_val = {}

    nonces_added_fraud = set()

    nonce_validation_failed = set()
    nonce_validation_success = set()
    dist_mismatch = {}

    end_time = time.time() + 600
    while time.time() < end_time:
        to_validate = get_generated(url_from)
        for nonce, dist in zip(
            to_validate["nonces"],
            to_validate["dist"],
        ):
            assert nonce not in nonce_to_dist_val, f"Nonce {nonce} already in nonce_to_dist_val"
            assert nonce not in nonce_to_dist_gen, f"Nonce {nonce} already in nonce_to_dist_gen"
            nonce_to_dist_gen[nonce] = dist

        for validated in get_validated(url_to):
            for nonce, dist in zip(
                validated["nonces"],
                validated["dist"],
            ):
                if nonce not in sent_to_validate:
                    continue
                # assert nonce in nonce_to_dist_gen, f"Nonce {nonce} not in nonce_to_dist_gen"
                assert nonce not in nonce_to_dist_val, f"Nonce {nonce} already in nonce_to_dist_val"

                nonce_to_dist_val[nonce] = dist
                if dist > R_TARGET:
                    nonce_validation_failed.add(nonce)
                else:
                    nonce_validation_success.add(nonce)

                if nonce in nonce_to_dist_gen and dist != nonce_to_dist_gen[nonce]:
                    dist_mismatch[nonce] = {
                        "dist_gen": nonce_to_dist_gen[nonce],
                        "dist_val": dist,
                    }
                received_from_validate.add(nonce)

        real_and_checked = set(nonce_to_dist_gen.keys()).intersection(received_from_validate)
        fraud_and_checked = nonces_added_fraud.intersection(received_from_validate)

        successfully_validated = len(real_and_checked.intersection(nonce_validation_success))
        not_validated          = len(real_and_checked.intersection(nonce_validation_failed))
        successfully_detected_fraud = len(fraud_and_checked.intersection(nonce_validation_failed))

        
        print(dedent(f"""
            {'#' * 80}
            validation_success     : {successfully_validated:>10} / {len(real_and_checked):>10}
            validation_failed      : {not_validated:>10} / {len(real_and_checked):>10}
            detected_fraud         : {successfully_detected_fraud:>10} / {len(fraud_and_checked):>10}
            sent_to_validate       : {len(sent_to_validate):>10}
            received_from_validate : {len(received_from_validate):>10}
            -- not checked yet: {len(sent_to_validate.difference(received_from_validate)):>10}
        """))

        # Just to double check we don't have data leaks
        to_validate["dist"] = []
        if last_max_nonce is not None \
            and len(nonce_to_dist_gen.keys()) > 0 \
            and len(to_validate["nonces"]) > 0:

            not_true_nonces_from_checked = list(range(last_max_nonce + 1, max(nonce_to_dist_gen.keys())))
            fraud_nonces = sample(
                not_true_nonces_from_checked,
                min(max(1, len(to_validate["nonces"]) // 3), len(not_true_nonces_from_checked))
            )
            for nonce in fraud_nonces:
                if nonce in nonce_to_dist_gen:
                    continue
                assert nonce not in nonce_to_dist_val
                assert nonce not in nonces_added_fraud
                nonces_added_fraud.add(nonce)
                to_validate["nonces"].append(nonce)
                to_validate["dist"].append(R_TARGET - 0.01)

            validate(url_to, to_validate)
            sent_to_validate.update(to_validate["nonces"])
            last_max_nonce = max(nonce_to_dist_gen.keys())

        time.sleep(30)


try:
    stop_all()
    init()
    check_generated_and_send_to_validate()
finally:
    stop_all()
