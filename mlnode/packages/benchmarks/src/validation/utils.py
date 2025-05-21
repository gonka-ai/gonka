import requests
from typing import (
    Dict,
    Any,
    List,
    Callable
)

from validation.data import (
    ModelInfo,
    RequestParams,
    ExperimentRequest,
    ValidationItem,
    Result,
    PositionResult
)

from common.logger import create_logger


logger = create_logger(__name__)


def _prepare_messages(
    prompt: str,
) -> List[Dict[str, Any]]:
    return [
        {"role": "system", "content": "You are a helpful assistant. Response should be really long and detailed."},
        {"role": "user", "content": prompt}
    ]


def inference(
    model_info: ModelInfo,
    request_params: RequestParams,
    prompt: str,
) -> Dict[str, Any]:
    url = f"{model_info.url}/v1/chat/completions"
    payload = {
        "model": model_info.name,
        "messages": _prepare_messages(prompt),
        "max_tokens": request_params.max_tokens,
        "temperature": request_params.temperature,
        "seed": request_params.seed,
        "stream": False,
        "logprobs": True,
        "n": 1,
        "top_logprobs": request_params.top_logprobs,
    }
    
    response = requests.post(url, json=payload)
    if response.status_code != 200:
        raise RuntimeError(f"Inference API request failed with status {response.status_code} {response.text}")
    return response.json()


def validation(
    model_info: ModelInfo,
    request_params: RequestParams,
    prompt: str,
    enforced_str: str
) -> Dict[str, Any]:
    url = f"{model_info.url}/v1/chat/completions"
    payload = {
        "model": model_info.name,
        "messages": _prepare_messages(prompt),
        "max_tokens": request_params.max_tokens,
        "temperature": request_params.temperature,
        "seed": request_params.seed,
        "stream": False,
        "logprobs": True,
        "top_logprobs": request_params.top_logprobs,
        "n": 1,
        "enforced_str": enforced_str,
    }
    
    response = requests.post(url, json=payload)
    if response.status_code != 200:
        raise RuntimeError(f"Validation API request failed with status {response.status_code} {response.text}")
    
    return response.json()


def _extract_logprobs(resp) -> Result:
    logprobs = resp["choices"][0]["logprobs"]["content"]
    results = []
    current_text = ""
    text = resp["choices"][0]["message"]["content"]
    for position in logprobs:
        res = PositionResult(
            token=position["token"],
            logprobs={logprob["token"]: logprob["logprob"] for logprob in position["top_logprobs"]}
        )
        results.append(res)
        current_text += position["token"]
        #TODO: fix on vLLM side (sometimes generation logprobs has <|eot_id|> token at the end but validation logprobs doesn't)
        if current_text == text:
            break

    return Result(results=results)


def generate_and_validate(
    experiment_request: ExperimentRequest
) -> ValidationItem:
    inference_resp = inference(
        experiment_request.inference_model,
        experiment_request.request_params,
        experiment_request.prompt,
    )
    inference_result = _extract_logprobs(inference_resp)
    validation_resp = validation(
        experiment_request.validation_model,
        experiment_request.request_params,
        experiment_request.prompt,
        enforced_str=inference_result.text
    )
    validation_result = _extract_logprobs(validation_resp)

    return experiment_request.to_result(
        inference_result,
        validation_result
    )


def token_distance(
    inf_position_logprobs: PositionResult,
    val_position_logprobs: PositionResult
):
    dist = 0
    n_matches = 0
    for k, v in inf_position_logprobs.logprobs.items():
        if k in val_position_logprobs.logprobs:
            n_matches += 1
            dist += abs(v - val_position_logprobs.logprobs[k]) / (1e-10 + abs(v) + abs(val_position_logprobs.logprobs[k])) / 2.
    return dist, n_matches



def _check_match(
    inf_result: Result,
    val_result: Result,
):
    if [r.token for r in inf_result.results] != [r.token for r in val_result.results]:
        logger.debug(
            f"tokens sequences don't match\n" +
            f"inference:\n {[r.token for r in inf_result.results]}\n" +
            f"{'-'*10}\n" +
            f"validation:\n {[r.token for r in val_result.results]}\n" +
            f"{'-'*100}"
        )
        return False
    return True

def distance(
    inf_result: Result,
    val_result: Result,
    distance_func: Callable = token_distance
):

    if not _check_match(inf_result, val_result):
        return -1, -1

    total_dist = 0
    total_n_matches = 0
    for inf_position, val_position in zip(inf_result.results, val_result.results):
        dist, n_matches = distance_func(inf_position, val_position)
        total_dist += dist
        total_n_matches += n_matches
    
    matches_ratio = total_n_matches / (len(inf_result.results)*len(inf_result.results[0].logprobs))
    total_dist /= (len(inf_result.results)*len(inf_result.results[0].logprobs))
    return total_dist, matches_ratio


def token_distance2(
    inf_position_logprobs: PositionResult,
    val_position_logprobs: PositionResult
):
    dist = 0.0
    n_matches = 0

    if not val_position_logprobs.logprobs:
        return len(inf_position_logprobs.logprobs), 0

    sorted_logprobs = sorted(val_position_logprobs.logprobs.values())
    
    if len(sorted_logprobs) >= 2:
        min_val_logprob_1 = sorted_logprobs[0]
        min_val_logprob_2 = sorted_logprobs[1]
    else:
        min_val_logprob_1 = sorted_logprobs[0]
        min_val_logprob_2 = min_val_logprob_1 - 1.0

    for token, inf_logprob in inf_position_logprobs.logprobs.items():
        if token in val_position_logprobs.logprobs:
            val_logprob = val_position_logprobs.logprobs[token]
            n_matches += 1
        else:
            val_logprob = min_val_logprob_1 - (min_val_logprob_2 - min_val_logprob_1)

        denom = 1e-10 + abs(inf_logprob) + abs(val_logprob)
        dist += abs(inf_logprob - val_logprob) / denom / 2.0

    return dist, n_matches


def similarity2(
    inf_result: Result,
    val_result: Result,
):
    dist, matches_ratio = distance2(inf_result, val_result)
    if dist == -1:
        return -1, -1
    return 1 - dist, matches_ratio


def distance2(
    inf_result: Result,
    val_result: Result,
):
    if not _check_match(inf_result, val_result):
        return -1, -1

    total_dist = 0
    total_n_matches = 0
    for inf_position, val_position in zip(inf_result.results, val_result.results):
        dist, n_matches = token_distance2(inf_position, val_position)
        total_dist += dist
        total_n_matches += n_matches
    
    matches_ratio = total_n_matches / (len(inf_result.results)*len(inf_result.results[0].logprobs))
    total_dist = (total_dist + 1.0) / (max(100, len(inf_result.results))*len(inf_result.results[0].logprobs) + 1.0)
    return total_dist, matches_ratio


import math

def js_divergence(probA: Dict[str, float], probB: Dict[str, float]) -> float:
    tokens = sorted(set(probA.keys()) | set(probB.keys()))
    pA = [probA.get(t, 0.0) for t in tokens]
    pB = [probB.get(t, 0.0) for t in tokens]
    m = [(a + b)/2 for (a, b) in zip(pA, pB)]
    return 0.5 * kl_divergence(pA, m) + 0.5 * kl_divergence(pB, m)

def kl_divergence(p, q):
    s = 0.0
    for pi, qi in zip(p, q):
        if pi > 0.0 and qi > 0.0:
            s += pi * math.log(pi / qi)
    return s

def token_distance_js(
    inf_position_logprobs: PositionResult,
    val_position_logprobs: PositionResult
):
    inf_probs = {t: math.exp(lp) for t, lp in inf_position_logprobs.logprobs.items()}
    val_probs = {t: math.exp(lp) for t, lp in val_position_logprobs.logprobs.items()}
    z_inf = sum(inf_probs.values()) or 1e-10
    z_val = sum(val_probs.values()) or 1e-10
    inf_probs = {t: p/z_inf for t,p in inf_probs.items()}
    val_probs = {t: p/z_val for t,p in val_probs.items()}
    jsd = js_divergence(inf_probs, val_probs)
    dist = math.sqrt(jsd)
    n_matches = len(set(inf_position_logprobs.logprobs.keys())
                    & set(val_position_logprobs.logprobs.keys()))
    return dist, n_matches