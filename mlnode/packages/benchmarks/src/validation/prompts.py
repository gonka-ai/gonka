from datasets import load_dataset
from typing import List


def get_squad_data_questions() -> List[str]:
    dataset = load_dataset('squad', keep_in_memory=True)
    questions = dataset['train']['question']
    return questions
