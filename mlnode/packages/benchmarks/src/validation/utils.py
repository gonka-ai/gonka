import requests
import math
import threading
from typing import (
    Dict,
    Any,
    List,
    Callable,
    Optional
)

from pydantic import BaseModel


from typing import Any, Dict, List
from pydantic import BaseModel, Field

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

_output_path_to_lock: Dict[str, threading.Lock] = {}
_registry_lock = threading.Lock()


def _get_lock_for_path(path: str) -> threading.Lock:
    """
    Returns a threading.Lock object for a specified file path.
    
    If the path is empty, a new Lock will be created. Keeps
    track of locks using a dictionary to ensure each path has
    a unique lock.

    Args:
        path (str): The file path to lock.

    Returns:
        threading.Lock: A lock object for the specified file path.
    """
    if not path:
        return threading.Lock()
    with _registry_lock:
        if path not in _output_path_to_lock:
            _output_path_to_lock[path] = threading.Lock()
        return _output_path_to_lock[path]


class EnforcedToken(BaseModel):
    token: str
    top_tokens: List[str] = Field(default_factory=list)

class EnforcedTokens(BaseModel):
    tokens: List[EnforcedToken]

    @classmethod
    def from_content(cls, content: List[Dict[str, Any]]) -> "EnforcedTokens":
        """
        Parses deep learning model response content into a structured form.

        Converts input content with tokens and their associated top
        logprobs into `EnforcedTokens` dataclass form.

        Args:
            content (List[Dict[str, Any]]): List containing dicts of positions
            having tokens and top_logprobs.

        Returns:
            EnforcedTokens: Parsed instance containing enforced tokens and their
            associated top tokens.
        """
        tokens = []
        for position in content:
            token = position["token"]
            top_tokens = [x["token"] for x in position["top_logprobs"]]
            tokens.append(EnforcedToken(token=token, top_tokens=top_tokens))
        return cls(tokens=tokens)
    
    @classmethod
    def from_result(cls, result: Result) -> "EnforcedTokens":
        """
        Converts a Result object into an EnforcedTokens instance.

        Takes each token from Result and incorporates its logprobs
        into the associated EnforcedToken structure.

        Args:
            result (Result): The Result object containing tokens.

        Returns:
            EnforcedTokens: Structured collection of tokens with probability data.
        """
        return cls(tokens=[EnforcedToken(token=r.token, top_tokens=list(r.logprobs.keys())) for r in result.results])

    
def _prepare_messages(
    prompt: str,
) -> List[Dict[str, Any]]:
    """
    Prepare structured messages input for the request payload.

    Takes a user input prompt string and formats it into structured
    components for communication with AI models.

    Args:
        prompt (str): User prompt to the AI assistant.

    Returns:
        List[Dict[str, Any]]: Structured message list.
    """
    return [
        {"role": "system", "content": "You are a helpful assistant. Response clear, correct and complete."},
        {"role": "user", "content": prompt}
    ]


def _sampling_extras(request_params: RequestParams) -> Dict[str, Any]:
    """Return optional sampling params that are set (non-None) plus additional_params.
    
    Constructs a dictionary of sampling parameters that include
    top_p, top_k, and repetition_penalty if they are not None,
    and combines them with any additional parameters from the
    request_params.

    Args:
        request_params (RequestParams): Request configuration that includes
        sampling parameters and additional parameters.

    Returns:
        Dict[str, Any]: Combined dictionary of sampling and additional
        parameters.
    """
    extras: Dict[str, Any] = {}
    if request_params.top_p is not None:
        extras["top_p"] = request_params.top_p
    if request_params.top_k is not None:
        extras["top_k"] = request_params.top_k
    if request_params.repetition_penalty is not None:
        extras["repetition_penalty"] = request_params.repetition_penalty
    extras.update(request_params.additional_params)
    return extras


def inference(
    model_info: ModelInfo,
    request_params: RequestParams,
    prompt: str,
) -> Dict[str, Any]:
    """
    Submits an inference request to a model and returns the response.

    Uses the provided model information and request parameters,
    constructs a payload with parameters and prompt, then makes
    a POST request to the model's completion endpoint.

    Args:
        model_info (ModelInfo): Contains model name and endpoint URL.
        request_params (RequestParams): Contains parameters to configure
        the request such as max tokens and temperature.
        prompt (str): The prompt to send to the model.

    Returns:
        Dict[str, Any]: The JSON response from the model.

    Raises:
        RuntimeError: If the request fails with a non-200 status code.
    """
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
        "skip_special_tokens": False,
        **_sampling_extras(request_params),
    }
    
    response = requests.post(url, json=payload)
    if response.status_code != 200:
        raise RuntimeError(f"Inference API request failed with status {response.status_code} {response.text}")
    return response.json()


def validation(
    model_info: ModelInfo,
    request_params: RequestParams,
    prompt: str,
    enforced_str: Optional[str] = None,
    enforced_tokens: Optional[EnforcedTokens] = None,
) -> Dict[str, Any]:
    """
    Executes a validation request with optional enforced constraints.

    Constructs a payload similar to inference, with additional
    optional fields for enforced string and enforced tokens,
    sends the request to the validation endpoint.

    Args:
        model_info (ModelInfo): Contains validation model info.
        request_params (RequestParams): Parameters for validation request.
        prompt (str): The prompt to validate against.
        enforced_str (Optional[str]): String constraint to enforce.
        enforced_tokens (Optional[EnforcedTokens]): Token constraints for the request.

    Returns:
        Dict[str, Any]: The JSON response from the validation model.

    Raises:
        RuntimeError: If the request fails or has a non-200 status code.
    """
    url = f"{model_info.url.rstrip('/')}/v1/chat/completions"
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
        "skip_special_tokens": False,
        **_sampling_extras(request_params),
    }
    
    if enforced_str:
        payload["enforced_str"] = enforced_str
    if enforced_tokens:
        payload["enforced_tokens"] = enforced_tokens.dict()

    response = requests.post(url, json=payload)
    if response.status_code != 200:
        raise RuntimeError(f"Validation API request failed with status {response.status_code} {response.text}\n(enforced_tokens: {enforced_tokens})\n(payload: {payload})")
    
    return response.json()


def _extract_logprobs(resp) -> Result:
    """
    Extracts token log probabilities from the API response.

    Breaks down the response to obtain logprobs and their
    respective text content for processing.

    Args:
        resp: The API response to extract from.

    Returns:
        Result: A result object containing the response text and logprobs
        for each position.
    """
    logprobs = resp["choices"][0]["logprobs"]["content"]
    text = resp["choices"][0]["message"]["content"]
    results = []
    for position in logprobs:
        res = PositionResult(
            token=position["token"],
            logprobs={logprob["token"]: logprob["logprob"] for logprob in position["top_logprobs"]}
        )
        results.append(res)

    return Result(text=text, results=results)


def _extract_enforced_tokens(resp) -> EnforcedTokens:
    """
    Converts API response to EnforcedTokens object.

    Parses through the API response to build an EnforcedTokens
    instance containing structured tokens and probabilities.

    Args:
        resp: The API response to be processed.

    Returns:
        EnforcedTokens: Parsed object with structured enforced tokens.
    """
    return EnforcedTokens.from_content(resp["choices"][0]["logprobs"]["content"])


def generate_and_validate(
    experiment_request: ExperimentRequest
) -> ValidationItem:
    """
    Conducts both inference and validation for an experiment request.

    Follows through by running inference on the request, then
    retrieves and compares results via validation to ensure consistency.

    Args:
        experiment_request (ExperimentRequest): Structured experiment data.

    Returns:
        ValidationItem: Final validation item built from inference
        and validation results.

    Raises:
        RuntimeError: If text sequences between inference and validation
        do not match.
    """
    inference_resp = inference(
        experiment_request.inference_model,
        experiment_request.request_params,
        experiment_request.prompt,
    )
    inference_result = _extract_logprobs(inference_resp)
    enforced_tokens = _extract_enforced_tokens(inference_resp)
    validation_resp = validation(
        experiment_request.validation_model,
        experiment_request.request_params,
        experiment_request.prompt,
        enforced_tokens=enforced_tokens
    )
    validation_result = _extract_logprobs(validation_resp)
    if validation_result.text != inference_result.text:
        raise RuntimeError(
            "Text sequences don't match between inference and validation."
        )

    item = experiment_request.to_result(
        inference_result,
        validation_result
    )

    if experiment_request.output_path:
        lock = _get_lock_for_path(experiment_request.output_path)
        with lock:
            try:
                with open(experiment_request.output_path, 'a') as f:
                    f.write(item.model_dump_json() + '\n')
            except Exception as e:
                logger.error(f"Failed to write result to {experiment_request.output_path}: {e}")

    return item


def token_distance(
    inf_position_logprobs: PositionResult,
    val_position_logprobs: PositionResult
):
    """
    Calculates token distance and matches count.

    Compares log probabilities between inference and validation
    positions and accumulates distance metrics.

    Args:
        inf_position_logprobs (PositionResult): Inference position data.
        val_position_logprobs (PositionResult): Validation position data.

    Returns:
        Tuple[float, int]: Distances and number of matches.
    """
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
    """
    Checks if result tokens match between inference and validation.

    Compares token sequences from inference and validation results to
    determine overall match validity. Logs mismatches for debugging.

    Args:
        inf_result (Result): Inference result object.
        val_result (Result): Validation result object.

    Returns:
        bool: Whether the tokens match.
    """
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
    """
    Computes distance metrics for sequence comparison.

    Evaluates total distance and match ratio of token sequences
    between inference and validation results using a distance function.

    Args:
        inf_result (Result): Inference result for comparison.
        val_result (Result): Validation result for comparison.
        distance_func (Callable): Function to compute distance on position basis.
            Defaults to token_distance.

    Returns:
        Tuple[float, float]: Total distance and matches ratio.
    """
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
    val_position_logprobs: PositionResult,
):
    """
    Matches Go customDistance/positionDistance.

    Iterates over validation tokens, builds fallback from inference
    side to calculate distance metrics between log probability distributions.

    Args:
        inf_position_logprobs (PositionResult): Inference position probabilities.
        val_position_logprobs (PositionResult): Validation position probabilities.

    Returns:
        Tuple[float, int]: Distance tally and matched token count.
    """
    dist = 0.0
    n_matches = 0

    if not inf_position_logprobs.logprobs or not val_position_logprobs.logprobs:
        return 100.0, 0

    sorted_inf_logprobs = sorted(inf_position_logprobs.logprobs.values())

    if len(sorted_inf_logprobs) >= 2:
        min_inf_1 = sorted_inf_logprobs[0]
        min_inf_2 = sorted_inf_logprobs[1]
    else:
        min_inf_1 = sorted_inf_logprobs[0]
        min_inf_2 = min_inf_1 - 100.0

    next_inf_logprob = min_inf_1 - (min_inf_2 - min_inf_1)

    for token, val_logprob in val_position_logprobs.logprobs.items():
        if token in inf_position_logprobs.logprobs:
            inf_logprob = inf_position_logprobs.logprobs[token]
            n_matches += 1
        else:
            inf_logprob = next_inf_logprob

        denom = 1e-6 + abs(val_logprob) + abs(inf_logprob)
        if math.isnan(denom) or denom == 0:
            continue
        term = abs(val_logprob - inf_logprob) / denom / 2.0
        if not math.isnan(term):
            dist += term

    return dist, n_matches


_BAD_LOGPROB_FLOOR = -9990.0


def _token_distance2_core(
    inf_position_logprobs: PositionResult,
    val_position_logprobs: PositionResult,
    skip_inf: bool = False,
    skip_zero: bool = False,
):
    """
    Shared core for distance2 clean variants.

    Same structure as token_distance2 (Go-aligned: iterates validation tokens,
    fallback from inference side) with optional cleaning:
      skip_inf  — skip pairs where either logprob <= _BAD_LOGPROB_FLOOR (-9999)
      skip_zero — skip pairs where one side is ~0.0 and the other is the max
                  logprob of its position (clamped high-confidence artifact)

    Args:
        inf_position_logprobs (PositionResult): Inference token probabilities data.
        val_position_logprobs (PositionResult): Validation token probabilities data.
        skip_inf (bool): Flag to skip pairs with "bad" log probabilities.
        skip_zero (bool): Flag to skip zero logprob vs max logprob pairs.

    Returns:
        Tuple[float, int]: Computed distance and match count for positions.
    """
    dist = 0.0
    n_matches = 0

    if not inf_position_logprobs.logprobs or not val_position_logprobs.logprobs:
        return 100.0, 0

    sorted_inf_logprobs = sorted(inf_position_logprobs.logprobs.values())

    if len(sorted_inf_logprobs) >= 2:
        min_inf_1 = sorted_inf_logprobs[0]
        min_inf_2 = sorted_inf_logprobs[1]
    else:
        min_inf_1 = sorted_inf_logprobs[0]
        min_inf_2 = min_inf_1 - 100.0

    next_inf_logprob = min_inf_1 - (min_inf_2 - min_inf_1)

    if skip_zero:
        inf_max = max(inf_position_logprobs.logprobs.values())
        val_max = max(val_position_logprobs.logprobs.values())

    for token, val_logprob in val_position_logprobs.logprobs.items():
        if token in inf_position_logprobs.logprobs:
            inf_logprob = inf_position_logprobs.logprobs[token]
            n_matches += 1
        else:
            inf_logprob = next_inf_logprob

        if skip_inf and (inf_logprob <= _BAD_LOGPROB_FLOOR or val_logprob <= _BAD_LOGPROB_FLOOR):
            continue

        if skip_zero:
            inf_is_zero = abs(inf_logprob) < 1e-6
            val_is_zero = abs(val_logprob) < 1e-6
            if inf_is_zero and not val_is_zero and abs(val_logprob - val_max) < 1e-6:
                continue
            if val_is_zero and not inf_is_zero and abs(inf_logprob - inf_max) < 1e-6:
                continue

        denom = 1e-6 + abs(val_logprob) + abs(inf_logprob)
        if math.isnan(denom) or denom == 0:
            continue
        term = abs(val_logprob - inf_logprob) / denom / 2.0
        if not math.isnan(term):
            dist += term

    return dist, n_matches


def _distance2_variant(
    inf_result: Result,
    val_result: Result,
    skip_inf: bool = False,
    skip_zero: bool = False,
):
    """
    Computes clean distance metrics by iterating over results.

    Signature variant for evaluating distances considering skipped
    condition cases (bad logprobs, zero-logprobs).

    Args:
        inf_result (Result): Inference result under comparison.
        val_result (Result): Validation result under comparison.
        skip_inf (bool): Use to skip known bad logprob values.
        skip_zero (bool): Use to skip specified zero-vs-max logprob pairs.

    Returns:
        Tuple[float, float]: Total computed distance and matches ratio.
    """
    if not _check_match(inf_result, val_result):
        return -1, -1

    total_dist = 0
    total_n_matches = 0
    for inf_position, val_position in zip(inf_result.results, val_result.results):
        dist, n_matches = _token_distance2_core(inf_position, val_position, skip_inf=skip_inf, skip_zero=skip_zero)
        total_dist += dist
        total_n_matches += n_matches

    n_logprobs = len(inf_result.results[0].logprobs) if inf_result.results[0].logprobs else 1
    matches_ratio = total_n_matches / (len(inf_result.results) * n_logprobs)
    total_dist = total_dist / (max(100, len(inf_result.results)) * n_logprobs)
    return total_dist, matches_ratio


def distance2_inf_clean(inf_result: Result, val_result: Result):
    """distance2 + skip -9999 pairs."""
    return _distance2_variant(inf_result, val_result, skip_inf=True)


def distance2_zero_clean(inf_result: Result, val_result: Result):
    """distance2 + skip 0.0-vs-max-logprob pairs."""
    return _distance2_variant(inf_result, val_result, skip_zero=True)


def distance2_clean(inf_result: Result, val_result: Result):
    """distance2 + both -9999 and 0.0 cleaning."""
    return _distance2_variant(inf_result, val_result, skip_inf=True, skip_zero=True)


def distance3(inf_result: Result, val_result: Result):
    """Alias for distance2_clean (backward compat)."""
    return distance2_clean(inf_result, val_result)


def similarity2(
    inf_result: Result,
    val_result: Result,
):
    """
    Computes similarity metric based on distance2 results.

    Uses inverted distance2 metrics to estimate similarity
    and produce a comparable output metric (matches ratio).

    Args:
        inf_result (Result): Inference comparison data.
        val_result (Result): Validation comparison data.

    Returns:
        Tuple[float, float]: Similarity score and matches ratio.
    """
    dist, matches_ratio = distance2(inf_result, val_result)
    if dist == -1:
        return -1, -1
    return 1 - dist, matches_ratio


def distance2(
    inf_result: Result,
    val_result: Result,
):
    """
    Computes the distance between two results ignoring skipped pairs.

    Computes a distance metric and matches ratio comparison
    ignoring positions without any overlaps.

    Args:
        inf_result (Result): Inference output results.
        val_result (Result): Validation output results.

    Returns:
        Tuple[float, float]: Distance and token overlap ratio metrics.
    """
    if not _check_match(inf_result, val_result):
        return -1, -1

    total_dist = 0
    total_n_matches = 0
    for inf_position, val_position in zip(inf_result.results, val_result.results):
        dist, n_matches = token_distance2(inf_position, val_position)
        total_dist += dist
        total_n_matches += n_matches

    n_logprobs = len(inf_result.results[0].logprobs) if inf_result.results[0].logprobs else 1
    matches_ratio = total_n_matches / (len(inf_result.results) * n_logprobs)
    total_dist = total_dist / (max(100, len(inf_result.results)) * n_logprobs)
    return total_dist, matches_ratio



import numpy as np
from typing import List, Dict
from validation.data import Result

BAD_LOGP = -10.0

def _clean_logprob(lp: float, floor: float = BAD_LOGP) -> float:
    """
    Cleans log probability values based on a specified floor.

    Adjusts log probabilities to avoid extremely low or
    nonsensical values by replacing them with a floor value.

    Args:
        lp (float): Original log probability.
        floor (float): Minimum value threshold, defaults to BAD_LOGP.

    Returns:
        float: Cleaned log probability adhering to the floor.
    """
    return lp if lp is not None and lp > floor else floor


def get_metric(logprobs: List[float]) -> float:
    """
    Computes a metric from a list of log probabilities.

    Uses the exponential mean of log probabilities, transforming
    them into a usable metric for evaluation or scoring.

    Args:
        logprobs (List[float]): List of log probability values.

    Returns:
        float: Calculated metric from the log probabilities list.
    """
    if not logprobs:
        return 0.0
    return float(np.exp(np.mean(logprobs)))


def get_metric_from_result(inf_result: Result) -> float:
    """
    Derives metric from inference result using token log probabilities.

    Processes each result entry, extracts and cleans log probabilities,
    then computes a collective metric via exponential mean.

    Args:
        inf_result (Result): Inference containing token probabilities.

    Returns:
        float: Metric representative of inference result quality.
    """
    per_token_lp: List[float] = []

    for r in inf_result.results:
        lp = r.logprobs.get(r.token, BAD_LOGP)
        per_token_lp.append(_clean_logprob(lp))

    return get_metric(per_token_lp)
