function get_cuda_devices() {
    local num_gpu=$1
    local index=$2
    local start_gpu=$((num_gpu * index))
    local end_gpu=$((start_gpu + num_gpu - 1))

    if [ "$num_gpu" -eq 1 ]; then
        echo $start_gpu
    else
        echo $(seq -s ',' $start_gpu $end_gpu)
    fi
}

child_pids=()

# Function to kill all child processes
cleanup() {
    echo "Cleaning up child processes..."
    local killed=0
    for pid in "${child_pids[@]}"; do
        if kill -TERM "$pid" 2>/dev/null; then
            ((killed++))
        fi
    done
    wait
    echo "All child processes terminated. Killed $killed processes."
    exit
}

if [ "$#" -lt 1 ]; then
    echo "Usage: $0 <train.py> [additional_python_args]"
    exit 1
fi

N=1
NUM_GPU=1

trap cleanup SIGINT SIGTERM SIGKILL

mkdir -p logs
> logs/log.log

export GLOBAL_ADDR=${GLOBAL_ADDR}
export GLOBAL_PORT=${GLOBAL_PORT}
export GLOBAL_RANK=${GLOBAL_RANK}
export GLOBAL_UNIQUE_ID=${GLOBAL_UNIQUE_ID}
export GLOBAL_WORLD_SIZE=${GLOBAL_WORLD_SIZE:-1}
export DATA_RANK=${DATA_RANK:-0}
export DATA_WORLD_SIZE=${DATA_WORLD_SIZE:-1}
export BASE_PORT=${BASE_PORT:-10001}
export EXPORT_GLOO_SOCKET_IFNAME=${EXPORT_GLOO_SOCKET_IFNAME}


if [ "$EXPORT_GLOO_SOCKET_IFNAME" ]; then
    echo "Setting GLOO_SOCKET_IFNAME to $EXPORT_GLOO_SOCKET_IFNAME"
    export GLOO_SOCKET_IFNAME=$EXPORT_GLOO_SOCKET_IFNAME
fi

if [ -z "$GLOBAL_RANK" ] || [ -z "$GLOBAL_UNIQUE_ID" ] || [ -z "$GLOBAL_WORLD_SIZE" ] || [ -z "$GLOBAL_ADDR" ] || [ -z "$GLOBAL_PORT" ]; then
    echo "GLOBAL_RANK or GLOBAL_UNIQUE_ID is not set"
    exit 1
fi

echo "BASE_PORT: $BASE_PORT"


WANDB_MODE="online" \
CUDA_VISIBLE_DEVICES=$(get_cuda_devices $NUM_GPU 0) \
ZERO_BAND_GLOBAL_PG_TIMEOUT_SECONDS=30 \
ZERO_BAND_LOG_LEVEL=DEBUG \
LOG_LEVEL=DEBUG \
torchrun \
    --nproc_per_node=$NUM_GPU \
    --node-rank 0 \
    --rdzv-endpoint localhost:$BASE_PORT \
    --nnodes=1 \
    $@ \
    --data.data_rank $GLOBAL_RANK \
    --data.data_world_size $GLOBAL_WORLD_SIZE \
    > logs/log.log 2>&1 &

child_pids+=($!)

tail -f logs/log.log &
child_pids+=($!)

wait
