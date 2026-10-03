"""Read numeric vLLM options from the arguments passed to the subprocess."""

from collections.abc import Sequence


def get_numeric_arg_value(args: Sequence[str], name: str, default: int = 1) -> int:
    aliases = {
        "--max-model-len": "--max-model",
        "--tensor-parallel-size": "-tp",
        "--pipeline-parallel-size": "-pp",
    }
    value = default
    for index, arg in enumerate(args):
        key, separator, inline_value = arg.partition("=")
        if key.startswith("--"):
            key = key.replace("_", "-")
        if key not in (name, aliases.get(name)):
            continue
        try:
            raw = inline_value if separator else args[index + 1]
            value = int(raw)
        except (ValueError, IndexError):
            pass
    return value
