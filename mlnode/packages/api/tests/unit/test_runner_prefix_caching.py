"""Unit tests for vLLM cache-metadata launch defaults."""

from api.inference.vllm.runner import VLLMRunner


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


def test_prefix_caching_flag_is_not_injected():
    # Prefix caching is the vLLM V1 default; no flag is injected for it.
    runner = VLLMRunner(model="test-model", dtype="auto")
    assert "--enable-prefix-caching" not in runner.additional_args


def test_config_summary_reports_effective_settings():
    runner = VLLMRunner(model="test-model", dtype="auto")
    summary = runner.get_config_summary()
    assert summary["prefix_caching"] is True  # V1 default
    assert summary["prompt_tokens_details"] is True


def test_config_summary_reports_prompt_tokens_details_opt_out():
    runner = VLLMRunner(model="test-model", dtype="auto")
    runner.additional_args.remove("--enable-prompt-tokens-details")
    assert runner.get_config_summary()["prompt_tokens_details"] is False