import pytest
from api.inference.manager import (
    InferenceManager,
    InferenceInitRequest,
)
from api.inference.vllm.runner_test_impl import VLLMRunnerTestImpl


def test_inference_manager_init_start_stop():
    manager = InferenceManager(
        runner_class=VLLMRunnerTestImpl
    )
    request = InferenceInitRequest(
        model="dummy-model",
        dtype="auto",
    )
    manager.init_vllm(request)
    assert not manager.is_running()

    manager.start()
    assert manager.is_running()

    manager.stop()
    assert not manager.is_running()

def test_inference_manager_init_twice():
    manager = InferenceManager(runner_class=VLLMRunnerTestImpl)
    request = InferenceInitRequest(
        model="dummy-model",
        dtype="auto",
    )
    manager.init_vllm(request)
    manager.start()
    with pytest.raises(Exception, match="already running"):
        manager.init_vllm(request)

def test_inference_manager_start_without_init():
    manager = InferenceManager(runner_class=VLLMRunnerTestImpl)
    with pytest.raises(Exception, match="not initialized"):
        manager.start()

def test_inference_manager_start_already_running():
    manager = InferenceManager(runner_class=VLLMRunnerTestImpl)
    request = InferenceInitRequest(
        model="dummy-model",
        dtype="auto",
    )
    manager.init_vllm(request)
    manager.start()
    with pytest.raises(Exception, match="already running"):
        manager.start()
