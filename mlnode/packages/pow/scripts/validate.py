import time
import os

from pow.app.client import (
    get_generated,
    get_validated,
    validate,
)

    
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


nonce_to_dist_gen = {}
nonce_to_dist_val = {}

nonce_invalid = []
nonce_valid = []
dist_mismatch = {}


def check_generated_and_send_to_validate(
    url_from,
    url_to,
    nonce_to_dist_gen,
    nonce_to_dist_val,
    dist_mismatch,
    nonce_invalid,
):
    while True:
        to_validate = get_generated(url_from)
        for nonce, dist in zip(
            to_validate["nonces"],
            to_validate["dist"],
        ):
            assert nonce not in nonce_to_dist_val
            assert nonce not in nonce_to_dist_gen
            nonce_to_dist_gen[nonce] = dist

        for validated in get_validated(url_to):
            for nonce, dist in zip(
                validated["nonces"],
                validated["dist"],
            ):
                assert nonce in nonce_to_dist_gen
                assert nonce not in nonce_to_dist_val

                nonce_to_dist_val[nonce] = dist
                if dist > R_TARGET:
                    nonce_invalid.append(nonce)
                    print(f"  !!!Invalid nonce: {nonce} | dist_gen: {nonce_to_dist_gen[nonce]} | dist_val: {dist}")
                else:
                    nonce_valid.append(nonce)

                if dist != nonce_to_dist_gen[nonce]:
                    dist_mismatch[nonce] = {
                        "dist_gen": nonce_to_dist_gen[nonce],
                        "dist_val": dist,
                    }

        print(
            f"Validated nonce: {len(nonce_valid):>5} from {len(nonce_to_dist_val):>10} validated | {len(dist_mismatch):>5} dist mismatch"
        )
        # Just to double check we don't have data leaks
        to_validate["dist"] = []
        if len(to_validate["nonces"]) > 0:
            validate(url_to, to_validate)

        time.sleep(10)


check_generated_and_send_to_validate(
    URL_1,
    URL_2,
    nonce_to_dist_gen,
    nonce_to_dist_val,
    dist_mismatch,
    nonce_invalid,
)
