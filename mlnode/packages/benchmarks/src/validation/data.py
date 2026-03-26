from pydantic import (
    BaseModel,
    Field,
    model_validator,
)
from typing import (
    List,
    Dict,
    Optional,
    Union,
)
import pandas as pd

import copy
from datetime import datetime


class PositionResult(BaseModel):
    token: str
    logprobs: Dict[str, float]


class Result(BaseModel):
    text: str
    results: List[PositionResult]


class ModelInfo(BaseModel):
    name: str
    url: str
    deploy_params: Dict[str, str] = Field(default_factory=dict)


class RequestParams(BaseModel):
    max_tokens: int
    temperature: float
    seed: int
    additional_params: Dict[str, Union[str, int, float]] = Field(default_factory=dict)
    top_logprobs: int = 3
    top_p: Optional[float] = None
    top_k: Optional[int] = None
    repetition_penalty: Optional[float] = None
    timeout_seconds: int = 300
    retries_max_attempts: int = 3
    retry_backoff_seconds_start: float = 1.0
    retry_backoff_multiplier: float = 2.0


class ValidationItem(BaseModel):
    prompt: str
    language: Optional[str] = None
    inference_result: Result
    validation_result: Result
    inference_model: ModelInfo
    validation_model: ModelInfo
    request_params: RequestParams

    def to_dict(self):
        return self.model_dump()


class ExperimentRequest(BaseModel):
    prompt: str
    language: Optional[str] = None
    inference_model: ModelInfo
    validation_model: ModelInfo
    request_params: RequestParams
    output_path: Optional[str] = None

    def to_result(self, inference_result: Result, validation_result: Result) -> ValidationItem:
        return ValidationItem(
            prompt=self.prompt,
            language=self.language,
            inference_result=inference_result,
            validation_result=validation_result,
            inference_model=self.inference_model,
            validation_model=self.validation_model,
            request_params=self.request_params
        )


def items_to_df(validation_results: List[ValidationItem]) -> pd.DataFrame:
    return pd.DataFrame([item.model_dump() for item in validation_results])


def df_to_items(df: pd.DataFrame) -> List[ValidationItem]:
    return [ValidationItem.model_validate(row) for row in df.to_dict(orient='records')]

def save_to_jsonl(
    validation_results: List[ValidationItem],
    path: str,
    append: bool = False
):
    mode = 'a' if append else 'w'
    with open(path, mode) as f:
        for result in validation_results:
            f.write(result.model_dump_json() + '\n')


def load_from_jsonl(
    path: str,
    n: int = None
) -> List[ValidationItem]:
    k = n if n is not None else float('inf')
    results = []
    with open(path, 'r') as f:
        for i, line in enumerate(f):
            if i >= k:
                break
            results.append(ValidationItem.model_validate_json(line))
    return results


def parse_gpu_count(gpu_spec: str) -> int:
    """Parse a GPU spec like '8xH100' or '1xA100' and return the leading count.

    Falls back to 1 when the spec is empty or cannot be parsed.
    """
    if not gpu_spec:
        return 1
    parts = gpu_spec.split('x', 1)
    leading = parts[0]
    digits = ''.join(ch for ch in leading if ch.isdigit())
    return int(digits) if digits else 1


def _set_flag_value(args: List[str], flag: str, value: str) -> None:
    # Update the first occurrence; append if missing; remove any extras
    first_idx = None
    for i, token in enumerate(args):
        if token == flag:
            first_idx = i
            break
    if first_idx is None:
        args.extend([flag, value])
        return
    # Set/append the value for the first occurrence
    if first_idx + 1 < len(args):
        args[first_idx + 1] = value
    else:
        args.append(value)
    # Remove any subsequent duplicate occurrences of the flag and their value
    i = first_idx + 2
    while i < len(args):
        if args[i] == flag:
            del args[i]
            if i < len(args):
                del args[i]
            continue
        i += 1


def apply_parallelism_args(model_preset: "ModelPreset", gpu_count: int) -> None:
    _set_flag_value(model_preset.additional_args, '--tensor-parallel-size', str(gpu_count))
    _set_flag_value(model_preset.additional_args, '--pipeline-parallel-size', '1')


class ModelPreset(BaseModel):
    model: str
    precision: str
    dtype: str
    additional_args: List[str] = Field(default_factory=list)

    def to_deploy_payload(self) -> Dict[str, Union[str, List[str]]]:
        return {
            "model": self.model,
            "dtype": self.dtype,
            "additional_args": self.additional_args,
        }


class ServerConfig(BaseModel):
    ip: str
    node_port: str
    inference_port: str
    gpu: str

    def up_url(self) -> str:
        return f"http://{self.ip}:{self.node_port}/api/v1/inference/up"

    def inference_url(self) -> str:
        return f"http://{self.ip}:{self.inference_port}/"


class RunParams(BaseModel):
    exp_name: str
    output_path: str
    n_prompts: int
    timeout: int
    tokenizer_model_name: str = "unsloth/llama-3-8b-Instruct"
    request: RequestParams


class InferenceValidationRun(BaseModel):
    model_inference: ModelPreset
    model_validation: ModelPreset
    server_inference: ServerConfig
    server_validation: ServerConfig
    run_inference: RunParams
    run_validation: RunParams
    max_workers: Optional[int] = None

    @model_validator(mode='after')
    def _apply_parallelism_from_servers(self) -> 'InferenceValidationRun':
        # Work on copies so original presets are not mutated
        self.model_inference = copy.deepcopy(self.model_inference)
        self.model_validation = copy.deepcopy(self.model_validation)

        # Update inference preset from inference server
        inf_gpus = parse_gpu_count(self.server_inference.gpu)
        apply_parallelism_args(self.model_inference, inf_gpus)

        # Update validation preset from validation server
        val_gpus = parse_gpu_count(self.server_validation.gpu)
        apply_parallelism_args(self.model_validation, val_gpus)

        # Apply max workers to both presets
        if self.max_workers is not None:
            _set_flag_value(self.model_inference.additional_args, '--max-num-seqs', str(self.max_workers))
            _set_flag_value(self.model_validation.additional_args, '--max-num-seqs', str(self.max_workers))

        return self

    def setting_filename(self) -> str:
        inf_model_name = f"{self.model_inference.model.split('/')[-1]}-{self.model_inference.precision}-{self.server_inference.gpu}"
        val_model_name = f"{self.model_validation.model.split('/')[-1]}-{self.model_validation.precision}-{self.server_validation.gpu}"
        timestamp = datetime.now().strftime("%Y-%m-%d_%H%M")
        return f"{inf_model_name}___{val_model_name}__{self.run_inference.exp_name}__{timestamp}.jsonl"