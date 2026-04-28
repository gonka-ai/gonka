from datasets import load_dataset
from typing import List, Dict, Tuple
import unicodedata
import regex as re
from common.logger import create_logger

logger = create_logger(__name__)

DATASET_HANDLES: Dict[str, str] = {
    "en": "tatsu-lab/alpaca",
    "hi": "iamshnoo/alpaca-cleaned-hindi",
    "sp": "bertin-project/alpaca-spanish",
    "ar": "gagan3012/alpaca-gpt4-arabic-updated",
    "ch": "silk-road/alpaca-data-gpt4-chinese",
}

INSTRUCTION_FIELD = {
    "ch": "instruction_zh",
    "en": "instruction",
    "hi": "instruction",
    "sp": "instruction",
    "ar": "instruction",
}

INPUT_FIELD = {
    "ch": "input_zh",
    "en": "input",
    "hi": "input",
    "sp": "input",
    "ar": "input",
}

TEMPLATE = """### Instruction:
{instruction}
### Input:
{input}
### Response:
"""


_ZS_OR_FORMAT = re.compile(r"[\p{Zs}\p{Zl}\p{Zp}\p{Cf}\p{Cc}]+", re.UNICODE)


def _is_effectively_empty(s: str) -> bool:
    if s is None:
        return True
    s2 = unicodedata.normalize("NFKC", s).strip()
    s2 = _ZS_OR_FORMAT.sub("", s2)
    return len(s2) == 0

def _normalize(s: str) -> str:
    if s is None:
        return ""
    s = unicodedata.normalize("NFKC", s)
    s = re.sub(r"[\p{Zs}\p{Zl}\p{Zp}]", " ", s)
    s = "\n".join(line.strip() for line in s.splitlines())
    return s.strip()



def get_language_alpaca_prompts(lang: str) -> List[str]:
    ds = load_dataset(DATASET_HANDLES[lang], keep_in_memory=True)
    prompts: List[str] = []
    instr_key = INSTRUCTION_FIELD[lang]
    input_key = INPUT_FIELD[lang]

    for ex in ds["train"]:
        instruction = _normalize(ex.get(instr_key, ""))
        input_text = _normalize(ex.get(input_key, ""))

        if _is_effectively_empty(instruction):
            continue

        prompt = TEMPLATE.format(
            instruction=instruction,
            input=input_text if not _is_effectively_empty(input_text) else "",
        )
        prompts.append(prompt)

    return prompts


def preload_all_language_prompts(langs: Tuple[str, ...] = ("en", "ch", "hi", "ar")) -> Dict[str, List[str]]:
    all_prompts_by_lang: Dict[str, List[str]] = {}
    for lang in langs:
        if lang not in DATASET_HANDLES:
            continue
        all_prompts_by_lang[lang] = get_language_alpaca_prompts(lang)
    logger.info(f"Loaded {langs} language prompts")
    return all_prompts_by_lang


def slice_mixed_language_prompts_with_langs(
    all_prompts_by_lang: Dict[str, List[str]],
    per_language_n: int,
    langs: Tuple[str, ...] = ("en", "ch", "hi", "ar"),
) -> Tuple[List[str], List[str]]:
    logger.info(f"Slicing {langs} language prompts (with langs), {per_language_n} prompts per language")
    prompts: List[str] = []
    languages: List[str] = []
    for lang in langs:
        lang_prompts = all_prompts_by_lang[lang][:per_language_n]
        prompts.extend(lang_prompts)
        languages.extend([lang] * len(lang_prompts))
    return prompts, languages


def get_squad_data_questions() -> List[str]:
    dataset = load_dataset('squad', keep_in_memory=True)
    prompts = [
        f"Context: {context}\nQuestion: {question}"
        for question, context in zip(dataset['train']['question'], dataset['train']['context'])
    ]
    return prompts
