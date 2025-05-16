from typing import List

from concurrent.futures import (
    ProcessPoolExecutor,
    as_completed
)
from validation.utils import generate_and_validate
from validation.data import (
    ValidationItem,
    ModelInfo,
    RequestParams,
    ExperimentRequest
)
from tqdm import tqdm


def run_validation(
    prompts: List[str],
    inference_model: ModelInfo,
    validation_model: ModelInfo,
    request_params: RequestParams,
    max_workers: int = 10,
) -> List[ValidationItem]:    
    args = [
        ExperimentRequest(
            prompt=prompt,
            inference_model=inference_model,
            validation_model=validation_model,
            request_params=request_params,
        )
        for prompt in prompts
    ]

    results = []
    with ProcessPoolExecutor(max_workers=max_workers) as executor:
        futures = {executor.submit(generate_and_validate, arg): arg for arg in args}
        for future in tqdm(as_completed(futures), total=len(futures), desc="Running validation", leave=False):
            results.append(future.result())

    return results
