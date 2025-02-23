from functools import partial
from typing import Optional

import torch
import torch.nn.functional as F
from datasets import load_dataset
from pydantic_config import BaseConfig
from torch.utils.data import DataLoader
from torchdata.stateful_dataloader import StatefulDataLoader

from zeroband.data.handler import TrainLLamaHandler
from zeroband.data.slicing import get_indexings
from zeroband.utils.logging import get_logger
from zeroband.utils.world_info import WorldInfo


logger = get_logger(__name__)


class DataConfig(BaseConfig):
    dataset_name_or_paths: str = "product-science/xlam-function-calling-60k-raw"
    seq_length: int = 1024
    num_workers: int = 1
    streaming: bool = True
    data_rank: Optional[int] = None
    data_world_size: Optional[int] = None
    seed: int = 42
    limit: Optional[int] = None
    num_shards: int = 5000


def collate_fn_padded(samples: list[dict[str, torch.LongTensor]], pad_token_id: int) -> dict[str, torch.LongTensor]:
    # We can't use it when decode
    padding_token_labels_id = -100
    padding_token_input_ids = pad_token_id # <|end_of_text|>
    input_ids = [sample['input_ids'] for sample in samples]
    labels = [sample['labels'] for sample in samples]
    seqlens = [sample['seqlens'] for sample in samples]

    try:
        padded_input_ids = left_pad_sequences(input_ids, batch_first=True, padding_value=padding_token_input_ids)
        padded_labels = left_pad_sequences(labels, batch_first=True, padding_value=padding_token_labels_id)
    except Exception as e:
        for x in labels:
            print("Label: " + str(x))
        raise e

    seqlens = torch.tensor(seqlens, dtype=torch.long)

    return {
        'input_ids': padded_input_ids,
        'labels': padded_labels,
        'seqlens': seqlens,
    }


def left_pad_sequences(sequences, batch_first=False, padding_value=-100):
    max_len = max(seq.size(0) for seq in sequences)
    padded_sequences = []
    for seq in sequences:
        pad_size = max_len - seq.size(0)
        padded_seq = F.pad(seq, (pad_size, 0), value=padding_value)
        padded_sequences.append(padded_seq)
    if batch_first:
        return torch.stack(padded_sequences)
    else:
        return torch.stack(padded_sequences).transpose(0, 1)


def find_subtensor(tensor: torch.Tensor, subtensor: torch.Tensor) -> int:
    """Find the first occurrence of subtensor in tensor.
    """
    try:
        if len(subtensor) > len(tensor):
            return -1
    except:
        return -1
    tensor_array = tensor.cpu().numpy()
    subtensor_array = subtensor.cpu().numpy()
    for i in range(len(tensor_array) - len(subtensor_array) + 1):
        if (tensor_array[i:i+len(subtensor_array)] == subtensor_array).all():
            return i   
    return -1

def ignore_nonrelevant_tokens(x, tokenizer, ignore_index = -100):
    #ignore nonassistant tokens
    #since padding is left, everything before answer is ignored ad thats enough
    answer_start = torch.LongTensor(
        tokenizer.encode(
            '<|start_header_id|>assistant<|end_header_id|>',
            add_special_tokens=False
        ),
    )
    index = find_subtensor(x["input_ids"], answer_start)
    if index != -1:
        x["labels"][:index + 3] = ignore_index
    else:
        x["labels"][:] = ignore_index
    return x

def get_dataloaders(
    tokenizer,
    world_info: WorldInfo,
    batch_size: int,
    data_config: DataConfig,
    add_response_test_inputs=False,
    shuffle: bool = True,
) -> tuple[DataLoader, DataLoader]:
    
    dataset = load_dataset(data_config.dataset_name_or_paths, streaming=False)
    handler = TrainLLamaHandler(tokenizer)
    train_dataset = dataset["train"]
    test_dataset = dataset["test"]  
    
    #Shuffle the dataset
    if shuffle:
        train_dataset = train_dataset.shuffle(seed=data_config.seed)

    if data_config.limit is not None:
        train_dataset = train_dataset.select(range(data_config.limit))
        test_dataset = test_dataset.select(range(data_config.limit))

    #reindex dataset s.t. it goes sequentially but diversify across nodes
    
    #We assume that all the global nodes have the same number of local processes
    #Later we should use the whole world info

    train_size = len(train_dataset)
    total_world_size = world_info.local_world_size * world_info.global_world_size
    rank = world_info.global_rank * world_info.local_world_size + world_info.local_rank
    indexing = get_indexings(train_size, total_world_size)[rank]
    train_dataset = train_dataset.select(indexing)
    logger.info(f"Sliced train dataset, first 20 indices: {indexing[:20]}")
    
    assert data_config.num_shards <= train_size, f"num_shards {data_config.num_shards} is greater than train_size {train_size}"
    train_iterable_dataset = train_dataset.to_iterable_dataset(num_shards=data_config.num_shards)
    test_iterable_dataset = test_dataset.to_iterable_dataset(num_shards=100)


    def tokenize_function(data, add_response=True):
        def tokenize(s):
            return tokenizer(s, truncation=True, max_length=data_config.seq_length, add_special_tokens=False)

        if add_response:
            input_ids = tokenize(handler.format_train_all(data) + tokenizer.eos_token)["input_ids"]
        else:
            input_ids = tokenize(handler.format_train_input(data) + tokenizer.eos_token)["input_ids"]
        output = {
            "input_ids": torch.Tensor(input_ids[:-1]).to(dtype=torch.long),
            "labels": torch.Tensor(input_ids[1:]).to(dtype=torch.long),
        }
        
        output = ignore_nonrelevant_tokens(output, tokenizer)
        output["seqlens"] = [len(output["input_ids"])]
        return output

    tokenized_train_dataset = train_iterable_dataset.map(
        tokenize_function,
        batched=False,
        remove_columns=train_dataset.column_names,
    )
    tokenized_test_dataset = test_iterable_dataset.map(
        tokenize_function,
        fn_kwargs={"add_response": add_response_test_inputs},
        batched=False,
        remove_columns=test_dataset.column_names,
    )

    tokenized_train_dataset = tokenized_train_dataset.filter(lambda x: not (x['labels'] == -100).all())
    tokenized_test_dataset = tokenized_test_dataset.filter(lambda x: not (x['labels'] == -100).all())
    collate_fn_padded_specific = partial(collate_fn_padded, pad_token_id=tokenizer.pad_token_id)
    train_dataloader = StatefulDataLoader(  
        tokenized_train_dataset,
        batch_size=batch_size,
        collate_fn=collate_fn_padded_specific,
        num_workers=data_config.num_workers,
    )
    test_dataloader =  StatefulDataLoader(
        tokenized_test_dataset,
        batch_size=batch_size,
        collate_fn=collate_fn_padded_specific,
        num_workers=data_config.num_workers,
    )
    logger.info(f"Data is loaded")
    return train_dataloader, test_dataloader


class EpochIterator:
    def __init__(self, dataloader, epoch=0):
        self.dataloader = dataloader
        self.epoch = epoch
        self.iterator = iter(dataloader)

    def __iter__(self):
        return self

    def __next__(self):
        try:
            return next(self.iterator)
        except StopIteration:
            logger.info("=" * 5 + f"Epoch {self.epoch} is finished! Starting a new epoch..." + "=" * 5)
            self.epoch += 1
            self.iterator = iter(self.dataloader)
            return next(self.iterator)
