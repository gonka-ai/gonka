################################################################################
FROM ghcr.io/gonka-ai/vllm:v0.9.1-poc-v2-post2-blackwell-sm120 AS app

ENV APP_PATH=/app/packages/train

RUN --mount=type=cache,target=/var/lib/apt/lists \
    --mount=type=cache,target=/root/.cache/pip \
    apt-get update && apt-get install -y --no-install-recommends \
    curl \
    git \
    python3-pip \
    build-essential \
    checkinstall \
    zlib1g-dev \
    && rm -rf /var/lib/apt/lists/* \
    && pip install --upgrade setuptools wheel pip \
    && curl -fLO https://www.openssl.org/source/old/1.1.1/openssl-1.1.1u.tar.gz \
    && tar -xzf openssl-1.1.1u.tar.gz \
    && cd openssl-1.1.1u \
    && ./config --prefix=/usr/local/openssl-1.1 \
                --openssldir=/usr/local/openssl-1.1/ssl \
    && make -j"$(nproc)" \
    && make install \
    && cd .. \
    && rm -rf openssl-1.1.1u.tar.gz openssl-1.1.1u

ENV PATH="/usr/local/openssl-1.1/bin:${PATH}" \
    LD_LIBRARY_PATH="/usr/local/openssl-1.1/lib:$LD_LIBRARY_PATH"


ENV POETRY_VERSION=2.0.1 \
    PYTHONUNBUFFERED=1 \
    POETRY_NO_INTERACTION=1 \
    DEBIAN_FRONTEND=noninteractive \
    APP_PATH=/app/packages/train

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
    cd ${APP_PATH} \
    && python3 -m venv ${APP_PATH}/.venv \
    && . ${APP_PATH}/.venv/bin/activate \
    && poetry install --no-interaction \
    && pip install https://github.com/Dao-AILab/flash-attention/releases/download/v2.7.4.post1/flash_attn-2.7.4.post1+cu12torch2.2cxx11abiFALSE-cp312-cp312-linux_x86_64.whl

COPY packages/train/src /app/packages/train/src
COPY packages/train/scripts /app/packages/train/scripts
COPY packages/train/resources /app/packages/train/resources
COPY packages/common/src /app/packages/common/src

# Just to provide metadata for the wandb project

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
