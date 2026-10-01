"""Unit tests for vLLM prefix-caching launch defaults."""

from api.inference.vllm.runner import VLLMRunner


def test_prefix_caching_enabled_by_default():
    runner = VLLMRunner(model="test-model", dtype="auto")
    assert "--enable-prefix-caching" in runner.additional_args


def test_prefix_caching_not_duplicated_when_already_passed():
    runner = VLLMRunner(
        model="test-model",
        dtype="auto",
        additional_args=["--enable-prefix-caching"],
    )
    assert runner.additional_args.count("--enable-prefix-caching") == 1


def test_prefix_caching_disabled_explicitly():
    runner = VLLMRunner(
        model="test-model",
        dtype="auto",
        additional_args=[],
    )
    # Flag can be removed deliberately by operators; summary must reflect it.
    runner.additional_args.remove("--enable-prefix-caching")
    assert runner.get_config_summary()["prefix_caching"] is False


def test_config_summary_reports_prefix_caching():
    runner = VLLMRunner(model="test-model", dtype="auto")
    assert runner.get_config_summary()["prefix_caching"] is True


def test_prompt_tokens_details_enabled_by_default():
    runner = VLLMRunner(model="test-model", dtype="auto")
    assert "--enable-prompt-tokens-details" in runner.additional_args


def test_prompt_tokens_details_not_duplicated_when_already_passed():
    runner = VLLMRunner(
        model="test-model",
        dtype="auto",
        additional_args=["--enable-prompt-tokens-details"],
    )
    assert runner.additional_args.count("--enable-prompt-tokens-details") == 1


def test_config_summary_reports_prompt_tokens_details():
    runner = VLLMRunner(model="test-model", dtype="auto")
    assert runner.get_config_summary()["prompt_tokens_details"] is True