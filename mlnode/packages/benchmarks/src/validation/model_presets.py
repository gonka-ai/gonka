from validation.data import ModelPreset


QWEN25_7B_INT8 = ModelPreset(
    model='RedHatAI/Qwen2.5-7B-Instruct-quantized.w8a16',
    precision='int8',
    dtype='auto',
    additional_args=[
        '--enable-auto-tool-choice',
        '--tool-call-parser', 'hermes',
    ],
)

QWEN25_7B_AWQ = ModelPreset(
    model='Qwen/Qwen2.5-7B-Instruct-AWQ',
    precision='int4',
    dtype='auto',
    additional_args=[
        '--enable-auto-tool-choice',
        '--tool-call-parser', 'hermes',
    ],
)


QWEN3_235B_FP8 = ModelPreset(
    model='Qwen/Qwen3-235B-A22B-Instruct-2507-FP8',
    precision='fp8',
    dtype='float16',
    additional_args=[
        '--enable-auto-tool-choice',
        '--tool-call-parser', 'hermes',
        '--max_model_len', '240000',
    ],
)

QWEN3_235B_INT4 = ModelPreset(
    model='chriswritescode/Qwen3-235B-A22B-Instruct-2507-INT4-W4A16',
    precision='int4',
    dtype='float16',
    additional_args=[
        '--enable-auto-tool-choice',
        '--tool-call-parser', 'hermes',
        '--max_model_len', '240000',
    ],
)
