################################################################################

FROM gcr.io/decentralized-ai/vllm:0.5.0.post1 AS builder

ENV POETRY_VERSION=1.6.1 \
    PYTHONUNBUFFERED=1 \
    POETRY_NO_INTERACTION=1 \
    DEBIAN_FRONTEND=noninteractive

RUN pip install --upgrade pip && \
    pip install "poetry==$POETRY_VERSION"

WORKDIR /app

COPY pyproject.toml poetry.lock /app/

RUN poetry config virtualenvs.in-project true \
    && poetry install --no-root

################################################################################

ARG USERNAME=pow
FROM gcr.io/decentralized-ai/vllm:0.5.0.post1 AS app

ARG USERNAME
ENV PYTHONUNBUFFERED=1 \
    PYTHONPATH=/app/src \
    USERNAME=$USERNAME

COPY --from=builder /app/.venv /app/.venv
COPY src /app/src
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

ENV PATH="/app/.venv/bin:$PATH"
WORKDIR /app
ENTRYPOINT ["/app/entrypoint.sh"]

################################################################################

ARG USERNAME=pow
FROM app AS dev

RUN mkdir /app/jupyter_data && \
    chmod -R 777 /app/jupyter_data
ENV JUPYTER_DATA_DIR=/app/jupyter_data

WORKDIR /app
ENTRYPOINT ["/app/entrypoint.sh"]
