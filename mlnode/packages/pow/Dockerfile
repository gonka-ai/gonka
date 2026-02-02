################################################################################
FROM ghcr.io/gonka-ai/vllm:v0.9.1-poc-v2-post2-blackwell-sm120 AS builder

ENV POETRY_VERSION=2.0.1 \
    PYTHONUNBUFFERED=1 \
    POETRY_NO_INTERACTION=1 \
    DEBIAN_FRONTEND=noninteractive

RUN pip install --upgrade pip && \
    pip install "poetry==$POETRY_VERSION"

WORKDIR /app/packages/pow

COPY packages/pow/pyproject.toml \
    packages/pow/poetry.lock \
    packages/pow/README.md \
    /app/packages/pow/
COPY packages/common/README.md \
    packages/common/pyproject.toml \
    packages/common/poetry.lock \
    /app/packages/common/

RUN mkdir -p /app/packages/pow/src/pow && \
    mkdir -p /app/packages/common/src/common && \
    touch /app/packages/pow/src/pow/__init__.py && \
    touch /app/packages/common/src/common/__init__.py

RUN --mount=type=cache,target=/root/.cache/pip \
    --mount=type=cache,target=/root/.cache/pypoetry \
    python3 -m venv /app/packages/pow/.venv \
    && . /app/packages/pow/.venv/bin/activate \
    && poetry install --no-interaction

################################################################################

ARG USERNAME=pow
FROM ghcr.io/gonka-ai/vllm:v0.9.1-poc-v2-post2-blackwell-sm120 AS app

ARG USERNAME
ENV PYTHONUNBUFFERED=1 \
    PYTHONPATH=/app/src \
    USERNAME=$USERNAME

COPY --from=builder /app/packages/pow/.venv /app/packages/pow/.venv
COPY packages/pow/src /app/packages/pow/src
COPY packages/common/src /app/packages/common/src
COPY packages/pow/entrypoint.sh /app/packages/pow/entrypoint.sh
RUN chmod +x /app/packages/pow/entrypoint.sh

ENV PATH="/app/packages/pow/.venv/bin:$PATH"

WORKDIR /app/packages/pow
ENTRYPOINT ["/app/packages/pow/entrypoint.sh"]


################################################################################
FROM builder AS test

RUN --mount=type=cache,target=/root/.cache/pip \
    --mount=type=cache,target=/root/.cache/pypoetry \
    cd /app/packages/pow/ \
    && poetry install --with dev \
    && . /app/packages/pow/.venv/bin/activate

COPY packages/pow/src /app/packages/pow/src
COPY packages/common/src /app/packages/common/src
COPY packages/pow/entrypoint.sh /app/packages/pow/entrypoint.sh
COPY packages/pow/tests /app/packages/pow/tests

RUN chmod +x /app/packages/pow/entrypoint.sh
WORKDIR /app

ENTRYPOINT ["/app/packages/pow/entrypoint.sh"]