################################################################################
FROM nvidia/cuda:12.6.0-devel-ubuntu22.04 AS app

ENV POETRY_VERSION=2.0.1 \
    PYTHONUNBUFFERED=1 \
    POETRY_NO_INTERACTION=1 \
    DEBIAN_FRONTEND=noninteractive \
    PYTHONPATH="/app"

ENV APP_PATH=/app/packages/train

RUN --mount=type=cache,target=/var/lib/apt/lists \
    --mount=type=cache,target=/root/.cache/pip \
    apt-get update && apt-get install -y \
    curl \
    git \
    python3-pip \
    && rm -rf /var/lib/apt/lists/* \
    && pip install --upgrade setuptools wheel pip

RUN pip install --upgrade pip && \
    pip install "poetry==$POETRY_VERSION"

WORKDIR /app

COPY packages/train/README.md \
    packages/train/pyproject.toml \
    packages/train/poetry.lock \
    ${APP_PATH}/
COPY packages/common/pyproject.toml \
    packages/common/poetry.lock \
    packages/common/README.md \
    /app/packages/common/

RUN mkdir -p /app/packages/train/src/zeroband \
    && touch /app/packages/train/src/zeroband/__init__.py \
    && mkdir -p /app/packages/common/src/common \
    && touch /app/packages/common/src/common/__init__.py

COPY packages/train/third_party ${APP_PATH}/third_party

RUN --mount=type=cache,target=/root/.cache/pip \
    --mount=type=cache,target=/root/.cache/pypoetry \
    poetry config virtualenvs.in-project true && \
    cd ${APP_PATH} && \
    poetry install --no-interaction \
    && pip install flash-attn --no-build-isolation

COPY packages/train/src /app/packages/train/src
COPY packages/train/scripts /app/packages/train/scripts
COPY packages/train/configs /app/packages/train/configs
COPY packages/common/src /app/packages/common/src

# Just to provide metadata for the wandb project
COPY .git /app/.git

ENV PYTHONPATH="/app"
ENV PYTHONPATH="${PYTHONPATH}:${APP_PATH}/src"

COPY packages/train/entrypoint.sh ${APP_PATH}/entrypoint.sh
RUN chmod +x ${APP_PATH}/entrypoint.sh
ENTRYPOINT [ "/app/packages/train/entrypoint.sh" ]

################################################################################
FROM app AS test

RUN --mount=type=cache,target=/root/.cache/pip \
    --mount=type=cache,target=/root/.cache/pypoetry \
    cd ${APP_PATH} \
    && PIP_NO_BUILD_ISOLATION=1  poetry install --with dev \
    && . ${APP_PATH}/.venv/bin/activate

COPY packages/train/entrypoint.sh ${APP_PATH}/entrypoint.sh
COPY packages/train/tests ${APP_PATH}/tests

RUN chmod +x ${APP_PATH}/entrypoint.sh
WORKDIR /app

ENTRYPOINT ["/app/packages/train/entrypoint.sh"]
