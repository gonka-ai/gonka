FROM nvidia/cuda:12.6.0-devel-ubuntu22.04

ENV POETRY_VERSION=2.0.1 \
    PYTHONUNBUFFERED=1 \
    POETRY_NO_INTERACTION=1 \
    DEBIAN_FRONTEND=noninteractive \
    PYTHONPATH="/app"

ENV APP_PATH=/app/mlnode/packages/train

RUN apt-get update && apt-get install -y \
    curl \
    git \
    python3-pip \
    && rm -rf /var/lib/apt/lists/* \
    && pip install --upgrade setuptools wheel pip

RUN pip install --upgrade pip && \
    pip install "poetry==$POETRY_VERSION"

WORKDIR /app

COPY packages/train/README.md ${APP_PATH}/README.md
COPY packages/train/pyproject.toml ${APP_PATH}/pyproject.toml

COPY packages/train/src/zeroband/__init__.py ${APP_PATH}/src/zeroband/__init__.py
COPY packages/train/third_party ${APP_PATH}/third_party

RUN poetry config virtualenvs.in-project true 
RUN cd ${APP_PATH} && poetry install --no-interaction --no-root
RUN pip install -e ${APP_PATH}/third_party/gorilla/berkeley-function-call-leaderboard[oss_eval_vllm]
RUN pip install flash-attn --no-build-isolation
RUN pip install fastapi uvicorn

COPY packages/train/src ${APP_PATH}/src
# Just to provide metadata for the wandb project
COPY .git /app/mlnode/.git

ENV PYTHONPATH="/app"
ENV PYTHONPATH="${PYTHONPATH}:${APP_PATH}/third_party/gorilla/berkeley-function-call-leaderboard"
ENV PYTHONPATH="${PYTHONPATH}:${APP_PATH}/src"

COPY packages/train/entrypoint.sh ${APP_PATH}/entrypoint.sh
RUN chmod +x ${APP_PATH}/entrypoint.sh
ENTRYPOINT [ "/app/mlnode/packages/train/entrypoint.sh" ]
