import pytest

from validation.data import (
    PositionResult,
    Result,
    ModelInfo,
    RequestParams,
    ValidationItem,
    ExperimentRequest,
    items_to_df,
    save_to_jsonl,
    load_from_jsonl,
    df_to_items
)
import pandas as pd
import tempfile

@pytest.fixture
def example_position_result():
    return PositionResult(token="test", logprobs={"a": -0.1, "b": -0.2})

@pytest.fixture
def example_result(example_position_result):
    return Result(results=[example_position_result, example_position_result])

@pytest.fixture
def example_model_info():
    return ModelInfo(name="test-model", url="http://localhost")

@pytest.fixture
def example_request_params():
    return RequestParams(max_tokens=10, temperature=0.7, seed=42)

@pytest.fixture
def example_validation_item(example_result, example_model_info):
    return ValidationItem(
        prompt="What is testing?",
        inference_result=example_result,
        validation_result=example_result,
        inference_model=example_model_info,
        validation_model=example_model_info,
        request_params=RequestParams(max_tokens=5, temperature=0.5, seed=42)
    )


def test_position_result_serialization(example_position_result):
    serialized = example_position_result.model_dump_json()
    deserialized = PositionResult.model_validate_json(serialized)
    assert example_position_result == deserialized


def test_result_text_property(example_result):
    assert example_result.text == example_result.results[0].token * 2


def test_validation_item_serialization(example_validation_item):
    serialized = example_validation_item.model_dump_json()
    deserialized = ValidationItem.model_validate_json(serialized)
    assert deserialized == example_validation_item


def test_experiment_request_to_result(example_model_info, example_result):
    prompt = "test prompt"
    exp_request = ExperimentRequest(
        prompt=prompt,
        inference_model=example_model_info,
        validation_model=example_model_info,
        request_params=RequestParams(max_tokens=10, temperature=0.2, seed=42)
    )

    validation_item = exp_request.to_result(example_result, example_result)

    assert validation_item.prompt == prompt
    assert validation_item.inference_result == example_result


def test_items_to_df_and_back(example_validation_item):
    items = [example_validation_item, example_validation_item]
    df = items_to_df(items)
    assert len(df) == 2

    items_back = [ValidationItem(**row) for row in df.to_dict(orient='records')]
    assert items == items_back


def test_jsonl_write_and_load(tmp_path, example_validation_item):
    path = tmp_path / "test.jsonl"
    items = [example_validation_item, example_validation_item]

    save_to_jsonl(items, str(path))
    loaded_items = load_from_jsonl(str(path))

    assert len(loaded_items) == 2
    assert loaded_items == items


def test_jsonl_append(tmp_path, example_validation_item):
    path = tmp_path / "test_append.jsonl"
    items1 = [example_validation_item]
    items2 = [example_validation_item, example_validation_item]
    
    save_to_jsonl(items1, str(path))
    save_to_jsonl(items2, str(path), append=True)
    
    loaded_items = load_from_jsonl(str(path))
    assert len(loaded_items) == 3
    assert loaded_items == items1 + items2


def test_df_to_items_and_back(example_validation_item):
    df = pd.DataFrame([example_validation_item.model_dump()])
    items = df_to_items(df)
    assert len(items) == 1
    assert items[0] == example_validation_item
