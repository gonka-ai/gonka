FROM python:3.12-slim AS builder

ENV POETRY_VERSION=1.6.1 \
    PYTHONUNBUFFERED=1 \
    POETRY_NO_INTERACTION=1

RUN pip install "poetry==$POETRY_VERSION"

WORKDIR /app

COPY pyproject.toml poetry.lock /app/

RUN poetry config virtualenvs.in-project true \
    && poetry install --no-root --no-dev

COPY src /app/src

FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONPATH=/app/src

WORKDIR /app

COPY --from=builder /app/.venv /app/.venv

COPY --from=builder /app/src /app/src

ENV PATH="/app/.venv/bin:$PATH"

CMD ["python", "-m", "pow"]
