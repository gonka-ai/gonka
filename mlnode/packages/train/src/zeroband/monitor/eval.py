from pathlib import Path
import shutil

import torch
import torch.distributed as dist
import torch.nn.functional as F
from einops import rearrange
from transformers import AutoTokenizer, AutoModelForCausalLM

from bfcl._llm_response_generation import get_involved_test_entries, process_multi_turn_test_case
from bfcl.utils import sort_key
from bfcl.eval_checker import eval_runner
from zeroband.utils.logging import get_logger


logger = get_logger(__name__)

def evaluate(model, tokenizer, test_dataloader_iterator, elastic_device_mesh, world_info):
    model.eval()
    total_loss = 0.0
    device = torch.device(f'cuda:{world_info.local_rank}')  # Ensure we're using the correct device
    total_loss_tokens = 0
    logger.info(f"Evaluating...")
    batches = 0
    with torch.no_grad():
        for batch in test_dataloader_iterator:
            input_ids = batch["input_ids"].to(device)
            labels = batch["labels"].to(device)
            attention_mask = (batch["input_ids"] != tokenizer.pad_token_id).to(device)
            with torch.amp.autocast("cuda"):
                outputs = model(input_ids, attention_mask=attention_mask)
                logits = outputs.logits
                flatten_logits = rearrange(logits, "b seq vocab -> (b seq) vocab")
                flatten_labels = rearrange(labels, "b seq -> (b seq)")
                loss = F.cross_entropy(flatten_logits, flatten_labels, reduction='sum') 

            total_loss += loss.item()
            total_loss_tokens += torch.sum(labels != -100).item()
            batches += 1
            if batches % 10 == 0:
                logger.info(f"Evaluated {batches} batches")
    
    # Aggregate total_loss and total_tokens across all processes
    total_loss_tensor = torch.tensor(total_loss, device=device)
    total_loss_tokens_tensor = torch.tensor(total_loss_tokens, device=device)
    
    dist.all_reduce(total_loss_tensor, op=dist.ReduceOp.SUM, group=elastic_device_mesh.local_pg)
    dist.all_reduce(total_loss_tokens_tensor, op=dist.ReduceOp.SUM, group=elastic_device_mesh.local_pg)
    
    avg_loss = total_loss_tensor.item() / total_loss_tokens_tensor.item()
    perplexity = torch.exp(torch.tensor(avg_loss))
    
    model.train()
    return {"test_loss": avg_loss, "test_perplexity": perplexity.item(), "batches": batches}


def load_from_checkpoint(
    model: AutoModelForCausalLM,
    checkpoint_path,
) -> AutoModelForCausalLM:
    with open(checkpoint_path, "rb") as f:
        model.load_state_dict(torch.load(f)["model_state_dict"])
    return model


def load_model(
    model_name_or_path,
    checkpoint_path=None,
    dtype=torch.float16,
    device_map="auto"
):
    tokenizer = AutoTokenizer.from_pretrained(model_name_or_path, use_fast=True)
    model = AutoModelForCausalLM.from_pretrained(
        model_name_or_path,
        torch_dtype=dtype,
        device_map=device_map,
    )
    if checkpoint_path is not None:
        model = load_from_checkpoint(
            model,
            checkpoint_path,
        )
    
    return model, tokenizer


def generate_tokenized_batch(
    x,
    model,
    tokenizer,
    temperature=0.7,
    do_sample=False,
    max_new_tokens=256,
):
    model.eval()
    device = model.device
    with torch.no_grad():
        outputs = model.generate(
            input_ids=x['input_ids'].to(device),
            attention_mask=(x['input_ids'] != tokenizer.pad_token_id).to(device),
            max_new_tokens=max_new_tokens,
            do_sample=do_sample,
            temperature=temperature,
        )

    answers = []
    for _, (inp, out) in enumerate(zip(x["input_ids"], outputs)):
        input_length = len(inp)  # Length of the original input tokens
        input_text = tokenizer.decode(inp, skip_special_tokens=True)
        generated_text = tokenizer.decode(out[input_length:], skip_special_tokens=True)
        answers.append({
            "input": input_text,
            "output": generated_text
        })

    return answers


def get_test_cases(test_category):
    all_test_file_paths, all_test_categories, all_test_entries_involved = (
        get_involved_test_entries(test_category, None)
    )

    test_cases_to_generate = [
        test_case
        for test_case in all_test_entries_involved
    ]
    test_cases_to_generate = process_multi_turn_test_case(test_cases_to_generate)

    return sorted(test_cases_to_generate, key=sort_key), all_test_categories


def generate_dict_batch(
    batch,
    handler,
    model,
    tokenizer,
    temperature=0.7,
    do_sample=False,
    max_new_tokens=256,
):
    model.eval()
    device = model.device
    texts = [handler.format_train_input(x) for x in batch]
    tokenizer.padding_side = 'left'
    x = tokenizer(texts, add_special_tokens=False, return_tensors='pt', padding=True)
    with torch.no_grad():
        outputs = model.generate(
            input_ids=x['input_ids'].to(device),
            attention_mask=(x['input_ids'] != tokenizer.pad_token_id).to(device),
            max_new_tokens=max_new_tokens,
            do_sample=do_sample,
            temperature=temperature, 
        )

    generated_text = tokenizer.decode(outputs[0], skip_special_tokens=False)
    answers = []
    for _, (inp, out, item) in enumerate(zip(x["input_ids"], outputs, batch)):
        input_length = len(inp)
        generated_text = tokenizer.decode(out[input_length:], skip_special_tokens=True)
        item["result"] = generated_text
        answers.append(item)

    return answers


def compute_score(
    score_dir,
    results_dir,
    model_name,
    test_categories,
    handler,
    override=True
):
    score_dir = Path(score_dir)
    score_dir.mkdir(exist_ok=True, parents=True)

    model_score = (score_dir / model_name)
    if override and model_score.exists():
        shutil.rmtree(model_score)

    results_dir = Path(results_dir)

    eval_runner.runner(
        model_name,
        test_categories,
        False,
        results_dir,
        score_dir,
        get_handler=lambda _: handler 
    )