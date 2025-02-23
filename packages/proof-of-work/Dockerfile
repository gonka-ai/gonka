################################################################################

FROM gcr.io/decentralized-ai/vllm:0.5.0.post1 AS builder

ENV POETRY_VERSION=2.0.1 \
    PYTHONUNBUFFERED=1 \
    POETRY_NO_INTERACTION=1 \
    DEBIAN_FRONTEND=noninteractive

RUN pip install --upgrade pip && \
    pip install "poetry==$POETRY_VERSION"

WORKDIR /app/mlnode/packages/proof-of-work

COPY pyproject.toml poetry.lock /app/mlnode/packages/proof-of-work/

RUN poetry config virtualenvs.in-project true \
    && poetry install --no-root

################################################################################

ARG USERNAME=pow
FROM gcr.io/decentralized-ai/vllm:0.5.0.post1 AS app

ARG USERNAME
ENV PYTHONUNBUFFERED=1 \
    PYTHONPATH=/app/src \
    USERNAME=$USERNAME

COPY --from=builder /app/mlnode/packages/proof-of-work/.venv /app/mlnode/packages/proof-of-work/.venv
COPY src /app/mlnode/packages/proof-of-work/src
COPY entrypoint.sh /app/mlnode/packages/proof-of-work/entrypoint.sh
RUN chmod +x /app/mlnode/packages/proof-of-work/entrypoint.sh

ENV PATH="/app/mlnode/packages/proof-of-work/.venv/bin:$PATH"

ENV PYTHONPATH=$PYTHONPATH:/app/mlnode/packages/proof-of-work/src/
ENV PYTHONPATH=$PYTHONPATH:/app/mlnode/packages/proof-of-work

WORKDIR /app/mlnode/packages/proof-of-work
ENTRYPOINT ["/app/mlnode/packages/proof-of-work/entrypoint.sh"]