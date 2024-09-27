ARG USERNAME="pow"

################################################################################

FROM pytorch/pytorch:2.4.0-cuda12.4-cudnn9-runtime AS builder

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

FROM pytorch/pytorch:2.4.0-cuda12.4-cudnn9-runtime
ARG USERNAME

ENV PYTHONUNBUFFERED=1 \
    PYTHONPATH=/app/src \
    USERNAME=$USERNAME

RUN useradd -m -s /bin/bash $USERNAME
RUN mkdir /app && chown -R $USERNAME:$USERNAME /app

USER $USERNAME
WORKDIR /app

COPY --from=builder /app/.venv /app/.venv
COPY src /app/src

ENV PATH="/app/.venv/bin:$PATH"

