import os
import time
import shutil

import torch
import torch.distributed as dist
from torch.nn import functional as F
from torch.nn.parallel import DistributedDataParallel as DDP
from transformers import AutoTokenizer, AutoModelForCausalLM, Adafactor
from einops import rearrange

from pydantic_config import parse_argv
from train import utils
from zeroband.dist.diloco import Diloco
from zeroband.dist.device_mesh import ElasticDeviceMesh
from zeroband.monitor.eval import evaluate
from zeroband.utils import PerfCounter
from zeroband.data.loader import get_dataloaders
from zeroband.monitor.metric_logger import WandbMetricLogger, DummyMetricLogger
from zeroband.utils.world_info import get_world_info
from zeroband.utils.logging import get_logger
from zeroband.monitor.checkpoint import CkptManager, TrainingProgress
from zeroband.lr_scheduler import get_scheduler
from zeroband.config import Config
from zeroband.train_utils import set_random_seed

#endregion

#region 4. MAIN TRAINING FUNCTION

def get_denominator(micro_batches):
    n_items = 0
    for mb in micro_batches:
        n_items += torch.sum(mb['labels'] != -100)
    return n_items

def train(config: Config):
    
    # check_params
    assert config.optim.batch_size % world_info.local_world_size == 0
    total_world_size = world_info.global_world_size * world_info.local_world_size
    batch_size = config.optim.batch_size // world_info.local_world_size
    assert batch_size % config.train.micro_bs == 0
    gradient_accumulation_steps = batch_size // config.train.micro_bs
    if world_info.global_world_size == 1:
        config.diloco = None
        logger.info("Running in single node mode, diloco is disabled")
    if config.ckpt is not None and config.ckpt.interval is not None and config.diloco is not None:
        assert (
            config.ckpt.interval % config.diloco.inner_steps == 0
        ), "ckpt interval must be a multiple of diloco inner steps as we only save at the end of an outer step"
    #endregion

    #region 4.2 Model and Tokenizer Loading
    model_name = "meta-llama/Llama-3.2-1B-Instruct"
    tokenizer = AutoTokenizer.from_pretrained(model_name, use_fast=True)
    tokenizer.pad_token = "<|end_of_text|>"
    logger.debug("tokenizer loaded")


    train_dataloader, test_dataloader = get_dataloaders(
        tokenizer=tokenizer,
        world_info=world_info,
        batch_size=config.train.micro_bs,
        data_config=config.data,
        add_response_test_inputs=True
    )
    logger.info(f"Data loaded")
    model = AutoModelForCausalLM.from_pretrained(
        model_name,
        use_cache=False
    )
    model.gradient_checkpointing_enable()
    logger.debug(f"Model loaded, local rank: {world_info.local_rank}")
    #endregion

    #region 4.3 Performance Setup
    gpu_peak_flops = utils.get_peak_flops(torch.cuda.get_device_name(torch.device("cuda")))
    logger.info(f"Peak FLOPS used for computing MFU: {gpu_peak_flops:.3e}")

    #endregion

    #region 4.4 Distributed Training Setup
    elastic_device_mesh = ElasticDeviceMesh(enable=config.diloco is not None)

    device = torch.device(f'cuda:{world_info.local_rank}')
    model = model.to(device)
    
    # Wrap the model with DDP
    if world_info.local_world_size > 1:
        model = DDP(
            model,
            device_ids=[world_info.local_rank],
            output_device=world_info.local_rank,
            process_group=elastic_device_mesh.local_pg
        )
        logger.debug("Model wrapped with DistributedDataParallel")
    logger.debug(f"world info: {world_info.json()}")
    #region 4.5 Optimizer and Scheduler Setup
    inner_optimizer = Adafactor(
        model.parameters(),
        lr=config.optim.lr,
        scale_parameter=False,   # Do not scale learning rate based on parameter count
        relative_step=False,     # Disable relative step updates
        warmup_init=False,       # Do not use Adafactor's warmup, rely on your external scheduler
        weight_decay=config.optim.weight_decay
    )

    if config.diloco is not None:
        diloco = Diloco(config.diloco, model, elastic_device_mesh)

    scheduler = get_scheduler(
        sched_type=config.optim.sched_type,
        optimizer=inner_optimizer,
        num_warmup_steps=config.optim.warmup_steps,
        num_stable_steps=config.optim.stable_steps,
        num_training_steps=config.optim.total_steps,
    )
    #endregion

    #region 4.6 Checkpoint and Progress Tracking
    training_progress = TrainingProgress(total_tokens=0, outer_step=0, step=0, total_items=0)

    ckpt_manager = CkptManager(
        config=config.ckpt,
        model=model,
        optimizer=inner_optimizer,
        scheduler=scheduler,
        dataloader=train_dataloader,
        training_progress=training_progress,
        data_rank=config.data.data_rank,
        diloco_offloaded_optimizer=diloco.outer_optimizer if config.diloco is not None else None,
        diloco_offloaded_param_list=diloco.param_list_cpu if config.diloco is not None else None,
    )


    #region 4.8 Logging and Monitoring Setup
    logger_cls = WandbMetricLogger if config.metric_logger_type == "wandb" else DummyMetricLogger
    metric_logger = logger_cls(
        config=config,
        world_info=world_info,
        resume=config.wandb_resume,
    )

    scaler = torch.amp.GradScaler("cuda")

    num_inner_steps = config.diloco.inner_steps if config.diloco is not None else 1
    perf_counter = PerfCounter(window_size=10)
    start_training_time = time.time()
    eval_time_elapsed = 0
    min_eval_loss = 1e10
    min_checkpoint_path = None
    
    #endregion

    train_dataloader_iterator = iter(train_dataloader)
    test_dataloader_iterator = iter(test_dataloader)

    #region 4.10 Main Training Loop
    #region 4.10.1 Outer Loop Setup
    epoch = 0
    while True:
        if num_inner_steps > 1:
            # if we don't use diloco we don't print the outer step logs
            logger.info(f"outer_step step: {training_progress.outer_step}")

        time_start_outer = time.perf_counter()

        if config.diloco is not None:
            # this is a patch for now to allow live recovery worker to not affect the all reduce at all
            num_effective_peers = elastic_device_mesh.global_pg.size()
            elastic_device_mesh.maybe_reinit_global_pg(admit_joiners=True)

        # at the beginning of the inner steps we allow joiner to arrive.
        # We maybe reinit before the all reduce but only to allow leaving, not to join anymore
    #endregion

    #region 4.10.2 Inner Steps Loop
        for inner_step in range(num_inner_steps):
            loss_batch = 0
            #TODO check how it works
            if config.diloco is not None:
                maybe_dest_rank = elastic_device_mesh.live_recovery.should_send_ckpt_to()
                if maybe_dest_rank is not None:
                    logger.info(f"Start live recovery to rank {maybe_dest_rank}")
                    ckpt_manager.send_ckpt_to_peer(elastic_device_mesh.global_pg, maybe_dest_rank)
                    elastic_device_mesh.live_recovery.reset()
            #endregion

            #region 4.10.2.2 Gradient Accumulation Loop
            micro_batches = []
            for grad_acc_step in range(gradient_accumulation_steps):
                try:
                    batch = next(train_dataloader_iterator)
                    micro_batches.append(batch)
                except StopIteration:
                    logger.info("="*5 + f"Epoch {epoch} finished, starting new epoch" + "="*5)
                    epoch += 1
                    train_dataloader_iterator = iter(train_dataloader)
                    batch = next(train_dataloader_iterator)
                    micro_batches.append(batch)
            denominator = get_denominator(micro_batches)
            for batch in micro_batches:
                input_ids = batch["input_ids"].to(device)
                labels = batch["labels"].to(device)
                attention_mask = (batch["input_ids"] != tokenizer.pad_token_id).to(device)

                with torch.amp.autocast("cuda"):
                    outputs = model(input_ids, attention_mask=attention_mask)
                    logits = outputs.logits
                    flatten_logits = rearrange(logits, "b seq vocab -> (b seq) vocab")
                    flatten_labels = rearrange(labels, "b seq -> (b seq)")

                    loss = F.cross_entropy(flatten_logits, flatten_labels, reduction="sum") / denominator
                    
                    loss += 1e-8
                    if torch.isnan(loss):
                        logger.warning(f"NaN loss detected. Skipping step. {loss} | {flatten_logits}")
                        continue

                scaler.scale(loss).backward()
                loss_batch += loss.detach()
            scaler.unscale_(inner_optimizer)
            torch.nn.utils.clip_grad_norm_(model.parameters(), 1.0)
            scaler.step(inner_optimizer)
            scaler.update()
            inner_optimizer.zero_grad()
            scheduler.step()

            loss_tensor = torch.tensor(loss_batch.item(), device=device)
            dist.all_reduce(loss_tensor, op=dist.ReduceOp.SUM, group=elastic_device_mesh.local_pg)
            loss_value = loss_tensor.item() / elastic_device_mesh.local_pg.size()
            #endregion

            #region 4.10.2.6 Progress Tracking and Metrics
            after_step_time = time.time()
            training_progress.step += 1
            inner_lr = [group["lr"] for group in inner_optimizer.param_groups][0]

            new_tokens = config.data.seq_length * batch_size
            perf_counter.count_tokens(new_tokens)

            training_progress.total_tokens += new_tokens
            training_progress.total_items += batch_size
            tokens_per_second = perf_counter.get_tokens_per_second()
            metrics = {
                "Train_loss": loss_value,
                "Step": training_progress.step,
                "Inner_lr": inner_lr,
                "Total_tokens_node": training_progress.total_tokens,
                "Total_tokens_global": training_progress.total_tokens * total_world_size,
                "Total_items_node": training_progress.total_items,
                "Total_items_global": training_progress.total_items * total_world_size,
                "Time": time.time(),
                "Training_time": after_step_time - start_training_time - eval_time_elapsed,
                "Tokens_per_second": tokens_per_second,
                "Tokens_per_second_global": tokens_per_second * total_world_size,
            }

            log = f"step: {training_progress.step}, loss: {loss_batch.item():.4f}"
            log += f", tokens_per_second: {tokens_per_second:.2f}"

            if config.diloco is not None:
                metrics["num_peers"] = elastic_device_mesh.global_pg.size()
                log += f", diloco_peers: {metrics['num_peers']}"

            metric_logger.log(metrics)

            if (training_progress.step % config.train.eval_interval == 1) and (training_progress.step > 1):
                eval_time_start = time.time()
                test_dataloader_iterator = iter(test_dataloader)
                logger.info(f"Evaluating at step {training_progress.step}...")
                eval_metrics = evaluate(model, tokenizer, test_dataloader_iterator, elastic_device_mesh, world_info)
                eval_metrics.update({
                    "Step": training_progress.step,
                    "Total_tokens_node": training_progress.total_tokens,
                    "Total_tokens_global": training_progress.total_tokens * total_world_size,
                    "Total_items_node": training_progress.total_items,
                    "Total_items_global": training_progress.total_items * total_world_size,
                    "Time": time.time(),
                    "training_time": after_step_time - start_training_time - eval_time_elapsed,
                })
                if eval_metrics["test_loss"] < min_eval_loss:
                    #new minimum
                    min_eval_loss = eval_metrics["test_loss"]
                    logger.info(f"New min eval loss: {min_eval_loss}")
                    #del previous minimum
                    if min_checkpoint_path is not None:
                        shutil.rmtree(min_checkpoint_path, ignore_errors=True)
                        logger.info(f"Deleted previous minimum checkpoint at {min_checkpoint_path}")
                    #doesnt work with remote yet
                    assert not config.ckpt.remote
                    min_checkpoint_path = ckpt_manager.save(remote=False, minimum=True)
                    logger.info(f"Saved checkpoint to {min_checkpoint_path}")
                    
                eval_time_elapsed += time.time() - eval_time_start
                metric_logger.log(eval_metrics)
                logger.info(f"Finished evaluation at step {training_progress.step}: {eval_metrics}")
            logger.info(log)
            #endregion
    #endregion

    #region 4.10.3 Diloco and Checkpoint Handling
        if config.diloco is not None:
            ckpt_manager.cache_inner_optimizer()
            time_start_inner = time.perf_counter()
            diloco.step(model=model, flag=training_progress.outer_step, num_effective_peers=num_effective_peers)
            diloco_time = time.perf_counter() - time_start_inner
        training_progress.outer_step += 1

        if (
            config.ckpt.interval is not None
            and training_progress.step > 0
            and training_progress.step % config.ckpt.interval == 0
        ):
            ckpt_manager.save()
    #endregion

    #region 4.10.4 Final Metrics and Exit Check
        if config.diloco:
            tokens_per_second = (
                batch_size
                * config.diloco.inner_steps
                * config.data.seq_length
                / (time.perf_counter() - time_start_outer)
            )

            metric_logger.log(
                {
                    "step": training_progress.step,
                    "outer_step": training_progress.outer_step,
                    "outer_tokens_per_second": tokens_per_second,
                    "all_reduce_step": diloco_time,
                }
            )
        if training_progress.step >= config.optim.total_steps:
            break

    #region 4.11 Cleanup and Exit
    metric_logger.finish()
    ckpt_manager.wait_for_blocking_job()
    del elastic_device_mesh  # allow to clean up for smoother tests transition
    logger.info("Training finished, exiting ...")


if __name__ == "__main__":
    # Allow eager fallback during production so that that the training runs dont die
    # However, in development, we want to know that we broke torch compile
    torch._dynamo.config.suppress_errors = "ZERO_BAND_DEV" not in os.environ
    torch.set_float32_matmul_precision("high")
    set_random_seed(42)
    world_info = get_world_info()
    logger = get_logger()
    torch.cuda.set_device(world_info.local_rank)
    config = Config(**parse_argv())
    logger.debug(f"config: {config.model_dump()}")
    train(config)