# syntax=docker/dockerfile:1.4
################################################################################
FROM gcr.io/decentralized-ai/vllm:0.5.0.post1 AS builder

ENV POETRY_VERSION=2.0.1 \
    PYTHONUNBUFFERED=1 \
    POETRY_NO_INTERACTION=1 \
    DEBIAN_FRONTEND=noninteractive

RUN pip install --upgrade pip && \
    pip install "poetry==$POETRY_VERSION"

WORKDIR /app

COPY packages/pow/pyproject.toml packages/pow/poetry.lock packages/pow/README.md \
    /app/packages/pow/
COPY packages/train/pyproject.toml packages/train/poetry.lock packages/train/README.md \
    /app/packages/train/
COPY packages/api/pyproject.toml packages/api/poetry.lock packages/api/README.md \
    /app/packages/api/
COPY packages/common/pyproject.toml packages/common/poetry.lock packages/common/README.md \
    /app/packages/common/

COPY packages/train/third_party /app/packages/train/third_party

RUN mkdir -p /app/packages/api/src/api \
        && touch /app/packages/api/src/api/__init__.py \
    && mkdir -p /app/packages/pow/src/pow \
        && touch /app/packages/pow/src/pow/__init__.py \
    && mkdir -p /app/packages/train/src/zeroband \
        && touch /app/packages/train/src/zeroband/__init__.py \
    && mkdir -p /app/packages/common/src/common \
        && touch /app/packages/common/src/common/__init__.py

RUN --mount=type=cache,target=/root/.cache/pip \
    --mount=type=cache,target=/root/.cache/pypoetry \
    poetry config virtualenvs.in-project true \
    && cd /app/packages/api \
    && poetry install --no-interaction \
    && pip install flash-attn --no-build-isolation \
    && . /app/packages/api/.venv/bin/activate

################################################################################
FROM gcr.io/decentralized-ai/vllm:0.5.0.post1 AS app
COPY --from=builder /app/packages/api/.venv /app/packages/api/.venv

COPY packages/api/src /app/packages/api/src

COPY packages/pow/src /app/packages/pow/src
COPY packages/pow/resources /app/packages/pow/resources
COPY packages/pow/scripts /app/packages/pow/scripts

COPY packages/train/src /app/packages/train/src
COPY packages/train/scripts /app/packages/train/scripts
COPY packages/train/configs /app/packages/train/configs

COPY packages/common/src /app/packages/common/src
COPY .git /app/.git

ENV PYTHONPATH="/app"
ENV PYTHONPATH="${PYTHONPATH}:/app/packages/api/src"

COPY packages/api/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
WORKDIR /app

ENTRYPOINT ["/app/entrypoint.sh"]


################################################################################
FROM builder AS test

RUN --mount=type=cache,target=/root/.cache/pip \
    --mount=type=cache,target=/root/.cache/pypoetry \
    cd /app/packages/api/ \
    && poetry install --with dev \
    && . /app/packages/api/.venv/bin/activate

COPY packages/api/src /app/packages/api/src
COPY packages/pow/src /app/packages/pow/src
COPY packages/pow/resources /app/packages/pow/resources
COPY packages/pow/scripts /app/packages/pow/scripts

COPY packages/train/src /app/packages/train/src
COPY packages/train/scripts /app/packages/train/scripts
COPY packages/train/configs /app/packages/train/configs

COPY packages/common/src /app/packages/common/src

COPY packages/api/tests /app/packages/api/tests
COPY packages/pow/tests /app/packages/pow/tests
COPY packages/train/tests /app/packages/train/tests

ENV PYTHONPATH="${PYTHONPATH}:/app/packages/api/src"

COPY packages/api/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
WORKDIR /app

ENTRYPOINT ["/app/entrypoint.sh"]